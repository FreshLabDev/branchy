// SPDX-License-Identifier: Apache-2.0
package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEveryKeyHasEnglish pins the one invariant translators cannot break: the
// English value is the fallback every other language falls back to, so a key
// without one renders as "[key]" in sixteen languages at once.
func TestEveryKeyHasEnglish(t *testing.T) {
	keys := Keys()
	if len(keys) == 0 {
		t.Fatal("translations.json is empty")
	}
	for _, key := range keys {
		text, ok := translations[key][DefaultLang]
		if !ok || strings.TrimSpace(text) == "" {
			t.Errorf("key %q has no English value", key)
		}
	}
}

// TestTranslationsAreHTMLSafe guards the one thing a translator can do that
// breaks Telegram rather than just reading badly: panel text is inserted into
// HTML verbatim, so an unescaped angle bracket or ampersand in a translation
// makes sendMessage reject the whole panel.
func TestTranslationsAreHTMLSafe(t *testing.T) {
	for key, byLang := range translations {
		for lang, text := range byLang {
			if strings.ContainsAny(text, "<>&") {
				t.Errorf("%s[%s] contains raw HTML metacharacters: %q", key, lang, text)
			}
		}
	}
}

// TestLanguageOptionsMatchTheFamilyOrder pins the shared picker: the family
// contract fixes both the order and the labels, so a person switching bots sees
// the same grid in the same place.
func TestLanguageOptionsMatchTheFamilyOrder(t *testing.T) {
	want := []string{"en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr", "sv", "be", "ca", "zh", "ja", "ar"}
	got := Codes()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("language order = %v, want %v", got, want)
	}
	labels := map[string]string{
		"en": "🇬🇧 English", "ru": "🇷🇺 Русский", "uk": "🇺🇦 Українська", "es": "🇪🇸 Español",
		"fr": "🇫🇷 Français", "de": "🇩🇪 Deutsch", "it": "🇮🇹 Italiano", "pl": "🇵🇱 Polski",
		"cs": "🇨🇿 Čeština", "tr": "🇹🇷 Türkçe", "sv": "🇸🇪 Svenska", "be": "🇧🇾 Беларуская",
		"ca": "🇦🇩 Català", "zh": "🇨🇳 中文", "ja": "🇯🇵 日本語", "ar": "🇦🇪 العربية",
	}
	for _, opt := range LANGUAGE_OPTIONS {
		if labels[opt.Code] != opt.Label {
			t.Errorf("label for %q = %q, want %q", opt.Code, opt.Label, labels[opt.Code])
		}
		if LabelOf(opt.Code) != opt.Label {
			t.Errorf("LabelOf(%q) = %q, want %q", opt.Code, LabelOf(opt.Code), opt.Label)
		}
	}
}

func TestInterpolatesPlaceholders(t *testing.T) {
	got := T("en", "home.connected", "login", "octocat")
	if got != "Connected as octocat" {
		t.Fatalf("T(home.connected) = %q", got)
	}
	got = T("en", "page.indicator", "page", "2", "pages", "5")
	if got != "Page 2 of 5" {
		t.Fatalf("T(page.indicator) = %q", got)
	}
	// An odd trailing pair is ignored rather than panicking: a caller that
	// forgot a value should still render a readable panel.
	if got := T("en", "home.connected", "login"); !strings.Contains(got, "{login}") {
		t.Fatalf("unpaired placeholder should be left alone, got %q", got)
	}
}

// TestFallsBackToEnglish is the behaviour a key depends on between the commit
// that adds it and the commit that translates it: an untranslated key renders
// in English instead of vanishing. Every key in the file now carries all
// sixteen languages, so the half-translated state has to be staged here rather
// than borrowed from a locale that happens to be behind.
func TestFallsBackToEnglish(t *testing.T) {
	translations["test.english.only"] = map[string]string{DefaultLang: "Back"}
	defer delete(translations, "test.english.only")
	if got := T("uk", "test.english.only"); got != "Back" {
		t.Fatalf("untranslated key = %q, want the English fallback", got)
	}
	// An unknown language behaves the same way as an untranslated key.
	if got := T("xx", "btn.back"); got != "Back" {
		t.Fatalf("unknown language = %q, want the English fallback", got)
	}
	// A missing key is loud, not blank, so it shows up in review.
	if got := T("en", "no.such.key"); got != "[no.such.key]" {
		t.Fatalf("missing key = %q, want [no.such.key]", got)
	}
}

func TestLangOfNormalizesTelegramHints(t *testing.T) {
	cases := map[string]string{
		"en":      "en",
		"en-US":   "en",
		"zh_CN":   "zh",
		"UK":      "uk",
		" ru ":    "ru",
		"":        "en",
		"klingon": "en", // unsupported hints fall back rather than render "[key]"
	}
	for in, want := range cases {
		if got := LangOf(in); got != want {
			t.Errorf("LangOf(%q) = %q, want %q", in, got, want)
		}
	}
	if IsSupported("klingon") {
		t.Fatal("klingon is not a supported language")
	}
	if !IsSupported("ar") {
		t.Fatal("ar must be supported")
	}
}

// TestTranslationsFileIsCanonical keeps the file the translation pass edits in
// one shape: key -> {lang: text}, sorted, so adding a language is a diff of
// added lines rather than a reshuffle.
func TestTranslationsFileIsCanonical(t *testing.T) {
	var raw map[string]map[string]string
	if err := json.Unmarshal(translationsJSON, &raw); err != nil {
		t.Fatalf("translations.json does not parse: %v", err)
	}
	for key, byLang := range raw {
		for lang := range byLang {
			if !IsSupported(lang) {
				t.Errorf("key %q carries unsupported language %q", key, lang)
			}
		}
	}
	keys := Keys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys are not sorted: %q before %q", keys[i-1], keys[i])
		}
	}
}
