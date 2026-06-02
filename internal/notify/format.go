// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"fmt"
	"html"
	"strings"
)

type Event struct {
	Type          string
	RepoFullName  string
	DefaultBranch string
	Actor         string
	Branch        string
	Title         string
	Summary       string
	URL           string
}

func GitHubEvent(event Event) string {
	lines := []string{
		fmt.Sprintf("<b>%s</b> %s", esc(event.RepoFullName), esc(event.Type)),
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
	if event.URL != "" {
		lines = append(lines, fmt.Sprintf(`<a href="%s">Open on GitHub</a>`, escAttr(event.URL)))
	}
	return strings.Join(lines, "\n")
}

func TestMessage(repoFullName string) string {
	return "<b>" + esc(repoFullName) + "</b> test\nBranchy can deliver notifications to this chat."
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
