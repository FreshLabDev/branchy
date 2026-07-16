// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"branchy/internal/db"
)

func TestHandlerRejectsInvalidSignatureBeforeParsing(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("secret", store, 5, Limits{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`not-json`))
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if store.listCalls != 0 || store.enqueueCalls != 0 {
		t.Fatalf("invalid signature should not touch store")
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("secret", store, 5, Limits{})

	body := strings.Repeat("a", maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-huge")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if store.listCalls != 0 || store.enqueueCalls != 0 {
		t.Fatalf("oversized body should not touch store")
	}
}

func TestHandlerRateLimitsAuthenticatedFloods(t *testing.T) {
	store := &fakeStore{}
	handler := NewHandler("secret", store, 5, Limits{RatePerSecond: 1, Burst: 2})
	now := time.Unix(1000, 0)
	handler.now = func() time.Time { return now }

	body := []byte(`{}`)
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "delivery-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send(); code == http.StatusTooManyRequests {
		t.Fatal("first request inside burst was limited")
	}
	if code := send(); code == http.StatusTooManyRequests {
		t.Fatal("second request inside burst was limited")
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want %d", code, http.StatusTooManyRequests)
	}
	now = now.Add(2 * time.Second)
	if code := send(); code == http.StatusTooManyRequests {
		t.Fatal("request after refill was limited")
	}
}

func TestHandlerRateLimitSkipsUnauthenticated(t *testing.T) {
	// The fix: the rate limiter runs after signature verification, so an unsigned
	// flood is rejected as 401 and never drains the bucket shared with real
	// GitHub deliveries.
	store := &fakeStore{}
	handler := NewHandler("secret", store, 5, Limits{RatePerSecond: 1, Burst: 1})
	now := time.Unix(1000, 0)
	handler.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`{}`))
		req.Header.Set("X-Hub-Signature-256", "sha256=bad")
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "delivery-bad")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unsigned request %d status = %d, want %d", i, rec.Code, http.StatusUnauthorized)
		}
	}

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-ok")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("an unsigned flood drained the bucket and 429'd a valid delivery")
	}
}

func TestHandlerIgnoresUnsupportedEvent(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	store := &fakeStore{}
	handler := NewHandler("secret", store, 5, Limits{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// An unsupported event is dropped before recording or enqueuing: GitHub does
	// not retry a 200, so there is nothing to dedupe.
	if store.enqueueCalls != 0 || store.listCalls != 0 {
		t.Fatalf("unsupported event should not list subscriptions or enqueue jobs")
	}
}

func TestHandlerSkipsDuplicateDelivery(t *testing.T) {
	body := pushFixture()
	store := &fakeStore{duplicate: true}
	handler := NewHandler("secret", store, 5, Limits{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// Dedup is decided atomically inside the enqueue (record + jobs in one tx),
	// so list/enqueue still run; the enqueue reports the duplicate and commits no
	// jobs.
	if store.enqueueCalls != 1 {
		t.Fatalf("enqueueCalls = %d, want 1", store.enqueueCalls)
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
	handler := NewHandler("secret", store, 7, Limits{})

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
	if store.enqueueCalls != 1 {
		t.Fatalf("enqueueCalls = %d, want 1", store.enqueueCalls)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(store.jobs))
	}
	if store.maxAttempts != 7 {
		t.Fatalf("maxAttempts = %d, want 7 (handler config should reach enqueue)", store.maxAttempts)
	}
	job := store.jobs[0]
	if job.SubscriptionID != "sub-1" || job.DestinationChatID != 123 {
		t.Fatalf("unexpected job: %+v", job)
	}
	if !strings.Contains(job.Text, "acme/repo") || !strings.Contains(job.Text, "new commit") {
		t.Fatalf("job text did not include formatted notification: %q", job.Text)
	}
	if job.PayloadFormat != db.NotificationPayloadRichHTMLV1 || job.RichText == "" {
		t.Fatalf("job did not persist versioned rich payload plus fallback: %+v", job)
	}
}

func TestHandlerReconcilesRenamedRepo(t *testing.T) {
	// The repo was renamed: the webhook payload carries the new name (acme/repo,
	// id 12345) while our cached subscription still has the old name. Delivery
	// must still match (by stable id) and the cached name must be reconciled.
	body := pushFixture()
	store := &fakeStore{subs: []db.Subscription{{
		ID:                "sub-1",
		DestinationChatID: 123,
		BranchMode:        "default",
		DefaultBranch:     "main",
		RepoFullName:      "acme/old-name",
	}}}
	handler := NewHandler("secret", store, 5, Limits{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignForTest("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(store.jobs) != 1 {
		t.Fatalf("jobs = %d, want 1 (a rename must not drop delivery)", len(store.jobs))
	}
	if store.reconcileCalls != 1 || store.reconciledName != "acme/repo" {
		t.Fatalf("reconcile = %dx name=%q; want 1, acme/repo", store.reconcileCalls, store.reconciledName)
	}
}

type fakeStore struct {
	duplicate      bool
	enqueueCalls   int
	listCalls      int
	reconcileCalls int
	reconciledName string
	deliveryID     string
	event          string
	repoFullName   string
	maxAttempts    int
	jobs           []db.NotificationJobInsert
	subs           []db.Subscription
}

func (f *fakeStore) ListActiveSubscriptionsForRepoEvent(context.Context, int64, string) ([]db.Subscription, error) {
	f.listCalls++
	return f.subs, nil
}

func (f *fakeStore) ReconcileSubscriptionRepoName(_ context.Context, _ int64, fullName string) error {
	f.reconcileCalls++
	f.reconciledName = fullName
	return nil
}

func (f *fakeStore) EnqueueNotificationJobs(_ context.Context, deliveryID, event, repoFullName string, jobs []db.NotificationJobInsert, maxAttempts int) (int, bool, error) {
	f.enqueueCalls++
	f.deliveryID = deliveryID
	f.event = event
	f.repoFullName = repoFullName
	f.maxAttempts = maxAttempts
	f.jobs = append([]db.NotificationJobInsert(nil), jobs...)
	if f.duplicate {
		return 0, true, nil
	}
	return len(jobs), false, nil
}

func pushFixture() []byte {
	return []byte(`{
		"ref":"refs/heads/main",
		"compare":"https://github.com/acme/repo/compare/a...b",
		"repository":{"id":12345,"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
		"pusher":{"name":"octocat"},
		"sender":{"login":"octocat"},
		"head_commit":{"message":"ship it","url":"https://github.com/acme/repo/commit/1"},
		"commits":[{"id":"1111111222222233333334444444555555566666","message":"ship it","url":"https://github.com/acme/repo/commit/1","author":{"username":"octocat"}}]
	}`)
}
