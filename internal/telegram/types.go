// SPDX-License-Identifier: Apache-2.0
package telegram

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

type Message struct {
	MessageID          int64  `json:"message_id"`
	EphemeralMessageID int64  `json:"ephemeral_message_id"`
	From               User   `json:"from"`
	ReceiverUser       *User  `json:"receiver_user"`
	Chat               Chat   `json:"chat"`
	Text               string `json:"text"`
}

type CallbackQuery struct {
	ID      string  `json:"id"`
	From    User    `json:"from"`
	Message Message `json:"message"`
	Data    string  `json:"data"`
}

type ChatMember struct {
	Status string `json:"status"`
	User   User   `json:"user"`
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type Update struct {
	UpdateID     int64              `json:"update_id"`
	Message      *Message           `json:"message"`
	Callback     *CallbackQuery     `json:"callback_query"`
	MyChatMember *ChatMemberUpdated `json:"my_chat_member"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
	// Style colors the button (Bot API 9.4+). One of stylePrimary, styleSuccess,
	// or styleDanger; empty uses the default app style. Clients older than
	// 2026-02-09 render it as a normal button, so it degrades gracefully.
	Style string `json:"style,omitempty"`
}

// Inline button color styles (Bot API 9.4). Used sparingly: one accented call
// to action per screen, plus danger for destructive actions.
const (
	stylePrimary = "primary" // blue: proceed / save / connect
	styleSuccess = "success" // green: terminal "done" action
	styleDanger  = "danger"  // red: destructive (delete)
)
