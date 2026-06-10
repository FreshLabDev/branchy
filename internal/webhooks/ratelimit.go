// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"sync"
	"time"
)

// rateLimiter is a token bucket: capacity `burst`, refilled at `perSecond`.
// It protects the public webhook endpoint from request floods; legitimate
// GitHub delivery bursts fit comfortably inside the default burst size.
type rateLimiter struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	tokens    float64
	last      time.Time
}

func newRateLimiter(perSecond, burst int) *rateLimiter {
	if perSecond <= 0 {
		perSecond = 30
	}
	if burst <= 0 {
		burst = 2 * perSecond
	}
	return &rateLimiter{
		perSecond: float64(perSecond),
		burst:     float64(burst),
		tokens:    float64(burst),
	}
}

func (l *rateLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() {
		l.tokens += now.Sub(l.last).Seconds() * l.perSecond
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
