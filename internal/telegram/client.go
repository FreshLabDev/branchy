// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	sleep   func(context.Context, time.Duration) error
}

func NewClient(token string) *Client {
	return &Client{
		token:   token,
		apiBase: "https://api.telegram.org",
		http:    &http.Client{Timeout: 30 * time.Second},
		sleep:   sleep,
	}
}

type APIError struct {
	Method      string
	StatusCode  int
	ErrorCode   int
	Description string
	RetryAfter  time.Duration
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
	if err := c.get(ctx, "getUpdates", values, &resp); err != nil {
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
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
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
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
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
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(method)+"?"+values.Encode(), nil)
		if err != nil {
			return err
		}
		retryAfter, err := c.do(method, req, out)
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

func (c *Client) post(ctx context.Context, method string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		retryAfter, err := c.do(method, req, out)
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

func (c *Client) do(method string, req *http.Request, out any) (time.Duration, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := parseAPIError(method, resp.StatusCode, resp.Body)
		return apiErr.RetryAfter, apiErr
	}
	return 0, json.NewDecoder(resp.Body).Decode(out)
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
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	description := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &payload); err == nil {
		if payload.Description != "" {
			description = payload.Description
		}
	}
	apiErr := &APIError{
		Method:      method,
		StatusCode:  statusCode,
		ErrorCode:   payload.ErrorCode,
		Description: description,
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
