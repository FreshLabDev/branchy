// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxMoreFileRows      = 30
	maxMoreFilePathRunes = 40
)

// PRMoreSnapshot is the durable More overlay header stored in
// notification_jobs.more_json. File rows are fetched live on tap, not stored
// here. Do not log Body; new jobs omit it.
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

// PRFile is one pull-request file row for the More overlay table.
type PRFile struct {
	Filename         string
	PreviousFilename string
	Status           string
	Additions        int
	Deletions        int
	Changes          int
}

func PRMoreFromEvent(event Event) PRMoreSnapshot {
	author := firstNonEmpty(event.Author, event.Actor)
	return PRMoreSnapshot{
		Number:       event.Number,
		Title:        event.Title,
		URL:          event.URL,
		RepoFullName: event.RepoFullName,
		HeadBranch:   event.HeadBranch,
		BaseBranch:   event.Branch,
		HeadSHA:      event.HeadSHA,
		IsDraft:      event.IsDraft,
		Author:       author,
		Additions:    event.Additions,
		Deletions:    event.Deletions,
		ChangedFiles: event.ChangedFiles,
		CommitCount:  event.CommitCount,
		DiffURL:      event.DiffURL,
		Merged:       event.Merged,
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

// PRMoreHTML renders the ephemeral overlay: a thin PR header, diff stats, and
// a file table. Description, labels, and people from more_json are ignored.
func PRMoreHTML(snapshot PRMoreSnapshot, files []PRFile) string {
	heading := prHeading(Event{Title: snapshot.Title, Number: snapshot.Number})
	var meta []string
	if span := prBranchSpan(snapshot.HeadBranch, snapshot.BaseBranch); span != "" {
		meta = append(meta, span)
	}
	if snapshot.IsDraft {
		meta = append(meta, "Draft")
	}
	if snapshot.Merged {
		meta = append(meta, "merged")
	}

	additions, deletions, fileCount := moreDiffStats(snapshot, files)
	var stats []string
	if additions > 0 {
		stats = append(stats, fmt.Sprintf("+%d", additions))
	}
	if deletions > 0 {
		stats = append(stats, fmt.Sprintf("−%d", deletions))
	}
	if fileCount > 0 {
		stats = append(stats, plural(fileCount, "file", "files"))
	}
	if snapshot.CommitCount > 0 {
		stats = append(stats, plural(snapshot.CommitCount, "commit", "commits"))
	}

	var body string
	switch {
	case len(files) > 0:
		body = renderMoreFileTable(files, fileCount)
	case additions == 0 && deletions == 0 && fileCount == 0:
		body = "<p><i>GitHub has not computed the diff yet.</i></p>"
	}

	open := safeURL(snapshot.URL)
	var buttons []actionButton
	if open != "" {
		buttons = append(buttons, actionButton{Label: "Open", URL: open, Primary: true})
	}
	if filesURL := prSubpageURL(snapshot.URL, "files"); filesURL != "" {
		buttons = append(buttons, actionButton{Label: "Files", URL: filesURL})
	}

	return boundNotification(Notification{
		RichHTML: joinSections(
			richHeading(iconPR, heading),
			quietLine(joinInline(meta...)),
			quietLine(strings.Join(stats, " · ")),
			body,
			renderActionButtons(buttons),
		),
	}).RichHTML
}

func moreDiffStats(snapshot PRMoreSnapshot, files []PRFile) (additions, deletions, fileCount int) {
	additions = snapshot.Additions
	deletions = snapshot.Deletions
	fileCount = snapshot.ChangedFiles
	if len(files) == 0 {
		return
	}
	additions, deletions = 0, 0
	for _, file := range files {
		additions += file.Additions
		deletions += file.Deletions
	}
	fileCount = len(files)
	if snapshot.ChangedFiles > fileCount {
		fileCount = snapshot.ChangedFiles
	}
	return
}

func renderMoreFileTable(files []PRFile, fileCount int) string {
	sorted := sortedPRFiles(files)
	shown := sorted
	if len(shown) > maxMoreFileRows {
		shown = shown[:maxMoreFileRows]
	}

	var b strings.Builder
	b.WriteString(`<table bordered striped compact><tr><th>File</th><th>+</th><th>−</th></tr>`)
	for _, file := range shown {
		b.WriteString("<tr><td><code>")
		b.WriteString(htmlEscape(fileLabel(file)))
		b.WriteString("</code></td><td>")
		if file.Additions > 0 {
			b.WriteString(fmt.Sprintf("+%d", file.Additions))
		}
		b.WriteString("</td><td>")
		if file.Deletions > 0 {
			b.WriteString(fmt.Sprintf("−%d", file.Deletions))
		}
		b.WriteString("</td></tr>")
	}
	b.WriteString("</table>")

	remaining := fileCount - len(shown)
	if extra := len(sorted) - len(shown); extra > remaining {
		remaining = extra
	}
	if remaining > 0 {
		b.WriteString(fmt.Sprintf("<p><i>and %d more</i></p>", remaining))
	}
	return b.String()
}

func sortedPRFiles(files []PRFile) []PRFile {
	out := append([]PRFile(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		ci, cj := fileChangeCount(out[i]), fileChangeCount(out[j])
		if ci != cj {
			return ci > cj
		}
		return out[i].Filename < out[j].Filename
	})
	return out
}

func fileChangeCount(file PRFile) int {
	if file.Changes > 0 {
		return file.Changes
	}
	return file.Additions + file.Deletions
}

func fileLabel(file PRFile) string {
	name := truncatePathLeft(file.Filename)
	prev := strings.TrimSpace(file.PreviousFilename)
	if prev == "" || strings.EqualFold(file.Status, "added") {
		return name
	}
	if strings.EqualFold(file.Status, "renamed") || prev != strings.TrimSpace(file.Filename) {
		return truncatePathLeft(prev) + " → " + name
	}
	return name
}

func truncatePathLeft(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\n", " "))
	if path == "" {
		return path
	}
	if utf8.RuneCountInString(path) <= maxMoreFilePathRunes {
		return path
	}
	runes := []rune(path)
	keep := maxMoreFilePathRunes - 1
	return "…" + string(runes[len(runes)-keep:])
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
