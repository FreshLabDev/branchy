// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"strings"
	"testing"
)

func TestGitHubEventEscapesHTML(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "push",
		RepoFullName: `acme/<repo>`,
		Actor:        `dev&ops`,
		Branch:       `main`,
		Title:        `<script>`,
		URL:          `https://github.com/acme/repo?x=1&y=2`,
	})
	if strings.Contains(text, "<script>") {
		t.Fatalf("expected title to be escaped: %s", text)
	}
	if !strings.Contains(text, "acme/&lt;repo&gt;") || !strings.Contains(text, "dev&amp;ops") {
		t.Fatalf("expected fields to be escaped: %s", text)
	}
	if !strings.Contains(text, `href="https://github.com/acme/repo?x=1&amp;y=2"`) {
		t.Fatalf("expected URL attribute to be escaped: %s", text)
	}
}

func TestGitHubEventDropsUnsafeURL(t *testing.T) {
	for _, raw := range []string{
		"javascript:alert(1)",
		"tg://resolve?domain=evil",
		"data:text/html,<script>",
		"//github.com/x",
	} {
		text := GitHubEvent(Event{Type: "push", RepoFullName: "acme/repo", URL: raw})
		if strings.Contains(text, "Open on GitHub") || strings.Contains(text, "<a href") {
			t.Fatalf("expected unsafe URL %q to be dropped: %s", raw, text)
		}
	}
}
