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
		return "Push"
	case "pull_request":
		return "Pull request"
	case "release":
		return "Release"
	default:
		return eventType
	}
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
	return raw
}
