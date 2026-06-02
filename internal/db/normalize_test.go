// SPDX-License-Identifier: Apache-2.0
package db

import (
	"reflect"
	"testing"
)

func TestNormalizeEventsSortsAndDedupes(t *testing.T) {
	got := NormalizeEvents([]string{"release", "push", "ignored", "pull_request", "push"})
	want := []string{"pull_request", "push", "release"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeEvents() = %#v, want %#v", got, want)
	}
}
