// SPDX-License-Identifier: Apache-2.0
package notify

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxRichBodyHTMLRunes     = 24000
	maxFallbackBodyHTMLRunes = 3000
	maxClassicMessageRunes   = 4096
	maxRichBodyBlocks        = 470
	maxRichBodyDepth         = 13
	maxRichBodyMedia         = 50
	maxRichTableColumns      = 20
)

var githubMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	// Raw GitHub HTML is never trusted directly. Rendering it into the parsed
	// fragment lets the strict sanitizer below preserve safe inline tags such as
	// <ins>/<sup> while dropping scripts, Telegram-specific tags, unsafe links,
	// attributes, and over-limit structures.
	goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe()),
)

type renderedGitHubBody struct {
	RichHTML     string
	FallbackHTML string
	Source       string
	Truncated    bool
}

// prepareGitHubBody retains the normalized-source seam used by older tests and
// callers. Delivery never sends this source directly: renderGitHubBody parses
// it, applies Telegram's structural limits, and serializes balanced HTML.
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

func renderGitHubBody(raw string, maxRunes int) renderedGitHubBody {
	source, truncated := prepareGitHubBody(raw, maxRunes)
	if source == "" {
		return renderedGitHubBody{}
	}

	var rendered bytes.Buffer
	if err := githubMarkdown.Convert([]byte(source), &rendered); err != nil {
		// Goldmark converts in memory and should not fail for arbitrary Markdown.
		// Keep a safe, useful message if that invariant ever changes.
		escaped := stdhtml.EscapeString(source)
		return renderedGitHubBody{
			RichHTML:     escaped,
			FallbackHTML: escaped,
			Source:       source,
			Truncated:    truncated,
		}
	}

	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(rendered.String()), contextNode)
	if err != nil {
		escaped := stdhtml.EscapeString(source)
		return renderedGitHubBody{
			RichHTML:     escaped,
			FallbackHTML: escaped,
			Source:       source,
			Truncated:    truncated,
		}
	}

	state := &sanitizeState{}
	var safeNodes []*xhtml.Node
	for _, node := range nodes {
		safeNodes = append(safeNodes, sanitizeRichNode(node, state, 0, true)...)
		if state.stopped {
			break
		}
	}
	truncated = truncated || state.truncated

	rich, richCut := renderHTMLNodes(safeNodes, maxRichBodyHTMLRunes)
	fallbackNodes := makeClassicNodes(safeNodes)
	fallback, _ := renderHTMLNodes(fallbackNodes, maxFallbackBodyHTMLRunes)
	return renderedGitHubBody{
		RichHTML:     strings.TrimSpace(rich),
		FallbackHTML: strings.TrimSpace(fallback),
		Source:       source,
		// Classic HTML is a shorter rollback payload. Cutting it must not mark
		// the preferred rich body as truncated (that would fold complete notes
		// into <details> even when the rich payload is whole).
		Truncated: truncated || richCut,
	}
}

type sanitizeState struct {
	blocks    int
	media     int
	truncated bool
	stopped   bool
}

var richBlockTags = map[string]bool{
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"p": true, "pre": true, "footer": true, "hr": true,
	"ul": true, "ol": true, "li": true, "blockquote": true, "aside": true,
	"figure": true, "table": true, "tr": true, "details": true,
}

var richAllowedTags = map[string]bool{
	"a": true, "b": true, "strong": true, "i": true, "em": true,
	"u": true, "ins": true, "s": true, "strike": true, "del": true,
	"code": true, "mark": true, "sub": true, "sup": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"p": true, "pre": true, "footer": true, "hr": true,
	"ul": true, "ol": true, "li": true, "blockquote": true, "aside": true,
	"figure": true, "figcaption": true, "cite": true,
	"table": true, "caption": true, "tr": true, "th": true, "td": true,
	"details": true, "summary": true, "input": true,
}

func sanitizeRichNode(node *xhtml.Node, state *sanitizeState, depth int, mediaBlock bool) []*xhtml.Node {
	if state.stopped {
		return nil
	}
	switch node.Type {
	case xhtml.TextNode:
		if node.Data == "" {
			return nil
		}
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: node.Data}}
	case xhtml.CommentNode:
		return nil
	case xhtml.ElementNode:
	default:
		return sanitizeRichChildren(node, state, depth, mediaBlock)
	}

	tag := strings.ToLower(node.Data)
	if tag == "br" {
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: "\n"}}
	}
	if isMediaTag(tag) {
		return sanitizeMedia(node, state, mediaBlock, "")
	}
	if tag == "figure" {
		return sanitizeFigure(node, state, depth, mediaBlock)
	}
	if isDroppedSubtreeTag(tag) {
		return nil
	}
	if isTelegramDroppedTag(tag) {
		// HTML5 treats unclosed/self-closing unknown tags as open elements, so
		// later safe siblings can become children. Unwrap instead of dropping
		// the subtree so injected tg-* chrome cannot hide the rest of the body.
		return sanitizeRichChildren(node, state, depth, mediaBlock)
	}
	if mediaBlock && tag != "p" && tag != "li" {
		if media, captionURL, ok := inlineMediaCandidate(node); ok {
			return sanitizeMedia(media, state, true, captionURL)
		}
	}
	if tag == "p" && paragraphHasMedia(node) {
		return sanitizeMediaParagraph(node, state, depth)
	}

	// Every generated or raw GFM node passes this allowlist. Raw attributes are
	// discarded below except for validated links and task-list state.
	if !richAllowedTags[tag] {
		return sanitizeRichChildren(node, state, depth, mediaBlock)
	}
	if depth >= maxRichBodyDepth {
		return sanitizeRichChildren(node, state, depth, false)
	}
	if richBlockTags[tag] {
		if state.blocks >= maxRichBodyBlocks {
			state.truncated = true
			state.stopped = true
			return nil
		}
		state.blocks++
	}

	if tag == "a" {
		href := safeNodeURL(attrValue(node, "href"))
		if href == "" {
			return sanitizeRichChildren(node, state, depth, false)
		}
		out := elementNode("a", xhtml.Attribute{Key: "href", Val: href})
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == xhtml.ElementNode && isMediaTag(strings.ToLower(child.Data)) {
				label := strings.TrimSpace(attrValue(child, "alt"))
				if label == "" {
					label = mediaDefaultLabel(child)
				}
				appendNodes(out, []*xhtml.Node{{Type: xhtml.TextNode, Data: label}})
				continue
			}
			appendNodes(out, sanitizeRichNode(child, state, depth+1, false))
		}
		return []*xhtml.Node{out}
	}
	if tag == "li" && paragraphHasMedia(node) {
		out := elementNode("li")
		appendNodes(out, sanitizeMediaChildren(node, state, depth+1))
		return []*xhtml.Node{out}
	}
	if tag == "input" {
		if strings.ToLower(attrValue(node, "type")) != "checkbox" {
			return nil
		}
		attrs := []xhtml.Attribute{{Key: "type", Val: "checkbox"}}
		if hasAttr(node, "checked") {
			attrs = append(attrs, xhtml.Attribute{Key: "checked", Val: ""})
		}
		return []*xhtml.Node{elementNode("input", attrs...)}
	}

	outTag := tag
	switch tag {
	case "strong":
		outTag = "b"
	case "em":
		outTag = "i"
	}
	out := elementNode(outTag)
	if tag == "details" && hasAttr(node, "open") {
		out.Attr = []xhtml.Attribute{{Key: "open", Val: ""}}
	}
	if tag == "table" {
		out.Attr = []xhtml.Attribute{
			{Key: "bordered", Val: ""},
			{Key: "striped", Val: ""},
			{Key: "compact", Val: ""},
		}
	}
	if tag == "tr" {
		columns := 0
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == xhtml.ElementNode && (strings.EqualFold(child.Data, "td") || strings.EqualFold(child.Data, "th")) {
				columns++
				if columns > maxRichTableColumns {
					state.truncated = true
					continue
				}
			}
			appendNodes(out, sanitizeRichNode(child, state, depth+1, false))
		}
	} else {
		childMediaBlock := tag == "blockquote" || tag == "details" || tag == "li"
		appendNodes(out, sanitizeRichChildren(node, state, depth+1, childMediaBlock))
	}
	return []*xhtml.Node{out}
}

func sanitizeRichChildren(node *xhtml.Node, state *sanitizeState, depth int, mediaBlock bool) []*xhtml.Node {
	var out []*xhtml.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		out = append(out, sanitizeRichNode(child, state, depth, mediaBlock)...)
		if state.stopped {
			break
		}
	}
	return out
}

func paragraphHasMedia(node *xhtml.Node) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if _, _, ok := inlineMediaCandidate(child); ok {
			return true
		}
	}
	return false
}

// Rich media must be a separate block. Goldmark places Markdown images inside
// paragraphs, including images adjacent to prose, so split those paragraphs
// around each image instead of silently degrading an otherwise valid image to
// a link.
func sanitizeMediaParagraph(node *xhtml.Node, state *sanitizeState, depth int) []*xhtml.Node {
	return sanitizeMediaChildren(node, state, depth)
}

func sanitizeMediaChildren(node *xhtml.Node, state *sanitizeState, depth int) []*xhtml.Node {
	var out []*xhtml.Node
	var paragraph *xhtml.Node
	flush := func() {
		if paragraph == nil || strings.TrimSpace(textContent(paragraph)) == "" {
			paragraph = nil
			return
		}
		if state.blocks >= maxRichBodyBlocks {
			state.truncated = true
			state.stopped = true
			paragraph = nil
			return
		}
		state.blocks++
		out = append(out, paragraph)
		paragraph = nil
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if media, captionURL, ok := inlineMediaCandidate(child); ok {
			flush()
			out = append(out, sanitizeMedia(media, state, true, captionURL)...)
			if state.stopped {
				break
			}
			continue
		}
		if child.Type == xhtml.ElementNode && richBlockTags[strings.ToLower(child.Data)] {
			flush()
			out = append(out, sanitizeRichNode(child, state, depth+1, false)...)
			if state.stopped {
				break
			}
			continue
		}
		if paragraph == nil {
			paragraph = elementNode("p")
		}
		appendNodes(paragraph, sanitizeRichNode(child, state, depth+1, false))
	}
	flush()
	return out
}

func inlineMediaCandidate(node *xhtml.Node) (*xhtml.Node, string, bool) {
	if node == nil || node.Type != xhtml.ElementNode {
		return nil, "", false
	}
	if isMediaTag(strings.ToLower(node.Data)) {
		return node, "", true
	}

	var media *xhtml.Node
	captionURL := ""
	valid := true
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if !valid {
			return
		}
		if current.Type == xhtml.TextNode {
			if strings.TrimSpace(current.Data) != "" {
				valid = false
			}
			return
		}
		if current.Type != xhtml.ElementNode {
			return
		}
		if isDroppedSubtreeTag(strings.ToLower(current.Data)) || isTelegramDroppedTag(strings.ToLower(current.Data)) {
			valid = false
			return
		}
		if captionURL == "" && strings.EqualFold(current.Data, "a") {
			captionURL = safeNodeURL(attrValue(current, "href"))
		}
		if isMediaTag(strings.ToLower(current.Data)) {
			if media != nil {
				valid = false
				return
			}
			media = current
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	if !valid || media == nil {
		return nil, "", false
	}
	return media, captionURL, true
}

func sanitizeFigure(node *xhtml.Node, state *sanitizeState, depth int, block bool) []*xhtml.Node {
	var media *xhtml.Node
	caption := ""
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if media == nil {
			if candidate, _, ok := inlineMediaCandidate(child); ok {
				media = candidate
			}
		}
		if child.Type == xhtml.ElementNode && strings.EqualFold(child.Data, "figcaption") {
			caption = strings.TrimSpace(textContent(child))
		}
	}
	if media == nil {
		return sanitizeRichChildren(node, state, depth, false)
	}
	return sanitizeMediaWithCaption(media, state, block, caption, "")
}

func sanitizeMedia(node *xhtml.Node, state *sanitizeState, block bool, captionURL string) []*xhtml.Node {
	return sanitizeMediaWithCaption(node, state, block, mediaCaption(node), captionURL)
}

func sanitizeMediaWithCaption(node *xhtml.Node, state *sanitizeState, block bool, caption, captionURL string) []*xhtml.Node {
	src := safeNodeURL(mediaSource(node))
	alt := strings.TrimSpace(attrValue(node, "title"))
	if caption != "" {
		alt = caption
	} else if alt == "" {
		alt = strings.TrimSpace(attrValue(node, "alt"))
	}
	if alt == "" {
		alt = mediaDefaultLabel(node)
	}
	if src == "" {
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: alt}}
	}
	if !block || state.media >= maxRichBodyMedia || state.blocks >= maxRichBodyBlocks {
		if state.media >= maxRichBodyMedia || state.blocks >= maxRichBodyBlocks {
			state.truncated = true
		}
		link := elementNode("a", xhtml.Attribute{Key: "href", Val: src})
		appendNodes(link, []*xhtml.Node{{Type: xhtml.TextNode, Data: alt}})
		return []*xhtml.Node{link}
	}

	neededBlocks := 1
	if state.blocks+neededBlocks > maxRichBodyBlocks {
		state.truncated = true
		link := elementNode("a", xhtml.Attribute{Key: "href", Val: src})
		appendNodes(link, []*xhtml.Node{{Type: xhtml.TextNode, Data: alt}})
		return []*xhtml.Node{link}
	}
	state.media++
	state.blocks += neededBlocks
	figure := elementNode("figure")
	mediaTag := strings.ToLower(node.Data)
	if mediaTag == "img" {
		mediaTag = mediaTagForURL(src)
	}
	mediaAttrs := []xhtml.Attribute{{Key: "src", Val: src}}
	appendNodes(figure, []*xhtml.Node{elementNode(mediaTag, mediaAttrs...)})
	if alt != mediaDefaultLabel(node) {
		caption := elementNode("figcaption")
		captionText := &xhtml.Node{Type: xhtml.TextNode, Data: alt}
		if captionURL != "" {
			link := elementNode("a", xhtml.Attribute{Key: "href", Val: captionURL})
			appendNodes(link, []*xhtml.Node{captionText})
			appendNodes(caption, []*xhtml.Node{link})
		} else {
			appendNodes(caption, []*xhtml.Node{captionText})
		}
		appendNodes(figure, []*xhtml.Node{caption})
	}
	return []*xhtml.Node{figure}
}

func mediaCaption(node *xhtml.Node) string {
	caption := strings.TrimSpace(attrValue(node, "title"))
	if caption == "" {
		caption = strings.TrimSpace(attrValue(node, "alt"))
	}
	return caption
}

func mediaDefaultLabel(node *xhtml.Node) string {
	switch strings.ToLower(node.Data) {
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	default:
		return "Image"
	}
}

func mediaSource(node *xhtml.Node) string {
	if src := attrValue(node, "src"); src != "" {
		return src
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.ElementNode && strings.EqualFold(child.Data, "source") {
			if src := attrValue(child, "src"); src != "" {
				return src
			}
		}
	}
	return ""
}

func isMediaTag(tag string) bool {
	return tag == "img" || tag == "video" || tag == "audio"
}

func isDroppedSubtreeTag(tag string) bool {
	switch tag {
	case "script", "style", "iframe", "object", "embed", "svg":
		return true
	default:
		return false
	}
}

func isTelegramDroppedTag(tag string) bool {
	return strings.HasPrefix(tag, "tg-")
}

func mediaTagForURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "img"
	}
	switch strings.ToLower(path.Ext(parsed.Path)) {
	case ".mp4", ".webm", ".gif":
		return "video"
	case ".mp3", ".m4a", ".ogg", ".oga", ".opus":
		return "audio"
	default:
		return "img"
	}
}

func safeNodeURL(raw string) string {
	if safeURL(raw) == "" {
		return ""
	}
	return strings.TrimSpace(raw)
}

func makeClassicNodes(nodes []*xhtml.Node) []*xhtml.Node {
	var out []*xhtml.Node
	for _, node := range nodes {
		out = append(out, makeClassicNode(node)...)
	}
	return out
}

func makeRichNoMediaNodes(nodes []*xhtml.Node) []*xhtml.Node {
	var out []*xhtml.Node
	for _, node := range nodes {
		out = append(out, makeRichNoMediaNode(node)...)
	}
	return out
}

func makeRichNoMediaNode(node *xhtml.Node) []*xhtml.Node {
	if node.Type == xhtml.TextNode {
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: node.Data}}
	}
	if node.Type != xhtml.ElementNode {
		return nil
	}
	tag := strings.ToLower(node.Data)
	if tag == "figure" {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != xhtml.ElementNode || !isMediaTag(strings.ToLower(child.Data)) {
				continue
			}
			return mediaLinkBlock(mediaSource(child), firstNonEmpty(figureCaption(node), mediaDefaultLabel(child)))
		}
	}
	if isMediaTag(tag) {
		return mediaLinkBlock(mediaSource(node), firstNonEmpty(mediaCaption(node), mediaDefaultLabel(node)))
	}

	out := elementNode(tag, append([]xhtml.Attribute(nil), node.Attr...)...)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendNodes(out, makeRichNoMediaNode(child))
	}
	return []*xhtml.Node{out}
}

func mediaLinkBlock(src, label string) []*xhtml.Node {
	src = safeNodeURL(src)
	if src == "" {
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: label}}
	}
	link := elementNode("a", xhtml.Attribute{Key: "href", Val: src})
	appendNodes(link, []*xhtml.Node{{Type: xhtml.TextNode, Data: label}})
	paragraph := elementNode("p")
	appendNodes(paragraph, []*xhtml.Node{link})
	return []*xhtml.Node{paragraph}
}

func makeClassicNode(node *xhtml.Node) []*xhtml.Node {
	if node.Type == xhtml.TextNode {
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: node.Data}}
	}
	if node.Type != xhtml.ElementNode {
		return nil
	}
	tag := strings.ToLower(node.Data)
	if tag == "figure" {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != xhtml.ElementNode || (child.Data != "img" && child.Data != "video" && child.Data != "audio") {
				continue
			}
			src := attrValue(child, "src")
			label := figureCaption(node)
			if label == "" {
				label = strings.ToUpper(child.Data[:1]) + child.Data[1:]
			}
			link := elementNode("a", xhtml.Attribute{Key: "href", Val: src})
			appendNodes(link, []*xhtml.Node{{Type: xhtml.TextNode, Data: label}})
			return []*xhtml.Node{link, {Type: xhtml.TextNode, Data: "\n"}}
		}
	}

	children := makeClassicChildren(node)
	switch tag {
	case "a", "b", "i", "u", "ins", "s", "strike", "del", "code", "pre", "blockquote":
		out := elementNode(tag)
		if tag == "a" {
			out.Attr = []xhtml.Attribute{{Key: "href", Val: attrValue(node, "href")}}
		}
		appendNodes(out, children)
		return []*xhtml.Node{out}
	case "h1", "h2", "h3", "h4", "h5", "h6", "summary":
		out := elementNode("b")
		appendNodes(out, children)
		return []*xhtml.Node{out, {Type: xhtml.TextNode, Data: "\n"}}
	case "li":
		return append([]*xhtml.Node{{Type: xhtml.TextNode, Data: "• "}}, append(children, &xhtml.Node{Type: xhtml.TextNode, Data: "\n"})...)
	case "td", "th":
		return append(children, &xhtml.Node{Type: xhtml.TextNode, Data: " | "})
	case "tr", "p", "figcaption", "caption":
		return append(children, &xhtml.Node{Type: xhtml.TextNode, Data: "\n"})
	case "hr":
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: "\n────────\n"}}
	case "input":
		mark := "☐ "
		if hasAttr(node, "checked") {
			mark = "☑ "
		}
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: mark}}
	case "img", "video", "audio":
		return nil
	default:
		return children
	}
}

func makeClassicChildren(node *xhtml.Node) []*xhtml.Node {
	var out []*xhtml.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		out = append(out, makeClassicNode(child)...)
	}
	return out
}

func figureCaption(node *xhtml.Node) string {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.ElementNode && child.Data == "figcaption" {
			return strings.TrimSpace(textContent(child))
		}
	}
	return ""
}

func textContent(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var b strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(textContent(child))
	}
	return b.String()
}

// renderHTMLNodes enforces a character limit while preserving balanced tags.
// It reserves each closing tag before rendering children, so truncation can
// never leave Telegram with malformed HTML.
func renderHTMLNodes(nodes []*xhtml.Node, maxRunes int) (string, bool) {
	var b strings.Builder
	remaining := maxRunes
	truncated := false
	for _, node := range nodes {
		if !renderHTMLNode(&b, node, &remaining) {
			truncated = true
			break
		}
	}
	return b.String(), truncated
}

func boundNotification(notification Notification) Notification {
	notification.RichHTML = boundHTMLFragment(notification.RichHTML, maxRichMessageBytes)
	notification.FallbackHTML = boundHTMLFragment(notification.FallbackHTML, maxClassicMessageRunes)
	return notification
}

// RichHTMLWithoutMedia keeps the Rich Message structure but replaces every
// media block with a source link. The outbox uses it after Telegram rejects a
// rich payload, so one inaccessible attachment does not discard tables, lists,
// details, or the rest of the notification formatting.
func RichHTMLWithoutMedia(value string) string {
	nodes, err := parseHTMLFragment(value)
	if err != nil {
		return ""
	}
	rendered, _ := renderHTMLNodes(makeRichNoMediaNodes(nodes), maxRichMessageBytes)
	return strings.TrimSpace(rendered)
}

// PlainTextFromHTML is the final content-error fallback. It deliberately keeps
// link destinations because classic HTML parsing may fail on the exact markup
// that made the earlier send attempt unsafe to repeat.
func PlainTextFromHTML(value string) string {
	nodes, err := parseHTMLFragment(value)
	if err != nil {
		plain, _ := truncateRunes(stdhtml.UnescapeString(value), maxClassicMessageRunes)
		return strings.TrimSpace(plain)
	}
	var b strings.Builder
	for _, node := range nodes {
		writePlainNode(&b, node)
	}
	plain := normalizePlainText(b.String())
	plain, _ = truncateRunes(plain, maxClassicMessageRunes)
	return strings.TrimSpace(plain)
}

// SanitizeLegacyRichMarkdown converts pending alpha.1/alpha.2 outbox payloads
// before delivery instead of sending their raw, user-controlled Markdown.
func SanitizeLegacyRichMarkdown(value string) Notification {
	rendered := renderGitHubBody(value, maxRichMessageBytes)
	notification := Notification{
		RichHTML:     rendered.RichHTML,
		FallbackHTML: rendered.FallbackHTML,
	}
	if strings.TrimSpace(notification.RichHTML) == "" {
		escaped := stdhtml.EscapeString(strings.TrimSpace(value))
		notification = Notification{RichHTML: escaped, FallbackHTML: escaped}
	}
	return boundNotification(notification)
}

func boundHTMLFragment(value string, maxRunes int) string {
	nodes, err := parseHTMLFragment(value)
	if err != nil {
		escaped, _ := truncateRunes(stdhtml.EscapeString(value), maxRunes)
		return escaped
	}
	rendered, _ := renderHTMLNodes(nodes, maxRunes)
	return strings.TrimSpace(rendered)
}

func parseHTMLFragment(value string) ([]*xhtml.Node, error) {
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	return xhtml.ParseFragment(strings.NewReader(value), contextNode)
}

func renderHTMLNode(b *strings.Builder, node *xhtml.Node, remaining *int) bool {
	if *remaining <= 0 {
		return false
	}
	if node.Type == xhtml.TextNode {
		return writeEscapedText(b, node.Data, remaining)
	}
	if node.Type != xhtml.ElementNode {
		return true
	}
	if isTransparentTableContainer(node.Data) {
		complete := true
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if !renderHTMLNode(b, child, remaining) {
				complete = false
				break
			}
		}
		return complete
	}

	open := "<" + node.Data
	for _, attr := range node.Attr {
		open += " " + attr.Key
		if attr.Val != "" {
			open += `="` + stdhtml.EscapeString(attr.Val) + `"`
		}
	}
	open += ">"
	if isVoidTag(node.Data) {
		if utf8.RuneCountInString(open) > *remaining {
			return false
		}
		b.WriteString(open)
		*remaining -= utf8.RuneCountInString(open)
		return true
	}
	close := fmt.Sprintf("</%s>", node.Data)
	required := utf8.RuneCountInString(open) + utf8.RuneCountInString(close)
	if required > *remaining {
		return false
	}
	b.WriteString(open)
	*remaining -= utf8.RuneCountInString(open)
	*remaining -= utf8.RuneCountInString(close)
	complete := true
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if !renderHTMLNode(b, child, remaining) {
			complete = false
			break
		}
	}
	b.WriteString(close)
	return complete
}

func isTransparentTableContainer(tag string) bool {
	switch strings.ToLower(tag) {
	case "thead", "tbody", "tfoot":
		return true
	default:
		return false
	}
}

func writePlainNode(b *strings.Builder, node *xhtml.Node) {
	if node.Type == xhtml.TextNode {
		b.WriteString(node.Data)
		return
	}
	if node.Type != xhtml.ElementNode {
		return
	}
	tag := strings.ToLower(node.Data)
	block := plainBlockTag(tag)
	if block {
		ensurePlainBreak(b)
	}
	before := b.Len()
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writePlainNode(b, child)
	}
	if tag == "a" {
		href := safeNodeURL(attrValue(node, "href"))
		label := b.String()[before:]
		if href != "" && !strings.Contains(label, href) {
			b.WriteString(" (")
			b.WriteString(href)
			b.WriteString(")")
		}
	}
	if tag == "tg-button" {
		href := safeNodeURL(attrValue(node, "url"))
		label := b.String()[before:]
		if href != "" && !strings.Contains(label, href) {
			b.WriteString(" (")
			b.WriteString(href)
			b.WriteString(")")
		}
	}
	if block {
		ensurePlainBreak(b)
	}
}

func plainBlockTag(tag string) bool {
	if richBlockTags[tag] || tag == "figcaption" || tag == "caption" || tag == "summary" {
		return true
	}
	return tag == "td" || tag == "th" || tag == "tg-button-row" || tag == "tg-button"
}

func ensurePlainBreak(b *strings.Builder) {
	if b.Len() == 0 {
		return
	}
	value := b.String()
	if value[len(value)-1] != '\n' {
		b.WriteByte('\n')
	}
}

func normalizePlainText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func writeEscapedText(b *strings.Builder, value string, remaining *int) bool {
	escaped := stdhtml.EscapeString(value)
	if utf8.RuneCountInString(escaped) <= *remaining {
		b.WriteString(escaped)
		*remaining -= utf8.RuneCountInString(escaped)
		return true
	}
	runes := []rune(value)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if utf8.RuneCountInString(stdhtml.EscapeString(string(runes[:mid]))) <= *remaining {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo > 0 {
		part := stdhtml.EscapeString(string(runes[:lo]))
		b.WriteString(part)
		*remaining -= utf8.RuneCountInString(part)
	}
	return false
}

func elementNode(tag string, attrs ...xhtml.Attribute) *xhtml.Node {
	return &xhtml.Node{Type: xhtml.ElementNode, Data: tag, Attr: attrs}
}

func appendNodes(parent *xhtml.Node, nodes []*xhtml.Node) {
	for _, node := range nodes {
		parent.AppendChild(node)
	}
}

func attrValue(node *xhtml.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func hasAttr(node *xhtml.Node, key string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return true
		}
	}
	return false
}

func isVoidTag(tag string) bool {
	switch tag {
	case "hr", "img", "input", "br":
		return true
	default:
		return false
	}
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

func wrapRenderedBody(body renderedGitHubBody, summary string) (string, string) {
	if strings.TrimSpace(body.RichHTML) == "" {
		return "", ""
	}
	if summary == "" {
		summary = "Notes"
	}
	collapsed := shouldCollapseBody(body.Source, body.Truncated)
	if collapsed && (body.Truncated || bodyHasRichStructure(body.RichHTML)) {
		rich := "<details><summary>" + esc(summary) + "</summary>" + body.RichHTML + "</details>"
		fallback := "<b>" + esc(summary) + "</b>\n" + body.FallbackHTML
		return rich, fallback
	}
	if collapsed {
		// Expandable quotes are flat RichText (official sample uses <br>, not nested
		// <p>). Wrapping Goldmark paragraphs as-is can 400 sendRichMessage.
		return "<blockquote expandable>" + flattenHTMLForExpandableQuote(body.RichHTML) + "</blockquote>", "<blockquote>" + body.FallbackHTML + "</blockquote>"
	}
	return "<blockquote>" + body.RichHTML + "</blockquote>", body.FallbackHTML
}

func flattenHTMLForExpandableQuote(html string) string {
	nodes, err := parseHTMLFragment(html)
	if err != nil {
		return html
	}
	var parts [][]*xhtml.Node
	for _, node := range nodes {
		flat := flattenNodeForExpandable(node)
		if expandableNodesEmpty(flat) {
			continue
		}
		parts = append(parts, flat)
	}
	var out []*xhtml.Node
	for i, part := range parts {
		if i > 0 {
			out = append(out, elementNode("br"))
		}
		out = append(out, part...)
	}
	rendered, _ := renderHTMLNodes(out, maxRichBodyHTMLRunes)
	return strings.TrimSpace(rendered)
}

func flattenNodeForExpandable(node *xhtml.Node) []*xhtml.Node {
	if node.Type == xhtml.TextNode {
		if node.Data == "" {
			return nil
		}
		return []*xhtml.Node{{Type: xhtml.TextNode, Data: node.Data}}
	}
	if node.Type != xhtml.ElementNode {
		return nil
	}
	tag := strings.ToLower(node.Data)
	if tag == "br" {
		return []*xhtml.Node{elementNode("br")}
	}
	if expandableInlineTag(tag) {
		out := elementNode(tag)
		if tag == "a" {
			out.Attr = append([]xhtml.Attribute(nil), node.Attr...)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			appendNodes(out, flattenNodeForExpandable(child))
		}
		return []*xhtml.Node{out}
	}
	var parts [][]*xhtml.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		flat := flattenNodeForExpandable(child)
		if expandableNodesEmpty(flat) {
			continue
		}
		parts = append(parts, flat)
	}
	var out []*xhtml.Node
	for i, part := range parts {
		if i > 0 && expandableBlockTag(tag) {
			out = append(out, elementNode("br"))
		}
		out = append(out, part...)
	}
	return out
}

func expandableInlineTag(tag string) bool {
	switch tag {
	case "a", "b", "strong", "i", "em", "u", "ins", "s", "strike", "del",
		"code", "mark", "sub", "sup", "cite":
		return true
	default:
		return false
	}
}

func expandableBlockTag(tag string) bool {
	return richBlockTags[tag] || tag == "div"
}

func expandableNodesEmpty(nodes []*xhtml.Node) bool {
	for _, node := range nodes {
		if node.Type == xhtml.TextNode && strings.TrimSpace(node.Data) != "" {
			return false
		}
		if node.Type == xhtml.ElementNode && node.Data != "br" {
			return false
		}
	}
	return true
}

func bodyHasRichStructure(html string) bool {
	lower := strings.ToLower(html)
	for _, marker := range []string{
		"<table", "<figure", "<h1", "<h2", "<h3", "<h4", "<h5", "<h6",
		"<pre", "<details", "<ul", "<ol",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
