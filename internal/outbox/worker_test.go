// SPDX-License-Identifier: Apache-2.0
package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"branchy/internal/db"
	"branchy/internal/telegram"
)

func TestClassifyRateLimitUsesRetryAfter(t *testing.T) {
	err := &telegram.APIError{
		Method:     "sendMessage",
		StatusCode: httpStatusTooManyRequests,
		RetryAfter: 3 * time.Second,
	}

	result := classifyError(err, 1)

	if !result.Temporary {
		t.Fatal("expected rate limit to be temporary")
	}
	if time.Until(result.RetryAt) <= 0 {
		t.Fatalf("retry_at should be in the future: %s", result.RetryAt)
	}
}

func TestClassifyPermanentTelegramError(t *testing.T) {
	err := &telegram.APIError{
		Method:      "sendMessage",
		StatusCode:  httpStatusForbidden,
		Description: "Forbidden: bot was blocked by the user",
	}

	result := classifyError(err, 1)

	if result.Temporary {
		t.Fatal("expected 403 to be permanent")
	}
	if result.Success {
		t.Fatal("expected failed result")
	}
}

func TestClassifyBlockedDestinationDisablesSubscription(t *testing.T) {
	err := &telegram.APIError{
		Method:      "sendMessage",
		StatusCode:  httpStatusForbidden,
		Description: "Forbidden: bot was blocked by the user",
	}

	result := classifyError(err, 1)

	if result.Temporary {
		t.Fatal("expected blocked bot to be permanent")
	}
	if !result.DisableSubscription {
		t.Fatal("expected a blocked destination to disable the subscription")
	}
}

func TestClassifyContentErrorKeepsSubscription(t *testing.T) {
	// A permanent error that is about the message, not the destination, must not
	// pause the subscription.
	err := &telegram.APIError{
		Method:      "sendMessage",
		StatusCode:  400,
		Description: "Bad Request: message text is empty",
	}

	result := classifyError(err, 1)

	if result.Temporary {
		t.Fatal("expected content error to be permanent")
	}
	if result.DisableSubscription {
		t.Fatal("a content error must not disable the subscription")
	}
}

func TestClassifySupergroupMigrationDisablesSubscription(t *testing.T) {
	// A group→supergroup upgrade kills the old chat_id for good (Telegram returns
	// migrate_to_chat_id): treat it as permanent and auto-pause so it stops
	// enqueuing jobs to the dead id forever.
	err := &telegram.APIError{
		Method:          "sendMessage",
		StatusCode:      400,
		Description:     "Bad Request: group chat was upgraded to a supergroup chat",
		MigrateToChatID: -1001234567890,
	}

	result := classifyError(err, 1)

	if result.Temporary {
		t.Fatal("a supergroup migration is permanent for the old chat id")
	}
	if !result.DisableSubscription {
		t.Fatal("a supergroup migration should auto-pause the subscription")
	}
}

func TestClassifyGenericErrorAsTemporary(t *testing.T) {
	result := classifyError(errors.New("network timeout"), 2)

	if !result.Temporary {
		t.Fatal("expected generic transport error to be temporary")
	}
	if result.RetryAt.IsZero() {
		t.Fatal("expected retry_at")
	}
}

func TestBackoffJitterSpreadsAroundBase(t *testing.T) {
	// attempt 1 has a 30s base, so jitter must stay within [15s, 45s] and
	// actually vary on both sides of the base rather than being a fixed value.
	lo, hi := 15*time.Second, 45*time.Second
	sawBelow, sawAbove := false, false
	for i := 0; i < 300; i++ {
		d := backoff(1)
		if d < lo || d > hi {
			t.Fatalf("backoff(1) = %s, want within [%s, %s]", d, lo, hi)
		}
		if d < 30*time.Second {
			sawBelow = true
		}
		if d > 30*time.Second {
			sawAbove = true
		}
	}
	if !sawBelow || !sawAbove {
		t.Fatal("expected jitter to vary on both sides of the base delay")
	}
}

func TestBackoffStaysCapped(t *testing.T) {
	// A very high attempt count must not overflow or exceed the jittered ceiling.
	for i := 0; i < 200; i++ {
		d := backoff(40)
		if d < 15*time.Minute/2 || d > 15*time.Minute*3/2 {
			t.Fatalf("capped backoff = %s, want within [7m30s, 22m30s]", d)
		}
	}
}

func TestWorkerSendSuccess(t *testing.T) {
	sender := &fakeSender{}
	worker := NewWorker(nil, sender, Config{})

	result := worker.send(context.Background(), testJob())

	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if sender.sent != 1 {
		t.Fatalf("sent = %d, want 1", sender.sent)
	}
}

type fakeSender struct {
	sent int
}

func (f *fakeSender) SendRichMarkdown(context.Context, int64, string) error {
	f.sent++
	return nil
}

func testJob() db.NotificationJob {
	return db.NotificationJob{
		DestinationChatID: 123,
		Text:              "hello",
	}
}

const (
	httpStatusTooManyRequests = 429
	httpStatusForbidden       = 403
)
