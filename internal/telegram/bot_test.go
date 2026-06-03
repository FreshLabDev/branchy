// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"testing"

	"branchy/internal/github"
)

func TestCreationActionsConsumeCallbackTokens(t *testing.T) {
	for _, action := range []string{"sub.branch", "sub.branch.selected"} {
		if !isConsumedAction(action) {
			t.Fatalf("%s should consume callback token", action)
		}
	}
}

func TestVisibleRepositoriesHidesArchivedRepositories(t *testing.T) {
	repos := []github.Repository{
		{FullName: "acme/active-admin", HasAdminPermission: true},
		{FullName: "acme/active-readonly"},
		{FullName: "acme/archived-admin", HasAdminPermission: true, Archived: true},
	}

	all := visibleRepositories(repos, false)
	if got, want := repoNames(all), []string{"acme/active-admin", "acme/active-readonly"}; !sameStrings(got, want) {
		t.Fatalf("visible repositories = %v, want %v", got, want)
	}

	subscribe := visibleRepositories(repos, true)
	if got, want := repoNames(subscribe), []string{"acme/active-admin"}; !sameStrings(got, want) {
		t.Fatalf("subscribe repositories = %v, want %v", got, want)
	}
}

func repoNames(repos []github.Repository) []string {
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.FullName)
	}
	return names
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
