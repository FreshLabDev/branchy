// SPDX-License-Identifier: Apache-2.0
package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListRepositoriesHidesArchivedRepositories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/repos" {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`[
			{"id":1,"full_name":"acme/active","name":"active","private":false,"archived":false,"default_branch":"main","html_url":"https://github.com/acme/active","owner":{"login":"acme"},"permissions":{"admin":true}},
			{"id":2,"full_name":"acme/archived","name":"archived","private":false,"archived":true,"default_branch":"main","html_url":"https://github.com/acme/archived","owner":{"login":"acme"},"permissions":{"admin":true}}
		]`))
	}))
	defer server.Close()

	client := NewClient(Config{UserAgent: "test"})
	client.apiBase = server.URL

	repos, err := client.ListRepositories(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %d, want 1: %+v", len(repos), repos)
	}
	if repos[0].FullName != "acme/active" || repos[0].Archived {
		t.Fatalf("unexpected repo returned: %+v", repos[0])
	}
}

func TestListRepositoriesUsesExpectedAffiliation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		affiliation := r.URL.Query().Get("affiliation")
		if !strings.Contains(affiliation, "owner") || !strings.Contains(affiliation, "organization_member") {
			t.Fatalf("unexpected affiliation query: %q", affiliation)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(Config{UserAgent: "test"})
	client.apiBase = server.URL

	if _, err := client.ListRepositories(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorClassifiesAuthFailures(t *testing.T) {
	cases := []struct {
		status int
		isAuth bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		}))

		client := NewClient(Config{UserAgent: "test"})
		client.apiBase = server.URL
		_, err := client.GetUser(context.Background(), "token")
		server.Close()

		if err == nil {
			t.Fatalf("status %d: expected an error", tc.status)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
			t.Fatalf("status %d: expected *APIError with that status, got %v", tc.status, err)
		}
		if got := IsAuthError(err); got != tc.isAuth {
			t.Fatalf("status %d: IsAuthError = %v, want %v", tc.status, got, tc.isAuth)
		}
	}
}

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
