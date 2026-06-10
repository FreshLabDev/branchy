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
	RecordDelivery(ctx context.Context, deliveryID, event string) (bool, error)
	ForgetDelivery(ctx context.Context, deliveryID string) error
	EnqueueNotificationJobs(ctx context.Context, deliveryID, repoFullName string, jobs []db.NotificationJobInsert, maxAttempts int) (int, error)
	ListActiveSubscriptionsForRepoEvent(ctx context.Context, repoFullName, event string) ([]db.Subscription, error)
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
	if !h.limiter.allow(h.now()) {
		// Counted for alerting; logged at Debug so a flood cannot also flood
		// the logs.
		metrics.WebhooksRateLimited.Inc()
		slog.Debug("webhook rate limited", "event", r.Header.Get("X-GitHub-Event"))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
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

	eventName := r.Header.Get("X-GitHub-Event")
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		http.Error(w, "missing delivery id", http.StatusBadRequest)
		return
	}
	inserted, err := h.store.RecordDelivery(r.Context(), deliveryID, eventName)
	if err != nil {
		slog.Error("webhook record delivery failed", "delivery_id", deliveryID, "error", err)
		http.Error(w, "store delivery", http.StatusInternalServerError)
		return
	}
	if !inserted {
		metrics.WebhooksDuplicate.Inc()
		slog.Debug("webhook duplicate ignored", "delivery_id", deliveryID, "event", eventName)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("duplicate delivery ignored\n"))
		return
	}
	metrics.WebhooksReceived.Inc()

	event, supported, err := ParseEvent(eventName, body)
	if err != nil {
		_ = h.store.ForgetDelivery(r.Context(), deliveryID)
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !supported {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("unsupported event ignored\n"))
		return
	}

	subs, err := h.store.ListActiveSubscriptionsForRepoEvent(r.Context(), event.RepoFullName, event.Type)
	if err != nil {
		slog.Error("webhook list subscriptions failed", "delivery_id", deliveryID, "repo", event.RepoFullName, "error", err)
		_ = h.store.ForgetDelivery(r.Context(), deliveryID)
		http.Error(w, "list subscriptions", http.StatusInternalServerError)
		return
	}

	text := notify.GitHubEvent(event)
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
		jobs = append(jobs, db.NotificationJobInsert{
			SubscriptionID:    sub.ID,
			DestinationChatID: sub.DestinationChatID,
			Text:              text,
		})
	}

	enqueued, err := h.store.EnqueueNotificationJobs(r.Context(), deliveryID, event.RepoFullName, jobs, h.maxAttempts)
	if err != nil {
		slog.Error("webhook enqueue jobs failed", "delivery_id", deliveryID, "repo", event.RepoFullName, "error", err)
		_ = h.store.ForgetDelivery(r.Context(), deliveryID)
		http.Error(w, "store jobs", http.StatusInternalServerError)
		return
	}
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
