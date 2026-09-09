// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"fmt"
	"strings"

	"github.com/FreshLabDev/tg"
)

// Branchy speaks English and only English. That is a product boundary recorded
// in AGENTS.md, not an unfinished localization: the subject matter is GitHub,
// whose own vocabulary — push, pull request, release, branch — is English in
// every locale, so a half-translated panel would read worse than an untranslated
// one. Telegram's setMyCommands does accept one list per language_code and other
// bots in the family register several; Branchy registers one on purpose.
//
// The strings Telegram is told about, and the ones the About card is built from,
// live here rather than inline at their call sites so the whole of Branchy's
// fixed copy can be read at once — and so adding languages later is one file to
// change rather than a hunt through main.go and bot.go.
const (
	tagline         = "Clean GitHub notifications in Telegram."
	aboutEvents     = "push, pull request, release"
	sourceURL       = "https://github.com/FreshLabDev/branchy"
	sourceLabel     = "FreshLabDev/branchy"
	sourceLicense   = "Apache-2.0"
	adminURL        = "https://t.me/amtiyo"
	adminLabel      = "@amtiyo"
	groupPanelText  = "Open a direct message with Branchy to configure notifications."
	privateStartCmd = "Open the Branchy menu"
	groupStartCmd   = "Open Branchy privately"
)

// PrivateCommands is the command menu for DMs. `/start` is Branchy's only
// command, so each scope is a single entry.
func PrivateCommands() []tg.BotCommand {
	return []tg.BotCommand{{Command: "start", Description: privateStartCmd}}
}

// GroupCommands is the command menu for groups and supergroups. IsEphemeral is
// what keeps the group reply private to whoever typed it; dropping it would put
// Branchy's DM prompt into a feed shared with everyone else.
func GroupCommands() []tg.BotCommand {
	return []tg.BotCommand{{Command: "start", Description: groupStartCmd, IsEphemeral: true}}
}

// aboutText renders the About card: what this is, which build is answering, and
// where to go when it misbehaves. The version is first because it is the first
// thing asked for in a bug report, and the repository is a link inside the quote
// rather than a button — a second way to reach one destination is duplication,
// not convenience.
func aboutText(version string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Branchy</b> · <i>%s</i>\n", esc(version))
	b.WriteString(tagline + "\n\n")
	b.WriteString("<blockquote>")
	fmt.Fprintf(&b, "Events · %s\n", aboutEvents)
	fmt.Fprintf(&b, "Source · <a href=\"%s\">%s</a> · %s\n", sourceURL, sourceLabel, sourceLicense)
	fmt.Fprintf(&b, "Admin · <a href=\"%s\">%s</a>", adminURL, adminLabel)
	b.WriteString("</blockquote>")
	return b.String()
}
