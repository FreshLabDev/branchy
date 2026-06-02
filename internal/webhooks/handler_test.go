// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"branchy/internal/db"
)

func TestHandlerRejectsInvalidSignatureBeforeParsing(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("secret", store)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`not-json`))
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if store.recordCalls != 0 {
		t.Fatalf("invalid signature should not touch store")
	}
}

func TestHandlerIgnoresUnsupportedEvent(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	store := &fakeStore{}
	handler := NewHandler("secret", store)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.recordCalls != 1 {
		t.Fatalf("unsupported event should record delivery once, got %d", store.recordCalls)
	}
	if store.enqueueCalls != 0 || store.listCalls != 0 {
		t.Fatalf("unsupported event should not list subscriptions or enqueue jobs")
	}
}

func TestHandlerSkipsDuplicateDeliveryBeforeParsing(t *testing.T) {
	body := []byte(`not-json`)
	store := &fakeStore{
		duplicate: true,
	}
	handler := NewHandler("secret", store)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.recordCalls != 1 {
		t.Fatalf("recordCalls = %d, want 1", store.recordCalls)
	}
	if store.listCalls != 0 || store.enqueueCalls != 0 || store.forgetCalls != 0 {
		t.Fatalf("duplicate should return before parsing/list/enqueue/forget: list=%d enqueue=%d forget=%d", store.listCalls, store.enqueueCalls, store.forgetCalls)
	}
}

func TestHandlerEnqueuesMatchingSubscription(t *testing.T) {
	body := pushFixture()
	store := &fakeStore{subs: []db.Subscription{{
		ID:                "sub-1",
		DestinationChatID: 123,
		BranchMode:        "default",
		DefaultBranch:     "main",
	}}}
	handler := NewHandler("secret", store)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.deliveryID != "delivery-1" || store.event != "push" || store.repoFullName != "acme/repo" {
		t.Fatalf("unexpected delivery record: delivery=%q event=%q repo=%q", store.deliveryID, store.event, store.repoFullName)
	}
	if store.recordCalls != 1 || store.enqueueCalls != 1 {
		t.Fatalf("record/enqueue calls = %d/%d, want 1/1", store.recordCalls, store.enqueueCalls)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(store.jobs))
	}
	job := store.jobs[0]
	if job.SubscriptionID != "sub-1" || job.DestinationChatID != 123 {
		t.Fatalf("unexpected job: %+v", job)
	}
	if !strings.Contains(job.Text, "acme/repo") || !strings.Contains(job.Text, "Push") {
		t.Fatalf("job text did not include formatted notification: %q", job.Text)
	}
}

type fakeStore struct {
	duplicate    bool
	recordCalls  int
	forgetCalls  int
	enqueueCalls int
	listCalls    int
	deliveryID   string
	event        string
	repoFullName string
	jobs         []db.NotificationJobInsert
	subs         []db.Subscription
}

func (f *fakeStore) ListActiveSubscriptionsForRepoEvent(context.Context, string, string) ([]db.Subscription, error) {
	f.listCalls++
	return f.subs, nil
}

func (f *fakeStore) RecordDelivery(_ context.Context, deliveryID, event string) (bool, error) {
	f.recordCalls++
	f.deliveryID = deliveryID
	f.event = event
	if f.duplicate {
		return false, nil
	}
	return true, nil
}

func (f *fakeStore) ForgetDelivery(context.Context, string) error {
	f.forgetCalls++
	return nil
}

func (f *fakeStore) EnqueueNotificationJobs(_ context.Context, deliveryID, repoFullName string, jobs []db.NotificationJobInsert) (int, error) {
	f.enqueueCalls++
	f.deliveryID = deliveryID
	f.repoFullName = repoFullName
	f.jobs = append([]db.NotificationJobInsert(nil), jobs...)
	return len(jobs), nil
}

func pushFixture() []byte {
	return []byte(`{
		"ref":"refs/heads/main",
		"compare":"https://github.com/acme/repo/compare/a...b",
		"repository":{"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
		"pusher":{"name":"octocat"},
		"sender":{"login":"octocat"},
		"head_commit":{"message":"ship it","url":"https://github.com/acme/repo/commit/1"},
		"commits":[{}]
	}`)
}
