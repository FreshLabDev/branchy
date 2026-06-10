// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstThenDenies(t *testing.T) {
	limiter := newRateLimiter(10, 5)
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		if !limiter.allow(now) {
			t.Fatalf("request %d inside burst denied", i+1)
		}
	}
	if limiter.allow(now) {
		t.Fatal("request beyond burst allowed")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	limiter := newRateLimiter(10, 5)
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		limiter.allow(now)
	}
	if limiter.allow(now) {
		t.Fatal("bucket should be empty")
	}
	// 10 tokens/s -> 200ms refills two tokens.
	now = now.Add(200 * time.Millisecond)
	if !limiter.allow(now) {
		t.Fatal("token should have refilled")
	}
	if !limiter.allow(now) {
		t.Fatal("second refilled token missing")
	}
	if limiter.allow(now) {
		t.Fatal("third request should be denied again")
	}
}

func TestRateLimiterDefaults(t *testing.T) {
	limiter := newRateLimiter(0, 0)
	if limiter.perSecond != 30 || limiter.burst != 60 {
		t.Fatalf("defaults = %v/%v, want 30/60", limiter.perSecond, limiter.burst)
	}
}
