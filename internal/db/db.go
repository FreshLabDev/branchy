// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Several subsystems (webhook handler, outbox worker, telegram poller,
	// health checks, per-repo advisory locks) share this pool concurrently.
	// pgx defaults to a small pool; size it explicitly so a webhook burst does
	// not starve the worker or the lock connection.
	if poolCfg.MaxConns < 10 {
		poolCfg.MaxConns = 10
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) HealthStatus(ctx context.Context) (HealthStatus, error) {
	if err := s.pool.Ping(ctx); err != nil {
		return HealthStatus{}, err
	}
	var status HealthStatus
	rows, err := s.pool.Query(ctx, `
		SELECT status, count(*)
		FROM notification_jobs
		WHERE status IN ('pending', 'processing', 'failed')
		GROUP BY status
	`)
	if err != nil {
		return HealthStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return HealthStatus{}, err
		}
		switch name {
		case "pending":
			status.OutboxPending = count
		case "processing":
			status.OutboxProcessing = count
		case "failed":
			status.OutboxFailed = count
		}
	}
	return status, rows.Err()
}

func (s *Store) UpsertTelegramUser(ctx context.Context, user TelegramUser) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO telegram_users (telegram_user_id, username, first_name, last_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (telegram_user_id) DO UPDATE SET
			username = EXCLUDED.username,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			updated_at = now()
	`, user.ID, user.Username, user.FirstName, user.LastName)
	return err
}

func (s *Store) UpsertTelegramChat(ctx context.Context, chat TelegramChat) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO telegram_chats (chat_id, type, title, username, added_by_telegram_user_id, bot_status, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (chat_id) DO UPDATE SET
			type = EXCLUDED.type,
			title = EXCLUDED.title,
			username = EXCLUDED.username,
			added_by_telegram_user_id = COALESCE(EXCLUDED.added_by_telegram_user_id, telegram_chats.added_by_telegram_user_id),
			bot_status = EXCLUDED.bot_status,
			active = EXCLUDED.active,
			updated_at = now()
	`, chat.ID, chat.Type, chat.Title, chat.Username, nullableInt64(chat.AddedByUser), chat.BotStatus, chat.Active)
	return err
}

func (s *Store) ListKnownGroups(ctx context.Context, telegramUserID int64) ([]TelegramChat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT chat_id, type, title, username, bot_status, active, COALESCE(added_by_telegram_user_id, 0)
		FROM telegram_chats
		WHERE added_by_telegram_user_id = $1
		  AND active = true
		  AND type IN ('group', 'supergroup')
		ORDER BY title, chat_id
	`, telegramUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []TelegramChat
	for rows.Next() {
		var chat TelegramChat
		if err := rows.Scan(&chat.ID, &chat.Type, &chat.Title, &chat.Username, &chat.BotStatus, &chat.Active, &chat.AddedByUser); err != nil {
			return nil, err
		}
		chats = append(chats, chat)
	}
	return chats, rows.Err()
}

func (s *Store) CreateOAuthState(ctx context.Context, state OAuthState, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_states (state, telegram_user_id, code_verifier, redirect_uri, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
	`, state.State, state.TelegramUserID, state.CodeVerifier, state.RedirectURI, interval(ttl))
	return err
}

func (s *Store) ConsumeOAuthState(ctx context.Context, state string) (OAuthState, error) {
	var out OAuthState
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_states
		WHERE state = $1 AND expires_at > now()
		RETURNING state, telegram_user_id, code_verifier, redirect_uri
	`, state).Scan(&out.State, &out.TelegramUserID, &out.CodeVerifier, &out.RedirectURI)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthState{}, ErrNotFound
	}
	return out, err
}

func (s *Store) UpsertGitHubConnection(ctx context.Context, conn GitHubConnection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO github_connections (telegram_user_id, github_user_id, github_login, encrypted_access_token, token_scope)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (telegram_user_id) DO UPDATE SET
			github_user_id = EXCLUDED.github_user_id,
			github_login = EXCLUDED.github_login,
			encrypted_access_token = EXCLUDED.encrypted_access_token,
			token_scope = EXCLUDED.token_scope,
			updated_at = now()
	`, conn.TelegramUserID, conn.GitHubUserID, conn.GitHubLogin, conn.EncryptedAccessToken, conn.TokenScope)
	return err
}

func (s *Store) GetGitHubConnection(ctx context.Context, telegramUserID int64) (GitHubConnection, error) {
	var conn GitHubConnection
	err := s.pool.QueryRow(ctx, `
		SELECT telegram_user_id, github_user_id, github_login, encrypted_access_token, token_scope
		FROM github_connections
		WHERE telegram_user_id = $1
	`, telegramUserID).Scan(&conn.TelegramUserID, &conn.GitHubUserID, &conn.GitHubLogin, &conn.EncryptedAccessToken, &conn.TokenScope)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitHubConnection{}, ErrNotFound
	}
	return conn, err
}

func (s *Store) UpsertRepository(ctx context.Context, repo Repository) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO repositories (github_repo_id, full_name, owner, name, private, default_branch, html_url, has_admin_permission)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (github_repo_id) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			owner = EXCLUDED.owner,
			name = EXCLUDED.name,
			private = EXCLUDED.private,
			default_branch = EXCLUDED.default_branch,
			html_url = EXCLUDED.html_url,
			has_admin_permission = EXCLUDED.has_admin_permission,
			updated_at = now()
	`, repo.GitHubRepoID, repo.FullName, repo.Owner, repo.Name, repo.Private, repo.DefaultBranch, repo.HTMLURL, repo.HasAdminPermission)
	return err
}

// CreateSubscription inserts a subscription or, when an identical one already
// exists, reactivates it. The boolean return reports whether a new row was
// inserted (xmax = 0) versus an existing row reused, so callers can roll back
// safely without destroying a subscription the user already had.
func (s *Store) CreateSubscription(ctx context.Context, sub Subscription) (string, bool, error) {
	var id string
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO subscriptions (
			telegram_user_id, destination_type, destination_chat_id, github_repo_id,
			repo_full_name, events, branch_mode, branch_name, branch_names,
			pull_request_actions, release_mode, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'active')
		ON CONFLICT (
			telegram_user_id, destination_type, destination_chat_id, github_repo_id,
			events, branch_mode, branch_names, pull_request_actions, release_mode
		) DO UPDATE SET
			status = 'active',
			updated_at = now()
		RETURNING id::text, (xmax = 0)
	`, sub.TelegramUserID, sub.DestinationType, sub.DestinationChatID, sub.GitHubRepoID, sub.RepoFullName, sub.Events, sub.BranchMode, legacyBranchName(sub.BranchMode, sub.BranchNames), sub.BranchNames, sub.PullRequestActions, sub.ReleaseMode).Scan(&id, &inserted)
	return id, inserted, err
}

func (s *Store) ListSubscriptionsByUser(ctx context.Context, telegramUserID int64) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id::text, s.telegram_user_id, s.destination_type, s.destination_chat_id,
		       s.github_repo_id, s.repo_full_name, s.events, s.branch_mode, s.branch_name,
		       s.branch_names, s.pull_request_actions, s.release_mode,
		       s.status, s.pause_reason, r.default_branch, r.html_url
		FROM subscriptions s
		JOIN repositories r ON r.github_repo_id = s.github_repo_id
		WHERE s.telegram_user_id = $1
		ORDER BY s.created_at DESC
	`, telegramUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (s *Store) GetSubscriptionForUser(ctx context.Context, telegramUserID int64, id string) (Subscription, error) {
	var sub Subscription
	err := s.pool.QueryRow(ctx, `
		SELECT s.id::text, s.telegram_user_id, s.destination_type, s.destination_chat_id,
		       s.github_repo_id, s.repo_full_name, s.events, s.branch_mode, s.branch_name,
		       s.branch_names, s.pull_request_actions, s.release_mode,
		       s.status, s.pause_reason, r.default_branch, r.html_url
		FROM subscriptions s
		JOIN repositories r ON r.github_repo_id = s.github_repo_id
		WHERE s.telegram_user_id = $1 AND s.id = $2
	`, telegramUserID, id).Scan(
		&sub.ID, &sub.TelegramUserID, &sub.DestinationType, &sub.DestinationChatID,
		&sub.GitHubRepoID, &sub.RepoFullName, &sub.Events, &sub.BranchMode,
		&sub.BranchName, &sub.BranchNames, &sub.PullRequestActions,
		&sub.ReleaseMode, &sub.Status, &sub.PauseReason, &sub.DefaultBranch, &sub.HTMLURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return sub, err
}

func (s *Store) ListActiveSubscriptionsForRepoEvent(ctx context.Context, repoFullName, event string) ([]Subscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id::text, s.telegram_user_id, s.destination_type, s.destination_chat_id,
		       s.github_repo_id, s.repo_full_name, s.events, s.branch_mode, s.branch_name,
		       s.branch_names, s.pull_request_actions, s.release_mode,
		       s.status, s.pause_reason, r.default_branch, r.html_url
		FROM subscriptions s
		JOIN repositories r ON r.github_repo_id = s.github_repo_id
		WHERE s.repo_full_name = $1
		  AND s.status = 'active'
		  AND $2 = ANY(s.events)
	`, repoFullName, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (s *Store) ListActiveEventsForRepo(ctx context.Context, repoFullName string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT unnest(events)
		FROM subscriptions
		WHERE repo_full_name = $1 AND status = 'active'
		ORDER BY 1
	`, repoFullName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// UpdateSubscriptionStatus changes a subscription's status on the user's behalf
// and clears any pause_reason: a manual pause/resume is a user decision that
// supersedes an automatic pause, so the reason note must not linger.
func (s *Store) UpdateSubscriptionStatus(ctx context.Context, telegramUserID int64, id, status string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET status = $3, pause_reason = '', updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, status)
	return requireAffected(tag, err)
}

func (s *Store) UpdateSubscriptionEventsAndSettings(ctx context.Context, telegramUserID int64, id string, events []string, branchMode string, branchNames []string, pullRequestActions []string, releaseMode string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET events = $3,
		    branch_mode = $4,
		    branch_name = $5,
		    branch_names = $6,
		    pull_request_actions = $7,
		    release_mode = $8,
		    updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, events, branchMode, legacyBranchName(branchMode, branchNames), branchNames, pullRequestActions, releaseMode)
	return requireAffected(tag, err)
}

func (s *Store) UpdateSubscriptionBranch(ctx context.Context, telegramUserID int64, id, mode string, branchNames []string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET branch_mode = $3, branch_name = $4, branch_names = $5, updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, mode, legacyBranchName(mode, branchNames), branchNames)
	return requireAffected(tag, err)
}

func (s *Store) UpdateSubscriptionPullRequestActions(ctx context.Context, telegramUserID int64, id string, actions []string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET pull_request_actions = $3, updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, actions)
	return requireAffected(tag, err)
}

func (s *Store) UpdateSubscriptionReleaseMode(ctx context.Context, telegramUserID int64, id, releaseMode string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET release_mode = $3, updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, releaseMode)
	return requireAffected(tag, err)
}

func (s *Store) UpdateSubscriptionDestination(ctx context.Context, telegramUserID int64, id, destinationType string, chatID int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE subscriptions
		SET destination_type = $3, destination_chat_id = $4, updated_at = now()
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id, destinationType, chatID)
	return requireAffected(tag, err)
}

func (s *Store) DeleteSubscription(ctx context.Context, telegramUserID int64, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM subscriptions
		WHERE telegram_user_id = $1 AND id = $2
	`, telegramUserID, id)
	return requireAffected(tag, err)
}

func (s *Store) UpsertRepoHook(ctx context.Context, repoID int64, fullName string, hookID int64, events []string, payloadURL string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO repo_hooks (github_repo_id, full_name, hook_id, events, payload_url)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (github_repo_id) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			hook_id = EXCLUDED.hook_id,
			events = EXCLUDED.events,
			payload_url = EXCLUDED.payload_url,
			updated_at = now()
	`, repoID, fullName, hookID, events, payloadURL)
	return err
}

func (s *Store) DeleteRepoHook(ctx context.Context, repoID int64) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM repo_hooks
		WHERE github_repo_id = $1
	`, repoID)
	return err
}

func (s *Store) RecordDelivery(ctx context.Context, deliveryID, event string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries (delivery_id, event, repo_full_name)
		VALUES ($1, $2, '')
		ON CONFLICT (delivery_id) DO NOTHING
	`, deliveryID, event)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) ForgetDelivery(ctx context.Context, deliveryID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM webhook_deliveries
		WHERE delivery_id = $1
	`, deliveryID)
	return err
}

func (s *Store) EnqueueNotificationJobs(ctx context.Context, deliveryID, repoFullName string, jobs []NotificationJobInsert, maxAttempts int) (int, error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	tag, err := tx.Exec(ctx, `
		UPDATE webhook_deliveries
		SET repo_full_name = $2
		WHERE delivery_id = $1
	`, deliveryID, repoFullName)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrNotFound
	}

	insertedJobs := 0
	for _, job := range jobs {
		tag, err := tx.Exec(ctx, `
			INSERT INTO notification_jobs (delivery_id, subscription_id, destination_chat_id, text, max_attempts)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (delivery_id, subscription_id) DO NOTHING
		`, deliveryID, job.SubscriptionID, job.DestinationChatID, job.Text, maxAttempts)
		if err != nil {
			return 0, err
		}
		insertedJobs += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return insertedJobs, nil
}

func (s *Store) ClaimPendingNotificationJobs(ctx context.Context, limit int, lease time.Duration) ([]NotificationJob, error) {
	if limit <= 0 {
		limit = 10
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, `
		SELECT id::text, delivery_id, COALESCE(subscription_id::text, ''), destination_chat_id, text, attempts, max_attempts, status
		FROM notification_jobs
		WHERE (
			status = 'pending'
			AND retry_at <= now()
		) OR (
			status = 'processing'
			AND locked_until <= now()
		)
		ORDER BY retry_at, created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, err
	}
	type claimRow struct {
		job    NotificationJob
		status string
	}
	var candidates []claimRow
	for rows.Next() {
		var row claimRow
		if err := rows.Scan(&row.job.ID, &row.job.DeliveryID, &row.job.SubscriptionID, &row.job.DestinationChatID, &row.job.Text, &row.job.Attempts, &row.job.MaxAttempts, &row.status); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var jobs []NotificationJob
	for _, row := range candidates {
		// A row already in 'processing' whose lease expired means a previous
		// worker claimed it but never reported a result (it crashed or the
		// status update was lost). Count that attempt so a poison message
		// cannot be re-leased forever; mark it failed once it exhausts
		// max_attempts instead of looping.
		if row.status == "processing" {
			nextAttempts := row.job.Attempts + 1
			if nextAttempts >= row.job.MaxAttempts {
				if _, err := tx.Exec(ctx, `
					UPDATE notification_jobs
					SET status = 'failed',
					    attempts = $2,
					    locked_until = NULL,
					    last_error = 'worker lease expired before the job reported a result',
					    updated_at = now(),
					    failed_at = now()
					WHERE id = $1
				`, row.job.ID, nextAttempts); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE notification_jobs
				SET status = 'processing',
				    attempts = $2,
				    locked_until = now() + $3::interval,
				    updated_at = now()
				WHERE id = $1
			`, row.job.ID, nextAttempts, interval(lease)); err != nil {
				return nil, err
			}
			row.job.Attempts = nextAttempts
			jobs = append(jobs, row.job)
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE notification_jobs
			SET status = 'processing',
			    locked_until = now() + $2::interval,
			    updated_at = now()
			WHERE id = $1
		`, row.job.ID, interval(lease)); err != nil {
			return nil, err
		}
		jobs = append(jobs, row.job)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

// FinishNotificationJob records the result of a send attempt and reports the
// resulting terminal/retry outcome so callers (the worker) can emit metrics and
// logs without re-deriving the decision.
func (s *Store) FinishNotificationJob(ctx context.Context, job NotificationJob, result NotificationJobResult) (JobOutcome, error) {
	if result.Success {
		return OutcomeSent, requireAffected(s.pool.Exec(ctx, `
			UPDATE notification_jobs
			SET status = 'sent',
			    updated_at = now(),
			    sent_at = now(),
			    locked_until = NULL,
			    last_error = ''
			WHERE id = $1
		`, job.ID))
	}

	nextAttempts := job.Attempts + 1
	lastError := truncate(result.Error, 1000)
	if result.Temporary && nextAttempts < job.MaxAttempts {
		retryAt := result.RetryAt
		if retryAt.IsZero() {
			retryAt = time.Now().Add(defaultRetryDelay(nextAttempts))
		}
		return OutcomeRetried, requireAffected(s.pool.Exec(ctx, `
			UPDATE notification_jobs
			SET status = 'pending',
			    attempts = $2,
			    retry_at = $3,
			    locked_until = NULL,
			    last_error = $4,
			    updated_at = now()
			WHERE id = $1
		`, job.ID, nextAttempts, retryAt, lastError))
	}

	return OutcomeFailed, s.failJob(ctx, job, nextAttempts, lastError, result.DisableSubscription)
}

// failJob marks a job failed and, when the destination is permanently
// unreachable, auto-pauses the owning subscription so it stops generating jobs
// that can only fail. The failed mark is persisted first and independently: a
// later error pausing the subscription must not roll it back (a job that can
// never be delivered must stay failed, not be re-leased). The pause is scoped to
// an active subscription so a user-paused one is left exactly as the user set
// it; if it does not happen, the next failed delivery pauses it.
func (s *Store) failJob(ctx context.Context, job NotificationJob, attempts int, lastError string, disableSubscription bool) error {
	if err := requireAffected(s.pool.Exec(ctx, `
		UPDATE notification_jobs
		SET status = 'failed',
		    attempts = $2,
		    locked_until = NULL,
		    last_error = $3,
		    updated_at = now(),
		    failed_at = now()
		WHERE id = $1
	`, job.ID, attempts, lastError)); err != nil {
		return err
	}

	if disableSubscription && job.SubscriptionID != "" {
		if _, err := s.pool.Exec(ctx, `
			UPDATE subscriptions
			SET status = 'paused', pause_reason = 'telegram_blocked', updated_at = now()
			WHERE id = $1::uuid AND status = 'active'
		`, job.SubscriptionID); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) CreateCallbackToken(ctx context.Context, telegramUserID int64, token, action string, payload any, ttl time.Duration) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO callback_tokens (token, telegram_user_id, action, payload, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
	`, token, telegramUserID, action, raw, interval(ttl))
	return err
}

func (s *Store) GetCallbackToken(ctx context.Context, telegramUserID int64, token string) (CallbackToken, error) {
	var out CallbackToken
	err := s.pool.QueryRow(ctx, `
		SELECT token, telegram_user_id, action, payload
		FROM callback_tokens
		WHERE token = $1 AND telegram_user_id = $2 AND expires_at > now() AND consumed_at IS NULL
	`, token, telegramUserID).Scan(&out.Token, &out.TelegramUserID, &out.Action, &out.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return CallbackToken{}, ErrNotFound
	}
	return out, err
}

func (s *Store) ConsumeCallbackToken(ctx context.Context, telegramUserID int64, token string) (CallbackToken, error) {
	var out CallbackToken
	err := s.pool.QueryRow(ctx, `
		UPDATE callback_tokens
		SET consumed_at = now()
		WHERE token = $1
		  AND telegram_user_id = $2
		  AND expires_at > now()
		  AND consumed_at IS NULL
		RETURNING token, telegram_user_id, action, payload
	`, token, telegramUserID).Scan(&out.Token, &out.TelegramUserID, &out.Action, &out.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return CallbackToken{}, ErrNotFound
	}
	return out, err
}

func (s *Store) GetRuntimeValue(ctx context.Context, key string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `
		SELECT value FROM runtime_state WHERE key = $1
	`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

func (s *Store) SetRuntimeValue(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime_state (key, value)
		VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET
			value = EXCLUDED.value,
			updated_at = now()
	`, key, value)
	return err
}

// CleanupExpired removes state that is safe to discard so long-running
// deployments do not accumulate unbounded rows: expired OAuth states, expired
// or consumed callback tokens, old webhook delivery dedupe records, and
// terminal (sent/failed) notification jobs older than the retention window.
func (s *Store) CleanupExpired(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM oauth_states WHERE expires_at < now()`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM callback_tokens WHERE expires_at < now() OR consumed_at IS NOT NULL`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM notification_jobs WHERE status IN ('sent', 'failed') AND updated_at < now() - $1::interval`, interval(retention)); err != nil {
		return err
	}
	// Delete old delivery records, but never one that still has a non-terminal
	// job: notification_jobs cascades on delete, so purging such a delivery
	// would silently drop a job that has not been sent yet.
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM webhook_deliveries d
		WHERE d.received_at < now() - $1::interval
		  AND NOT EXISTS (
			SELECT 1 FROM notification_jobs j
			WHERE j.delivery_id = d.delivery_id
			  AND j.status IN ('pending', 'processing')
		)
	`, interval(retention)); err != nil {
		return err
	}
	return nil
}

func scanSubscriptions(rows pgx.Rows) ([]Subscription, error) {
	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(
			&sub.ID, &sub.TelegramUserID, &sub.DestinationType, &sub.DestinationChatID,
			&sub.GitHubRepoID, &sub.RepoFullName, &sub.Events, &sub.BranchMode,
			&sub.BranchName, &sub.BranchNames, &sub.PullRequestActions,
			&sub.ReleaseMode, &sub.Status, &sub.PauseReason, &sub.DefaultBranch, &sub.HTMLURL,
		); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

func requireAffected(tag pgconn.CommandTag, err error) error {
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcodeUniqueViolation {
			return ErrDuplicateConfig
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func interval(d time.Duration) string {
	seconds := int64(d.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d seconds", seconds)
}

func defaultRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 12 {
		attempts = 12
	}
	delay := time.Duration(30*(1<<(attempts-1))) * time.Second
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	// Spread synchronized retries (see outbox.jitter); this fallback path is
	// only hit when the worker did not supply an explicit retry_at.
	half := int64(delay / 2)
	if half <= 0 {
		return delay
	}
	return delay - time.Duration(half) + time.Duration(rand.Int63n(2*half+1))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

var ErrNotFound = errors.New("not found")

// ErrDuplicateConfig is returned when a write would collide with an existing
// subscription that has the exact same configuration (the unique index).
var ErrDuplicateConfig = errors.New("duplicate subscription configuration")

// pgerrcodeUniqueViolation is the PostgreSQL SQLSTATE for a unique_violation.
const pgerrcodeUniqueViolation = "23505"

var defaultPullRequestActions = []string{"opened", "merged", "closed"}

func NormalizeEvents(events []string) []string {
	allowed := map[string]bool{
		"push":         true,
		"pull_request": true,
		"release":      true,
	}
	seen := make(map[string]bool)
	var out []string
	for _, event := range events {
		event = strings.TrimSpace(event)
		if allowed[event] && !seen[event] {
			out = append(out, event)
			seen[event] = true
		}
	}
	sort.Strings(out)
	return out
}

func DefaultPullRequestActions() []string {
	return append([]string(nil), defaultPullRequestActions...)
}

func NormalizePullRequestActions(actions []string) []string {
	seen := make(map[string]bool)
	for _, action := range actions {
		seen[strings.TrimSpace(action)] = true
	}
	out := make([]string, 0, len(defaultPullRequestActions))
	for _, action := range defaultPullRequestActions {
		if seen[action] {
			out = append(out, action)
		}
	}
	return out
}

func NormalizeBranchNames(branches []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, branch := range branches {
		branch = strings.TrimSpace(branch)
		if branch == "" || seen[branch] {
			continue
		}
		out = append(out, branch)
		seen[branch] = true
	}
	sort.Strings(out)
	return out
}

func legacyBranchName(mode string, branches []string) string {
	if mode != "selected" || len(branches) == 0 {
		return ""
	}
	return branches[0]
}
