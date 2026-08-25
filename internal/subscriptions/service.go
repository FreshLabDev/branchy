// SPDX-License-Identifier: Apache-2.0
package subscriptions

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/notify"
	"branchy/internal/oauth"
)

// ValidationError marks a failure caused by user input rather than a system
// fault. Callers may surface its message to the user directly; other errors
// should be logged and shown as a generic message.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(message string) error { return &ValidationError{Message: message} }

// translateWriteErr converts a duplicate-configuration database error into a
// user-facing validation message; other errors pass through unchanged.
func translateWriteErr(err error) error {
	if errors.Is(err, db.ErrDuplicateConfig) {
		return invalid("You already have a subscription with these exact settings.")
	}
	return err
}

// translateGitHubErr turns the common webhook-management failures into
// actionable user messages. A 401 is deliberately left as a *github.APIError so
// the bot can detect it (IsAuthError) and drive the reconnect flow instead.
func translateGitHubErr(err error, repoFullName string) error {
	var apiErr *github.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case 403:
		// 403 covers several distinct causes (missing admin rights, rate limit,
		// SSO enforcement), so the wording stays a hint, not an assertion.
		if strings.Contains(strings.ToLower(apiErr.Body), "rate limit") {
			return invalid("GitHub is rate limiting Branchy. Please try again in a few minutes.")
		}
		return invalid("Couldn't manage the webhook on " + repoFullName + ". Check that you have admin rights on the repository, then try again.")
	case 404:
		return invalid(repoFullName + " is no longer accessible on GitHub.")
	default:
		return err
	}
}

type Store interface {
	GetGitHubConnection(ctx context.Context, telegramUserID int64) (db.GitHubConnection, error)
	UpsertRepository(ctx context.Context, repo db.Repository) error
	CreateSubscription(ctx context.Context, sub db.Subscription) (string, bool, error)
	FindSubscriptionByConfig(ctx context.Context, sub db.Subscription) (db.Subscription, error)
	GetSubscriptionForUser(ctx context.Context, telegramUserID int64, id string) (db.Subscription, error)
	UpdateSubscriptionStatus(ctx context.Context, telegramUserID int64, id, status string) error
	RestoreSubscriptionStatus(ctx context.Context, telegramUserID int64, id, status, pauseReason string) error
	UpdateSubscriptionEventsAndSettings(ctx context.Context, telegramUserID int64, id string, events []string, branchMode string, branchNames []string, pullRequestActions []string, releaseMode string) error
	UpdateSubscriptionBranch(ctx context.Context, telegramUserID int64, id, mode string, branchNames []string) error
	UpdateSubscriptionPullRequestActions(ctx context.Context, telegramUserID int64, id string, actions []string) error
	UpdateSubscriptionReleaseMode(ctx context.Context, telegramUserID int64, id, releaseMode string) error
	UpdateSubscriptionDestination(ctx context.Context, telegramUserID int64, id, destinationType string, chatID int64) error
	DeleteSubscription(ctx context.Context, telegramUserID int64, id string) error
	RestoreSubscription(ctx context.Context, sub db.Subscription) error
	ListActiveEventsForRepo(ctx context.Context, repoID int64) ([]string, error)
	UpsertRepoHook(ctx context.Context, repoID int64, fullName string, hookID int64, events []string, payloadURL string) error
	DeleteRepoHook(ctx context.Context, repoID int64) error
}

type Sender interface {
	SendRichHTML(ctx context.Context, chatID int64, text string) error
	SendHTML(ctx context.Context, chatID int64, text string) error
}

type Config struct {
	PublicBaseURL       string
	GitHubWebhookSecret string
}

type Service struct {
	cfg       Config
	store     Store
	github    *github.Client
	sealer    *oauth.TokenSealer
	repoLocks sync.Map // githubRepoID -> *sync.Mutex
}

func NewService(cfg Config, store Store, githubClient *github.Client, sealer *oauth.TokenSealer) *Service {
	return &Service{cfg: cfg, store: store, github: githubClient, sealer: sealer}
}

// repoMutex serializes webhook synchronization per repository within this
// process. The MVP runs as a single service instance (see docs/architecture),
// so an in-process lock is sufficient and avoids pinning a database connection
// across the GitHub HTTP call. Running multiple instances would require a
// cross-process lock instead.
func (s *Service) repoMutex(repoID int64) *sync.Mutex {
	actual, _ := s.repoLocks.LoadOrStore(strconv.FormatInt(repoID, 10), &sync.Mutex{})
	return actual.(*sync.Mutex)
}

func (s *Service) Create(ctx context.Context, telegramUserID int64, repo github.Repository, destinationType string, destinationChatID int64, events []string, branchMode string, branchNames []string, pullRequestActions []string, releaseMode string) (string, error) {
	if repo.Archived {
		return "", invalid("This repository is archived, so GitHub webhooks cannot be configured.")
	}
	settings, err := normalizeSettings(events, branchMode, branchNames, pullRequestActions, releaseMode)
	if err != nil {
		return "", err
	}
	storedRepo := toDBRepo(repo)
	if err := s.store.UpsertRepository(ctx, storedRepo); err != nil {
		return "", err
	}
	requested := db.Subscription{
		TelegramUserID:     telegramUserID,
		DestinationType:    destinationType,
		DestinationChatID:  destinationChatID,
		GitHubRepoID:       storedRepo.GitHubRepoID,
		RepoFullName:       storedRepo.FullName,
		Events:             settings.Events,
		BranchMode:         settings.BranchMode,
		BranchNames:        settings.BranchNames,
		PullRequestActions: settings.PullRequestActions,
		ReleaseMode:        settings.ReleaseMode,
	}
	existing, lookupErr := s.store.FindSubscriptionByConfig(ctx, requested)
	if lookupErr != nil && !errors.Is(lookupErr, db.ErrNotFound) {
		return "", lookupErr
	}
	id, created, err := s.store.CreateSubscription(ctx, requested)
	if err != nil {
		return "", err
	}
	if err := s.syncWebhookWithRollback(ctx, telegramUserID, storedRepo.GitHubRepoID, storedRepo.FullName, func() error {
		if created {
			return s.store.DeleteSubscription(ctx, telegramUserID, id)
		}
		if lookupErr == nil {
			return s.store.RestoreSubscriptionStatus(ctx, telegramUserID, id, existing.Status, existing.PauseReason)
		}
		return nil
	}); err != nil {
		return "", err
	}
	slog.Info("subscription created", "telegram_user_id", telegramUserID, "repo", storedRepo.FullName, "events", settings.Events)
	return id, nil
}

func (s *Service) SetStatus(ctx context.Context, telegramUserID int64, id, status string) error {
	if status != "active" && status != "paused" {
		return invalid("Invalid status.")
	}
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	if err := s.store.UpdateSubscriptionStatus(ctx, telegramUserID, id, status); err != nil {
		return err
	}
	return s.syncWebhookWithRollback(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName, func() error {
		return s.store.RestoreSubscriptionStatus(ctx, telegramUserID, id, sub.Status, sub.PauseReason)
	})
}

func (s *Service) SetEvents(ctx context.Context, telegramUserID int64, id string, events []string) error {
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	settings, err := normalizeSettings(events, sub.BranchMode, sub.BranchNames, sub.PullRequestActions, sub.ReleaseMode)
	if err != nil {
		return err
	}
	if err := s.store.UpdateSubscriptionEventsAndSettings(ctx, telegramUserID, id, settings.Events, settings.BranchMode, settings.BranchNames, settings.PullRequestActions, settings.ReleaseMode); err != nil {
		return translateWriteErr(err)
	}
	return s.syncWebhookWithRollback(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName, func() error {
		return s.store.UpdateSubscriptionEventsAndSettings(ctx, telegramUserID, id,
			sub.Events, sub.BranchMode, sub.BranchNames, sub.PullRequestActions, sub.ReleaseMode)
	})
}

func (s *Service) SetBranch(ctx context.Context, telegramUserID int64, id, mode string, branchNames []string) error {
	if err := validateBranch(mode, branchNames); err != nil {
		return err
	}
	return translateWriteErr(s.store.UpdateSubscriptionBranch(ctx, telegramUserID, id, mode, normalizeBranchNamesForMode(mode, branchNames)))
}

func (s *Service) SetPullRequestActions(ctx context.Context, telegramUserID int64, id string, actions []string) error {
	actions = db.NormalizePullRequestActions(actions)
	if len(actions) == 0 {
		return invalid("Choose at least one pull request action.")
	}
	return translateWriteErr(s.store.UpdateSubscriptionPullRequestActions(ctx, telegramUserID, id, actions))
}

func (s *Service) SetReleaseMode(ctx context.Context, telegramUserID int64, id, releaseMode string) error {
	if !validReleaseMode(releaseMode) {
		return invalid("Invalid release setting.")
	}
	return translateWriteErr(s.store.UpdateSubscriptionReleaseMode(ctx, telegramUserID, id, releaseMode))
}

func (s *Service) SetDestination(ctx context.Context, telegramUserID int64, id, destinationType string, chatID int64) error {
	if destinationType != "dm" && destinationType != "group" {
		return invalid("Invalid destination.")
	}
	return translateWriteErr(s.store.UpdateSubscriptionDestination(ctx, telegramUserID, id, destinationType, chatID))
}

func (s *Service) Delete(ctx context.Context, telegramUserID int64, id string) error {
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteSubscription(ctx, telegramUserID, id); err != nil {
		return err
	}
	return s.syncWebhookWithRollback(ctx, telegramUserID, sub.GitHubRepoID, sub.RepoFullName, func() error {
		return s.store.RestoreSubscription(ctx, sub)
	})
}

func (s *Service) SendTest(ctx context.Context, sender Sender, telegramUserID int64, id string) error {
	sub, err := s.store.GetSubscriptionForUser(ctx, telegramUserID, id)
	if err != nil {
		return err
	}
	notification := notify.TestNotification(sub.RepoFullName)
	err = sender.SendRichHTML(ctx, sub.DestinationChatID, notification.RichHTML)
	if err == nil || !isContentSendError(err) {
		return err
	}
	return sender.SendHTML(ctx, sub.DestinationChatID, notification.FallbackHTML)
}

func isContentSendError(err error) bool {
	var statusErr interface {
		HTTPStatus() int
		IsUnreachableDestination() bool
	}
	if !errors.As(err, &statusErr) {
		return false
	}
	if statusErr.IsUnreachableDestination() {
		return false
	}
	switch statusErr.HTTPStatus() {
	case 400, 403, 404, 413:
		return true
	default:
		return false
	}
}

func (s *Service) EnsureWebhook(ctx context.Context, telegramUserID, repoID int64, repoFullName string) error {
	// Serialize per repository so concurrent subscription edits cannot push a
	// stale event union to GitHub and leave the hook diverged from the database.
	// The stable GitHub repository id keeps this lock valid across renames.
	mu := s.repoMutex(repoID)
	mu.Lock()
	defer mu.Unlock()

	events, err := s.store.ListActiveEventsForRepo(ctx, repoID)
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
			return translateGitHubErr(err, repoFullName)
		}
		slog.Info("repo webhook removed", "repo", repoFullName)
		return s.store.DeleteRepoHook(ctx, repoID)
	}
	sort.Strings(events)
	hook, err := s.github.EnsureWebhook(ctx, token, repoFullName, payloadURL, s.cfg.GitHubWebhookSecret, events)
	if err != nil {
		return translateGitHubErr(err, repoFullName)
	}
	slog.Info("repo webhook synced", "repo", repoFullName, "events", events)
	return s.store.UpsertRepoHook(ctx, repoID, repoFullName, hook.ID, events, payloadURL)
}

// syncWebhookWithRollback keeps a subscription mutation and the external
// GitHub hook aligned when hook management fails. The database mutation is
// compensated first, then the previous hook configuration is restored on a
// best-effort basis. A durable webhook reconciler would be needed to remove
// the unavoidable crash window between the database and GitHub calls; this
// compensation closes the normal error path without leaving a user-facing
// operation half-applied.
func (s *Service) syncWebhookWithRollback(ctx context.Context, telegramUserID, repoID int64, repoFullName string, rollback func() error) error {
	if err := s.EnsureWebhook(ctx, telegramUserID, repoID, repoFullName); err == nil {
		return nil
	} else {
		if rollbackErr := rollback(); rollbackErr != nil {
			slog.Error("subscription webhook sync rollback failed",
				"repo", repoFullName, "repo_id", repoID, "error", rollbackErr)
			return err
		}
		if restoreErr := s.EnsureWebhook(ctx, telegramUserID, repoID, repoFullName); restoreErr != nil {
			slog.Warn("previous repository webhook restore failed",
				"repo", repoFullName, "repo_id", repoID, "error", restoreErr)
		}
		return err
	}
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

type settings struct {
	Events             []string
	BranchMode         string
	BranchNames        []string
	PullRequestActions []string
	ReleaseMode        string
}

func normalizeSettings(events []string, branchMode string, branchNames []string, pullRequestActions []string, releaseMode string) (settings, error) {
	events = db.NormalizeEvents(events)
	if len(events) == 0 {
		return settings{}, invalid("Choose at least one event.")
	}
	if releaseMode == "" {
		releaseMode = "all"
	}
	if !validReleaseMode(releaseMode) {
		return settings{}, invalid("Invalid release setting.")
	}

	if pullRequestActions == nil {
		pullRequestActions = db.DefaultPullRequestActions()
	} else {
		pullRequestActions = db.NormalizePullRequestActions(pullRequestActions)
	}
	if contains(events, "pull_request") && len(pullRequestActions) == 0 {
		return settings{}, invalid("Choose at least one pull request action.")
	}

	if !usesBranchFilter(events) {
		branchMode = "all"
		branchNames = nil
	} else {
		if branchMode == "" {
			branchMode = "all"
		}
		if err := validateBranch(branchMode, branchNames); err != nil {
			return settings{}, err
		}
	}

	return settings{
		Events:             events,
		BranchMode:         branchMode,
		BranchNames:        normalizeBranchNamesForMode(branchMode, branchNames),
		PullRequestActions: pullRequestActions,
		ReleaseMode:        releaseMode,
	}, nil
}

func validateBranch(mode string, branchNames []string) error {
	switch mode {
	case "all", "default":
		return nil
	case "selected":
		if len(db.NormalizeBranchNames(branchNames)) == 0 {
			return invalid("Choose at least one branch.")
		}
		return nil
	default:
		return invalid("Invalid branch filter.")
	}
}

func normalizeBranchNamesForMode(mode string, branchNames []string) []string {
	if mode != "selected" {
		return []string{}
	}
	return db.NormalizeBranchNames(branchNames)
}

func usesBranchFilter(events []string) bool {
	return contains(events, "push") || contains(events, "pull_request")
}

func validReleaseMode(mode string) bool {
	return mode == "all" || mode == "releases" || mode == "prereleases"
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
