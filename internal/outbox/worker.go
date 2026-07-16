// SPDX-License-Identifier: Apache-2.0
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	"branchy/internal/db"
	"branchy/internal/metrics"
	"branchy/internal/notify"
	"branchy/internal/telegram"
)

// Config tunes the worker loop. Zero values fall back to the defaults below, so
// callers can pass an empty Config for the standard MVP behavior.
type Config struct {
	BatchSize    int
	PollInterval time.Duration
	SendTimeout  time.Duration
	Lease        time.Duration
}

type Store interface {
	ClaimPendingNotificationJobs(ctx context.Context, limit int, lease time.Duration) ([]db.NotificationJob, error)
	FinishNotificationJob(ctx context.Context, job db.NotificationJob, result db.NotificationJobResult) (db.JobOutcome, error)
}

type Sender interface {
	SendRichHTML(ctx context.Context, chatID int64, richHTML string) error
	SendHTML(ctx context.Context, chatID int64, text string) error
	SendText(ctx context.Context, chatID int64, text string) error
}

type Worker struct {
	store        Store
	sender       Sender
	batchSize    int
	pollInterval time.Duration
	sendTimeout  time.Duration
	lease        time.Duration
	lastPollUnix atomic.Int64
}

func NewWorker(store Store, sender Sender, cfg Config) *Worker {
	w := &Worker{
		store:        store,
		sender:       sender,
		batchSize:    cfg.BatchSize,
		pollInterval: cfg.PollInterval,
		sendTimeout:  cfg.SendTimeout,
		lease:        cfg.Lease,
	}
	if w.batchSize <= 0 {
		w.batchSize = 20
	}
	if w.pollInterval <= 0 {
		w.pollInterval = 2 * time.Second
	}
	if w.sendTimeout <= 0 {
		w.sendTimeout = 20 * time.Second
	}
	if w.lease <= 0 {
		w.lease = 2 * time.Minute
	}
	return w
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		jobs, err := w.store.ClaimPendingNotificationJobs(ctx, w.batchSize, w.lease)
		if err == nil {
			w.lastPollUnix.Store(time.Now().Unix())
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("notification outbox poll failed", "error", err)
		}
		if len(jobs) > 0 {
			slog.Debug("claimed notification batch", "count", len(jobs))
		}
		for _, job := range jobs {
			result := w.send(ctx, job)
			// Persist the result under a short context derived from Background,
			// not the cancelable run-loop ctx: a send that just succeeded must be
			// recorded as 'sent' even while shutdown is cancelling the loop,
			// otherwise the job stays 'processing' and is re-sent on restart (a
			// duplicate). The status fence in FinishNotificationJob keeps a late
			// write safe if another worker already finalized the job.
			finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			outcome, err := w.store.FinishNotificationJob(finishCtx, job, result)
			cancel()
			if err != nil {
				slog.Error("notification job update failed", "job_id", job.ID, "error", err)
			} else {
				recordOutcome(outcome, result)
				logOutcome(job, outcome, result)
			}
			// Stop taking new work once shutdown is signaled; the just-processed
			// job's result has already been persisted above.
			if ctx.Err() != nil {
				return nil
			}
		}
		if len(jobs) > 0 {
			continue
		}
		if err := sleep(ctx, w.pollInterval); err != nil {
			return nil
		}
	}
}

func (w *Worker) LastPoll() time.Time {
	unix := w.lastPollUnix.Load()
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func (w *Worker) send(ctx context.Context, job db.NotificationJob) db.NotificationJobResult {
	if job.PayloadFormat == db.NotificationPayloadRichMarkdownV1 {
		legacy := notify.SanitizeLegacyRichMarkdown(job.Text)
		return w.sendRichWithFallbacks(ctx, job, legacy.RichHTML, legacy.FallbackHTML)
	}
	if job.PayloadFormat != db.NotificationPayloadRichHTMLV1 || strings.TrimSpace(job.RichText) == "" {
		return w.sendHTMLWithFallback(ctx, job, job.Text)
	}
	return w.sendRichWithFallbacks(ctx, job, job.RichText, job.Text)
}

func (w *Worker) sendRichWithFallbacks(ctx context.Context, job db.NotificationJob, richHTML, fallbackHTML string) db.NotificationJobResult {
	err := w.sendAttempt(ctx, func(attemptCtx context.Context) error {
		return w.sender.SendRichHTML(attemptCtx, job.DestinationChatID, richHTML)
	})
	if err == nil {
		return db.NotificationJobResult{Success: true}
	}
	if !shouldFallbackContent(err) {
		return classifyError(err, job.Attempts+1)
	}

	withoutMedia := notify.RichHTMLWithoutMedia(richHTML)
	if withoutMedia != "" && withoutMedia != strings.TrimSpace(richHTML) {
		slog.Warn("rich notification rejected; retrying without media", "job_id", job.ID)
		err = w.sendAttempt(ctx, func(attemptCtx context.Context) error {
			return w.sender.SendRichHTML(attemptCtx, job.DestinationChatID, withoutMedia)
		})
		if err == nil {
			return db.NotificationJobResult{Success: true}
		}
		if !shouldFallbackContent(err) {
			return classifyError(err, job.Attempts+1)
		}
	}

	slog.Warn("rich notification rejected; using classic HTML fallback", "job_id", job.ID)
	return w.sendHTMLWithFallback(ctx, job, fallbackHTML)
}

func (w *Worker) sendHTMLWithFallback(ctx context.Context, job db.NotificationJob, fallbackHTML string) db.NotificationJobResult {
	err := w.sendAttempt(ctx, func(attemptCtx context.Context) error {
		return w.sender.SendHTML(attemptCtx, job.DestinationChatID, fallbackHTML)
	})
	if err == nil {
		return db.NotificationJobResult{Success: true}
	}
	if !shouldFallbackContent(err) {
		return classifyError(err, job.Attempts+1)
	}

	plain := notify.PlainTextFromHTML(fallbackHTML)
	if plain == "" {
		return classifyError(err, job.Attempts+1)
	}
	slog.Warn("classic HTML notification rejected; using plain text fallback", "job_id", job.ID)
	err = w.sendAttempt(ctx, func(attemptCtx context.Context) error {
		return w.sender.SendText(attemptCtx, job.DestinationChatID, plain)
	})
	if err == nil {
		return db.NotificationJobResult{Success: true}
	}
	return classifyError(err, job.Attempts+1)
}

func (w *Worker) sendAttempt(ctx context.Context, send func(context.Context) error) error {
	attemptCtx, cancel := context.WithTimeout(ctx, w.sendTimeout)
	defer cancel()
	return send(attemptCtx)
}

// Fallback is for content or method-availability failures only. Rate
// limits, server failures, transport errors, cancellation, and unreachable
// destinations retain normal retry/disable behavior instead of double-sending.
func shouldFallbackContent(err error) bool {
	var apiErr *telegram.APIError
	if !errors.As(err, &apiErr) || isUnreachableDestination(apiErr) {
		return false
	}
	return apiErr.StatusCode == 400 || apiErr.StatusCode == 403 || apiErr.StatusCode == 404 || apiErr.StatusCode == 413
}

func classifyError(err error, nextAttempt int) db.NotificationJobResult {
	result := db.NotificationJobResult{
		Error: err.Error(),
	}

	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 429 || apiErr.RetryAfter > 0 {
			metrics.TelegramRateLimited.Inc()
		}
		if apiErr.RetryAfter > 0 {
			result.Temporary = true
			result.RetryAt = time.Now().Add(apiErr.RetryAfter)
			return result
		}
		if apiErr.StatusCode == 429 || apiErr.StatusCode >= 500 {
			result.Temporary = true
			result.RetryAt = time.Now().Add(backoff(nextAttempt))
			return result
		}
		// A non-retryable Telegram error. If it says the destination is gone for
		// good, flag the subscription for auto-pause so it stops queuing jobs
		// that can only fail.
		result.DisableSubscription = isUnreachableDestination(apiErr)
		return result
	}

	if errors.Is(err, context.Canceled) {
		result.Temporary = true
		result.RetryAt = time.Now().Add(backoff(nextAttempt))
		return result
	}

	result.Temporary = true
	result.RetryAt = time.Now().Add(backoff(nextAttempt))
	return result
}

func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// Cap the exponent before shifting: 30s<<11 already exceeds the 15m ceiling,
	// and capping keeps the shift well clear of integer overflow even if
	// max_attempts is configured very high.
	if attempt > 12 {
		attempt = 12
	}
	delay := time.Duration(30*(1<<(attempt-1))) * time.Second
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	return jitter(delay)
}

// jitter spreads a delay across [d/2, 3d/2) so a batch of jobs that failed
// together (e.g. one Telegram outage) does not retry in a synchronized wave.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := int64(d / 2)
	return d - time.Duration(half) + time.Duration(rand.Int63n(2*half+1))
}

// isUnreachableDestination reports whether a permanent Telegram error means the
// chat can never receive messages again (blocked, kicked, deleted, deactivated),
// as opposed to a transient or content error. Matched on the human description
// because Telegram overloads 400/403 across many cases; these markers track the
// Bot API wording and may need updating if Telegram changes it. The fallback is
// safe: an unmatched permanent error just fails the job without auto-pausing.
func isUnreachableDestination(apiErr *telegram.APIError) bool {
	if apiErr.StatusCode != 400 && apiErr.StatusCode != 403 {
		return false
	}
	// A group→supergroup upgrade also makes the old chat_id permanently dead
	// (Telegram returns migrate_to_chat_id, not a fresh deliverable chat): treat
	// it as unreachable so the subscription auto-pauses instead of enqueuing jobs
	// to the dead id forever.
	if apiErr.MigrateToChatID != 0 {
		return true
	}
	desc := strings.ToLower(apiErr.Description)
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

func recordOutcome(outcome db.JobOutcome, result db.NotificationJobResult) {
	switch outcome {
	case db.OutcomeSent:
		metrics.NotificationsSent.Inc()
	case db.OutcomeRetried:
		metrics.NotificationsRetried.Inc()
	case db.OutcomeFailed:
		metrics.NotificationsFailed.Inc(failureReason(result))
		if result.DisableSubscription {
			metrics.SubscriptionsAutoPaused.Inc("telegram_blocked")
		}
	}
}

// failureReason labels a permanent failure: "exhausted" means a retryable error
// ran out of attempts, "permanent" means the error was non-retryable up front
// (e.g. the user blocked the bot).
func failureReason(result db.NotificationJobResult) string {
	if result.Temporary {
		return "exhausted"
	}
	return "permanent"
}

// logOutcome emits one structured line per send attempt. Sent is Debug (the
// happy path is high volume); retries and failures are louder because they are
// what operators act on. Error text is a Telegram description, never a token.
func logOutcome(job db.NotificationJob, outcome db.JobOutcome, result db.NotificationJobResult) {
	switch outcome {
	case db.OutcomeSkipped:
		slog.Debug("notification job already finalized; skipped stale update", "job_id", job.ID)
	case db.OutcomeSent:
		slog.Debug("notification sent", "job_id", job.ID, "chat_id", job.DestinationChatID, "attempts", job.Attempts)
	case db.OutcomeRetried:
		slog.Info("notification retry scheduled", "job_id", job.ID, "attempts", job.Attempts, "retry_at", result.RetryAt, "error", result.Error)
	case db.OutcomeFailed:
		if result.DisableSubscription {
			slog.Warn("subscription auto-paused after permanent delivery failure",
				"job_id", job.ID, "subscription_id", job.SubscriptionID, "error", result.Error)
			return
		}
		slog.Warn("notification failed", "job_id", job.ID, "attempts", job.Attempts, "reason", failureReason(result), "error", result.Error)
	}
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
