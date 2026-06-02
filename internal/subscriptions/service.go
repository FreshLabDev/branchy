// SPDX-License-Identifier: Apache-2.0
package subscriptions

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/notify"
	"branchy/internal/oauth"
)

type Store interface {
	GetGitHubConnection(ctx context.Context, telegramUserID int64) (db.GitHubConnection, error)
	UpsertRepository(ctx context.Context, repo db.Repository) error
	CreateSubscription(ctx context.Context, sub db.Subscription) (string, error)
	GetSubscriptionForUser(ctx context.Context, telegramUserID int64, id string) (db.Subscription, error)
	UpdateSubscriptionStatus(ctx context.Context, telegramUserID int64, id, status string) error
	UpdateSubscriptionEvents(ctx context.Context, telegramUserID int64, id string, events []string) error
	UpdateSubscriptionBranch(ctx context.Context, telegramUserID int64, id, mode, branchName string) error
	UpdateSubscriptionDestination(ctx context.Context, telegramUserID int64, id, destinationType string, chatID int64) error
	DeleteSubscription(ctx context.Context, telegramUserID int64, id string) error
	ListActiveEventsForRepo(ctx context.Context, repoFullName string) ([]string, error)
	UpsertRepoHook(ctx context.Context, repoID int64, fullName string, hookID int64, events []string, payloadURL string) error
	DeleteRepoHook(ctx context.Context, repoID int64) error
}

type Sender interface {
	SendHTML(ctx context.Context, chatID int64, text string) error
}

type Config struct {
	PublicBaseURL       string
	GitHubWebhookSecret string
}

type Service struct {
	cfg    Config
	store  Store
	github *github.Client
	sealer *oauth.TokenSealer
}

func NewService(cfg Config, store Store, githubClient *github.Client, sealer *oauth.TokenSealer) *Service {
	return &Service{cfg: cfg, store: store, github: githubClient, sealer: sealer}
}

func (s *Service) Create(ctx context.Context, telegramUserID int64, repo github.Repository, destinationType string, destinationChatID int64, events []string, branchMode, branchName string) (string, error) {
	events = db.NormalizeEvents(events)
	if len(events) == 0 {
		return "", fmt.Errorf("choose at least one event")
	}
	if err := validateBranch(branchMode, branchName); err != nil {
		return "", err
	}
	storedRepo := toDBRepo(repo)
	if err := s.store.UpsertRepository(ctx, storedRepo); err != nil {
		return "", err
	}
	id, err := s.store.CreateSubscription(ctx, db.Subscription{
		TelegramUserID:    telegramUserID,
		DestinationType:   destinationType,
		DestinationChatID: destinationChatID,
		GitHubRepoID:      storedRepo.GitHubRepoID,
		RepoFullName:      storedRepo.FullName,
		Events:            events,
		BranchMode:        branchMode,
		BranchName:        branchName,
	})
	if err != nil {
		return "", err
	}
	if err := s.EnsureWebhook(ctx, telegramUserID, storedRepo.GitHubRepoID, storedRepo.FullName); err != nil {
		_ = s.store.DeleteSubscription(ctx, telegramUserID, id)
		return "", err
	}
	return id, nil
}

func (s *Service) SetStatus(ctx context.Context, telegramUserID int64, id, status string) error {
	if status != "active" && status != "paused" {
		return fmt.Errorf("invalid status")
	}
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	if err := s.store.UpdateSubscriptionStatus(ctx, telegramUserID, id, status); err != nil {
		return err
	}
	return s.EnsureWebhook(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName)
}

func (s *Service) SetEvents(ctx context.Context, telegramUserID int64, id string, events []string) error {
	events = db.NormalizeEvents(events)
	if len(events) == 0 {
		return fmt.Errorf("choose at least one event")
	}
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	if err := s.store.UpdateSubscriptionEvents(ctx, telegramUserID, id, events); err != nil {
		return err
	}
	return s.EnsureWebhook(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName)
}

func (s *Service) SetBranch(ctx context.Context, telegramUserID int64, id, mode, branchName string) error {
	if err := validateBranch(mode, branchName); err != nil {
		return err
	}
	return s.store.UpdateSubscriptionBranch(ctx, telegramUserID, id, mode, branchName)
}

func (s *Service) SetDestination(ctx context.Context, telegramUserID int64, id, destinationType string, chatID int64) error {
	if destinationType != "dm" && destinationType != "group" {
		return fmt.Errorf("invalid destination")
	}
	return s.store.UpdateSubscriptionDestination(ctx, telegramUserID, id, destinationType, chatID)
}

func (s *Service) Delete(ctx context.Context, telegramUserID int64, id string) error {
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteSubscription(ctx, telegramUserID, id); err != nil {
		return err
	}
	return s.EnsureWebhook(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName)
}

func (s *Service) SendTest(ctx context.Context, sender Sender, telegramUserID int64, id string) error {
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	return sender.SendHTML(ctx, sub.DestinationChatID, notify.TestMessage(sub.RepoFullName))
}

func (s *Service) EnsureWebhook(ctx context.Context, telegramUserID, repoID int64, repoFullName string) error {
	events, err := s.store.ListActiveEventsForRepo(ctx, repoFullName)
	if err != nil {
		return err
	}
	conn, err := s.store.GetGitHubConnection(ctx, telegramUserID)
	if err != nil {
		return err
	}
	token, err := s.sealer.Decrypt(conn.EncryptedAccessToken)
	if err != nil {
		return err
	}
	payloadURL := strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/webhooks/github"
	if len(events) == 0 {
		if err := s.github.DeleteWebhookByURL(ctx, token, repoFullName, payloadURL); err != nil {
			return err
		}
		return s.store.DeleteRepoHook(ctx, repoID)
	}
	sort.Strings(events)
	hook, err := s.github.EnsureWebhook(ctx, token, repoFullName, payloadURL, s.cfg.GitHubWebhookSecret, events)
	if err != nil {
		return err
	}
	return s.store.UpsertRepoHook(ctx, repoID, repoFullName, hook.ID, events, payloadURL)
}

func toDBRepo(repo github.Repository) db.Repository {
	return db.Repository{
		GitHubRepoID:       repo.ID,
		FullName:           repo.FullName,
		Owner:              repo.Owner,
		Name:               repo.Name,
		Private:            repo.Private,
		DefaultBranch:      repo.DefaultBranch,
		HTMLURL:            repo.HTMLURL,
		HasAdminPermission: repo.HasAdminPermission,
	}
}

func validateBranch(mode, branchName string) error {
	switch mode {
	case "all", "default":
		return nil
	case "selected":
		if strings.TrimSpace(branchName) == "" {
			return fmt.Errorf("choose a branch")
		}
		return nil
	default:
		return fmt.Errorf("invalid branch filter")
	}
}
