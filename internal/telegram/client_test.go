// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendRichHTMLPostsRichMessage(t *testing.T) {
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":123,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	if err := client.SendRichHTML(context.Background(), 123, "<b>hello</b>"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/sendRichMessage") {
		t.Fatalf("path = %q, want sendRichMessage", gotPath)
	}
	for _, want := range []string{`"chat_id":123`, `"html":"\u003cb\u003ehello\u003c/b\u003e"`, `"skip_entity_detection":true`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q:\n%s", want, gotBody)
		}
	}
}

func TestSendTextOmitsParseMode(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":123,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	if err := client.SendText(context.Background(), 123, "<not markup>"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "parse_mode") || !strings.Contains(gotBody, `"text":"\u003cnot markup\u003e"`) {
		t.Fatalf("plain text request unexpectedly enabled parsing:\n%s", gotBody)
	}
}

func TestSetMyCommandsSupportsEphemeralGroupScope(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	err := client.SetMyCommandsForScope(context.Background(), []BotCommand{{
		Command: "start", Description: "Open privately", IsEphemeral: true,
	}}, &BotCommandScope{Type: "all_group_chats"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"is_ephemeral":true`, `"type":"all_group_chats"`, `"command":"start"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q:\n%s", want, gotBody)
		}
	}
}

func TestSendEphemeralMessageTargetsInvokingUser(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":-100,"type":"supergroup"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	_, err := client.SendEphemeralMessage(context.Background(), -100, 42, 77, "Open in DM", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"chat_id":-100`,
		`"ephemeral_message_parameters":{"receiver_user_id":42}`,
		`"reply_parameters":{"ephemeral_message_id":77}`,
		`"link_preview_options":{"is_disabled":true}`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q:\n%s", want, gotBody)
		}
	}
	if strings.Count(gotBody, `"receiver_user_id":42`) != 1 {
		t.Fatalf("receiver_user_id should live only inside ephemeral_message_parameters:\n%s", gotBody)
	}
}

func TestTelegramErrorDoesNotExposeBotToken(t *testing.T) {
	const token = "123456:secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"server error"}`))
	}))
	defer server.Close()

	client := NewClient(token)
	client.apiBase = server.URL

	err := client.SendHTML(context.Background(), 123, "hello")
	if err == nil {
		t.Fatal("expected telegram error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "/bot") {
		t.Fatalf("error exposed bot token path: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "sendMessage") {
		t.Fatalf("error should keep the method name: %q", err.Error())
	}
}

func TestTelegramClientRetriesRateLimit(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":123,"type":"private"},"text":"hello"}}`))
	}))
	defer server.Close()

	var slept time.Duration
	client := NewClient("token")
	client.apiBase = server.URL
	client.sleep = func(_ context.Context, d time.Duration) error {
		slept = d
		return nil
	}

	if err := client.SendHTML(context.Background(), 123, "hello"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if slept != time.Second {
		t.Fatalf("slept = %s, want 1s", slept)
	}
}

func TestTelegramTransportErrorDoesNotExposeBotToken(t *testing.T) {
	const token = "123456:secret-token"
	client := NewClient(token)
	// Closed port: the http transport fails and wraps the full request URL
	// (token included) into the error message.
	client.apiBase = "http://127.0.0.1:1"
	client.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error exposed bot token: %q", err.Error())
	}
}

func TestTelegramTransportErrorKeepsCancellationCause(t *testing.T) {
	client := NewClient("token")
	client.apiBase = "http://127.0.0.1:1"
	client.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.GetMe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("redacted error lost context.Canceled: %v", err)
	}
}

func TestTelegramGetRetriesTransientFailures(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"server error"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"branchy_bot"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	client.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	me, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if me.Username != "branchy_bot" {
		t.Fatalf("username = %q", me.Username)
	}
}

func TestTelegramGetDoesNotRetryPermanentErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	client.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	if _, err := client.GetMe(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (404 must not be retried)", calls)
	}
}

func TestPollRetryDelayBacksOffAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for failures := 1; failures <= 20; failures++ {
		d := pollRetryDelay(failures)
		if d < time.Second {
			t.Fatalf("failures=%d delay=%s, want >= 1s", failures, d)
		}
		if d > 90*time.Second {
			t.Fatalf("failures=%d delay=%s, want <= 90s (60s cap + jitter)", failures, d)
		}
		_ = prev
		prev = d
	}
}

func TestTelegramRateLimitSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sleep(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep error = %v, want context.Canceled", err)
	}
}

func TestAPIErrorDetectsUnreachableDestination(t *testing.T) {
	blocked := &APIError{StatusCode: 403, Description: "Forbidden: bot was blocked by the user"}
	if !blocked.IsUnreachableDestination() {
		t.Fatal("blocked-chat 403 should be unreachable")
	}
	content := &APIError{StatusCode: 400, Description: "can't parse entities"}
	if content.IsUnreachableDestination() {
		t.Fatal("content 400 should not be treated as unreachable")
	}
	migrated := &APIError{StatusCode: 400, Description: "Bad Request", MigrateToChatID: 1}
	if !migrated.IsUnreachableDestination() {
		t.Fatal("migrate_to_chat_id should be unreachable")
	}
}
