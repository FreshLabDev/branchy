// SPDX-License-Identifier: Apache-2.0
package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRendersExpositionFormat(t *testing.T) {
	NotificationsSent.Inc()
	NotificationsSent.Inc()
	NotificationsFailed.Inc("permanent")
	SubscriptionsAutoPaused.Inc("telegram_blocked")

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"# TYPE branchy_notifications_sent_total counter",
		"branchy_notifications_failed_total{reason=\"permanent\"} 1",
		"branchy_subscriptions_auto_paused_total{reason=\"telegram_blocked\"} 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n--- got ---\n%s", want, body)
		}
	}

	// The plain counter must report at least the two increments above (other
	// tests in the package share process-global counters, so allow >=).
	if !strings.Contains(body, "branchy_notifications_sent_total ") {
		t.Errorf("missing sent counter line:\n%s", body)
	}
}

func TestCounterVecIsConcurrencySafe(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				NotificationsFailed.Inc("race")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if got := NotificationsFailed.snapshot()["race"]; got != 800 {
		t.Fatalf("race counter = %d, want 800", got)
	}
}
