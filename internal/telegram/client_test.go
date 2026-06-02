// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestTelegramRateLimitSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sleep(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep error = %v, want context.Canceled", err)
	}
}
