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

func TestClassifyGenericErrorAsTemporary(t *testing.T) {
	result := classifyError(errors.New("network timeout"), 2)

	if !result.Temporary {
		t.Fatal("expected generic transport error to be temporary")
	}
	if result.RetryAt.IsZero() {
		t.Fatal("expected retry_at")
	}
}

func TestWorkerSendSuccess(t *testing.T) {
	sender := &fakeSender{}
	worker := NewWorker(nil, sender)

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

func (f *fakeSender) SendHTML(context.Context, int64, string) error {
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
