// SPDX-License-Identifier: Apache-2.0
package webhooks

import (
	"testing"

	"branchy/internal/notify"
)

func TestParsePush(t *testing.T) {
	body := []byte(`{
		"ref":"refs/heads/main",
		"compare":"https://github.com/acme/repo/compare/a...b",
		"repository":{"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
		"pusher":{"name":"octocat"},
		"sender":{"login":"octocat"},
		"head_commit":{"message":"ship it\n\nbody","url":"https://github.com/acme/repo/commit/1"},
		"commits":[
			{"id":"1111111222222233333334444444555555566666","message":"ship it\n\nbody","url":"https://github.com/acme/repo/commit/1111111","author":{"username":"octocat"}},
			{"id":"2222222333333344444445555555666666677777","message":"follow up","url":"https://github.com/acme/repo/commit/2222222","author":{"name":"Mona"}}
		]
	}`)
	event, supported, err := ParseEvent("push", body)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatal("push should be supported")
	}
	if event.Branch != "main" || event.Actor != "octocat" || event.CommitCount != 2 || event.CompareURL == "" || len(event.Commits) != 2 {
		t.Fatalf("unexpected push event: %+v", event)
	}
	if event.Commits[0].SHA == "" || event.Commits[0].Message != "ship it" || event.Commits[1].Author != "Mona" {
		t.Fatalf("unexpected parsed commits: %+v", event.Commits)
	}
}

func TestParsePushIgnoresTagsDeletesAndEmptyPushes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "tag", body: `{"ref":"refs/tags/v1","repository":{"full_name":"acme/repo"},"commits":[{"id":"1"}]}`},
		{name: "delete", body: `{"ref":"refs/heads/main","deleted":true,"repository":{"full_name":"acme/repo"},"commits":[]}`},
		{name: "empty", body: `{"ref":"refs/heads/main","repository":{"full_name":"acme/repo"},"commits":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, supported, err := ParseEvent("push", []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if supported {
				t.Fatal("push should be ignored")
			}
		})
	}
}

func TestParsePullRequest(t *testing.T) {
	body := []byte(`{
		"action":"opened",
		"number":42,
		"repository":{"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
		"pull_request":{"title":"Add feature","html_url":"https://github.com/acme/repo/pull/42","base":{"ref":"main"}},
		"sender":{"login":"octocat"}
	}`)
	event, supported, err := ParseEvent("pull_request", body)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatal("pull_request should be supported")
	}
	if event.Branch != "main" || event.Title != "Add feature" || event.Summary != "opened pull request" || event.Action != "opened" || event.Number != 42 {
		t.Fatalf("unexpected pull request event: %+v", event)
	}
}

func TestParseRelease(t *testing.T) {
	body := []byte(`{
		"action":"published",
		"repository":{"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
		"release":{"name":"v1.0.0","tag_name":"v1.0.0","target_commitish":"release","html_url":"https://github.com/acme/repo/releases/tag/v1.0.0","body":"## Added\n- release notes","prerelease":true},
		"sender":{"login":"octocat"}
	}`)
	event, supported, err := ParseEvent("release", body)
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Fatal("release should be supported")
	}
	if event.Branch != "release" || event.Title != "v1.0.0" || event.TagName != "v1.0.0" || event.Action != "published" || event.Body == "" || !event.Prerelease {
		t.Fatalf("unexpected release event: %+v", event)
	}
}

func TestParseReleaseIgnoresNoisyActions(t *testing.T) {
	for _, action := range []string{"created", "prereleased", "deleted"} {
		body := []byte(`{
			"action":"` + action + `",
			"repository":{"full_name":"acme/repo","default_branch":"main","html_url":"https://github.com/acme/repo"},
			"release":{"name":"v1.0.0","tag_name":"v1.0.0","target_commitish":"release","html_url":"https://github.com/acme/repo/releases/tag/v1.0.0"},
			"sender":{"login":"octocat"}
		}`)
		_, supported, err := ParseEvent("release", body)
		if err != nil {
			t.Fatal(err)
		}
		if supported {
			t.Fatalf("release action %q should be ignored", action)
		}
	}
}

func TestMatchesBranch(t *testing.T) {
	event := notify.Event{Branch: "main", DefaultBranch: "main"}
	tests := []struct {
		name   string
		filter SubscriptionFilter
		want   bool
	}{
		{name: "all", filter: SubscriptionFilter{BranchMode: "all"}, want: true},
		{name: "default match", filter: SubscriptionFilter{BranchMode: "default", DefaultBranch: "main"}, want: true},
		{name: "default mismatch", filter: SubscriptionFilter{BranchMode: "default", DefaultBranch: "develop"}, want: false},
		{name: "selected match", filter: SubscriptionFilter{BranchMode: "selected", BranchName: "main"}, want: true},
		{name: "selected mismatch", filter: SubscriptionFilter{BranchMode: "selected", BranchName: "release"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesBranch(tt.filter, event)
			if got != tt.want {
				t.Fatalf("MatchesBranch() = %v, want %v", got, tt.want)
			}
		})
	}
}
