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
