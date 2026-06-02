// SPDX-License-Identifier: Apache-2.0
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"branchy/internal/db"
	"branchy/internal/telegram"
)

type Store interface {
	ClaimPendingNotificationJobs(ctx context.Context, limit int, lease time.Duration) ([]db.NotificationJob, error)
	FinishNotificationJob(ctx context.Context, job db.NotificationJob, result db.NotificationJobResult) error
}

type Sender interface {
	SendHTML(ctx context.Context, chatID int64, text string) error
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

func NewWorker(store Store, sender Sender) *Worker {
	return &Worker{
		store:        store,
		sender:       sender,
		batchSize:    20,
		pollInterval: 2 * time.Second,
		sendTimeout:  20 * time.Second,
		lease:        2 * time.Minute,
	}
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
		for _, job := range jobs {
			result := w.send(ctx, job)
			if err := w.store.FinishNotificationJob(ctx, job, result); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Error("notification job update failed", "job_id", job.ID, "error", err)
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
	sendCtx, cancel := context.WithTimeout(ctx, w.sendTimeout)
	defer cancel()

	err := w.sender.SendHTML(sendCtx, job.DestinationChatID, job.Text)
	if err == nil {
		return db.NotificationJobResult{Success: true}
	}
	return classifyError(err, job.Attempts+1)
}

func classifyError(err error, nextAttempt int) db.NotificationJobResult {
	result := db.NotificationJobResult{
		Error: err.Error(),
	}

	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) {
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
	delay := time.Duration(30*(1<<(attempt-1))) * time.Second
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
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
