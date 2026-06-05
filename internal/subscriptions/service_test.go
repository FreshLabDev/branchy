// SPDX-License-Identifier: Apache-2.0
package subscriptions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"branchy/internal/db"
	"branchy/internal/github"
)

func TestCreateRejectsArchivedRepositoryBeforeStoreWrite(t *testing.T) {
	service := NewService(Config{}, failingStore{}, nil, nil)

	_, err := service.Create(context.Background(), 123, github.Repository{
		FullName:           "acme/archived",
		Archived:           true,
		HasAdminPermission: true,
	}, "dm", 123, []string{"push"}, "all", nil, nil, "")
	if err == nil {
		t.Fatal("expected archived repository validation error")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T, want ValidationError", err)
	}
}

func TestNormalizeSettingsAllowsReleaseOnlyWithoutBranch(t *testing.T) {
	settings, err := normalizeSettings([]string{"release"}, "selected", []string{}, nil, "prereleases")
	if err != nil {
		t.Fatal(err)
	}
	if settings.BranchMode != "all" || len(settings.BranchNames) != 0 {
		t.Fatalf("release-only branch settings = %q/%v, want all/no branches", settings.BranchMode, settings.BranchNames)
	}
	if settings.ReleaseMode != "prereleases" {
		t.Fatalf("release mode = %q, want prereleases", settings.ReleaseMode)
	}
}

func TestNormalizeSettingsKeepsSelectedBranches(t *testing.T) {
	settings, err := normalizeSettings([]string{"push", "pull_request"}, "selected", []string{"main", "develop", "main"}, []string{"closed", "opened"}, "all")
	if err != nil {
		t.Fatal(err)
	}
	if settings.BranchMode != "selected" || !sameStrings(settings.BranchNames, []string{"develop", "main"}) {
		t.Fatalf("branch settings = %q/%v, want selected develop+main", settings.BranchMode, settings.BranchNames)
	}
	if !sameStrings(settings.PullRequestActions, []string{"opened", "closed"}) {
		t.Fatalf("pull request actions = %v, want opened+closed", settings.PullRequestActions)
	}
}

func TestTranslateWriteErrMapsDuplicateConfig(t *testing.T) {
	err := translateWriteErr(db.ErrDuplicateConfig)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T, want ValidationError", err)
	}
	if translateWriteErr(nil) != nil {
		t.Fatal("translateWriteErr(nil) should stay nil")
	}
	other := errors.New("boom")
	if !errors.Is(translateWriteErr(other), other) {
		t.Fatal("non-duplicate errors should pass through unchanged")
	}
}

func TestNormalizeSettingsRejectsEmptyPullRequestActions(t *testing.T) {
	_, err := normalizeSettings([]string{"pull_request"}, "all", nil, []string{}, "all")
	if err == nil {
		t.Fatal("expected empty pull request actions to be rejected")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T, want ValidationError", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type failingStore struct{}

func (failingStore) GetGitHubConnection(context.Context, int64) (db.GitHubConnection, error) {
	return db.GitHubConnection{}, errors.New("store should not be called")
}

func (failingStore) UpsertRepository(context.Context, db.Repository) error {
	return errors.New("store should not be called")
}

func (failingStore) CreateSubscription(context.Context, db.Subscription) (string, bool, error) {
	return "", false, errors.New("store should not be called")
}

func (failingStore) GetSubscriptionForUser(context.Context, int64, string) (db.Subscription, error) {
	return db.Subscription{}, errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionStatus(context.Context, int64, string, string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionEventsAndSettings(context.Context, int64, string, []string, string, []string, []string, string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionBranch(context.Context, int64, string, string, []string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionPullRequestActions(context.Context, int64, string, []string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionReleaseMode(context.Context, int64, string, string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionDestination(context.Context, int64, string, string, int64) error {
	return errors.New("store should not be called")
}

func (failingStore) DeleteSubscription(context.Context, int64, string) error {
	return errors.New("store should not be called")
}

func (failingStore) ListActiveEventsForRepo(context.Context, string) ([]string, error) {
	return nil, errors.New("store should not be called")
}

func (failingStore) UpsertRepoHook(context.Context, int64, string, int64, []string, string) error {
	return errors.New("store should not be called")
}

func (failingStore) DeleteRepoHook(context.Context, int64) error {
	return errors.New("store should not be called")
}

func TestTranslateGitHubErr(t *testing.T) {
	forbidden := &github.APIError{StatusCode: 403, Path: "/repos/acme/repo/hooks"}
	if err := translateGitHubErr(forbidden, "acme/repo"); err == nil {
		t.Fatal("expected a message for 403")
	} else {
		var v *ValidationError
		if !errors.As(err, &v) {
			t.Fatalf("403 should become a ValidationError, got %T", err)
		}
	}

	notFound := &github.APIError{StatusCode: 404}
	var v *ValidationError
	if !errors.As(translateGitHubErr(notFound, "acme/repo"), &v) {
		t.Fatal("404 should become a ValidationError")
	}

	// A 403 that is actually a rate limit gets a distinct, accurate message.
	rateLimited := &github.APIError{StatusCode: 403, Body: `{"message":"You have exceeded a secondary rate limit"}`}
	var rl *ValidationError
	if !errors.As(translateGitHubErr(rateLimited, "acme/repo"), &rl) {
		t.Fatal("rate-limited 403 should become a ValidationError")
	}
	if !strings.Contains(rl.Message, "rate limit") {
		t.Fatalf("rate-limit message = %q, want it to mention the rate limit", rl.Message)
	}

	// 401 must pass through unchanged so the bot can detect it and prompt a
	// reconnect rather than showing a dead-end message.
	unauthorized := &github.APIError{StatusCode: 401}
	if !github.IsAuthError(translateGitHubErr(unauthorized, "acme/repo")) {
		t.Fatal("401 must remain an auth error after translation")
	}
}
