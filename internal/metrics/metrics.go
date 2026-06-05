// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes a handful of process counters in the Prometheus text
// exposition format. It is intentionally dependency-free: the MVP needs a few
// monotonic counters for alerting (delivery failures, rate limits, auto-pauses),
// not a full client library. Add a metric by declaring it with registerCounter
// or registerVec below; the handler renders the registry automatically.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing, lock-free metric.
type Counter struct{ n atomic.Int64 }

func (c *Counter) Inc()         { c.n.Add(1) }
func (c *Counter) Add(d int64)  { c.n.Add(d) }
func (c *Counter) value() int64 { return c.n.Load() }

// CounterVec is a counter partitioned by a single label (e.g. "reason"). Cells
// are created lazily on first use.
type CounterVec struct {
	label string
	mu    sync.Mutex
	cells map[string]*atomic.Int64
}

func (c *CounterVec) Inc(labelValue string) {
	c.mu.Lock()
	cell := c.cells[labelValue]
	if cell == nil {
		cell = new(atomic.Int64)
		c.cells[labelValue] = cell
	}
	c.mu.Unlock()
	cell.Add(1)
}

func (c *CounterVec) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.cells))
	for k, v := range c.cells {
		out[k] = v.Load()
	}
	return out
}

type entry struct {
	name    string
	help    string
	counter *Counter
	vec     *CounterVec
}

var registry []entry

func registerCounter(name, help string) *Counter {
	c := &Counter{}
	registry = append(registry, entry{name: name, help: help, counter: c})
	return c
}

func registerVec(name, help, label string) *CounterVec {
	v := &CounterVec{label: label, cells: map[string]*atomic.Int64{}}
	registry = append(registry, entry{name: name, help: help, vec: v})
	return v
}

// Process counters. Names follow Prometheus convention: snake_case with a
// _total suffix for counters.
var (
	WebhooksReceived      = registerCounter("branchy_webhooks_received_total", "GitHub webhook deliveries accepted after signature verification.")
	WebhooksRejected      = registerCounter("branchy_webhooks_rejected_total", "GitHub webhook deliveries rejected for an invalid signature.")
	WebhooksDuplicate     = registerCounter("branchy_webhooks_duplicate_total", "GitHub webhook deliveries skipped as duplicates.")
	NotificationsEnqueued = registerCounter("branchy_notifications_enqueued_total", "Notification jobs created from webhook deliveries.")
	NotificationsSent     = registerCounter("branchy_notifications_sent_total", "Notification jobs delivered to Telegram.")
	NotificationsRetried  = registerCounter("branchy_notifications_retried_total", "Notification send attempts rescheduled for retry.")
	TelegramRateLimited   = registerCounter("branchy_telegram_rate_limited_total", "Telegram 429 rate-limit responses observed.")

	NotificationsFailed     = registerVec("branchy_notifications_failed_total", "Notification jobs that failed permanently, by reason.", "reason")
	SubscriptionsAutoPaused = registerVec("branchy_subscriptions_auto_paused_total", "Subscriptions paused automatically by the system, by reason.", "reason")
)

// Handler serves the registry in Prometheus text exposition format.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		write(w)
	})
}

func write(w io.Writer) {
	for _, e := range registry {
		fmt.Fprintf(w, "# HELP %s %s\n", e.name, e.help)
		fmt.Fprintf(w, "# TYPE %s counter\n", e.name)
		if e.counter != nil {
			fmt.Fprintf(w, "%s %d\n", e.name, e.counter.value())
			continue
		}
		snap := e.vec.snapshot()
		keys := make([]string, 0, len(snap))
		for k := range snap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s{%s=\"%s\"} %d\n", e.name, e.vec.label, escapeLabelValue(k), snap[k])
		}
	}
}

// escapeLabelValue escapes a label value per the Prometheus text format:
// backslash, double-quote, and newline. (Go's %q is close but not identical.)
func escapeLabelValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}
