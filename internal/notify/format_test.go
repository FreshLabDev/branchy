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
	if !strings.Contains(text, `url="https://github.com/acme/repo?x=1&amp;y=2"`) ||
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
		"<h2>" + iconCommits + " 1 new commit</h2>",
		"<p>pushed to FreshLabDev/branchy · <code>main</code> · <b>amtiYo</b></p>",
		`<tg-button type="url" style="primary" url="https://github.com/FreshLabDev/branchy/compare/a...b">Open compare</tg-button>`,
		`<tg-button type="copy_text" text="9b0f75e340e43784ea868a51078b450d172e63d0">Copy SHA</tg-button>`,
		`<a href="https://github.com/FreshLabDev/branchy/commit/9b0f75e340e43784ea868a51078b450d172e63d0">9b0f75e</a> fix: extract inline urls from text`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted commit notification missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<hr>") {
		t.Fatalf("cards should not use a divider:\n%s", text)
	}
	if strings.Contains(text, "Compare changes") {
		t.Fatalf("rich commit notification should use action buttons, not compare links:\n%s", text)
	}
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
	if !strings.Contains(text, "<h2>"+iconCommits+" 3 new commits</h2>") {
		t.Fatalf("expected title-first commit heading:\n%s", text)
	}
	if !strings.Contains(text, "3 new commits") || !strings.Contains(text, "<code>dev</code>") {
		t.Fatalf("expected plural commit summary:\n%s", text)
	}
	if strings.Contains(text, "copy_text") || strings.Contains(text, "Copy SHA") {
		t.Fatalf("multi-commit push must not include Copy SHA:\n%s", text)
	}
	if !strings.Contains(text, "<br>") {
		t.Fatalf("commit lines should use rich line breaks:\n%s", text)
	}
	for _, sha := range []string{"cbfa266", "f58a526", "9580b30"} {
		if !strings.Contains(text, ">"+sha+"</a>") {
			t.Fatalf("expected commit %s to be linked:\n%s", sha, text)
		}
	}
}

func TestGitHubEventShowsAllCommitsThatFit(t *testing.T) {
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

func TestGitHubEventBodyStaysUnderSoftCap(t *testing.T) {
	// Overlong body must truncate with a full-notes link rather than shipping
	// an unbounded payload to Telegram.
	body := strings.Repeat("All the release notes go here. ", 800)
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		Title:        strings.Repeat("T", 500),
		TagName:      "v1",
		URL:          "https://github.com/acme/repo/releases/tag/v1",
		Body:         body,
	})
	if len(text) > maxRichMessageBytes {
		t.Fatalf("message length = %d, exceeds Telegram rich limit %d", len(text), maxRichMessageBytes)
	}
	if !strings.Contains(text, "Full release notes") {
		t.Fatalf("a body trimmed to fit should still link to the full notes:\n%s", text)
	}
	if !strings.Contains(text, "<details>") {
		t.Fatalf("a truncated body should collapse into details:\n%s", text)
	}
}

func TestGitHubEventCollapsesOnlyLongBodies(t *testing.T) {
	mk := func(body string) string {
		return GitHubEvent(Event{Type: "release", RepoFullName: "acme/repo", TagName: "v1", URL: "https://github.com/acme/repo/releases/tag/v1", Body: body})
	}

	short := mk("A quick one-line release note.")
	if strings.Contains(short, "<details>") || strings.Contains(short, "expandable") {
		t.Fatalf("a short body must not be collapsed:\n%s", short)
	}
	if !strings.Contains(short, "<blockquote><p>A quick one-line release note.</p></blockquote>") {
		t.Fatalf("a short body should render as a rich HTML quote:\n%s", short)
	}

	wordy := mk(strings.Repeat("This release changes a lot of things. ", 30))
	if strings.Contains(wordy, "<details>") || !strings.Contains(wordy, "<blockquote expandable>") {
		t.Fatalf("a long flat body should use an expandable quote:\n%s", wordy)
	}
	if strings.Contains(wordy, "<blockquote expandable><p>") || strings.Contains(wordy, "<blockquote expandable>\n<p>") {
		t.Fatalf("expandable quotes must be flat RichText, not nested paragraphs:\n%s", wordy)
	}

	var lines []string
	for i := 0; i < 15; i++ {
		lines = append(lines, fmt.Sprintf("- item %d", i))
	}
	tall := mk(strings.Join(lines, "\n"))
	if !strings.Contains(tall, "<details>") {
		t.Fatalf("a tall structured body should collapse into details:\n%s", tall)
	}
}

func TestGitHubEventRendersGFMBodyToRichHTML(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1.2.3",
		URL:          "https://github.com/acme/repo/releases/tag/v1.2.3",
		Body:         "> See the [migration guide](https://github.com/acme/repo/wiki) and **back up** first",
	})
	if !strings.Contains(text, `<a href="https://github.com/acme/repo/wiki">migration guide</a>`) {
		t.Fatalf("body link should be rendered safely:\n%s", text)
	}
	if !strings.Contains(text, "<b>back up</b>") {
		t.Fatalf("body bold should be rendered safely:\n%s", text)
	}
	if strings.Contains(text, "**back up**") {
		t.Fatalf("raw Markdown must not reach Telegram:\n%s", text)
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
		"<h2>" + iconPR + " #7 Fix &lt;format&gt; &amp; &#34;escape&#34;</h2>",
		"<p>opened in FreshLabDev/branchy · <code>main</code> · <b>amtiYo</b></p>",
		`<tg-button type="url" style="primary" url="https://github.com/FreshLabDev/branchy/pull/7">Open pull request</tg-button>`,
		"<p>This PR <b>improves</b> things.</p>",
		"<p>See <code>notify</code> for details.</p>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted PR notification missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Copy #7") || strings.Contains(text, `text="#7"`) {
		t.Fatalf("PR cards must not copy the issue number:\n%s", text)
	}
	if strings.Contains(text, "callback_data") || strings.Contains(text, ">More</tg-button>") {
		t.Fatalf("PR cards without a job id must not include More:\n%s", text)
	}
	if strings.Contains(text, "<hr>") || strings.Contains(text, "Pull request opened") {
		t.Fatalf("PR cards should be title-first without a divider or extra action paragraph:\n%s", text)
	}
	if strings.Contains(text, `<a href="https://github.com/FreshLabDev/branchy/pull/7">#7`) {
		t.Fatalf("rich PR title should not duplicate the Open button:\n%s", text)
	}
}

func TestGitHubNotificationFallbackKeepsPRTitleLink(t *testing.T) {
	got := GitHubNotification(Event{
		Type:         "pull_request",
		RepoFullName: "FreshLabDev/branchy",
		Actor:        "amtiYo",
		Branch:       "main",
		Title:        `Fix <format> & "escape"`,
		Action:       "opened",
		Number:       7,
		URL:          "https://github.com/FreshLabDev/branchy/pull/7",
	})
	if !strings.Contains(got.FallbackHTML, `<a href="https://github.com/FreshLabDev/branchy/pull/7">#7 Fix &lt;format&gt; &amp; &#34;escape&#34;</a>`) {
		t.Fatalf("classic fallback should keep the title link:\n%s", got.FallbackHTML)
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
	if !strings.Contains(text, "merged in FreshLabDev/branchy") {
		t.Fatalf("expected merged label in the quiet line:\n%s", text)
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
		"<h2>" + iconRelease + " v0.1.0-alpha.1 &lt;format&gt; &amp; &#34;escape&#34;</h2>",
		"<p>Pre-release in FreshLabDev/branchy · <code>v0.1.0-alpha.1</code> · <b>amtiYo</b></p>",
		`<tg-button type="url" style="primary" url="https://github.com/FreshLabDev/branchy/releases/tag/v0.1.0-alpha.1">Open release</tg-button>`,
		"<h2>Added</h2>",
		`<li><b>Commit notifications</b> with <a href="https://github.com/FreshLabDev/branchy/compare/a...b">compare links</a></li>`,
		"<li><i>Release notes</i> with <code>code</code> and <del>old wording</del></li>",
		"<p>Keep it concise",
		`<figure><img src="https://github.com/FreshLabDev/branchy/assets/release.png"><figcaption>Screenshot</figcaption></figure>`,
		"Underlined bit",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted release notification missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Copy tag") || strings.Contains(text, `text="v0.1.0-alpha.1"`) {
		t.Fatalf("release cards must not include Copy tag:\n%s", text)
	}
	// Short multi-line body stays a quote, not details.
	if strings.Contains(text, "<details>") {
		t.Fatalf("short release body should not use details:\n%s", text)
	}
	for _, unwanted := range []string{"Tag: <code>", "Target: <code>", "Pre-release published"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("release notification should not contain %q:\n%s", unwanted, text)
		}
	}
}

func TestGitHubEventReleaseBodyCapFitsLongNotes(t *testing.T) {
	// Within the soft release body cap: no Full release notes link.
	body := strings.Repeat("All the release notes go here. ", 96)
	if len([]rune(body)) >= maxReleaseBodyRunes {
		t.Fatalf("test body should sit under the release cap, got %d runes", len([]rune(body)))
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
		if strings.Contains(text, "Open on GitHub") || strings.Contains(text, "<a href") || strings.Contains(text, "javascript:") || strings.Contains(text, "tg://") {
			t.Fatalf("expected unsafe URL %q to be dropped: %s", raw, text)
		}
	}
}

func TestPrepareGitHubBodyStripsCommentsAndTruncates(t *testing.T) {
	body, truncated := prepareGitHubBody("<!-- hidden -->hello world", 5)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if strings.Contains(body, "hidden") {
		t.Fatalf("HTML comment leaked:\n%s", body)
	}
	if !strings.HasPrefix(body, "hello") || !strings.Contains(body, "...") {
		t.Fatalf("unexpected truncated body: %q", body)
	}
}

func TestPrepareGitHubBodyPreservesMarkdown(t *testing.T) {
	body, truncated := prepareGitHubBody("**bold** and [x](https://example.com)", 1000)
	if truncated {
		t.Fatal("short body should not truncate")
	}
	if body != "**bold** and [x](https://example.com)" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestGitHubEventRejectsTelegramSpecificBodyInjection(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body: `<tg-emoji emoji-id="1">spoof</tg-emoji>
<tg-button-row><tg-button type="url" url="https://evil.example">pwn</tg-button></tg-button-row>

[mention](tg://user?id=42)

<script>alert(1)</script>`,
	})
	for _, forbidden := range []string{"<tg-emoji", `url="https://evil.example"`, "tg://", "<script>"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("untrusted body injected %q:\n%s", forbidden, text)
		}
	}
}

func TestGitHubEventKeepsContentAfterUnclosedTelegramTag(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         `<p>before</p><tg-button type="url" url="https://evil.example" /><p>SAFE_CONTENT</p>`,
	})
	if !strings.Contains(text, "SAFE_CONTENT") || !strings.Contains(text, "before") {
		t.Fatalf("unclosed tg-button swallowed the rest of the body:\n%s", text)
	}
	if strings.Contains(text, "evil.example") {
		t.Fatalf("unclosed tg-button leaked its URL into the notification:\n%s", text)
	}
}

func TestGitHubEventDoesNotTreatFallbackCutAsTruncation(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "[l%d](https://example.com/path/to/a/pretty/long/link/%d)\n\n", i, i)
	}
	got := GitHubNotification(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		URL:          "https://github.com/acme/repo/releases/tag/v1",
		Body:         b.String(),
	})
	if !strings.Contains(got.RichHTML, "l79") {
		t.Fatalf("rich body should keep the last link:\n%s", got.RichHTML)
	}
	if strings.Contains(got.RichHTML, "Full release notes") {
		t.Fatalf("a complete rich body must not be marked truncated because classic fallback was cut:\n%s", got.RichHTML)
	}
}

func TestGitHubEventDoesNotPromoteMediaFromDroppedContainers(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         `<object><img src="https://example.com/tracker.png"></object>safe`,
	})
	if strings.Contains(text, "tracker.png") || !strings.Contains(text, "safe") {
		t.Fatalf("media escaped a dropped container:\n%s", text)
	}
}

func TestGitHubEventKeepsAllowlistedRawGFMHTML(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         "<ins>added</ins> and H<sub>2</sub>O with x<sup>2</sup>",
	})
	for _, want := range []string{"<ins>added</ins>", "<sub>2</sub>", "<sup>2</sup>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("allowlisted GFM HTML missing %q:\n%s", want, text)
		}
	}
}

func TestGitHubEventKeepsFirstFiftyMediaBlocks(t *testing.T) {
	var lines []string
	for i := 0; i < 51; i++ {
		lines = append(lines, fmt.Sprintf("![Screenshot %d](https://example.com/%d.png)", i, i))
	}
	notification := GitHubNotification(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		URL:          "https://github.com/acme/repo/releases/tag/v1",
		Body:         strings.Join(lines, "\n\n"),
	})
	if got := strings.Count(notification.RichHTML, "<img "); got != maxRichBodyMedia {
		t.Fatalf("visible media = %d, want %d:\n%s", got, maxRichBodyMedia, notification.RichHTML)
	}
	if !strings.Contains(notification.RichHTML, `<a href="https://example.com/50.png">Screenshot 50</a>`) {
		t.Fatalf("media past the limit should remain available as a link:\n%s", notification.RichHTML)
	}
	if strings.Contains(notification.FallbackHTML, "<img ") || !strings.Contains(notification.FallbackHTML, "https://example.com/0.png") {
		t.Fatalf("classic fallback should turn media into links:\n%s", notification.FallbackHTML)
	}
}

func TestGitHubEventUsesMediaTypeAndMarkdownTitle(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         `![fallback alt](https://example.com/demo.gif "Demo animation")`,
	})
	if !strings.Contains(text, `<video src="https://example.com/demo.gif"></video>`) {
		t.Fatalf("GIF should use a video media block:\n%s", text)
	}
	if !strings.Contains(text, "<figcaption>Demo animation</figcaption>") || strings.Contains(text, "<figcaption>fallback alt</figcaption>") {
		t.Fatalf("Markdown title should be the media caption:\n%s", text)
	}
}

func TestGitHubEventPromotesLinkedImageToVisibleMedia(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         `[![Screenshot](https://example.com/screenshot.png "New screen")](https://example.com/demo)`,
	})
	if !strings.Contains(text, `<img src="https://example.com/screenshot.png">`) {
		t.Fatalf("linked image should remain visible media:\n%s", text)
	}
	if !strings.Contains(text, `<figcaption><a href="https://example.com/demo">New screen</a></figcaption>`) {
		t.Fatalf("linked image should preserve its destination in the caption:\n%s", text)
	}
}

func TestGitHubEventKeepsRawVideoAndAudioMedia(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body: strings.Join([]string{
			`<video src="https://example.com/demo.mp4"></video>`,
			`<audio src="https://example.com/demo.mp3"></audio>`,
		}, "\n\n"),
	})
	for _, want := range []string{
		`<video src="https://example.com/demo.mp4"></video>`,
		`<audio src="https://example.com/demo.mp3"></audio>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("raw media missing %q:\n%s", want, text)
		}
	}
}

func TestGitHubEventCapsTableColumns(t *testing.T) {
	var headers, separators, values []string
	for i := 0; i < 25; i++ {
		headers = append(headers, fmt.Sprintf("h%d", i))
		separators = append(separators, "---")
		values = append(values, fmt.Sprintf("v%d", i))
	}
	body := "| " + strings.Join(headers, " | ") + " |\n| " + strings.Join(separators, " | ") + " |\n| " + strings.Join(values, " | ") + " |"
	text := GitHubEvent(Event{Type: "release", RepoFullName: "acme/repo", TagName: "v1", Body: body})
	if got := strings.Count(text, "<th>"); got != maxRichTableColumns {
		t.Fatalf("header columns = %d, want %d:\n%s", got, maxRichTableColumns, text)
	}
	if !strings.Contains(text, `<table bordered striped compact>`) {
		t.Fatalf("release tables should be compact and bordered:\n%s", text)
	}
	if got := strings.Count(text, "<td>"); got != maxRichTableColumns {
		t.Fatalf("body columns = %d, want %d:\n%s", got, maxRichTableColumns, text)
	}
	for _, unsupported := range []string{"<thead", "<tbody", "<tfoot"} {
		if strings.Contains(text, unsupported) {
			t.Fatalf("HTML5 table wrapper %q must not reach Telegram:\n%s", unsupported, text)
		}
	}
}

func TestRichHTMLWithoutMediaPreservesStructureAndLinksSources(t *testing.T) {
	rich := `<details><summary>Notes</summary><table><tr><th>Kind</th></tr><tr><td>Photo</td></tr></table><figure><img src="https://example.com/a.png"><figcaption>Screenshot</figcaption></figure></details>`
	got := RichHTMLWithoutMedia(rich)
	for _, want := range []string{"<details>", "<table>", "<tr>", `<a href="https://example.com/a.png">Screenshot</a>`} {
		if !strings.Contains(got, want) {
			t.Fatalf("media-free rich fallback missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"<img", "<figure", "<tbody"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("media-free rich fallback kept %q:\n%s", unwanted, got)
		}
	}
}

func TestPlainTextFromHTMLKeepsButtonURL(t *testing.T) {
	got := PlainTextFromHTML(`<h2>Repo</h2><tg-button-row><tg-button type="url" url="https://example.com/pr">Open pull request</tg-button></tg-button-row>`)
	if strings.Contains(got, "<") || !strings.Contains(got, "Open pull request (https://example.com/pr)") {
		t.Fatalf("unexpected plain text fallback: %q", got)
	}
}

func TestGitHubNotificationFallbackKeepsClassicCompareLink(t *testing.T) {
	notification := GitHubNotification(Event{
		Type:         "push",
		RepoFullName: "acme/repo",
		Actor:        "dev",
		CompareURL:   "https://github.com/acme/repo/compare/a...b",
		Commits:      []Commit{{SHA: "abcdef1", Message: "fix", URL: "https://github.com/acme/repo/commit/abcdef1"}},
		CommitCount:  1,
	})
	if strings.Contains(notification.FallbackHTML, "tg-button") {
		t.Fatalf("classic fallback must not include rich buttons:\n%s", notification.FallbackHTML)
	}
	if !strings.Contains(notification.FallbackHTML, `<a href="https://github.com/acme/repo/compare/a...b">Compare changes</a>`) {
		t.Fatalf("classic fallback should keep the compare link:\n%s", notification.FallbackHTML)
	}
}

func TestTestNotificationUsesRichCard(t *testing.T) {
	got := TestNotification("acme/repo")
	if !strings.Contains(got.RichHTML, "<h2>Test notification</h2>") || strings.Contains(got.RichHTML, "<hr>") {
		t.Fatalf("test notification should stay a short title-first card:\n%s", got.RichHTML)
	}
	if !strings.Contains(got.RichHTML, "<p>acme/repo</p>") {
		t.Fatalf("test notification should mention the repo:\n%s", got.RichHTML)
	}
	if strings.Contains(got.RichHTML, "tg-button") {
		t.Fatalf("test notification should not invent GitHub buttons:\n%s", got.RichHTML)
	}
	if got.FallbackHTML != TestMessage("acme/repo") {
		t.Fatalf("classic test fallback = %q", got.FallbackHTML)
	}
}

func TestPlainTextFromHTMLKeepsLinkDestination(t *testing.T) {
	got := PlainTextFromHTML(`<b>Release</b><p>See <a href="https://example.com/notes">notes</a>.</p>`)
	if strings.Contains(got, "<") || !strings.Contains(got, "Release") || !strings.Contains(got, "notes (https://example.com/notes)") {
		t.Fatalf("unexpected plain text fallback: %q", got)
	}
}

func TestGitHubEventPRMoreUsesCallbackLinkButton(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "pull_request",
		RepoFullName: "FreshLabDev/branchy",
		Author:       "amtiYo",
		Actor:        "webhook-bot",
		HeadBranch:   "feat/cards",
		Branch:       "dev",
		Title:        "Readable cards",
		Action:       "opened",
		Number:       7,
		URL:          "https://github.com/FreshLabDev/branchy/pull/7",
		MoreJobID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if !strings.Contains(text, "<p>opened in FreshLabDev/branchy · <code>feat/cards → dev</code> · <b>amtiYo</b></p>") {
		t.Fatalf("PR quiet line should use author and head → base:\n%s", text)
	}
	want := `<tg-button type="callback_data" style="link" data="m:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">More</tg-button>`
	if !strings.Contains(text, want) {
		t.Fatalf("More button missing:\n%s", text)
	}
	if strings.Contains(text, `style="link"`) && strings.Contains(text, `type="url"`) && strings.Contains(text, `type="url" style="link"`) {
		t.Fatalf("style=link must not be applied to URL buttons:\n%s", text)
	}
	if strings.Contains(text, `type="url" style="link"`) {
		t.Fatalf("style=link is callback-only:\n%s", text)
	}
}

func TestPRMoreHTMLHidesZeroDiffAndAddsSubpages(t *testing.T) {
	got := PRMoreHTML(PRMoreSnapshot{
		Number:       7,
		Title:        "Readable cards",
		URL:          "https://github.com/FreshLabDev/branchy/pull/7",
		Body:         "A **short** description.",
		HeadBranch:   "feat/cards",
		BaseBranch:   "dev",
		IsDraft:      true,
		Author:       "amtiYo",
		Labels:       []string{"ux"},
		Reviewers:    []string{"octocat"},
		Additions:    0,
		Deletions:    0,
		ChangedFiles: 0,
		CommitCount:  0,
	})
	for _, want := range []string{
		"<h2>" + iconPR + " #7 Readable cards</h2>",
		"<code>feat/cards → dev</code>",
		"Draft",
		"<code>ux</code>",
		"<code>octocat</code>",
		"<p>A <b>short</b> description.</p>",
		`<tg-button type="url" style="primary" url="https://github.com/FreshLabDev/branchy/pull/7">Open</tg-button>`,
		`url="https://github.com/FreshLabDev/branchy/pull/7/files">Files</tg-button>`,
		`url="https://github.com/FreshLabDev/branchy/pull/7/commits">Commits</tg-button>`,
		`url="https://github.com/FreshLabDev/branchy/pull/7/checks">Checks</tg-button>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PR More overlay missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"+0", "−0", "0 file", "0 commit", "<details>", "Copy "} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("PR More overlay should not contain %q:\n%s", unwanted, got)
		}
	}
}

func TestPRMoreHTMLShowsNonZeroDiff(t *testing.T) {
	got := PRMoreHTML(PRMoreSnapshot{
		Number:       8,
		Title:        "Stats",
		URL:          "https://github.com/acme/repo/pull/8",
		Additions:    12,
		Deletions:    3,
		ChangedFiles: 4,
		CommitCount:  2,
	})
	if !strings.Contains(got, "+12 · −3 · 4 files · 2 commits") {
		t.Fatalf("expected diff stats:\n%s", got)
	}
}

func TestSanitizeLegacyRichMarkdownRemovesUnsafeHTML(t *testing.T) {
	got := SanitizeLegacyRichMarkdown(`**Release** <script>alert(1)</script> [notes](https://example.com)`)
	if !strings.Contains(got.RichHTML, "<b>Release</b>") || !strings.Contains(got.RichHTML, `href="https://example.com"`) {
		t.Fatalf("legacy rich markdown lost safe formatting: %s", got.RichHTML)
	}
	if strings.Contains(strings.ToLower(got.RichHTML), "<script") || strings.Contains(got.RichHTML, "alert(1)") {
		t.Fatalf("legacy rich markdown kept unsafe content: %s", got.RichHTML)
	}
}

func TestGitHubEventCapsRichBlocksAndNesting(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		URL:          "https://github.com/acme/repo/releases/tag/v1",
		Body:         strings.Repeat("paragraph\n\n", 600),
	})
	if got := strings.Count(text, "<p>"); got > maxRichBodyBlocks+1 {
		t.Fatalf("paragraph blocks = %d, max %d", got, maxRichBodyBlocks)
	}
	if !strings.Contains(text, "Full release notes") {
		t.Fatalf("block truncation should keep the source link:\n%s", text)
	}

	deep := strings.Repeat("> ", 30) + "nested"
	deepText := GitHubEvent(Event{Type: "release", RepoFullName: "acme/repo", TagName: "v1", Body: deep})
	if got := strings.Count(deepText, "<blockquote>"); got > maxRichBodyDepth+1 {
		t.Fatalf("blockquote nesting = %d, safe max %d:\n%s", got, maxRichBodyDepth+1, deepText)
	}
}

func TestGitHubEventKeepsGFMTaskLists(t *testing.T) {
	text := GitHubEvent(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		Body:         "- [x] shipped\n- [ ] follow-up",
	})
	for _, want := range []string{`<input type="checkbox" checked>`, `<input type="checkbox">`} {
		if !strings.Contains(text, want) {
			t.Fatalf("task list missing %q:\n%s", want, text)
		}
	}
}

func TestGitHubNotificationFallbackAndRichHTMLStayBoundedAndBalanced(t *testing.T) {
	notification := GitHubNotification(Event{
		Type:         "release",
		RepoFullName: "acme/repo",
		TagName:      "v1",
		URL:          "https://github.com/acme/repo/releases/tag/v1",
		Body:         strings.Repeat("& ", 10000),
	})
	if len([]rune(notification.RichHTML)) > maxRichMessageBytes {
		t.Fatalf("rich payload has %d chars, limit %d", len([]rune(notification.RichHTML)), maxRichMessageBytes)
	}
	if len([]rune(notification.FallbackHTML)) > 4096 {
		t.Fatalf("fallback payload has %d chars, classic limit 4096", len([]rune(notification.FallbackHTML)))
	}
	if strings.Count(notification.RichHTML, "<p>") != strings.Count(notification.RichHTML, "</p>") ||
		strings.Count(notification.RichHTML, "<details>") != strings.Count(notification.RichHTML, "</details>") {
		t.Fatalf("truncation produced unbalanced rich HTML:\n%s", notification.RichHTML)
	}
}

func FuzzGitHubNotificationBodyBounds(f *testing.F) {
	for _, seed := range []string{
		"plain text",
		"[link](javascript:alert(1)) <tg-emoji emoji-id=\"1\">x</tg-emoji>",
		"![image](https://example.com/a.png) text ![second](https://example.com/b.png)",
		strings.Repeat("> ", 40) + "nested",
		"<details><summary>x</summary><script>bad</script>safe</details>",
		strings.Repeat("&<> ", 3000),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		notification := GitHubNotification(Event{
			Type:         "release",
			RepoFullName: "acme/repo",
			TagName:      "v1",
			URL:          "https://github.com/acme/repo/releases/tag/v1",
			Body:         body,
		})
		if len([]rune(notification.RichHTML)) > maxRichMessageBytes {
			t.Fatalf("rich payload has %d chars", len([]rune(notification.RichHTML)))
		}
		if len([]rune(notification.FallbackHTML)) > maxClassicMessageRunes {
			t.Fatalf("classic payload has %d chars", len([]rune(notification.FallbackHTML)))
		}
		for _, forbidden := range []string{"<script", "<iframe", "<tg-emoji"} {
			if strings.Contains(strings.ToLower(notification.RichHTML), forbidden) {
				t.Fatalf("unsafe tag survived: %s", forbidden)
			}
		}
	})
}
