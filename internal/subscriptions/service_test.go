// SPDX-License-Identifier: Apache-2.0
package subscriptions

import (
	"context"
	"errors"
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
	}, "dm", 123, []string{"push"}, "all", "")
	if err == nil {
		t.Fatal("expected archived repository validation error")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T, want ValidationError", err)
	}
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

func (failingStore) UpdateSubscriptionEvents(context.Context, int64, string, []string) error {
	return errors.New("store should not be called")
}

func (failingStore) UpdateSubscriptionBranch(context.Context, int64, string, string, string) error {
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
