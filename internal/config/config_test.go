// SPDX-License-Identifier: Apache-2.0
package config

import (
	"testing"
	"time"
)

func TestEnvDuration(t *testing.T) {
	t.Setenv("X_DUR", "1m30s")
	d, err := envDuration("X_DUR", time.Second)
	if err != nil || d != 90*time.Second {
		t.Fatalf("envDuration = %s, %v; want 1m30s, nil", d, err)
	}

	if d, err := envDuration("X_DUR_UNSET", 2*time.Second); err != nil || d != 2*time.Second {
		t.Fatalf("fallback = %s, %v; want 2s, nil", d, err)
	}

	t.Setenv("X_DUR_BAD", "nope")
	if _, err := envDuration("X_DUR_BAD", time.Second); err == nil {
		t.Fatal("expected error for malformed duration")
	}

	t.Setenv("X_DUR_NEG", "-5s")
	if _, err := envDuration("X_DUR_NEG", time.Second); err == nil {
		t.Fatal("expected error for non-positive duration")
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("X_INT", "42")
	if n, err := envInt("X_INT", 1); err != nil || n != 42 {
		t.Fatalf("envInt = %d, %v; want 42, nil", n, err)
	}

	if n, err := envInt("X_INT_UNSET", 9); err != nil || n != 9 {
		t.Fatalf("fallback = %d, %v; want 9, nil", n, err)
	}

	t.Setenv("X_INT_BAD", "1.5")
	if _, err := envInt("X_INT_BAD", 1); err == nil {
		t.Fatal("expected error for malformed int")
	}

	t.Setenv("X_INT_ZERO", "0")
	if _, err := envInt("X_INT_ZERO", 1); err == nil {
		t.Fatal("expected error for non-positive int")
	}
}

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/branchy")
	t.Setenv("PUBLIC_BASE_URL", "https://example.test")
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("GITHUB_CLIENT_ID", "id")
	t.Setenv("GITHUB_CLIENT_SECRET", "secret")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "hook")
	t.Setenv("APP_SECRET", "this-is-a-sufficiently-long-app-secret")
}

func TestLoadOutboxDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OutboxPollInterval != 2*time.Second {
		t.Errorf("OutboxPollInterval = %s, want 2s", cfg.OutboxPollInterval)
	}
	if cfg.OutboxBatchSize != 20 {
		t.Errorf("OutboxBatchSize = %d, want 20", cfg.OutboxBatchSize)
	}
	if cfg.OutboxRetention != 7*24*time.Hour {
		t.Errorf("OutboxRetention = %s, want 168h", cfg.OutboxRetention)
	}
	if cfg.NotificationMaxAttempts != 5 {
		t.Errorf("NotificationMaxAttempts = %d, want 5", cfg.NotificationMaxAttempts)
	}
}

func TestLoadOutboxOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("OUTBOX_POLL_INTERVAL", "5s")
	t.Setenv("OUTBOX_BATCH_SIZE", "50")
	t.Setenv("OUTBOX_RETENTION_DAYS", "30")
	t.Setenv("NOTIFICATION_MAX_ATTEMPTS", "8")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OutboxPollInterval != 5*time.Second {
		t.Errorf("OutboxPollInterval = %s, want 5s", cfg.OutboxPollInterval)
	}
	if cfg.OutboxBatchSize != 50 {
		t.Errorf("OutboxBatchSize = %d, want 50", cfg.OutboxBatchSize)
	}
	if cfg.OutboxRetention != 30*24*time.Hour {
		t.Errorf("OutboxRetention = %s, want 720h", cfg.OutboxRetention)
	}
	if cfg.NotificationMaxAttempts != 8 {
		t.Errorf("NotificationMaxAttempts = %d, want 8", cfg.NotificationMaxAttempts)
	}
}

func TestLoadRejectsBadOutboxValue(t *testing.T) {
	setRequired(t)
	t.Setenv("OUTBOX_BATCH_SIZE", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected Load to reject a negative batch size")
	}
}

func TestLoadRejectsMalformedPublicBaseURL(t *testing.T) {
	for _, bad := range []string{"branchy.example.com", "ftp://example.test", "/relative/path", "not a url"} {
		setRequired(t)
		t.Setenv("PUBLIC_BASE_URL", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("expected Load to reject PUBLIC_BASE_URL=%q", bad)
		}
	}
}

func TestLoadAcceptsValidPublicBaseURL(t *testing.T) {
	for _, good := range []string{"https://branchy.example.com", "http://localhost:8080", "https://example.test/base"} {
		setRequired(t)
		t.Setenv("PUBLIC_BASE_URL", good)
		if _, err := Load(); err != nil {
			t.Fatalf("PUBLIC_BASE_URL=%q should be accepted, got %v", good, err)
		}
	}
}
