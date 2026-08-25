// SPDX-License-Identifier: Apache-2.0
package db

import (
	"strings"
	"testing"
)

func TestCompactUUIDRoundTrip(t *testing.T) {
	id, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 {
		t.Fatalf("uuid length = %d, want 36 (%q)", len(id), id)
	}
	compact := CompactUUID(id)
	if len(compact) != 32 {
		t.Fatalf("compact length = %d, want 32", len(compact))
	}
	got, ok := ExpandCompactUUID(compact)
	if !ok || got != id {
		t.Fatalf("ExpandCompactUUID(%q) = %q %v, want %q", compact, got, ok, id)
	}
	if _, ok := ExpandCompactUUID("not-a-uuid"); ok {
		t.Fatal("invalid compact id should be rejected")
	}
	upper, ok := ExpandCompactUUID(strings.ToUpper(compact))
	if !ok || upper != id {
		t.Fatalf("uppercase compact should round-trip, got %q", upper)
	}
}

func TestMoreCallbackFitsTelegramLimit(t *testing.T) {
	id, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	data := "m:" + CompactUUID(id)
	if len(data) != 34 {
		t.Fatalf("callback data length = %d, want 34", len(data))
	}
}
