// SPDX-License-Identifier: Apache-2.0

// Package i18n holds Branchy's user-facing text. Strings live in
// translations.json (key -> {lang: text}) embedded at build time, and the
// language set mirrors the sibling voicy, searchy and vido bots so a person who
// picked a language in one bot is understood by the rest.
//
// T(lang, key, pairs...) returns the localized string with {placeholder}
// interpolation, falling back to English.
//
// Two conventions the translators need to know about:
//
//   - Values are inserted into Telegram HTML verbatim, so a translation must not
//     contain a bare "<", ">" or "&". Data interpolated through a {placeholder}
//     is escaped by the caller, never by the translation.
//   - The "err.action.*" keys are infinitive fragments ("update the
//     subscription") substituted into err.generic's {action} slot. Translate
//     them as the verb phrase that sentence needs, not as a standalone line.
package i18n

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

const DefaultLang = "en"

//go:embed translations.json
var translationsJSON []byte

// translations[key][lang] = text
var translations map[string]map[string]string

func init() {
	if err := json.Unmarshal(translationsJSON, &translations); err != nil {
		panic("i18n: invalid translations.json: " + err.Error())
	}
}

// LangOption is a selectable language for the language keyboard.
type LangOption struct {
	Code  string
	Label string // flag + native name, e.g. "🇬🇧 English"
}

// LANGUAGE_OPTIONS is the order shown in the language picker. It mirrors the
// set the other bots in the family offer, so the same grid appears everywhere.
var LANGUAGE_OPTIONS = []LangOption{
	{"en", "🇬🇧 English"},
	{"ru", "🇷🇺 Русский"},
	{"uk", "🇺🇦 Українська"},
	{"es", "🇪🇸 Español"},
	{"fr", "🇫🇷 Français"},
	{"de", "🇩🇪 Deutsch"},
	{"it", "🇮🇹 Italiano"},
	{"pl", "🇵🇱 Polski"},
	{"cs", "🇨🇿 Čeština"},
	{"tr", "🇹🇷 Türkçe"},
	{"sv", "🇸🇪 Svenska"},
	{"be", "🇧🇾 Беларуская"},
	{"ca", "🇦🇩 Català"},
	{"zh", "🇨🇳 中文"},
	{"ja", "🇯🇵 日本語"},
	{"ar", "🇦🇪 العربية"},
}

var supported = func() map[string]bool {
	m := make(map[string]bool, len(LANGUAGE_OPTIONS))
	for _, o := range LANGUAGE_OPTIONS {
		m[o.Code] = true
	}
	return m
}()

// IsSupported reports whether code is one of our languages.
func IsSupported(code string) bool { return supported[code] }

// Codes lists every supported language code in picker order.
func Codes() []string {
	out := make([]string, 0, len(LANGUAGE_OPTIONS))
	for _, o := range LANGUAGE_OPTIONS {
		out = append(out, o.Code)
	}
	return out
}

// LangOf maps a Telegram language_code ("en-US", "uk") or a preference stored by
// another bot to a language Branchy can render, falling back to English.
func LangOf(languageCode string) string {
	if c := Normalize(languageCode); supported[c] {
		return c
	}
	return DefaultLang
}

// Normalize lowercases and strips region: "en-US" / "zh_CN" -> "en" / "zh".
func Normalize(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	return code
}

// LabelOf returns the flag and native name for a language code.
func LabelOf(code string) string {
	for _, o := range LANGUAGE_OPTIONS {
		if o.Code == code {
			return o.Label
		}
	}
	return code
}

// T returns the localized string for key in lang, interpolating {name}
// placeholders from pairs (name, value, name, value, …). It falls back to
// English, then to "[key]" so a missing string is visible rather than blank.
func T(lang, key string, pairs ...string) string {
	byLang, ok := translations[key]
	if !ok {
		return "[" + key + "]"
	}
	text, ok := byLang[lang]
	if !ok || text == "" {
		if text, ok = byLang[DefaultLang]; !ok {
			return "[" + key + "]"
		}
	}
	if len(pairs) >= 2 {
		repl := make([]string, 0, len(pairs))
		for i := 0; i+1 < len(pairs); i += 2 {
			repl = append(repl, "{"+pairs[i]+"}", pairs[i+1])
		}
		text = strings.NewReplacer(repl...).Replace(text)
	}
	return text
}

// Keys lists every translation key, sorted. Used by tests to assert coverage.
func Keys() []string {
	out := make([]string, 0, len(translations))
	for k := range translations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
