// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"context"
	"io"
	"net/http"

	"branchy/internal/db"
	"branchy/internal/notify"
)

type Store interface {
	RecordDelivery(ctx context.Context, deliveryID, event string) (bool, error)
	ForgetDelivery(ctx context.Context, deliveryID string) error
	EnqueueNotificationJobs(ctx context.Context, deliveryID, repoFullName string, jobs []db.NotificationJobInsert) (int, error)
	ListActiveSubscriptionsForRepoEvent(ctx context.Context, repoFullName, event string) ([]db.Subscription, error)
}

type Handler struct {
	secret string
	store  Store
}

func NewHandler(secret string, store Store) *Handler {
	return &Handler{
		secret: secret,
		store:  store,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if !VerifySignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
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
		http.Error(w, "store delivery", http.StatusInternalServerError)
		return
	}
	if !inserted {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("duplicate delivery ignored\n"))
		return
	}

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

	if _, err := h.store.EnqueueNotificationJobs(r.Context(), deliveryID, event.RepoFullName, jobs); err != nil {
		_ = h.store.ForgetDelivery(r.Context(), deliveryID)
		http.Error(w, "store jobs", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
