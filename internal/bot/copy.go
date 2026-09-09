// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"strings"

	"branchy/internal/i18n"
	"github.com/FreshLabDev/tg"
)

// Facts the About card states. They are the same in every language, so they are
// not translation keys: only the labels in front of them are.
const (
	productName   = "Branchy"
	aboutEvents   = "push, pull request, release"
	sourceURL     = "https://github.com/FreshLabDev/branchy"
	sourceLabel   = "FreshLabDev/branchy"
	sourceLicense = "Apache-2.0"
	adminURL      = "https://t.me/amtiyo"
	adminLabel    = "@amtiyo"
)

// panel is the shape every Branchy screen takes, and the only one. A bold
// title, an italic one-line hint under it, then whatever the buttons cannot say
// themselves inside a blockquote.
//
// badge is the version on the About card and empty everywhere else: the family
// About card is specified as "<b>Name</b> · <i>vX.Y.Z</i>" on its first line,
// which is this same head with one extra field rather than a second helper.
//
// body is omitted rather than quoted empty. A screen whose keyboard already says
// everything — the language grid, the event checkboxes — has no substance to
// quote, and repeating the buttons in the text is the thing the family contract
// forbids outright.
//
// Every argument is inserted verbatim, so callers escape interpolated data
// before it gets here; translations are authored HTML-safe and asserted so.
func panel(title, badge, hint, body string) string {
	var b strings.Builder
	b.WriteString("<b>")
	b.WriteString(title)
	b.WriteString("</b>")
	if badge != "" {
		b.WriteString(" · <i>")
		b.WriteString(badge)
		b.WriteString("</i>")
	}
	if hint != "" {
		b.WriteString("\n<i>")
		b.WriteString(hint)
		b.WriteString("</i>")
	}
	if body != "" {
		b.WriteString("\n\n<blockquote>")
		b.WriteString(body)
		b.WriteString("</blockquote>")
	}
	return b.String()
}

// PrivateCommands is the command menu for DMs. `/start` is Branchy's only
// command, so each scope is a single entry.
func PrivateCommands(lang string) []tg.BotCommand {
	return []tg.BotCommand{{Command: "start", Description: i18n.T(lang, "cmd.private")}}
}

// GroupCommands is the command menu for groups and supergroups. IsEphemeral is
// what keeps the group reply private to whoever typed it; dropping it would put
// Branchy's DM prompt into a feed shared with everyone else.
func GroupCommands(lang string) []tg.BotCommand {
	return []tg.BotCommand{{Command: "start", Description: i18n.T(lang, "cmd.group"), IsEphemeral: true}}
}

// aboutText renders the About card: what this is, which build is answering, and
// where to go when it misbehaves. The version is on the title line because it is
// the first thing a bug report has to state, and the repository is a link inside
// the quote rather than a button — a second way to reach one destination is
// duplication, not convenience. No commit hash and no build timestamp: neither
// answers a question a person reading this card is asking.
func aboutText(lang, version string) string {
	rows := []string{
		i18n.T(lang, "about.events") + " · " + aboutEvents,
		i18n.T(lang, "about.source") + " · " + link(sourceURL, sourceLabel) + " · " + sourceLicense,
		i18n.T(lang, "about.admin") + " · " + link(adminURL, adminLabel),
	}
	return panel(productName, esc(version), i18n.T(lang, "about.tagline"), strings.Join(rows, "\n"))
}

func link(url, label string) string {
	return `<a href="` + url + `">` + esc(label) + `</a>`
}
