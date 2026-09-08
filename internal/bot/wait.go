// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"math/rand"
	"time"
)

// The poll loop backs off on its own schedule, separately from the retries the
// Telegram client does inside one request.

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

// jitterDuration spreads a delay across [d/2, 3d/2) so that instances which
// failed together do not retry in a synchronized wave.
func jitterDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := int64(d / 2)
	return d - time.Duration(half) + time.Duration(rand.Int63n(2*half+1))
}
