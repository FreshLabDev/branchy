// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"strings"
)

// prepareGitHubBody normalizes and truncates a GitHub Markdown body for Telegram
// Rich Markdown (sendRichMessage). Telegram renders GFM natively, so Branchy
// does not convert Markdown to HTML — only light cleanup and size limits.
func prepareGitHubBody(raw string, maxRunes int) (string, bool) {
	body := normalizeMarkdown(raw)
	body, truncated := truncateRunes(body, maxRunes)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	if truncated {
		body += "\n\n..."
	}
	return body, truncated
}

// wrapBody wraps a prepared body for scannable group delivery:
// short notes become a Markdown blockquote; long or truncated notes use a
// collapsible <details> block (Rich Markdown / Rich HTML hybrid).
func wrapBody(body, summary string, truncated bool) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if summary == "" {
		summary = "Notes"
	}
	if shouldCollapseBody(body, truncated) {
		return "<details>\n<summary>" + esc(summary) + "</summary>\n\n" + body + "\n\n</details>"
	}
	return quoteMarkdown(body)
}

func quoteMarkdown(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out[i] = ">"
			continue
		}
		out[i] = "> " + line
	}
	return strings.Join(out, "\n")
}

func normalizeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	value = strings.ReplaceAll(value, "<br />", "\n")
	value = strings.ReplaceAll(value, "<br>", "\n")
	return stripHTMLComments(value)
}

func stripHTMLComments(value string) string {
	for {
		start := strings.Index(value, "<!--")
		if start < 0 {
			return value
		}
		end := strings.Index(value[start+4:], "-->")
		if end < 0 {
			return value[:start]
		}
		value = value[:start] + value[start+4+end+3:]
	}
}

func truncateRunes(value string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", strings.TrimSpace(value) != ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return strings.TrimSpace(string(runes[:maxRunes])), true
}
