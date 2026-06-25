// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"fmt"
	"html"
	"net/url"
	"strings"
)

type Event struct {
	Type          string
	RepoID        int64
	RepoFullName  string
	DefaultBranch string
	Actor         string
	Branch        string
	Title         string
	Summary       string
	URL           string
	Body          string
	Commits       []Commit
	CommitCount   int
	CompareURL    string
	Action        string
	Number        int
	TagName       string
	Prerelease    bool
	Merged        bool
}

// Leading icons per event type. One neutral glyph per message for quick visual
// scanning; kept deliberately minimal. Swap these to retheme.
const (
	iconCommits = "📝"
	iconPR      = "🔀"
	iconRelease = "📦"
)

type Commit struct {
	SHA     string
	Message string
	URL     string
	Author  string
}

func GitHubEvent(event Event) string {
	switch event.Type {
	case "push":
		return commitEvent(event)
	case "pull_request":
		return pullRequestEvent(event)
	case "release":
		return releaseEvent(event)
	}

	lines := []string{
		fmt.Sprintf("<b>%s</b> %s", esc(event.RepoFullName), esc(eventTitle(event.Type))),
		fmt.Sprintf("Actor: %s", esc(event.Actor)),
	}
	if event.Branch != "" {
		lines = append(lines, fmt.Sprintf("Branch: %s", esc(event.Branch)))
	}
	if event.Title != "" {
		lines = append(lines, esc(event.Title))
	}
	if event.Summary != "" {
		lines = append(lines, esc(event.Summary))
	}
	if link := safeURL(event.URL); link != "" {
		lines = append(lines, fmt.Sprintf(`<a href="%s">Open on GitHub</a>`, escAttr(link)))
	}
	return strings.Join(lines, "\n")
}

func TestMessage(repoFullName string) string {
	return "<b>" + esc(repoFullName) + "</b>\nTest notification. Branchy can deliver notifications to this chat."
}

func eventTitle(eventType string) string {
	switch eventType {
	case "push":
		return "Commits"
	case "pull_request":
		return "Pull request"
	case "release":
		return "Release"
	default:
		return eventType
	}
}

// Commit-list sizing: show as many commits as comfortably fit in one message
// instead of a fixed handful. The list is budgeted by visible text length
// (maxCommitListRunes) — well under Telegram's 4096-char limit — so a busy push
// is not hidden behind "+N more" when it would have fit, while a pathologically
// long list still collapses to a remainder line rather than an unwieldy wall.
// GitHub caps a push payload at ~20 commits, so in practice every commit shows;
// maxCommitLines is only a high safety bound against crafted input.
const (
	maxCommitLines     = 50
	maxCommitListRunes = 2400
)

func commitEvent(event Event) string {
	count := event.CommitCount
	if count == 0 {
		count = len(event.Commits)
	}
	summary := plural(count, "new commit", "new commits")
	if event.Branch != "" {
		summary += fmt.Sprintf(" · <code>%s</code>", esc(event.Branch))
	}
	header := fmt.Sprintf("%s <b>%s</b>\n%s", iconCommits, esc(event.RepoFullName), summary)

	var list []string
	used := 0
	for _, commit := range event.Commits {
		line, cost := formatCommitLine(commit)
		// Always include the first commit; past that, stop once another line
		// would push the visible list over the soft budget or the line ceiling.
		if len(list) > 0 && (len(list) >= maxCommitLines || used+cost > maxCommitListRunes) {
			break
		}
		list = append(list, line)
		used += cost
	}
	if remaining := len(event.Commits) - len(list); remaining > 0 {
		list = append(list, fmt.Sprintf("<i>+%s</i>", plural(remaining, "more commit", "more commits")))
	}

	var meta []string
	if event.Actor != "" {
		meta = append(meta, "Pushed by <b>"+esc(event.Actor)+"</b>")
	}
	if link := safeURL(firstNonEmpty(event.CompareURL, event.URL)); link != "" {
		meta = append(meta, fmt.Sprintf(`<a href="%s">Compare changes</a>`, escAttr(link)))
	}

	return joinSections(header, strings.Join(list, "\n"), strings.Join(meta, " · "))
}

func pullRequestEvent(event Event) string {
	action := humanizePRAction(event)
	header := fmt.Sprintf("%s <b>%s</b>\nPull request %s", iconPR, esc(event.RepoFullName), esc(action))

	var meta []string
	if title := truncateText(event.Title, maxTitleRunes); title != "" {
		label := title
		if event.Number > 0 {
			label = fmt.Sprintf("#%d %s", event.Number, title)
		}
		if link := safeURL(event.URL); link != "" {
			meta = append(meta, fmt.Sprintf(`<a href="%s">%s</a>`, escAttr(link), esc(label)))
		} else {
			meta = append(meta, esc(label))
		}
	}
	var sub []string
	if event.Branch != "" {
		sub = append(sub, "into <code>"+esc(event.Branch)+"</code>")
	}
	if event.Actor != "" {
		sub = append(sub, "by <b>"+esc(event.Actor)+"</b>")
	}
	if len(sub) > 0 {
		meta = append(meta, strings.Join(sub, " · "))
	}

	var body string
	if showsPRBody(action) {
		rendered, truncated := bodyBlock(event.Body, 700)
		body = rendered
		if truncated {
			if link := safeURL(event.URL); link != "" {
				body += fmt.Sprintf("\n"+`<a href="%s">Read more</a>`, escAttr(link))
			}
		}
	}

	return joinSections(header, strings.Join(meta, "\n"), body)
}

func humanizePRAction(event Event) string {
	if event.Merged {
		return "merged"
	}
	return strings.ReplaceAll(firstNonEmpty(event.Action, "updated"), "_", " ")
}

func showsPRBody(action string) bool {
	switch action {
	case "opened", "reopened", "ready for review":
		return true
	default:
		return false
	}
}

// maxReleaseBodyRunes caps how much of a release's notes we render. Telegram
// allows 4096 visible characters per message; bodyBlock holds the rendered body
// to this many *visible* characters and the header/footer/title are bounded, so
// the whole message stays under the limit while no longer cutting typical
// release notes off mid-sentence.
const maxReleaseBodyRunes = 3500

// maxTitleRunes bounds an attacker-influenced release/PR title in the header so
// it cannot, together with the body, push the message past Telegram's limit.
const maxTitleRunes = 200

func releaseEvent(event Event) string {
	label := "Release"
	if event.Prerelease {
		label = "Pre-release"
	}
	versionLine := fmt.Sprintf("<b>%s</b>", label)
	if version := truncateText(firstNonEmpty(event.Title, event.TagName), maxTitleRunes); version != "" {
		if link := safeURL(event.URL); link != "" {
			versionLine += fmt.Sprintf(` · <a href="%s">%s</a>`, escAttr(link), esc(version))
		} else {
			versionLine += " · " + esc(version)
		}
	}
	header := fmt.Sprintf("%s <b>%s</b>\n%s", iconRelease, esc(event.RepoFullName), versionLine)

	var meta string
	if event.Actor != "" {
		meta = "by <b>" + esc(event.Actor) + "</b>"
	}

	body, truncated := bodyBlock(event.Body, maxReleaseBodyRunes)
	result := joinSections(header, meta, body)
	if truncated {
		if link := safeURL(event.URL); link != "" {
			result += "\n\n" + fmt.Sprintf(`<a href="%s">Full release notes</a>`, escAttr(link))
		}
	}
	return result
}

// formatCommitLine renders one commit row and returns its approximate visible
// length (sha + space + subject) so commitEvent can budget the list — Telegram
// counts visible text, not the surrounding <a>/<code> tags, against its limit.
func formatCommitLine(commit Commit) (string, int) {
	sha := shortSHA(commit.SHA)
	if sha == "" {
		sha = "commit"
	}
	prefix := esc(sha)
	if link := safeURL(commit.URL); link != "" {
		prefix = fmt.Sprintf(`<a href="%s">%s</a>`, escAttr(link), esc(sha))
	}
	message := truncateText(firstLine(commit.Message), 72)
	if message == "" {
		message = "(no message)"
	}
	return fmt.Sprintf("%s %s", prefix, esc(message)), len([]rune(sha)) + 1 + len([]rune(message))
}

// joinSections joins the non-empty sections with a blank line between them, so
// header / body / footer read as distinct, scannable blocks.
func joinSections(sections ...string) string {
	var parts []string
	for _, section := range sections {
		if strings.TrimSpace(section) != "" {
			parts = append(parts, section)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Body collapse thresholds: a rendered body is folded into an *expandable*
// (collapsed) Telegram quote only when it is genuinely long — past either limit,
// or already truncated. Short and medium notes read better shown in full, so
// they get a plain blockquote. Telegram itself previews an expandable quote at
// ~3 lines, so these sit deliberately higher to avoid collapsing modest bodies.
// Raise to collapse less, lower to collapse more.
const (
	collapseBodyRuneThreshold = 600
	collapseBodyLineThreshold = 10
)

// bodyBlock renders a Markdown body and, when it has no block-level elements,
// wraps it in a Telegram quote — expandable (collapsed) only when long, plain
// otherwise. Telegram forbids nesting blockquote/pre, so bodies that already
// contain them are shown as-is.
func bodyBlock(raw string, maxRunes int) (string, bool) {
	rendered, truncated := renderGitHubMarkdown(raw, maxRunes)
	// Rendering can make the visible text LONGER than the raw slice — a Markdown
	// image becomes an "Image: " prefix — so a raw-rune cap alone can overshoot
	// Telegram's 4096 visible-char limit and get the whole message rejected.
	// Shrink the raw cap and re-render until the visible body fits its budget.
	for rawCap := maxRunes; rawCap > 64 && visibleRuneCount(rendered) > maxRunes; {
		rawCap = rawCap * 3 / 4
		rendered, _ = renderGitHubMarkdown(raw, rawCap)
		truncated = true
	}
	if rendered == "" {
		return "", false
	}
	if !strings.Contains(rendered, "<pre") && !strings.Contains(rendered, "<blockquote") {
		open := "<blockquote>"
		if shouldCollapseBody(rendered, truncated) {
			open = "<blockquote expandable>"
		}
		rendered = open + rendered + "</blockquote>"
	}
	return rendered, truncated
}

// shouldCollapseBody reports whether a rendered body is long enough to fold into
// an expandable quote: a truncated body is always long, otherwise it is judged
// by line count (tall) or visible length (wordy).
func shouldCollapseBody(rendered string, truncated bool) bool {
	if truncated {
		return true
	}
	if strings.Count(rendered, "\n")+1 > collapseBodyLineThreshold {
		return true
	}
	return visibleRuneCount(rendered) > collapseBodyRuneThreshold
}

// visibleRuneCount approximates the user-visible length of rendered HTML by
// skipping tag spans — Telegram counts entity text, not the <a>/<b>/<code> tags,
// so this reflects how long the message actually reads.
func visibleRuneCount(s string) int {
	n, inTag := 0, false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			n++
		}
	}
	return n
}

func truncateText(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 1 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func plural(count int, one, many string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", count, many)
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func esc(value string) string {
	if value == "" {
		return "-"
	}
	return html.EscapeString(value)
}

func escAttr(value string) string {
	return html.EscapeString(value)
}

// safeURL returns the URL only if it uses an http(s) scheme with a host.
// GitHub-generated event URLs are always https://github.com/..., so this drops
// any unexpected scheme (javascript:, tg:, data:, ...) before it reaches a
// Telegram <a href> link, which would otherwise be deliverable to a group.
func safeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return ""
	}
	return strings.TrimSpace(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return value[:idx]
	}
	return value
}
