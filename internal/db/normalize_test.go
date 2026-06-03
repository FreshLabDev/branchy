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

func TestNormalizePullRequestActionsKeepsProductOrder(t *testing.T) {
	got := NormalizePullRequestActions([]string{"closed", "ignored", "opened", "closed", "merged"})
	want := []string{"opened", "merged", "closed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePullRequestActions() = %#v, want %#v", got, want)
	}
}

func TestNormalizeBranchNamesSortsAndDedupes(t *testing.T) {
	got := NormalizeBranchNames([]string{"release", " main ", "", "release", "develop"})
	want := []string{"develop", "main", "release"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeBranchNames() = %#v, want %#v", got, want)
	}
}
