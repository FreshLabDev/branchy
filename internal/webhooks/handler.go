// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"branchy/internal/db"
	"branchy/internal/metrics"
	"branchy/internal/notify"
)

// maxBodyBytes bounds a webhook payload. GitHub caps payloads at 25 MB but
// the events Branchy consumes stay far smaller; oversized requests get an
// explicit 413 instead of a confusing signature failure on a truncated body.
const maxBodyBytes = 5 * 1024 * 1024

type Store interface {
	// EnqueueNotificationJobs records the delivery and enqueues its jobs in one
	// transaction, returning (enqueued, duplicate, error). A true duplicate means
	// the delivery was already fully processed.
	EnqueueNotificationJobs(ctx context.Context, deliveryID, event, repoFullName string, jobs []db.NotificationJobInsert, maxAttempts int) (int, bool, error)
	// ListActiveSubscriptionsForRepoEvent matches by stable github_repo_id so a
	// repo rename does not break delivery.
	ListActiveSubscriptionsForRepoEvent(ctx context.Context, repoID int64, event string) ([]db.Subscription, error)
	ReconcileSubscriptionRepoName(ctx context.Context, repoID int64, fullName string) error
}

type Handler struct {
	secret      string
	store       Store
	maxAttempts int
	limiter     *rateLimiter
	now         func() time.Time
}

// Limits tunes the endpoint rate limiter; zero values fall back to defaults
// (30 requests/second, burst 60) that real GitHub traffic never approaches.
type Limits struct {
	RatePerSecond int
	Burst         int
}

func NewHandler(secret string, store Store, maxAttempts int, limits Limits) *Handler {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	return &Handler{
		secret:      secret,
		store:       store,
		maxAttempts: maxAttempts,
		limiter:     newRateLimiter(limits.RatePerSecond, limits.Burst),
		now:         time.Now,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxBodyBytes {
		slog.Warn("webhook payload too large",
			"event", r.Header.Get("X-GitHub-Event"),
			"delivery_id", r.Header.Get("X-GitHub-Delivery"))
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !VerifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		// Counted for alerting; logged at Debug so a burst of bad signatures
		// cannot flood logs. RemoteAddr is omitted (it is the proxy's, not the
		// caller's, behind a TLS-terminating reverse proxy).
		metrics.WebhooksRejected.Inc()
		slog.Debug("webhook signature rejected", "event", r.Header.Get("X-GitHub-Event"))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	// Rate-limit only authenticated traffic: the check sits after signature
	// verification, so an unsigned flood is rejected as 401 without draining the
	// shared bucket and 429-ing real GitHub deliveries. Logged at Debug so a
	// flood cannot also flood the logs.
	if !h.limiter.allow(h.now()) {
		metrics.WebhooksRateLimited.Inc()
		slog.Debug("webhook rate limited", "event", r.Header.Get("X-GitHub-Event"))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	eventName := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}

	event, supported, err := ParseEvent(eventName, body)
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !supported {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("unsupported event ignored\n"))
		return
	}

	subs, err := h.store.ListActiveSubscriptionsForRepoEvent(r.Context(), event.RepoID, event.Type)
	if err != nil {
		slog.Error("webhook list subscriptions failed", "delivery_id", deliveryID, "repo", event.RepoFullName, "error", err)
		http.Error(w, "list subscriptions", http.StatusInternalServerError)
		return
	}
	// If GitHub renamed the repo, the payload carries the new name while our
	// cached one is stale; refresh it. Delivery itself already matched by the
	// stable repo id, so this only keeps the displayed name current.
	for _, sub := range subs {
		if sub.RepoFullName != event.RepoFullName {
			if err := h.store.ReconcileSubscriptionRepoName(r.Context(), event.RepoID, event.RepoFullName); err != nil {
				slog.Warn("reconcile repo name failed", "repo_id", event.RepoID, "error", err)
			}
			break
		}
	}

	var shared *notify.Notification
	var jobs []db.NotificationJobInsert
	for _, sub := range subs {
		filter := SubscriptionFilter{
			BranchMode:         sub.BranchMode,
			BranchNames:        sub.BranchNames,
			DefaultBranch:      sub.DefaultBranch,
			PullRequestActions: sub.PullRequestActions,
			ReleaseMode:        sub.ReleaseMode,
		}
		if !MatchesSubscription(filter, event) {
			continue
		}
		jobID, err := db.NewUUID()
		if err != nil {
			slog.Error("webhook job id failed", "delivery_id", deliveryID, "error", err)
			http.Error(w, "store jobs", http.StatusInternalServerError)
			return
		}
		var card notify.Notification
		var moreJSON []byte
		if event.Type == "pull_request" {
			jobEvent := event
			jobEvent.MoreJobID = db.CompactUUID(jobID)
			card = notify.GitHubNotification(jobEvent)
			raw, err := notify.PRMoreJSON(event)
			if err != nil {
				slog.Error("webhook more snapshot failed", "delivery_id", deliveryID, "error", err)
				http.Error(w, "store jobs", http.StatusInternalServerError)
				return
			}
			moreJSON = raw
		} else {
			if shared == nil {
				n := notify.GitHubNotification(event)
				shared = &n
			}
			card = *shared
		}
		jobs = append(jobs, db.NotificationJobInsert{
			ID:                jobID,
			SubscriptionID:    sub.ID,
			DestinationChatID: sub.DestinationChatID,
			Text:              card.FallbackHTML,
			RichText:          card.RichHTML,
			PayloadFormat:     db.NotificationPayloadRichHTMLV1,
			MoreJSON:          moreJSON,
		})
	}

	// Recording the delivery and enqueuing its jobs is one atomic call: a crash
	// before it commits leaves no idempotency marker, so a GitHub retry
	// re-processes cleanly instead of being silently dropped as a duplicate.
	enqueued, duplicate, err := h.store.EnqueueNotificationJobs(r.Context(), deliveryID, eventName, event.RepoFullName, jobs, h.maxAttempts)
	if err != nil {
		slog.Error("webhook enqueue jobs failed", "delivery_id", deliveryID, "repo", event.RepoFullName, "error", err)
		http.Error(w, "store jobs", http.StatusInternalServerError)
		return
	}
	if duplicate {
		metrics.WebhooksDuplicate.Inc()
		slog.Debug("webhook duplicate ignored", "delivery_id", deliveryID, "event", eventName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("duplicate delivery ignored\n"))
		return
	}
	metrics.WebhooksReceived.Inc()
	metrics.NotificationsEnqueued.Add(int64(enqueued))
	slog.Info("webhook processed",
		"delivery_id", deliveryID,
		"event", event.Type,
		"repo", event.RepoFullName,
		"matched", len(jobs),
		"enqueued", enqueued,
	)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
