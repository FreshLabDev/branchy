// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	token   string
	apiBase string
	http    *http.Client
	timeout time.Duration
	sleep   func(context.Context, time.Duration) error
}

// Option configures optional Client behavior.
type Option func(*Client)

// WithTimeout sets the per-request deadline for regular API calls. The
// long-polling getUpdates call derives its own, longer deadline from the poll
// duration, so this value does not need to cover it.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

func NewClient(token string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		apiBase: "https://api.telegram.org",
		// No global http.Client timeout: each attempt carries its own context
		// deadline, and a single fixed value cannot fit both quick calls and
		// the getUpdates long poll.
		http:    &http.Client{},
		timeout: 30 * time.Second,
		sleep:   sleep,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type APIError struct {
	Method      string
	StatusCode  int
	ErrorCode   int
	Description string
	RetryAfter  time.Duration
	// MigrateToChatID is set when Telegram reports that a group was upgraded to a
	// supergroup (parameters.migrate_to_chat_id): the old chat_id is dead and
	// this is the chat_id to use instead.
	MigrateToChatID int64
}

func (e *APIError) Error() string {
	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = "request failed"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram %s failed: %s (retry after %s)", e.Method, description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram %s failed: %s", e.Method, description)
}

// HTTPStatus returns the HTTP status of a Telegram API error so callers can
// classify content vs transport failures without importing this package's
// concrete type.
func (e *APIError) HTTPStatus() int { return e.StatusCode }

// IsUnreachableDestination reports a permanent Telegram error that means the
// chat can never receive messages again (blocked, kicked, deleted, deactivated).
// Matched on the human description because Telegram overloads 400/403 across
// many cases. Callers can duck-type this method without importing this package.
func (e *APIError) IsUnreachableDestination() bool {
	if e == nil || (e.StatusCode != 400 && e.StatusCode != 403) {
		return false
	}
	if e.MigrateToChatID != 0 {
		return true
	}
	desc := strings.ToLower(e.Description)
	for _, marker := range []string{
		"bot was blocked",
		"user is deactivated",
		"bot was kicked",
		"bot is not a member",
		"chat not found",
		"group chat was deleted",
		"group chat was upgraded to a supergroup",
		"need administrator rights",
	} {
		if strings.Contains(desc, marker) {
			return true
		}
	}
	return false
}

// IsMessageNotModified reports whether err is Telegram's benign "message is not
// modified" response, which happens whenever an inline-keyboard tap re-renders
// identical text (a toggle that yields the same state, or a double tap). It
// should be treated as success so the bot does not post a duplicate message.
func IsMessageNotModified(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return strings.Contains(strings.ToLower(apiErr.Description), "message is not modified")
	}
	return false
}

type Me struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func (c *Client) GetMe(ctx context.Context) (Me, error) {
	var resp struct {
		OK     bool `json:"ok"`
		Result Me   `json:"result"`
	}
	if err := c.get(ctx, "getMe", url.Values{}, &resp); err != nil {
		return Me{}, err
	}
	if !resp.OK {
		return Me{}, fmt.Errorf("telegram getMe returned ok=false")
	}
	return resp.Result, nil
}

// BotCommand is one entry in the bot's command menu (the blue "/" menu in
// Telegram clients), registered via SetMyCommands.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	IsEphemeral bool   `json:"is_ephemeral,omitempty"`
}

type BotCommandScope struct {
	Type string `json:"type"`
}

// SetMyCommands registers the bot's command list so the commands are
// discoverable in Telegram's "/" menu instead of users having to know them.
func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	return c.SetMyCommandsForScope(ctx, commands, nil)
}

func (c *Client) SetMyCommandsForScope(ctx context.Context, commands []BotCommand, scope *BotCommandScope) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	req := map[string]any{"commands": commands}
	if scope != nil {
		req["scope"] = scope
	}
	if err := c.post(ctx, "setMyCommands", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram setMyCommands returned ok=false")
	}
	return nil
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	values := url.Values{}
	values.Set("timeout", strconv.Itoa(timeoutSeconds))
	values.Set("allowed_updates", `["message","callback_query","my_chat_member"]`)
	if offset > 0 {
		values.Set("offset", strconv.FormatInt(offset, 10))
	}
	var resp struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	// The deadline must outlive the server-side long poll plus headroom for the
	// handshake and response transfer; a flat client timeout would cut the
	// request off mid-poll.
	reqTimeout := time.Duration(timeoutSeconds)*time.Second + 15*time.Second
	if err := c.getWithTimeout(ctx, "getUpdates", values, &resp, reqTimeout); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	return resp.Result, nil
}

func (c *Client) SendHTML(ctx context.Context, chatID int64, text string) error {
	_, err := c.SendMessage(ctx, chatID, text, nil)
	return err
}

// SendText is the final notification fallback. It intentionally omits
// parse_mode so malformed or newly unsupported HTML cannot prevent delivery.
func (c *Client) SendText(ctx context.Context, chatID int64, text string) error {
	req := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.post(ctx, "sendMessage", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram sendMessage returned ok=false")
	}
	return nil
}

// SendRichHTML sends a Bot API 10.1+ rich message. Branchy renders and
// sanitizes GitHub Markdown before this boundary; Telegram receives only the
// versioned Rich HTML stored in the notification outbox.
func (c *Client) SendRichHTML(ctx context.Context, chatID int64, richHTML string) error {
	req := map[string]any{
		"chat_id": chatID,
		"rich_message": map[string]any{
			"html":                  richHTML,
			"skip_entity_detection": true,
		},
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.post(ctx, "sendRichMessage", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram sendRichMessage returned ok=false")
	}
	return nil
}

// SendRichMarkdown is retained as a transport compatibility seam. The current
// worker sanitizes legacy alpha jobs into Rich HTML before reaching the client.
func (c *Client) SendRichMarkdown(ctx context.Context, chatID int64, markdown string) error {
	req := map[string]any{
		"chat_id": chatID,
		"rich_message": map[string]any{
			"markdown":              markdown,
			"skip_entity_detection": true,
		},
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.post(ctx, "sendRichMessage", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram sendRichMessage returned ok=false")
	}
	return nil
}

// SendHTMLWithButton sends an HTML message with a single inline button bound to
// a static callback action. It is used by decoupled callers (e.g. the OAuth
// flow) that need to drop the user into a bot menu without importing the
// Telegram UI types.
func (c *Client) SendHTMLWithButton(ctx context.Context, chatID int64, text, buttonText, callbackData string) error {
	_, err := c.SendMessage(ctx, chatID, text, &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: buttonText, CallbackData: callbackData}},
		},
	})
	return err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (Message, error) {
	req := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if markup != nil {
		req["reply_markup"] = markup
	}
	var resp struct {
		OK     bool    `json:"ok"`
		Result Message `json:"result"`
	}
	if err := c.post(ctx, "sendMessage", req, &resp); err != nil {
		return Message{}, err
	}
	if !resp.OK {
		return Message{}, fmt.Errorf("telegram sendMessage returned ok=false")
	}
	return resp.Result, nil
}

// SendEphemeralMessage replies to a Bot API 10.2+ ephemeral group command. The
// response is visible only to the invoking user and must reference the
// ephemeral command message Telegram delivered to the bot.
func (c *Client) SendEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, text string, markup *InlineKeyboardMarkup) (Message, error) {
	req := map[string]any{
		"chat_id": chatID,
		"ephemeral_message_parameters": map[string]any{
			"receiver_user_id": receiverUserID,
		},
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_parameters": map[string]any{
			"ephemeral_message_id": ephemeralMessageID,
		},
	}
	if markup != nil {
		req["reply_markup"] = markup
	}
	var resp struct {
		OK     bool    `json:"ok"`
		Result Message `json:"result"`
	}
	if err := c.post(ctx, "sendMessage", req, &resp); err != nil {
		return Message{}, err
	}
	if !resp.OK {
		return Message{}, fmt.Errorf("telegram sendMessage returned ok=false")
	}
	return resp.Result, nil
}

func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string, markup *InlineKeyboardMarkup) error {
	req := map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if markup != nil {
		req["reply_markup"] = markup
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.post(ctx, "editMessageText", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram editMessageText returned ok=false")
	}
	return nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string, alert bool) error {
	req := map[string]any{
		"callback_query_id": callbackID,
		"show_alert":        alert,
	}
	if text != "" {
		req["text"] = text
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.post(ctx, "answerCallbackQuery", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram answerCallbackQuery returned ok=false")
	}
	return nil
}

func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (ChatMember, error) {
	req := map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}
	var resp struct {
		OK     bool       `json:"ok"`
		Result ChatMember `json:"result"`
	}
	if err := c.post(ctx, "getChatMember", req, &resp); err != nil {
		return ChatMember{}, err
	}
	if !resp.OK {
		return ChatMember{}, fmt.Errorf("telegram getChatMember returned ok=false")
	}
	return resp.Result, nil
}

func (c *Client) get(ctx context.Context, method string, values url.Values, out any) error {
	return c.getWithTimeout(ctx, method, values, out, c.timeout)
}

// getWithTimeout performs an idempotent GET with retries: transport failures
// (DNS, connect, reset) and Telegram 429/5xx responses are retried with a
// short jittered backoff, honoring Retry-After when Telegram provides one.
// POSTs are not retried this way because a lost response would double-send.
func (c *Client) getWithTimeout(ctx context.Context, method string, values url.Values, out any, timeout time.Duration) error {
	const maxAttempts = 4
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		retryAfter, err := c.attempt(ctx, timeout, func(attemptCtx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(attemptCtx, http.MethodGet, c.endpoint(method)+"?"+values.Encode(), nil)
		}, method, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil || attempt == maxAttempts || !retryableError(err) {
			return err
		}
		delay := retryAfter
		if delay <= 0 {
			delay = retryDelay(attempt)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return lastErr
		}
	}
	return lastErr
}

func (c *Client) post(ctx context.Context, method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		retryAfter, err := c.attempt(ctx, c.timeout, func(attemptCtx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.endpoint(method), bytes.NewReader(raw))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		}, method, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if retryAfter <= 0 || attempt == 1 {
			return err
		}
		if err := c.sleep(ctx, retryAfter); err != nil {
			return err
		}
	}
	return lastErr
}

// attempt runs one request under its own deadline. The response body is fully
// consumed before the deadline is released.
func (c *Client) attempt(ctx context.Context, timeout time.Duration, build func(context.Context) (*http.Request, error), method string, out any) (time.Duration, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := build(attemptCtx)
	if err != nil {
		return 0, c.redactError(err)
	}
	return c.do(method, req, out)
}

func (c *Client) do(method string, req *http.Request, out any) (time.Duration, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, c.redactError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAPIError(method, resp.StatusCode, resp.Body)
		return apiErr.RetryAfter, apiErr
	}
	return 0, json.NewDecoder(resp.Body).Decode(out)
}

// redactError rewrites a request/transport error so its message never contains
// the bot token: net/http and net/url errors embed the full request URL, which
// includes the /bot<TOKEN>/ path segment. The underlying cause stays wrapped so
// errors.Is/As checks (e.g. context.Canceled) keep working, but the URL-bearing
// wrapper itself is dropped from the chain.
func (c *Client) redactError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if c.token != "" {
		msg = strings.ReplaceAll(msg, c.token, "***")
	}
	cause := err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
	}
	return &transportError{msg: msg, cause: cause}
}

type transportError struct {
	msg   string
	cause error
}

func (e *transportError) Error() string { return e.msg }
func (e *transportError) Unwrap() error { return e.cause }

// retryableError reports whether an idempotent request is worth retrying:
// Telegram 429/5xx responses and transport-level failures qualify; a canceled
// caller context never does (the retry loop checks the caller's ctx
// separately, so an expired per-attempt deadline still retries).
func retryableError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	return !errors.Is(err, context.Canceled)
}

// retryDelay backs off 500ms, 1s, 2s (jittered): transient hiccups recover
// fast and three retries stay well inside one long-poll cycle.
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 3 {
		attempt = 3
	}
	return jitterDuration(500 * time.Millisecond << (attempt - 1))
}

// jitterDuration spreads a delay across [d/2, 3d/2) so callers that failed
// together do not retry in a synchronized wave.
func jitterDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := int64(d / 2)
	return d - time.Duration(half) + time.Duration(rand.Int63n(2*half+1))
}

func (c *Client) endpoint(method string) string {
	return strings.TrimRight(c.apiBase, "/") + "/bot" + c.token + "/" + method
}

func parseAPIError(method string, statusCode int, body io.Reader) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	var payload struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	description := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &payload); err == nil {
		if payload.Description != "" {
			description = payload.Description
		}
	}
	apiErr := &APIError{
		Method:          method,
		StatusCode:      statusCode,
		ErrorCode:       payload.ErrorCode,
		Description:     description,
		MigrateToChatID: payload.Parameters.MigrateToChatID,
	}
	if payload.Parameters.RetryAfter > 0 {
		apiErr.RetryAfter = time.Duration(payload.Parameters.RetryAfter) * time.Second
	}
	return apiErr
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
