// SPDX-License-Identifier: Apache-2.0
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const apiBase = "https://api.github.com"

type Config struct {
	ClientID     string
	ClientSecret string
	UserAgent    string
}

type Client struct {
	cfg     Config
	apiBase string
	http    *http.Client
}

func NewClient(cfg Config) *Client {
	if cfg.UserAgent == "" {
		cfg.UserAgent = "branchy"
	}
	return &Client{
		cfg:     cfg,
		apiBase: apiBase,
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

type OAuthToken struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type Repository struct {
	ID                 int64
	FullName           string
	Owner              string
	Name               string
	Private            bool
	DefaultBranch      string
	HTMLURL            string
	HasAdminPermission bool
}

type Branch struct {
	Name string `json:"name"`
}

type Hook struct {
	ID     int64    `json:"id"`
	Events []string `json:"events"`
	Active bool     `json:"active"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

func (c *Client) ExchangeOAuthToken(ctx context.Context, code, redirectURI, codeVerifier string) (OAuthToken, error) {
	body := map[string]string{
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
		"code_verifier": codeVerifier,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return OAuthToken{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", bytes.NewReader(raw))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	var token OAuthToken
	if err := c.doJSON(req, &token); err != nil {
		return OAuthToken{}, err
	}
	if token.AccessToken == "" {
		return OAuthToken{}, fmt.Errorf("github oauth returned empty access token")
	}
	return token, nil
}

func (c *Client) GetUser(ctx context.Context, accessToken string) (User, error) {
	req, err := c.apiRequest(ctx, http.MethodGet, "/user", accessToken, nil)
	if err != nil {
		return User{}, err
	}
	var user User
	if err := c.doJSON(req, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (c *Client) ListRepositories(ctx context.Context, accessToken string) ([]Repository, error) {
	var repos []Repository
	for page := 1; ; page++ {
		path := fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=full_name&affiliation=owner,collaborator,organization_member", page)
		req, err := c.apiRequest(ctx, http.MethodGet, path, accessToken, nil)
		if err != nil {
			return nil, err
		}
		var batch []struct {
			ID            int64  `json:"id"`
			FullName      string `json:"full_name"`
			Name          string `json:"name"`
			Private       bool   `json:"private"`
			DefaultBranch string `json:"default_branch"`
			HTMLURL       string `json:"html_url"`
			Owner         struct {
				Login string `json:"login"`
			} `json:"owner"`
			Permissions struct {
				Admin bool `json:"admin"`
			} `json:"permissions"`
		}
		if err := c.doJSON(req, &batch); err != nil {
			return nil, err
		}
		for _, item := range batch {
			repos = append(repos, Repository{
				ID:                 item.ID,
				FullName:           item.FullName,
				Owner:              item.Owner.Login,
				Name:               item.Name,
				Private:            item.Private,
				DefaultBranch:      item.DefaultBranch,
				HTMLURL:            item.HTMLURL,
				HasAdminPermission: item.Permissions.Admin,
			})
		}
		if len(batch) < 100 {
			break
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	return repos, nil
}

func (c *Client) ListBranches(ctx context.Context, accessToken, fullName string) ([]Branch, error) {
	owner, repo, err := splitFullName(fullName)
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repo), page)
		req, err := c.apiRequest(ctx, http.MethodGet, path, accessToken, nil)
		if err != nil {
			return nil, err
		}
		var batch []Branch
		if err := c.doJSON(req, &batch); err != nil {
			return nil, err
		}
		branches = append(branches, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return branches, nil
}

func (c *Client) EnsureWebhook(ctx context.Context, accessToken, fullName, payloadURL, secret string, events []string) (Hook, error) {
	owner, repo, err := splitFullName(fullName)
	if err != nil {
		return Hook{}, err
	}
	events = normalizeEvents(events)
	if len(events) == 0 {
		events = []string{"push"}
	}

	hooksPath := fmt.Sprintf("/repos/%s/%s/hooks", url.PathEscape(owner), url.PathEscape(repo))
	req, err := c.apiRequest(ctx, http.MethodGet, hooksPath+"?per_page=100", accessToken, nil)
	if err != nil {
		return Hook{}, err
	}
	var hooks []Hook
	if err := c.doJSON(req, &hooks); err != nil {
		return Hook{}, err
	}
	for _, hook := range hooks {
		if hook.Config.URL == payloadURL {
			return c.updateHook(ctx, accessToken, owner, repo, hook.ID, payloadURL, secret, events)
		}
	}
	return c.createHook(ctx, accessToken, owner, repo, payloadURL, secret, events)
}

func (c *Client) DeleteWebhookByURL(ctx context.Context, accessToken, fullName, payloadURL string) error {
	owner, repo, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	hook, found, err := c.findHookByURL(ctx, accessToken, owner, repo, payloadURL)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(repo), hook.ID)
	req, err := c.apiRequest(ctx, http.MethodDelete, path, accessToken, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

func (c *Client) createHook(ctx context.Context, accessToken, owner, repo, payloadURL, secret string, events []string) (Hook, error) {
	body := hookRequest(payloadURL, secret, events)
	raw, err := json.Marshal(body)
	if err != nil {
		return Hook{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/hooks", url.PathEscape(owner), url.PathEscape(repo))
	req, err := c.apiRequest(ctx, http.MethodPost, path, accessToken, bytes.NewReader(raw))
	if err != nil {
		return Hook{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var hook Hook
	return hook, c.doJSON(req, &hook)
}

func (c *Client) updateHook(ctx context.Context, accessToken, owner, repo string, hookID int64, payloadURL, secret string, events []string) (Hook, error) {
	body := hookRequest(payloadURL, secret, events)
	raw, err := json.Marshal(body)
	if err != nil {
		return Hook{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/hooks/%d", url.PathEscape(owner), url.PathEscape(repo), hookID)
	req, err := c.apiRequest(ctx, http.MethodPatch, path, accessToken, bytes.NewReader(raw))
	if err != nil {
		return Hook{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var hook Hook
	return hook, c.doJSON(req, &hook)
}

func (c *Client) findHookByURL(ctx context.Context, accessToken, owner, repo, payloadURL string) (Hook, bool, error) {
	hooksPath := fmt.Sprintf("/repos/%s/%s/hooks", url.PathEscape(owner), url.PathEscape(repo))
	req, err := c.apiRequest(ctx, http.MethodGet, hooksPath+"?per_page=100", accessToken, nil)
	if err != nil {
		return Hook{}, false, err
	}
	var hooks []Hook
	if err := c.doJSON(req, &hooks); err != nil {
		return Hook{}, false, err
	}
	for _, hook := range hooks {
		if hook.Config.URL == payloadURL {
			return hook, true, nil
		}
	}
	return Hook{}, false, nil
}

func hookRequest(payloadURL, secret string, events []string) map[string]any {
	return map[string]any{
		"name":   "web",
		"active": true,
		"events": events,
		"config": map[string]string{
			"url":          payloadURL,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
	}
}

func (c *Client) apiRequest(ctx context.Context, method, path, accessToken string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.apiBase, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func splitFullName(fullName string) (string, string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository full name: %q", fullName)
	}
	return parts[0], parts[1], nil
}

func normalizeEvents(events []string) []string {
	allowed := map[string]bool{
		"push":         true,
		"pull_request": true,
		"release":      true,
	}
	seen := make(map[string]bool)
	var out []string
	for _, event := range events {
		if allowed[event] && !seen[event] {
			out = append(out, event)
			seen[event] = true
		}
	}
	sort.Strings(out)
	return out
}
