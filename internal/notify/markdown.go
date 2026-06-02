// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func renderGitHubMarkdown(markdown string, maxRunes int) (string, bool) {
	markdown = normalizeMarkdown(markdown)
	markdown, truncated := truncateRunes(markdown, maxRunes)
	if strings.TrimSpace(markdown) == "" {
		return "", false
	}

	var out []string
	var quote []string
	var code []string
	inCode := false

	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		out = append(out, "<blockquote>"+esc(strings.Join(quote, "\n"))+"</blockquote>")
		quote = nil
	}
	flushCode := func() {
		if len(code) == 0 {
			out = append(out, "<pre></pre>")
		} else {
			out = append(out, "<pre>"+esc(strings.Join(code, "\n"))+"</pre>")
		}
		code = nil
	}

	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flushQuote()
			if inCode {
				flushCode()
				inCode = false
			} else {
				inCode = true
			}
			continue
		}
		if inCode {
			code = append(code, strings.TrimRight(line, "\r"))
			continue
		}
		if trimmed == "" {
			flushQuote()
			appendBlankLine(&out)
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			quote = append(quote, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
			continue
		}

		flushQuote()
		switch {
		case isRule(trimmed):
			appendBlankLine(&out)
		case markdownHeading(trimmed) != "":
			out = append(out, "<b>"+renderMarkdownInline(markdownHeading(trimmed))+"</b>")
		case unorderedListItem(trimmed) != "":
			out = append(out, "- "+renderMarkdownInline(stripTaskMarker(unorderedListItem(trimmed))))
		case orderedListItem(trimmed) != "":
			number, item := parseOrderedListItem(trimmed)
			out = append(out, fmt.Sprintf("%d. %s", number, renderMarkdownInline(stripTaskMarker(item))))
		default:
			out = append(out, renderMarkdownInline(trimmed))
		}
	}
	flushQuote()
	if inCode {
		flushCode()
	}

	rendered := strings.TrimSpace(compactBlankLines(out))
	if truncated && rendered != "" {
		rendered += "\n..."
	}
	return rendered, truncated
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

func appendBlankLine(lines *[]string) {
	if len(*lines) == 0 || (*lines)[len(*lines)-1] == "" {
		return
	}
	*lines = append(*lines, "")
}

func compactBlankLines(lines []string) string {
	var compact []string
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if !blank && len(compact) > 0 {
				compact = append(compact, "")
			}
			blank = true
			continue
		}
		compact = append(compact, line)
		blank = false
	}
	return strings.Join(compact, "\n")
}

func isRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	first := line[0]
	if first != '-' && first != '*' && first != '_' {
		return false
	}
	for _, r := range line {
		if r != rune(first) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func markdownHeading(line string) string {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return ""
	}
	return strings.TrimSpace(line[level+1:])
}

func unorderedListItem(line string) string {
	if len(line) < 3 {
		return ""
	}
	if (line[0] == '-' || line[0] == '*' || line[0] == '+') && unicode.IsSpace(rune(line[1])) {
		return strings.TrimSpace(line[2:])
	}
	return ""
}

func orderedListItem(line string) string {
	_, item := parseOrderedListItem(line)
	return item
}

func parseOrderedListItem(line string) (int, string) {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(line) || (line[i] != '.' && line[i] != ')') {
		return 0, ""
	}
	if i+1 >= len(line) || !unicode.IsSpace(rune(line[i+1])) {
		return 0, ""
	}
	number, err := strconv.Atoi(line[:i])
	if err != nil {
		return 0, ""
	}
	return number, strings.TrimSpace(line[i+2:])
}

func stripTaskMarker(item string) string {
	trimmed := strings.TrimSpace(item)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "[x] "):
		return "[x] " + strings.TrimSpace(trimmed[4:])
	case strings.HasPrefix(lower, "[ ] "):
		return "[ ] " + strings.TrimSpace(trimmed[4:])
	default:
		return item
	}
}

func renderMarkdownInline(value string) string {
	return renderMarkdownInlineDepth(value, 0)
}

func renderMarkdownInlineDepth(value string, depth int) string {
	if depth > 4 || value == "" {
		return esc(value)
	}
	var b strings.Builder
	for i := 0; i < len(value); {
		lower := strings.ToLower(value[i:])
		switch {
		case strings.HasPrefix(value[i:], "!["):
			if alt, link, end, ok := parseMarkdownLink(value, i+1); ok {
				if safe := safeURL(link); safe != "" {
					b.WriteString(`Image: <a href="`)
					b.WriteString(escAttr(safe))
					b.WriteString(`">`)
					b.WriteString(esc(alt))
					b.WriteString("</a>")
				} else {
					b.WriteString("Image: ")
					b.WriteString(esc(alt))
				}
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "["):
			if label, link, end, ok := parseMarkdownLink(value, i); ok {
				if safe := safeURL(link); safe != "" {
					b.WriteString(`<a href="`)
					b.WriteString(escAttr(safe))
					b.WriteString(`">`)
					b.WriteString(esc(label))
					b.WriteString("</a>")
				} else {
					b.WriteString(esc(label))
				}
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "`"):
			if inner, end, ok := parseDelimited(value, i, "`"); ok {
				b.WriteString("<code>")
				b.WriteString(esc(inner))
				b.WriteString("</code>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "***"):
			if inner, end, ok := parseDelimited(value, i, "***"); ok {
				b.WriteString("<b><i>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</i></b>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "**"):
			if inner, end, ok := parseDelimited(value, i, "**"); ok {
				b.WriteString("<b>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</b>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "__"):
			if inner, end, ok := parseDelimited(value, i, "__"); ok {
				b.WriteString("<b>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</b>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "~~"):
			if inner, end, ok := parseDelimited(value, i, "~~"); ok {
				b.WriteString("<s>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</s>")
				i = end
				continue
			}
		case strings.HasPrefix(lower, "<ins>"):
			if inner, end, ok := parseHTMLPair(value, i, "ins"); ok {
				b.WriteString("<u>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</u>")
				i = end
				continue
			}
		case strings.HasPrefix(lower, "<u>"):
			if inner, end, ok := parseHTMLPair(value, i, "u"); ok {
				b.WriteString("<u>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</u>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "*"):
			if inner, end, ok := parseDelimited(value, i, "*"); ok {
				b.WriteString("<i>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</i>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "_") && delimiterBoundary(value, i):
			if inner, end, ok := parseDelimited(value, i, "_"); ok && delimiterBoundary(value, end-1) {
				b.WriteString("<i>")
				b.WriteString(renderMarkdownInlineDepth(inner, depth+1))
				b.WriteString("</i>")
				i = end
				continue
			}
		case strings.HasPrefix(value[i:], "https://") || strings.HasPrefix(value[i:], "http://"):
			if link, end := parseAutoURL(value, i); link != "" {
				b.WriteString(`<a href="`)
				b.WriteString(escAttr(link))
				b.WriteString(`">`)
				b.WriteString(esc(link))
				b.WriteString("</a>")
				i = end
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		b.WriteString(esc(string(r)))
		i += size
	}
	return b.String()
}

func parseMarkdownLink(value string, start int) (string, string, int, bool) {
	closeLabel := strings.Index(value[start:], "]")
	if closeLabel < 0 {
		return "", "", start, false
	}
	closeLabel += start
	if closeLabel+1 >= len(value) || value[closeLabel+1] != '(' {
		return "", "", start, false
	}
	closeURL := strings.Index(value[closeLabel+2:], ")")
	if closeURL < 0 {
		return "", "", start, false
	}
	closeURL += closeLabel + 2
	label := value[start+1 : closeLabel]
	link := strings.TrimSpace(value[closeLabel+2 : closeURL])
	return label, link, closeURL + 1, true
}

func parseDelimited(value string, start int, delimiter string) (string, int, bool) {
	contentStart := start + len(delimiter)
	close := strings.Index(value[contentStart:], delimiter)
	if close < 0 {
		return "", start, false
	}
	close += contentStart
	if close == contentStart {
		return "", start, false
	}
	return value[contentStart:close], close + len(delimiter), true
}

func parseHTMLPair(value string, start int, tag string) (string, int, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	lower := strings.ToLower(value)
	if !strings.HasPrefix(lower[start:], open) {
		return "", start, false
	}
	contentStart := start + len(open)
	closeStart := strings.Index(lower[contentStart:], close)
	if closeStart < 0 {
		return "", start, false
	}
	closeStart += contentStart
	return value[contentStart:closeStart], closeStart + len(close), true
}

func parseAutoURL(value string, start int) (string, int) {
	end := start
	for end < len(value) && !unicode.IsSpace(rune(value[end])) {
		end++
	}
	link := strings.TrimRight(value[start:end], ".,;:!?)]}")
	if safeURL(link) == "" {
		return "", start
	}
	return link, start + len(link)
}

func delimiterBoundary(value string, idx int) bool {
	before := rune(0)
	after := rune(0)
	if idx > 0 {
		before, _ = utf8.DecodeLastRuneInString(value[:idx])
	}
	if idx+1 < len(value) {
		after, _ = utf8.DecodeRuneInString(value[idx+1:])
	}
	return !(isWordRune(before) && isWordRune(after))
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
