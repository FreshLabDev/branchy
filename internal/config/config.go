// SPDX-License-Identifier: Apache-2.0
package config

import (
	"fmt"
	"os"
	"strings"
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
