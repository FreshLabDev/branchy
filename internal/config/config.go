// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr            string
	DatabaseURL         string
	MigrationsDir       string
	AutoMigrate         bool
	PublicBaseURL       string
	TelegramBotToken    string
	GitHubClientID      string
	GitHubClientSecret  string
	GitHubOAuthScope    string
	GitHubWebhookSecret string
	AppSecret           string

	// Outbox worker and retention tuning. Sensible defaults keep the MVP
	// zero-config; operators raise these for higher volume.
	OutboxPollInterval      time.Duration
	OutboxBatchSize         int
	OutboxSendTimeout       time.Duration
	OutboxLease             time.Duration
	OutboxRetention         time.Duration
	NotificationMaxAttempts int

	// Upstream API timeouts and webhook endpoint limits, tunable without a
	// rebuild.
	TelegramAPITimeout time.Duration
	GitHubAPITimeout   time.Duration
	WebhookRateLimit   int
	WebhookRateBurst   int
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		MigrationsDir:       env("MIGRATIONS_DIR", "migrations"),
		AutoMigrate:         envBool("AUTO_MIGRATE", true),
		PublicBaseURL:       strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		GitHubClientID:      os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:  os.Getenv("GITHUB_CLIENT_SECRET"),
		GitHubOAuthScope:    env("GITHUB_OAUTH_SCOPE", "repo read:user"),
		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		AppSecret:           os.Getenv("APP_SECRET"),
	}

	required := map[string]string{
		"DATABASE_URL":          cfg.DatabaseURL,
		"PUBLIC_BASE_URL":       cfg.PublicBaseURL,
		"TELEGRAM_BOT_TOKEN":    cfg.TelegramBotToken,
		"GITHUB_CLIENT_ID":      cfg.GitHubClientID,
		"GITHUB_CLIENT_SECRET":  cfg.GitHubClientSecret,
		"GITHUB_WEBHOOK_SECRET": cfg.GitHubWebhookSecret,
		"APP_SECRET":            cfg.AppSecret,
	}
	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("%s is required", key)
		}
	}
	if len(cfg.AppSecret) < 32 {
		return Config{}, fmt.Errorf("APP_SECRET must be at least 32 characters")
	}

	var err error
	if cfg.OutboxPollInterval, err = envDuration("OUTBOX_POLL_INTERVAL", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OutboxBatchSize, err = envInt("OUTBOX_BATCH_SIZE", 20); err != nil {
		return Config{}, err
	}
	if cfg.OutboxSendTimeout, err = envDuration("OUTBOX_SEND_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OutboxLease, err = envDuration("OUTBOX_LEASE", 2*time.Minute); err != nil {
		return Config{}, err
	}
	retentionDays, err := envInt("OUTBOX_RETENTION_DAYS", 7)
	if err != nil {
		return Config{}, err
	}
	cfg.OutboxRetention = time.Duration(retentionDays) * 24 * time.Hour
	if cfg.NotificationMaxAttempts, err = envInt("NOTIFICATION_MAX_ATTEMPTS", 5); err != nil {
		return Config{}, err
	}
	if cfg.TelegramAPITimeout, err = envDuration("TELEGRAM_API_TIMEOUT", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.GitHubAPITimeout, err = envDuration("GITHUB_API_TIMEOUT", 20*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.WebhookRateLimit, err = envInt("WEBHOOK_RATE_LIMIT", 30); err != nil {
		return Config{}, err
	}
	if cfg.WebhookRateBurst, err = envInt("WEBHOOK_RATE_BURST", 60); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

// envDuration parses a Go duration string (e.g. "2s", "1m30s"). It must be
// positive; a malformed or non-positive value fails startup rather than
// silently falling back, so a typo is caught loudly.
func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return d, nil
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return n, nil
}
