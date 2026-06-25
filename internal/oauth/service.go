// SPDX-License-Identifier: Apache-2.0
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"branchy/internal/db"
	"branchy/internal/github"
)

type Store interface {
	CreateOAuthState(ctx context.Context, state db.OAuthState, ttl time.Duration) error
	ConsumeOAuthState(ctx context.Context, state string) (db.OAuthState, error)
	UpsertGitHubConnection(ctx context.Context, conn db.GitHubConnection) error
}

type Notifier interface {
	SendHTML(ctx context.Context, chatID int64, text string) error
	SendHTMLWithButton(ctx context.Context, chatID int64, text, buttonText, callbackData string) error
}

type ServiceConfig struct {
	ClientID     string
	ClientSecret string
	Scope        string
	PublicBase   string
}

type Service struct {
	cfg      ServiceConfig
	store    Store
	github   *github.Client
	sealer   *TokenSealer
	notifier Notifier
}

func NewService(cfg ServiceConfig, store Store, githubClient *github.Client, sealer *TokenSealer, notifier Notifier) *Service {
	return &Service{
		cfg:      cfg,
		store:    store,
		github:   githubClient,
		sealer:   sealer,
		notifier: notifier,
	}
}

func (s *Service) CreateAuthURL(ctx context.Context, telegramUserID int64) (string, error) {
	state, err := randomURLToken(24)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return "", err
	}
	redirectURI := s.cfg.PublicBase + "/oauth/github/callback"
	if err := s.store.CreateOAuthState(ctx, db.OAuthState{
		State:          state,
		TelegramUserID: telegramUserID,
		CodeVerifier:   verifier,
		RedirectURI:    redirectURI,
	}, 15*time.Minute); err != nil {
		return "", err
	}

	challengeBytes := sha256.Sum256([]byte(verifier))
	values := url.Values{}
	values.Set("client_id", s.cfg.ClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", s.cfg.Scope)
	values.Set("state", state)
	values.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challengeBytes[:]))
	values.Set("code_challenge_method", "S256")
	return "https://github.com/login/oauth/authorize?" + values.Encode(), nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.handleCallback(r.Context(), r); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Branchy</title><p>GitHub connection failed: %s</p>", html.EscapeString(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<!doctype html><title>Branchy</title><p>GitHub connected. You can return to Telegram.</p>"))
}

func (s *Service) handleCallback(ctx context.Context, r *http.Request) error {
	// Surface GitHub's own error first: when a user declines consent, GitHub
	// redirects with error=access_denied and state but no code, so the
	// missing-code guard below would otherwise mask the real reason.
	if errValue := r.URL.Query().Get("error"); errValue != "" {
		return fmt.Errorf("%s", errValue)
	}
	code := r.URL.Query().Get("code")
	stateValue := r.URL.Query().Get("state")
	if code == "" || stateValue == "" {
		return fmt.Errorf("missing code or state")
	}

	state, err := s.store.ConsumeOAuthState(ctx, stateValue)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("expired or invalid OAuth state")
		}
		return err
	}
	token, err := s.github.ExchangeOAuthToken(ctx, code, state.RedirectURI, state.CodeVerifier)
	if err != nil {
		return err
	}
	user, err := s.github.GetUser(ctx, token.AccessToken)
	if err != nil {
		return err
	}
	encrypted, err := s.sealer.Encrypt(token.AccessToken)
	if err != nil {
		return err
	}
	if err := s.store.UpsertGitHubConnection(ctx, db.GitHubConnection{
		TelegramUserID:       state.TelegramUserID,
		GitHubUserID:         user.ID,
		GitHubLogin:          user.Login,
		EncryptedAccessToken: encrypted,
		TokenScope:           token.Scope,
	}); err != nil {
		return err
	}
	slog.Info("github connected", "telegram_user_id", state.TelegramUserID, "github_login", user.Login)
	if s.notifier != nil {
		// Send a message that drops the user straight into the (now connected)
		// main menu, so they are not left looking at a stale "not connected"
		// screen after returning from the browser.
		_ = s.notifier.SendHTMLWithButton(ctx, state.TelegramUserID,
			"Connected to GitHub as <b>"+html.EscapeString(user.Login)+"</b>.\nOpen Branchy to create a subscription.",
			"Open Branchy", "home")
	}
	return nil
}

func randomURLToken(byteLen int) (string, error) {
	raw := make([]byte, byteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
