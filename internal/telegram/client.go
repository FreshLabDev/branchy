// SPDX-License-Identifier: Apache-2.0

// Package telegram adapts the shared client to Branchy's own habits. The
// protocol, transport, retries and token redaction all live in
// github.com/FreshLabDev/tg; what stays here are the send shapes the
// notification outbox and the OAuth flow ask for, which encode Branchy's
// conventions rather than Telegram's.
package telegram

import (
	"context"

	"github.com/FreshLabDev/tg"
)

// Client is the shared client plus those shapes.
type Client struct {
	*tg.Client
}

// New builds the client Branchy uses everywhere.
func New(token string, opts ...tg.Option) *Client {
	return &Client{Client: tg.New(token, opts...)}
}

// SendHTML is the ordinary notification path.
func (c *Client) SendHTML(ctx context.Context, chatID int64, text string) error {
	_, err := c.SendMessage(ctx, chatID, text, nil)
	return err
}

// SendText is the final notification fallback. It omits parse_mode entirely,
// so malformed or newly unsupported HTML cannot prevent delivery.
func (c *Client) SendText(ctx context.Context, chatID int64, text string) error {
	_, err := c.SendPlainText(ctx, chatID, text)
	return err
}

// SendRichHTML sends a rich message. Branchy renders and sanitizes GitHub
// Markdown before this boundary; Telegram receives only the versioned Rich
// HTML stored in the notification outbox.
func (c *Client) SendRichHTML(ctx context.Context, chatID int64, richHTML string) error {
	_, err := c.Client.SendRichHTML(ctx, chatID, 0, 0, richHTML, nil)
	return err
}

// SendRichMarkdown is retained as a transport compatibility seam for legacy
// alpha jobs that were queued as Markdown.
func (c *Client) SendRichMarkdown(ctx context.Context, chatID int64, markdown string) error {
	_, err := c.Client.SendRichMarkdown(ctx, chatID, markdown, nil)
	return err
}

// SendEphemeralRichHTML delivers a rich message visible only to
// receiverUserID. callbackQueryID ties the overlay to the button tap that
// asked for it; the public card stays in place.
func (c *Client) SendEphemeralRichHTML(ctx context.Context, chatID, receiverUserID int64, callbackQueryID, richHTML string) error {
	_, err := c.Client.SendEphemeralRichHTML(ctx, chatID, receiverUserID, callbackQueryID, richHTML, nil)
	return err
}

// SendHTMLWithButton drops a caller into a bot menu without making it build
// the keyboard types. The OAuth flow is the one user.
func (c *Client) SendHTMLWithButton(ctx context.Context, chatID int64, text, buttonText, callbackData string) error {
	_, err := c.SendMessage(ctx, chatID, text, &tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: buttonText, CallbackData: callbackData}},
		},
	})
	return err
}

// AnswerCallbackQuery keeps Branchy's flag-shaped call: most answers are a
// quiet toast, a few are a modal the user has to dismiss.
func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackID, text string, alert bool) error {
	if alert {
		return c.Client.AnswerCallbackQueryAlert(ctx, callbackID, text)
	}
	return c.Client.AnswerCallbackQuery(ctx, callbackID, text)
}
