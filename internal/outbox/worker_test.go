// SPDX-License-Identifier: Apache-2.0
package outbox

import (
	"context"
	"errors"
	"strings"
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
	if sender.richSent != 1 || sender.htmlSent != 0 {
		t.Fatalf("rich/html sends = %d/%d, want 1/0", sender.richSent, sender.htmlSent)
	}
}

func TestWorkerFallsBackAfterRichContentError(t *testing.T) {
	sender := &fakeSender{richErr: &telegram.APIError{
		Method: "sendRichMessage", StatusCode: 400, Description: "Bad Request: invalid rich message",
	}}
	worker := NewWorker(nil, sender, Config{})

	result := worker.send(context.Background(), testJob())

	if !result.Success {
		t.Fatalf("expected fallback success, got %+v", result)
	}
	if sender.richSent != 1 || sender.htmlSent != 1 {
		t.Fatalf("rich/html sends = %d/%d, want 1/1", sender.richSent, sender.htmlSent)
	}
}

func TestWorkerRetriesRichWithoutMediaBeforeClassicHTML(t *testing.T) {
	contentErr := &telegram.APIError{
		Method: "sendRichMessage", StatusCode: 400, Description: "Bad Request: failed to fetch media",
	}
	sender := &fakeSender{richErrs: []error{contentErr, nil}}
	worker := NewWorker(nil, sender, Config{})
	job := testJob()
	job.RichText = `<p>hello</p><figure><img src="https://example.com/a.png"><figcaption>Screenshot</figcaption></figure>`

	result := worker.send(context.Background(), job)

	if !result.Success {
		t.Fatalf("expected media-free rich fallback success, got %+v", result)
	}
	if sender.richSent != 2 || sender.htmlSent != 0 || sender.textSent != 0 {
		t.Fatalf("rich/html/text sends = %d/%d/%d, want 2/0/0", sender.richSent, sender.htmlSent, sender.textSent)
	}
	if len(sender.richPayloads) != 2 || !strings.Contains(sender.richPayloads[0], "<img ") || strings.Contains(sender.richPayloads[1], "<img ") {
		t.Fatalf("unexpected rich fallback payloads: %#v", sender.richPayloads)
	}
	if !strings.Contains(sender.richPayloads[1], `href="https://example.com/a.png"`) {
		t.Fatalf("media-free rich fallback lost source link: %s", sender.richPayloads[1])
	}
}

func TestWorkerFallsBackFromClassicHTMLToPlainText(t *testing.T) {
	contentErr := &telegram.APIError{
		Method: "sendMessage", StatusCode: 400, Description: "Bad Request: can't parse entities",
	}
	sender := &fakeSender{htmlErr: contentErr}
	worker := NewWorker(nil, sender, Config{})
	job := testJob()
	job.PayloadFormat = db.NotificationPayloadHTMLV1
	job.RichText = ""
	job.Text = `<b>Hello</b> <a href="https://example.com">site</a>`

	result := worker.send(context.Background(), job)

	if !result.Success {
		t.Fatalf("expected plain text fallback success, got %+v", result)
	}
	if sender.htmlSent != 1 || sender.textSent != 1 {
		t.Fatalf("html/text sends = %d/%d, want 1/1", sender.htmlSent, sender.textSent)
	}
	if len(sender.textPayloads) != 1 || strings.Contains(sender.textPayloads[0], "<b>") || !strings.Contains(sender.textPayloads[0], "https://example.com") {
		t.Fatalf("unexpected plain text payload: %#v", sender.textPayloads)
	}
}

func TestWorkerGivesFallbackAttemptAFreshDeadline(t *testing.T) {
	contentErr := &telegram.APIError{
		Method: "sendRichMessage", StatusCode: 400, Description: "Bad Request: invalid rich message",
	}
	sender := &fakeSender{
		richFunc: func(ctx context.Context, _ int64, _ string) error {
			<-ctx.Done()
			return contentErr
		},
		htmlFunc: func(ctx context.Context, _ int64, _ string) error {
			return ctx.Err()
		},
	}
	worker := NewWorker(nil, sender, Config{SendTimeout: 10 * time.Millisecond})

	result := worker.send(context.Background(), testJob())

	if !result.Success {
		t.Fatalf("fallback inherited the expired rich deadline: %+v", result)
	}
}

func TestWorkerDoesNotFallbackOnRichRateLimit(t *testing.T) {
	sender := &fakeSender{richErr: &telegram.APIError{
		Method: "sendRichMessage", StatusCode: 429, Description: "Too Many Requests",
	}}
	worker := NewWorker(nil, sender, Config{})

	result := worker.send(context.Background(), testJob())

	if !result.Temporary {
		t.Fatalf("expected retry, got %+v", result)
	}
	if sender.richSent != 1 || sender.htmlSent != 0 {
		t.Fatalf("rich/html sends = %d/%d, want 1/0", sender.richSent, sender.htmlSent)
	}
}

func TestWorkerFallsBackOnMediaPermissionError(t *testing.T) {
	sender := &fakeSender{richErr: &telegram.APIError{
		Method: "sendRichMessage", StatusCode: 403, Description: "Forbidden: not enough rights to send photos",
	}}
	worker := NewWorker(nil, sender, Config{})

	result := worker.send(context.Background(), testJob())

	if !result.Success || sender.htmlSent != 1 {
		t.Fatalf("media permission error should use text-only fallback: result=%+v html=%d", result, sender.htmlSent)
	}
}

func TestWorkerSendsLegacyJobAsClassicHTML(t *testing.T) {
	sender := &fakeSender{}
	worker := NewWorker(nil, sender, Config{})
	job := testJob()
	job.PayloadFormat = db.NotificationPayloadHTMLV1
	job.RichText = ""

	result := worker.send(context.Background(), job)

	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if sender.richSent != 0 || sender.htmlSent != 1 {
		t.Fatalf("rich/html sends = %d/%d, want 0/1", sender.richSent, sender.htmlSent)
	}
}

func TestWorkerSanitizesPendingAlphaRichMarkdownJob(t *testing.T) {
	sender := &fakeSender{}
	worker := NewWorker(nil, sender, Config{})
	job := testJob()
	job.PayloadFormat = db.NotificationPayloadRichMarkdownV1
	job.RichText = ""
	job.Text = "**legacy rich markdown** <script>bad()</script>"

	result := worker.send(context.Background(), job)

	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if sender.richSent != 1 || sender.htmlSent != 0 || sender.textSent != 0 {
		t.Fatalf("rich/html/text sends = %d/%d/%d, want 1/0/0", sender.richSent, sender.htmlSent, sender.textSent)
	}
	if len(sender.richPayloads) != 1 || !strings.Contains(sender.richPayloads[0], "<b>legacy rich markdown</b>") || strings.Contains(sender.richPayloads[0], "<script") {
		t.Fatalf("legacy payload was not sanitized: %#v", sender.richPayloads)
	}
}

type fakeSender struct {
	richSent     int
	htmlSent     int
	textSent     int
	richErr      error
	htmlErr      error
	textErr      error
	richErrs     []error
	richPayloads []string
	textPayloads []string
	richFunc     func(context.Context, int64, string) error
	htmlFunc     func(context.Context, int64, string) error
}

func (f *fakeSender) SendRichHTML(ctx context.Context, chatID int64, text string) error {
	f.richSent++
	f.richPayloads = append(f.richPayloads, text)
	if f.richFunc != nil {
		return f.richFunc(ctx, chatID, text)
	}
	if len(f.richErrs) > 0 {
		err := f.richErrs[0]
		f.richErrs = f.richErrs[1:]
		return err
	}
	return f.richErr
}

func (f *fakeSender) SendHTML(ctx context.Context, chatID int64, text string) error {
	f.htmlSent++
	if f.htmlFunc != nil {
		return f.htmlFunc(ctx, chatID, text)
	}
	return f.htmlErr
}

func (f *fakeSender) SendText(_ context.Context, _ int64, text string) error {
	f.textSent++
	f.textPayloads = append(f.textPayloads, text)
	return f.textErr
}

func testJob() db.NotificationJob {
	return db.NotificationJob{
		DestinationChatID: 123,
		Text:              "hello",
		RichText:          "<p>hello</p>",
		PayloadFormat:     db.NotificationPayloadRichHTMLV1,
	}
}

const (
	httpStatusTooManyRequests = 429
	httpStatusForbidden       = 403
)
