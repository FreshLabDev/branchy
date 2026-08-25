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
		notification = commitNotification(event)
	case "pull_request":
		notification = pullRequestNotification(event)
	case "release":
		notification = releaseNotification(event)
	default:
		notification = genericNotification(event)
	}
	return boundNotification(notification)
}

func TestMessage(repoFullName string) string {
	return "<b>" + esc(repoFullName) + "</b>\n\nTest notification. Branchy can deliver notifications to this chat."
}

func TestNotification(repoFullName string) Notification {
	return boundNotification(Notification{
		RichHTML: joinSections(
			richTitle("", repoFullName, "Test notification"),
			"<p>Branchy can deliver notifications to this chat.</p>",
		),
		FallbackHTML: TestMessage(repoFullName),
	})
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

func commitNotification(event Event) Notification {
	count := event.CommitCount
	if count == 0 {
		count = len(event.Commits)
	}
	summary := plural(count, "new commit", "new commits")
	if event.Branch != "" {
		summary += fmt.Sprintf(" · <code>%s</code>", esc(event.Branch))
	}

	var list []string
	used := 0
	for _, commit := range event.Commits {
		line, cost := formatCommitLine(commit)
		if len(list) > 0 && (len(list) >= maxCommitLines || used+cost > maxCommitListRunes) {
			break
		}
		list = append(list, line)
		used += cost
	}
	if remaining := len(event.Commits) - len(list); remaining > 0 {
		list = append(list, fmt.Sprintf("<i>+%s</i>", plural(remaining, "more commit", "more commits")))
	}
	richList := richLineList(list)
	classicList := strings.Join(list, "\n")

	var actor string
	if event.Actor != "" {
		actor = "Pushed by <b>" + esc(event.Actor) + "</b>"
	}
	openURL := safeURL(firstNonEmpty(event.CompareURL, event.URL))
	if openURL == "" && len(event.Commits) == 1 {
		openURL = safeURL(event.Commits[0].URL)
	}
	var buttons []actionButton
	if openURL != "" {
		label := "Open compare"
		if event.CompareURL == "" {
			label = "Open on GitHub"
		}
		buttons = append(buttons, actionButton{Label: label, URL: openURL, Primary: true})
	}
	if len(event.Commits) == 1 {
		if copy := copyableSHA(event.Commits[0].SHA); copy != "" {
			buttons = append(buttons, actionButton{Label: "Copy SHA", CopyText: copy})
		}
	}

	classicMeta := actor
	if link := safeURL(firstNonEmpty(event.CompareURL, event.URL)); link != "" {
		classicMeta = joinInline(classicMeta, fmt.Sprintf(`<a href="%s">Compare changes</a>`, escAttr(link)))
	}

	return Notification{
		RichHTML: joinSections(
			richTitle(iconCommits, event.RepoFullName, summary),
			richList,
			richFooter(actor),
			renderActionButtons(buttons),
		),
		FallbackHTML: joinSections(
			classicTitle(iconCommits, event.RepoFullName, summary),
			classicList,
			classicMeta,
		),
	}
}

func pullRequestNotification(event Event) Notification {
	action := humanizePRAction(event)
	summary := "Pull request " + esc(action)

	var richTitleLine, classicTitleLine string
	if title := truncateText(event.Title, maxTitleRunes); title != "" {
		label := title
		if event.Number > 0 {
			label = fmt.Sprintf("#%d %s", event.Number, title)
		}
		richTitleLine = esc(label)
		classicTitleLine = richTitleLine
		if link := safeURL(event.URL); link != "" {
			classicTitleLine = fmt.Sprintf(`<a href="%s">%s</a>`, escAttr(link), esc(label))
		}
	}
	var sub []string
	if event.Branch != "" {
		sub = append(sub, "into <code>"+esc(event.Branch)+"</code>")
	}
	if event.Actor != "" {
		sub = append(sub, "by <b>"+esc(event.Actor)+"</b>")
	}
	subLine := strings.Join(sub, " · ")

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

	var buttons []actionButton
	if link := safeURL(event.URL); link != "" {
		buttons = append(buttons, actionButton{Label: "Open pull request", URL: link, Primary: true})
	}
	if event.Number > 0 {
		copy := fmt.Sprintf("#%d", event.Number)
		if len(copy) <= maxCopyTextRunes {
			buttons = append(buttons, actionButton{Label: "Copy " + copy, CopyText: copy})
		}
	}

	return Notification{
		RichHTML: joinSections(
			richTitle(iconPR, event.RepoFullName, summary),
			richParagraphs(richTitleLine, subLine),
			richBody,
			renderActionButtons(buttons),
		),
		FallbackHTML: joinSections(
			classicTitle(iconPR, event.RepoFullName, summary),
			classicParagraphs(classicTitleLine, subLine),
			fallbackBody,
		),
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
	maxCopyTextRunes    = 256
	// Absolute guard for the whole notification payload.
	maxRichMessageBytes = 32768
)

func releaseNotification(event Event) Notification {
	label := "Release"
	if event.Prerelease {
		label = "Pre-release"
	}
	version := truncateText(firstNonEmpty(event.Title, event.TagName), maxTitleRunes)
	richSummary := fmt.Sprintf("<b>%s</b>", label)
	classicSummary := richSummary
	if version != "" {
		richSummary += " · " + esc(version)
		if link := safeURL(event.URL); link != "" {
			classicSummary += fmt.Sprintf(` · <a href="%s">%s</a>`, escAttr(link), esc(version))
		} else {
			classicSummary += " · " + esc(version)
		}
	}

	var meta string
	if event.Actor != "" {
		meta = "by <b>" + esc(event.Actor) + "</b>"
	}

	prepared := renderGitHubBody(event.Body, maxReleaseBodyRunes)
	richBody, fallbackBody := wrapRenderedBody(prepared, "Release notes")
	if prepared.Truncated {
		if link := safeURL(event.URL); link != "" {
			more := "\n\n" + fmt.Sprintf(`<a href="%s">Full release notes</a>`, escAttr(link))
			richBody += more
			fallbackBody += more
		}
	}

	var buttons []actionButton
	if link := safeURL(event.URL); link != "" {
		buttons = append(buttons, actionButton{Label: "Open release", URL: link, Primary: true})
	}
	if copy := copyableText(strings.TrimSpace(event.TagName)); copy != "" {
		buttons = append(buttons, actionButton{Label: "Copy tag", CopyText: copy})
	}

	return Notification{
		RichHTML: joinSections(
			richTitle(iconRelease, event.RepoFullName, richSummary),
			richFooter(meta),
			richBody,
			renderActionButtons(buttons),
		),
		FallbackHTML: joinSections(
			classicTitle(iconRelease, event.RepoFullName, classicSummary),
			meta,
			fallbackBody,
		),
	}
}

func genericNotification(event Event) Notification {
	summary := esc(eventTitle(event.Type))
	var details []string
	if event.Actor != "" {
		details = append(details, "Actor: "+esc(event.Actor))
	}
	if event.Branch != "" {
		details = append(details, "Branch: "+esc(event.Branch))
	}
	if event.Title != "" {
		details = append(details, esc(event.Title))
	}
	if event.Summary != "" {
		details = append(details, esc(event.Summary))
	}
	detailHTML := strings.Join(details, "\n")
	richDetails := richLineList(details)
	open := safeURL(event.URL)
	classic := detailHTML
	if open != "" {
		classic = joinSections(classic, fmt.Sprintf(`<a href="%s">Open on GitHub</a>`, escAttr(open)))
	}
	var buttons []actionButton
	if open != "" {
		buttons = append(buttons, actionButton{Label: "Open on GitHub", URL: open, Primary: true})
	}
	return Notification{
		RichHTML: joinSections(
			richTitle("", event.RepoFullName, summary),
			richDetails,
			renderActionButtons(buttons),
		),
		FallbackHTML: joinSections(
			fmt.Sprintf("<b>%s</b> %s", esc(event.RepoFullName), summary),
			classic,
		),
	}
}

type actionButton struct {
	Label    string
	URL      string
	CopyText string
	Primary  bool
}

func renderActionButtons(buttons []actionButton) string {
	if len(buttons) == 0 {
		return ""
	}
	var parts []string
	for _, button := range buttons {
		label := html.EscapeString(button.Label)
		switch {
		case button.URL != "":
			style := ""
			if button.Primary {
				style = ` style="primary"`
			}
			parts = append(parts, fmt.Sprintf(`<tg-button type="url"%s url="%s">%s</tg-button>`, style, escAttr(button.URL), label))
		case button.CopyText != "":
			parts = append(parts, fmt.Sprintf(`<tg-button type="copy_text" text="%s">%s</tg-button>`, escAttr(button.CopyText), label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return `<tg-button-row align="left">` + strings.Join(parts, "") + `</tg-button-row>`
}

func richTitle(icon, repo, summaryHTML string) string {
	heading := esc(repo)
	if icon != "" {
		heading = icon + " " + heading
	}
	return fmt.Sprintf("<h2>%s</h2>\n<p>%s</p>\n<hr>", heading, summaryHTML)
}

func classicTitle(icon, repo, summaryHTML string) string {
	heading := esc(repo)
	if icon != "" {
		heading = icon + " <b>" + heading + "</b>"
	} else {
		heading = "<b>" + heading + "</b>"
	}
	return heading + "\n\n" + summaryHTML
}

func richFooter(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "<footer>" + text + "</footer>"
}

func joinInline(parts ...string) string {
	var out []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, " · ")
}

func copyableSHA(sha string) string {
	return copyableText(strings.TrimSpace(sha))
}

func copyableText(value string) string {
	if value == "" || len([]rune(value)) > maxCopyTextRunes {
		return ""
	}
	return value
}

// formatCommitLine renders one commit row and returns its approximate visible
// length (sha + space + subject) so commitNotification can budget the list.
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

func richParagraphs(lines ...string) string {
	var parts []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			parts = append(parts, "<p>"+line+"</p>")
		}
	}
	return strings.Join(parts, "\n")
}

func classicParagraphs(lines ...string) string {
	var parts []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "\n")
}

func richLineList(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "<p>" + strings.Join(lines, "<br>\n") + "</p>"
}

// Body collapse thresholds: long notes fold into expandable quotes or
// <details>; short notes stay as a plain blockquote. Raise to collapse less.
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
