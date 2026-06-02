// SPDX-License-Identifier: Apache-2.0
package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteWebhookByURLDeletesMatchingHook(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/hooks":
			_, _ = w.Write([]byte(`[{"id":7,"active":true,"events":["push"],"config":{"url":"https://branchy.test/webhooks/github"}}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/acme/repo/hooks/7":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(Config{UserAgent: "test"})
	client.apiBase = server.URL

	if err := client.DeleteWebhookByURL(context.Background(), "token", "acme/repo", "https://branchy.test/webhooks/github"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("expected matching hook to be deleted")
	}
}

func TestDeleteWebhookByURLNoopsWhenHookMissing(t *testing.T) {
	var deleteCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/repo/hooks":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete:
			deleteCalls++
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(Config{UserAgent: "test"})
	client.apiBase = server.URL

	if err := client.DeleteWebhookByURL(context.Background(), "token", "acme/repo", "https://branchy.test/webhooks/github"); err != nil {
		t.Fatal(err)
	}
	if deleteCalls != 0 {
		t.Fatalf("deleteCalls = %d, want 0", deleteCalls)
	}
}
