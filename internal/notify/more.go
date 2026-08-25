// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

const (
	maxMoreNames = 12
)

// PRMoreSnapshot is the durable More overlay payload stored in
// notification_jobs.more_json. It is the webhook snapshot, not a live GitHub
// fetch. Do not log Body.
type PRMoreSnapshot struct {
	Number       int      `json:"number,omitempty"`
	Title        string   `json:"title,omitempty"`
	URL          string   `json:"url,omitempty"`
	Body         string   `json:"body,omitempty"`
	RepoFullName string   `json:"repo_full_name,omitempty"`
	HeadBranch   string   `json:"head_branch,omitempty"`
	BaseBranch   string   `json:"base_branch,omitempty"`
	HeadSHA      string   `json:"head_sha,omitempty"`
	IsDraft      bool     `json:"is_draft,omitempty"`
	Author       string   `json:"author,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Assignees    []string `json:"assignees,omitempty"`
	Reviewers    []string `json:"reviewers,omitempty"`
	Additions    int      `json:"additions,omitempty"`
	Deletions    int      `json:"deletions,omitempty"`
	ChangedFiles int      `json:"changed_files,omitempty"`
	CommitCount  int      `json:"commit_count,omitempty"`
	DiffURL      string   `json:"diff_url,omitempty"`
	Merged       bool     `json:"merged,omitempty"`
	MergedBy     string   `json:"merged_by,omitempty"`
	MergedAt     string   `json:"merged_at,omitempty"`
	ClosedAt     string   `json:"closed_at,omitempty"`
	Action       string   `json:"action,omitempty"`
}

func PRMoreFromEvent(event Event) PRMoreSnapshot {
	author := firstNonEmpty(event.Author, event.Actor)
	return PRMoreSnapshot{
		Number:       event.Number,
		Title:        event.Title,
		URL:          event.URL,
		Body:         event.Body,
		RepoFullName: event.RepoFullName,
		HeadBranch:   event.HeadBranch,
		BaseBranch:   event.Branch,
		HeadSHA:      event.HeadSHA,
		IsDraft:      event.IsDraft,
		Author:       author,
		Labels:       event.Labels,
		Assignees:    event.Assignees,
		Reviewers:    event.Reviewers,
		Additions:    event.Additions,
		Deletions:    event.Deletions,
		ChangedFiles: event.ChangedFiles,
		CommitCount:  event.CommitCount,
		DiffURL:      event.DiffURL,
		Merged:       event.Merged,
		MergedBy:     event.MergedBy,
		MergedAt:     event.MergedAt,
		ClosedAt:     event.ClosedAt,
		Action:       event.Action,
	}
}

func PRMoreJSON(event Event) (json.RawMessage, error) {
	if event.Type != "pull_request" {
		return nil, nil
	}
	raw, err := json.Marshal(PRMoreFromEvent(event))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// PRMoreHTML renders the ephemeral overlay. It is not a copy of the public
// card: description is shown without a wrapping <details>, and the button row
// is Open / Files / Commits / Checks.
func PRMoreHTML(snapshot PRMoreSnapshot) string {
	heading := prHeading(Event{Title: snapshot.Title, Number: snapshot.Number})
	var meta []string
	if span := prBranchSpan(snapshot.HeadBranch, snapshot.BaseBranch); span != "" {
		meta = append(meta, span)
	}
	if sha := shortSHA(snapshot.HeadSHA); sha != "" {
		meta = append(meta, "<code>"+htmlEscape(sha)+"</code>")
	}
	if snapshot.IsDraft {
		meta = append(meta, "Draft")
	}
	if snapshot.Merged {
		meta = append(meta, "merged")
	}
	if name := boldName(snapshot.Author); name != "" {
		meta = append(meta, name)
	}

	var stats []string
	if snapshot.Additions > 0 {
		stats = append(stats, fmt.Sprintf("+%d", snapshot.Additions))
	}
	if snapshot.Deletions > 0 {
		stats = append(stats, fmt.Sprintf("−%d", snapshot.Deletions))
	}
	if snapshot.ChangedFiles > 0 {
		stats = append(stats, plural(snapshot.ChangedFiles, "file", "files"))
	}
	if snapshot.CommitCount > 0 {
		stats = append(stats, plural(snapshot.CommitCount, "commit", "commits"))
	}

	var people []string
	if line := namedList("Labels", snapshot.Labels); line != "" {
		people = append(people, line)
	}
	if line := namedList("Assignees", snapshot.Assignees); line != "" {
		people = append(people, line)
	}
	if line := namedList("Reviewers", snapshot.Reviewers); line != "" {
		people = append(people, line)
	}
	if snapshot.MergedBy != "" {
		people = append(people, "Merged by "+boldName(snapshot.MergedBy))
	}
	if snapshot.ClosedAt != "" && !snapshot.Merged {
		people = append(people, "Closed "+htmlEscape(snapshot.ClosedAt))
	} else if snapshot.MergedAt != "" {
		people = append(people, "Merged "+htmlEscape(snapshot.MergedAt))
	}

	prepared := renderGitHubBody(snapshot.Body, maxReleaseBodyRunes)
	body := wrapOverlayBody(prepared)
	if prepared.Truncated {
		if link := safeURL(snapshot.URL); link != "" {
			body += "\n\n" + fmt.Sprintf(`<a href="%s">Read more</a>`, escAttr(link))
		}
	}

	open := safeURL(snapshot.URL)
	var buttons []actionButton
	if open != "" {
		buttons = append(buttons, actionButton{Label: "Open", URL: open, Primary: true})
	}
	if files := prSubpageURL(snapshot.URL, "files"); files != "" {
		buttons = append(buttons, actionButton{Label: "Files", URL: files})
	}
	if commits := prSubpageURL(snapshot.URL, "commits"); commits != "" {
		buttons = append(buttons, actionButton{Label: "Commits", URL: commits})
	}
	if checks := prSubpageURL(snapshot.URL, "checks"); checks != "" {
		buttons = append(buttons, actionButton{Label: "Checks", URL: checks})
	}

	return boundNotification(Notification{
		RichHTML: joinSections(
			richHeading(iconPR, heading),
			quietLine(joinInline(meta...)),
			quietLine(strings.Join(stats, " · ")),
			richLineList(people),
			body,
			renderActionButtons(buttons),
		),
	}).RichHTML
}

func wrapOverlayBody(body renderedGitHubBody) string {
	if strings.TrimSpace(body.RichHTML) == "" {
		return ""
	}
	if shouldCollapseBody(body.Source, body.Truncated) && !bodyHasRichStructure(body.RichHTML) {
		return "<blockquote expandable>" + flattenHTMLForExpandableQuote(body.RichHTML) + "</blockquote>"
	}
	if shouldCollapseBody(body.Source, body.Truncated) {
		return body.RichHTML
	}
	return "<blockquote>" + body.RichHTML + "</blockquote>"
}

func namedList(label string, names []string) string {
	names = clipNames(names)
	if len(names) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		escaped = append(escaped, "<code>"+htmlEscape(name)+"</code>")
	}
	if len(escaped) == 0 {
		return ""
	}
	return htmlEscape(label) + ": " + strings.Join(escaped, " ")
}

func clipNames(names []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= maxMoreNames {
			break
		}
	}
	return out
}

func prSubpageURL(htmlURL, page string) string {
	base := strings.TrimRight(strings.TrimSpace(htmlURL), "/")
	if base == "" || page == "" {
		return ""
	}
	return safeURL(base + "/" + page)
}

func htmlEscape(value string) string {
	return strings.TrimSpace(html.EscapeString(value))
}
