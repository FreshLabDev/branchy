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

// Notification carries both the preferred Bot API Rich HTML payload and a
// classic HTML fallback that fits sendMessage. Keeping both representations in
// the outbox makes retries deterministic and rollback-safe.
type Notification struct {
	RichHTML     string
	FallbackHTML string
}

// GitHubEvent returns the preferred Rich HTML representation. It remains as a
// compatibility seam for formatting callers and tests; delivery code should
// persist both variants from GitHubNotification.
func GitHubEvent(event Event) string {
	return GitHubNotification(event).RichHTML
}

func GitHubNotification(event Event) Notification {
	var notification Notification
	switch event.Type {
	case "push":
		text := commitEvent(event)
		notification = Notification{RichHTML: text, FallbackHTML: text}
	case "pull_request":
		notification = pullRequestNotification(event)
	case "release":
		notification = releaseNotification(event)
	default:
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
		text := strings.Join(lines, "\n")
		notification = Notification{RichHTML: text, FallbackHTML: text}
	}
	return boundNotification(notification)
}

func TestMessage(repoFullName string) string {
	return "<b>" + esc(repoFullName) + "</b>\n\nTest notification. Branchy can deliver notifications to this chat."
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
// instead of a fixed handful. Budgeted by approximate visible text length.
// GitHub caps a push payload at ~20 commits; maxCommitLines is a high safety
// bound against crafted input.
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
	// Double newline keeps the title and event summary visually separate in
	// Telegram Rich Messages.
	header := fmt.Sprintf("%s <b>%s</b>\n\n%s", iconCommits, esc(event.RepoFullName), summary)

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

func pullRequestNotification(event Event) Notification {
	action := humanizePRAction(event)
	header := fmt.Sprintf("%s <b>%s</b>\n\nPull request %s", iconPR, esc(event.RepoFullName), esc(action))

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

	var richBody, fallbackBody string
	if showsPRBody(action) {
		prepared := renderGitHubBody(event.Body, maxPRBodyRunes)
		richBody, fallbackBody = wrapRenderedBody(prepared, "Description")
		if prepared.Truncated {
			if link := safeURL(event.URL); link != "" {
				more := fmt.Sprintf("\n\n"+`<a href="%s">Read more</a>`, escAttr(link))
				richBody += more
				fallbackBody += more
			}
		}
	}

	return Notification{
		RichHTML:     joinSections(header, strings.Join(meta, "\n"), richBody),
		FallbackHTML: joinSections(header, strings.Join(meta, "\n"), fallbackBody),
	}
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

// Soft body caps stay well under Telegram Rich Message's 32_768 UTF-8 limit so
// group chats stay scannable even when release notes include media and tables.
const (
	maxPRBodyRunes      = 2500
	maxReleaseBodyRunes = 10000
	maxTitleRunes       = 200
	// Absolute guard for the whole notification payload.
	maxRichMessageBytes = 32768
)

func releaseNotification(event Event) Notification {
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
	header := fmt.Sprintf("%s <b>%s</b>\n\n%s", iconRelease, esc(event.RepoFullName), versionLine)

	var meta string
	if event.Actor != "" {
		meta = "by <b>" + esc(event.Actor) + "</b>"
	}

	prepared := renderGitHubBody(event.Body, maxReleaseBodyRunes)
	richBody, fallbackBody := wrapRenderedBody(prepared, "Release notes")
	rich := joinSections(header, meta, richBody)
	fallback := joinSections(header, meta, fallbackBody)
	if prepared.Truncated {
		if link := safeURL(event.URL); link != "" {
			more := "\n\n" + fmt.Sprintf(`<a href="%s">Full release notes</a>`, escAttr(link))
			rich += more
			fallback += more
		}
	}
	return Notification{RichHTML: rich, FallbackHTML: fallback}
}

// formatCommitLine renders one commit row and returns its approximate visible
// length (sha + space + subject) so commitEvent can budget the list.
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

// Body collapse thresholds: long notes fold into <details>; short notes use a
// plain Markdown blockquote. Raise to collapse less, lower to collapse more.
const (
	collapseBodyRuneThreshold = 600
	collapseBodyLineThreshold = 10
)

func shouldCollapseBody(body string, truncated bool) bool {
	if truncated {
		return true
	}
	if strings.Count(body, "\n")+1 > collapseBodyLineThreshold {
		return true
	}
	return len([]rune(body)) > collapseBodyRuneThreshold
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
