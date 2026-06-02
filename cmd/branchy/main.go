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
	"syscall"
	"time"

	"branchy/internal/config"
	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/oauth"
	"branchy/internal/outbox"
	"branchy/internal/subscriptions"
	"branchy/internal/telegram"
	"branchy/internal/webhooks"
)

func main() {
	if err := run(); err != nil {
		slog.Error("branchy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
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
	})
	tg := telegram.NewClient(cfg.TelegramBotToken)
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
	notificationWorker := outbox.NewWorker(store, tg)
	hookHandler := webhooks.NewHandler(cfg.GitHubWebhookSecret, store)
	startedAt := time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		serveHealth(w, r, store, bot.LastPoll, notificationWorker.LastPoll, startedAt)
	})
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

	errCh := make(chan error, 3)
	go func() {
		slog.Info("http server starting", "addr", cfg.HTTPAddr)
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	go func() {
		slog.Info("telegram polling starting")
		errCh <- bot.Run(ctx)
	}()
	go func() {
		slog.Info("notification outbox worker starting")
		errCh <- notificationWorker.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err != nil {
			stop()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
			return err
		}
		return nil
	}
}

type healthResponse struct {
	OK                   bool       `json:"ok"`
	DB                   bool       `json:"db"`
	OutboxPending        int64      `json:"outbox_pending"`
	OutboxProcessing     int64      `json:"outbox_processing"`
	OutboxFailed         int64      `json:"outbox_failed"`
	TelegramPollingFresh bool       `json:"telegram_polling_fresh"`
	TelegramLastPollAt   *time.Time `json:"telegram_last_poll_at,omitempty"`
	OutboxWorkerFresh    bool       `json:"outbox_worker_fresh"`
	OutboxWorkerLastPoll *time.Time `json:"outbox_worker_last_poll_at,omitempty"`
	Error                string     `json:"error,omitempty"`
}

type healthStore interface {
	HealthStatus(ctx context.Context) (db.HealthStatus, error)
}

func serveHealth(w http.ResponseWriter, r *http.Request, store healthStore, telegramLastPoll func() time.Time, workerLastPoll func() time.Time, startedAt time.Time) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := healthResponse{}
	status, err := store.HealthStatus(ctx)
	if err != nil {
		resp.Error = err.Error()
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
