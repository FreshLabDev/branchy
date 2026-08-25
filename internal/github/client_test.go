// SPDX-License-Identifier: Apache-2.0
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestListPullRequestFilesOmitsPatchAndStopsAfterTwoPages(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/repos/acme/repo/pulls/7/files" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		switch page {
		case "1":
			_, _ = w.Write([]byte(pullRequestFilesPageJSON(100, "a.go", "SECRET_PATCH")))
		case "2":
			_, _ = w.Write([]byte(pullRequestFilesPageJSON(100, "b.go", "SECRET_PATCH")))
		default:
			t.Fatalf("unexpected extra page %q", page)
		}
	}))
	defer server.Close()

	client := NewClient(Config{UserAgent: "test", APIURL: server.URL})
	files, err := client.ListPullRequestFiles(context.Background(), "owner-token", "acme/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 200 {
		t.Fatalf("files = %d, want 200", len(files))
	}
	if files[0].Filename != "a.go-000" || files[100].Filename != "b.go-000" {
		t.Fatalf("unexpected filenames: %+v %+v", files[0], files[100])
	}
	if files[0].PreviousFilename != "old-a.go-000" || files[0].Status != "renamed" {
		t.Fatalf("rename metadata dropped: %+v", files[0])
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("pages = %v, want [1 2]", pages)
	}
	raw, err := json.Marshal(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET_PATCH") || strings.Contains(string(raw), "patch") {
		t.Fatalf("patch must not be retained: %s", raw)
	}
}

func TestListPullRequestFilesRejectsInvalidInput(t *testing.T) {
	client := NewClient(Config{UserAgent: "test"})
	if _, err := client.ListPullRequestFiles(context.Background(), "token", "not-a-repo", 1); err == nil {
		t.Fatal("expected invalid full name error")
	}
	if _, err := client.ListPullRequestFiles(context.Background(), "token", "acme/repo", 0); err == nil {
		t.Fatal("expected invalid number error")
	}
}

func pullRequestFilesPageJSON(n int, prefix, patch string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"filename":"%s-%03d","previous_filename":"old-%s-%03d","status":"renamed","additions":%d,"deletions":1,"changes":%d,"patch":%q}`,
			prefix, i, prefix, i, i+1, i+2, patch)
	}
	b.WriteByte(']')
	return b.String()
}
