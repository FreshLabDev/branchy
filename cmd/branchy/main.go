// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"branchy/internal/config"
	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/metrics"
	"branchy/internal/oauth"
	"branchy/internal/outbox"
	"branchy/internal/subscriptions"
	"branchy/internal/telegram"
	"branchy/internal/webhooks"
)

// Build information, stamped at link time via -ldflags "-X main.version=...".
// They keep their "dev" defaults for a plain `go build`/`go run`, so a developer
// build is always distinguishable from a released one in logs and /healthz.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("branchy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	slog.Info("branchy starting", "version", version, "commit", commit, "built", date)

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.AutoMigrate {
		if err := db.RunMigrations(ctx, pool, cfg.MigrationsDir); err != nil {
			return err
		}
	}

	store := db.NewStore(pool)
	gh := github.NewClient(github.Config{
		ClientID:     cfg.GitHubClientID,
		ClientSecret: cfg.GitHubClientSecret,
		UserAgent:    "branchy-mvp",
		Timeout:      cfg.GitHubAPITimeout,
	})
	tg := telegram.NewClient(cfg.TelegramBotToken, telegram.WithTimeout(cfg.TelegramAPITimeout))
	sealer := oauth.NewTokenSealer(cfg.AppSecret)
	oauthSvc := oauth.NewService(oauth.ServiceConfig{
		ClientID:     cfg.GitHubClientID,
		ClientSecret: cfg.GitHubClientSecret,
		Scope:        cfg.GitHubOAuthScope,
		PublicBase:   cfg.PublicBaseURL,
	}, store, gh, sealer, tg)
	subSvc := subscriptions.NewService(subscriptions.Config{
		PublicBaseURL:       cfg.PublicBaseURL,
		GitHubWebhookSecret: cfg.GitHubWebhookSecret,
	}, store, gh, sealer)
	bot := telegram.NewBot(store, tg, oauthSvc, gh, sealer, subSvc)
	notificationWorker := outbox.NewWorker(store, tg, outbox.Config{
		BatchSize:    cfg.OutboxBatchSize,
		PollInterval: cfg.OutboxPollInterval,
		SendTimeout:  cfg.OutboxSendTimeout,
		Lease:        cfg.OutboxLease,
	})
	hookHandler := webhooks.NewHandler(cfg.GitHubWebhookSecret, store, cfg.NotificationMaxAttempts, webhooks.Limits{
		RatePerSecond: cfg.WebhookRateLimit,
		Burst:         cfg.WebhookRateBurst,
	})

	startedAt := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		serveHealth(w, r, store, bot.LastPoll, notificationWorker.LastPoll, startedAt)
	})
	mux.Handle("GET /metrics", metrics.Handler())
	mux.Handle("GET /oauth/github/callback", oauthSvc)
	mux.Handle("POST /webhooks/github", hookHandler)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("http server starting", "addr", cfg.HTTPAddr)
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("telegram polling starting")
		if err := bot.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("notification outbox worker starting")
		if err := notificationWorker.Run(ctx); err != nil {
			errCh <- err
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("maintenance cleaner starting", "retention", cfg.OutboxRetention)
		runCleaner(ctx, store, cfg.OutboxRetention)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		ensureTelegramCommands(ctx, tg, 30*time.Second)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}
	// If a shutdown signal won the race against a fatal goroutine error (e.g. a
	// bind failure buffered the same instant a SIGTERM arrived), drain it so the
	// real failure is still reported instead of a misleading clean exit.
	if runErr == nil {
		select {
		case runErr = <-errCh:
		default:
		}
	}

	// Cancel the signal context so the polling and worker loops observe it,
	// shut the HTTP server, then wait for every goroutine to return before the
	// deferred pool.Close() runs. This avoids closing the pool while a
	// subsystem is still mid-query.
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = server.Shutdown(shutdownCtx)
	cancel()
	wg.Wait()
	return runErr
}

type commandRegistrar interface {
	SetMyCommandsForScope(ctx context.Context, commands []telegram.BotCommand, scope *telegram.BotCommandScope) error
}

// ensureTelegramCommands runs independently from HTTP serving and polling.
// Each scope is retried until it succeeds, so a transient first-deploy failure
// cannot leave group /start public or unavailable until the next restart.
func ensureTelegramCommands(ctx context.Context, registrar commandRegistrar, retryBase time.Duration) {
	if retryBase <= 0 {
		retryBase = 30 * time.Second
	}
	privateReady := false
	groupReady := false
	delay := retryBase
	for !privateReady || !groupReady {
		if ctx.Err() != nil {
			return
		}
		if !privateReady {
			commands := []telegram.BotCommand{{Command: "start", Description: "Open the Branchy menu"}}
			attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := registrar.SetMyCommandsForScope(attemptCtx, commands, &telegram.BotCommandScope{Type: "all_private_chats"})
			cancel()
			if err != nil {
				slog.Warn("set private telegram commands failed; will retry", "error", err, "retry_in", delay)
			} else {
				privateReady = true
			}
		}
		if !groupReady {
			commands := []telegram.BotCommand{{
				Command: "start", Description: "Open Branchy privately", IsEphemeral: true,
			}}
			attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := registrar.SetMyCommandsForScope(attemptCtx, commands, &telegram.BotCommandScope{Type: "all_group_chats"})
			cancel()
			if err != nil {
				slog.Warn("set group telegram commands failed; will retry", "error", err, "retry_in", delay)
			} else {
				groupReady = true
			}
		}
		if privateReady && groupReady {
			return
		}
		if err := sleepContext(ctx, delay); err != nil {
			return
		}
		if delay < 5*time.Minute {
			delay *= 2
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// runCleaner periodically removes expired and terminal state so a long-running
// deployment does not accumulate unbounded rows.
func runCleaner(ctx context.Context, store *db.Store, retention time.Duration) {
	clean := func() {
		if err := store.CleanupExpired(ctx, retention); err != nil && ctx.Err() == nil {
			slog.Warn("state cleanup failed", "error", err)
		}
	}
	clean()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			clean()
		}
	}
}

type healthResponse struct {
	OK                   bool       `json:"ok"`
	Version              string     `json:"version"`
	DB                   bool       `json:"db"`
	OutboxPending        int64      `json:"outbox_pending"`
	OutboxProcessing     int64      `json:"outbox_processing"`
	OutboxFailed         int64      `json:"outbox_failed"`
	TelegramPollingFresh bool       `json:"telegram_polling_fresh"`
	TelegramLastPollAt   *time.Time `json:"telegram_last_poll_at,omitempty"`
	OutboxWorkerFresh    bool       `json:"outbox_worker_fresh"`
	OutboxWorkerLastPoll *time.Time `json:"outbox_worker_last_poll_at,omitempty"`
}

type healthStore interface {
	HealthStatus(ctx context.Context) (db.HealthStatus, error)
}

func serveHealth(w http.ResponseWriter, r *http.Request, store healthStore, telegramLastPoll func() time.Time, workerLastPoll func() time.Time, startedAt time.Time) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := healthResponse{Version: version}
	status, err := store.HealthStatus(ctx)
	if err != nil {
		// Log the detail server-side; do not leak DB error strings to the
		// public endpoint that shares a port with the webhook/OAuth routes.
		slog.Error("health db check failed", "error", err)
	} else {
		resp.DB = true
		resp.OutboxPending = status.OutboxPending
		resp.OutboxProcessing = status.OutboxProcessing
		resp.OutboxFailed = status.OutboxFailed
	}
	resp.TelegramLastPollAt = optionalTime(telegramLastPoll())
	resp.OutboxWorkerLastPoll = optionalTime(workerLastPoll())
	resp.TelegramPollingFresh = fresh(startedAt, telegramLastPoll(), 90*time.Second)
	resp.OutboxWorkerFresh = fresh(startedAt, workerLastPoll(), 90*time.Second)
	resp.OK = resp.DB && resp.TelegramPollingFresh && resp.OutboxWorkerFresh

	w.Header().Set("Content-Type", "application/json")
	if !resp.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func fresh(startedAt, lastSeen time.Time, maxAge time.Duration) bool {
	if lastSeen.IsZero() {
		return time.Since(startedAt) < maxAge
	}
	return time.Since(lastSeen) < maxAge
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
