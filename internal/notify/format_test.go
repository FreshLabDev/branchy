// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"fmt"
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
		Commits: []Commit{{
			SHA:     "11111112222222",
			Message: `<script> & "quote"`,
			URL:     `https://github.com/acme/repo/commit/1111111?x=1&y=2`,
			Author:  `dev&ops`,
		}},
		CommitCount: 1,
	})
	if strings.Contains(text, "<script>") {
		t.Fatalf("expected title to be escaped: %s", text)
	}
	if !strings.Contains(text, "acme/&lt;repo&gt;") || !strings.Contains(text, "dev&amp;ops") {
		t.Fatalf("expected fields to be escaped: %s", text)
	}
	if !strings.Contains(text, `href="https://github.com/acme/repo?x=1&amp;y=2"`) ||
		!strings.Contains(text, `href="https://github.com/acme/repo/commit/1111111?x=1&amp;y=2"`) {
		t.Fatalf("expected URL attribute to be escaped: %s", text)
	}
}

func TestGitHubEventFormatsSingleCommit(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "push",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "main",
		CompareURL:   "https://github.com/FreshLabDev/branchy/compare/a...b",
		Commits: []Commit{{
			SHA:     "9b0f75e340e43784ea868a51078b450d172e63d0",
			Message: "fix: extract inline urls from text",
			URL:     "https://github.com/FreshLabDev/branchy/commit/9b0f75e340e43784ea868a51078b450d172e63d0",
			Author:  "amtiYo",
		}},
		CommitCount: 1,
	})
	for _, want := range []string{
		iconCommits + " <b>FreshLabDev/branchy</b>",
		"1 new commit · <code>main</code>",
		"Pushed by <b>amtiYo</b>",
		`<a href="https://github.com/FreshLabDev/branchy/compare/a...b">Compare changes</a>`,
		`<a href="https://github.com/FreshLabDev/branchy/commit/9b0f75e340e43784ea868a51078b450d172e63d0">9b0f75e</a> fix: extract inline urls from text`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted commit notification missing %q:\n%s", want, text)
		}
	}
	// The author appears once (footer), not under every commit.
	if strings.Count(text, "amtiYo") != 1 {
		t.Fatalf("author should appear once:\n%s", text)
	}
}

func TestGitHubEventFormatsMultipleCommits(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "push",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "dev",
		CompareURL:   "https://github.com/FreshLabDev/branchy/compare/a...c",
		Commits: []Commit{
			{SHA: "cbfa2669fef76b17a16dfee3e124c78c69cd8fad", Message: "fix: handle runtime media edge cases", URL: "https://github.com/FreshLabDev/branchy/commit/cbfa2669fef76b17a16dfee3e124c78c69cd8fad", Author: "amtiYo"},
			{SHA: "f58a52600b9c6bd6948851ad2bf475f54158c74b", Message: "fix: clarify dev audit comments", URL: "https://github.com/FreshLabDev/branchy/commit/f58a52600b9c6bd6948851ad2bf475f54158c74b", Author: "amtiYo"},
			{SHA: "9580b30ffd74623036d9afd88fd0cd59513252fd", Message: "Merge pull request #22 from FreshLabDev/codex/fix-runtime", URL: "https://github.com/FreshLabDev/branchy/commit/9580b30ffd74623036d9afd88fd0cd59513252fd", Author: "amtiYo"},
		},
		CommitCount: 3,
	})
	if !strings.Contains(text, "3 new commits · <code>dev</code>") {
		t.Fatalf("expected plural commit summary:\n%s", text)
	}
	for _, sha := range []string{"cbfa266", "f58a526", "9580b30"} {
		if !strings.Contains(text, ">"+sha+"</a>") {
			t.Fatalf("expected commit %s to be linked:\n%s", sha, text)
		}
	}
}

func TestGitHubEventShowsAllCommitsThatFit(t *testing.T) {
	// 15 short commits all fit well within the text budget, so none are hidden
	// behind a "+N more" line (the old fixed cap of 10 would have hidden 5).
	var commits []Commit
	for i := 0; i < 15; i++ {
		commits = append(commits, Commit{
			SHA:     fmt.Sprintf("%07dabcdef", i),
			Message: fmt.Sprintf("fix issue number %d", i),
			URL:     "https://github.com/acme/repo/commit/x",
		})
	}
	text := GitHubEvent(Event{Type: "push", RepoFullName: "acme/repo", Branch: "main", Commits: commits, CommitCount: 15})
	if strings.Contains(text, "more commit") {
		t.Fatalf("15 short commits should all fit without a remainder line:\n%s", text)
	}
	if got := strings.Count(text, "github.com/acme/repo/commit/x"); got != 15 {
		t.Fatalf("expected 15 commit lines, got %d:\n%s", got, text)
	}
}

func TestGitHubEventTrimsOverlongCommitListToRemainder(t *testing.T) {
	// A pathologically long list of long-subject commits exceeds the text budget,
	// so it is trimmed and the overflow is summarized as "+N more commits".
	var commits []Commit
	for i := 0; i < 60; i++ {
		commits = append(commits, Commit{
			SHA:     fmt.Sprintf("%07dabcdef", i),
			Message: strings.Repeat("x", 72),
			URL:     "https://github.com/acme/repo/commit/x",
		})
	}
	text := GitHubEvent(Event{Type: "push", RepoFullName: "acme/repo", Branch: "main", Commits: commits, CommitCount: 60})
	if !strings.Contains(text, "more commits") {
		t.Fatalf("an overlong commit list should be trimmed to a remainder line:\n%s", text)
	}
	shown := strings.Count(text, "github.com/acme/repo/commit/x")
	if shown == 0 || shown >= 60 {
		t.Fatalf("expected a trimmed subset of commits, got %d of 60", shown)
	}
}

func TestGitHubEventCollapsesOnlyLongBodies(t *testing.T) {
	mk := func(body string) string {
		return GitHubEvent(Event{Type: "release", RepoFullName: "acme/repo", TagName: "v1", URL: "https://github.com/acme/repo/releases/tag/v1", Body: body})
	}

	short := mk("A quick one-line release note.")
	if strings.Contains(short, "expandable") {
		t.Fatalf("a short body must not be collapsed:\n%s", short)
	}
	if !strings.Contains(short, "<blockquote>") {
		t.Fatalf("a short body should still render in a plain quote:\n%s", short)
	}

	// Wordy (~1100 visible chars) crosses the rune threshold.
	wordy := mk(strings.Repeat("This release changes a lot of things. ", 30))
	if !strings.Contains(wordy, "<blockquote expandable>") {
		t.Fatalf("a long body should collapse into an expandable quote:\n%s", wordy)
	}

	// Tall (15 short lines) crosses the line threshold despite low char count.
	var lines []string
	for i := 0; i < 15; i++ {
		lines = append(lines, fmt.Sprintf("- item %d", i))
	}
	tall := mk(strings.Join(lines, "\n"))
	if !strings.Contains(tall, "<blockquote expandable>") {
		t.Fatalf("a tall body should collapse into an expandable quote:\n%s", tall)
	}
}

func TestGitHubEventRendersMarkdownInsideBlockquote(t *testing.T) {
	// Inline Markdown inside a GitHub blockquote (the common callout region of
	// release notes) must render as entities, not raw source.
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1.2.3",
		URL:          "https://github.com/acme/repo/releases/tag/v1.2.3",
		Body:         "> See the [migration guide](https://github.com/acme/repo/wiki) and **back up** first",
	})
	if !strings.Contains(text, `<a href="https://github.com/acme/repo/wiki">migration guide</a>`) {
		t.Fatalf("blockquote link should render as an anchor:\n%s", text)
	}
	if !strings.Contains(text, "<b>back up</b>") {
		t.Fatalf("blockquote bold should render as <b>:\n%s", text)
	}
	if strings.Contains(text, "[migration guide]") || strings.Contains(text, "**back up**") {
		t.Fatalf("raw markdown leaked from the blockquote:\n%s", text)
	}
}

func TestGitHubEventFormatsPullRequest(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "pull_request",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "main",
		Title:        `Fix <format> & "escape"`,
		Action:       "opened",
		Number:       7,
		URL:          "https://github.com/FreshLabDev/branchy/pull/7",
		Body:         "This PR **improves** things.\n\nSee `notify` for details.",
	})
	for _, want := range []string{
		iconPR + " <b>FreshLabDev/branchy</b>",
		"Pull request opened",
		"into <code>main</code> · by <b>amtiYo</b>",
		`<a href="https://github.com/FreshLabDev/branchy/pull/7">#7 Fix &lt;format&gt; &amp; &#34;escape&#34;</a>`,
		// A short PR description renders (Markdown) inside a plain, non-collapsed
		// quote — only long bodies get the expandable (collapsed) variant.
		"<blockquote>",
		"<b>improves</b>",
		"<code>notify</code>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted PR notification missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "#7") != 1 {
		t.Fatalf("PR number should be shown once as the link label:\n%s", text)
	}
}

func TestGitHubEventMergedPullRequestHidesBody(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "pull_request",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "main",
		Title:        "Fix things",
		Action:       "closed",
		Merged:       true,
		Number:       7,
		URL:          "https://github.com/FreshLabDev/branchy/pull/7",
		Body:         "Body that should not be shown on merge.",
	})
	if !strings.Contains(text, "Pull request merged") {
		t.Fatalf("expected merged label:\n%s", text)
	}
	if strings.Contains(text, "should not be shown") {
		t.Fatalf("body should be hidden on non-opening actions:\n%s", text)
	}
}

func TestGitHubEventFormatsRelease(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "main",
		Title:        `v0.1.0-alpha.1 <format> & "escape"`,
		Action:       "published",
		TagName:      "v0.1.0-alpha.1",
		Prerelease:   true,
		URL:          "https://github.com/FreshLabDev/branchy/releases/tag/v0.1.0-alpha.1",
		Body: "## Added\n" +
			"- **Commit notifications** with [compare links](https://github.com/FreshLabDev/branchy/compare/a...b)\n" +
			"- _Release notes_ with `code` and ~~old wording~~\n" +
			"> Keep it concise\n" +
			"![Screenshot](https://github.com/FreshLabDev/branchy/assets/release.png)\n" +
			"<ins>Underlined bit</ins>",
	})
	for _, want := range []string{
		iconRelease + " <b>FreshLabDev/branchy</b>",
		`<b>Pre-release</b> · <a href="https://github.com/FreshLabDev/branchy/releases/tag/v0.1.0-alpha.1">v0.1.0-alpha.1 &lt;format&gt; &amp; &#34;escape&#34;</a>`,
		"<b>Added</b>",
		`- <b>Commit notifications</b> with <a href="https://github.com/FreshLabDev/branchy/compare/a...b">compare links</a>`,
		"- <i>Release notes</i> with <code>code</code> and <s>old wording</s>",
		"<blockquote>Keep it concise</blockquote>",
		`Image: <a href="https://github.com/FreshLabDev/branchy/assets/release.png">Screenshot</a>`,
		"<u>Underlined bit</u>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted release notification missing %q:\n%s", want, text)
		}
	}
	// The tag/target plumbing, the "Release notes" label, and the redundant
	// "published" wording are gone (the words may still appear in body text).
	for _, unwanted := range []string{"Tag: <code>", "Target: <code>", "<b>Release notes</b>", "Pre-release published"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("release notification should not contain %q:\n%s", unwanted, text)
		}
	}
}

func TestGitHubEventReleaseBodyCapFitsLongNotes(t *testing.T) {
	// ~2880 runes: above the old 1800 cap but within Telegram's 4096-char
	// limit, so the full notes must render without truncation or a
	// "Full release notes" fallback link.
	body := strings.Repeat("All the release notes go here. ", 96)
	if len([]rune(body)) <= 1800 || len([]rune(body)) >= maxReleaseBodyRunes {
		t.Fatalf("test body should sit between the old and new caps, got %d runes", len([]rune(body)))
	}
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		TagName:      "v1.0.0",
		URL:          "https://github.com/FreshLabDev/branchy/releases/tag/v1.0.0",
		Body:         body,
	})
	if strings.Contains(text, "Full release notes") || strings.Contains(text, "\n...") {
		t.Fatalf("release notes within the cap should not be truncated:\n%s", text)
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

func TestRenderGitHubMarkdownDropsUnsafeLinksAndComments(t *testing.T) {
	text, truncated := renderGitHubMarkdown(`<!-- hidden -->
[safe](https://github.com/FreshLabDev/branchy) [bad](javascript:alert(1))
https://github.com/FreshLabDev/branchy.`, 1000)
	if truncated {
		t.Fatal("short markdown should not be truncated")
	}
	if strings.Contains(text, "hidden") || strings.Contains(text, "javascript:") {
		t.Fatalf("unsafe markdown leaked into output:\n%s", text)
	}
	for _, want := range []string{
		`<a href="https://github.com/FreshLabDev/branchy">safe</a>`,
		"bad",
		`<a href="https://github.com/FreshLabDev/branchy">https://github.com/FreshLabDev/branchy</a>.`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown output missing %q:\n%s", want, text)
		}
	}
}

func TestRenderGitHubMarkdownTruncates(t *testing.T) {
	text, truncated := renderGitHubMarkdown("abcdef", 3)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if text != "abc\n..." {
		t.Fatalf("unexpected truncated text: %q", text)
	}
}
