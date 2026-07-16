// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"branchy/internal/db"
	"branchy/internal/github"
)

func TestStartCommandTargetsBot(t *testing.T) {
	self := func() string { return "branchybot" }
	noSelf := func() string { return "" }
	cases := []struct {
		text string
		self func() string
		want bool
	}{
		{"/start", self, true},                  // bare start (general)
		{"/start@branchybot", self, true},       // our own mention
		{"/start@BranchyBot", self, true},       // case-insensitive
		{"/start@branchybot extra", self, true}, // trailing args ignored
		{"/start payload", self, true},          // deep-link payload
		{"/start\tpayload", self, true},         // any Telegram whitespace separator
		{"/start@quoto_bot", self, false},       // another bot
		{"/start@quoto_bot extra", self, false}, // another bot + args
		{"/start@branchybot", noSelf, false},    // own username unknown → ignore
		{"/help", self, false},
		{"/startfoo", self, false},
		{"hello", self, false},
		{"", self, false},
	}
	for _, c := range cases {
		if got := startCommandTargetsBot(c.text, c.self); got != c.want {
			t.Errorf("startCommandTargetsBot(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestGroupStartRepliesOnlyThroughEphemeralMessage(t *testing.T) {
	var calls int
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":-100,"type":"supergroup"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	store := &touchStore{}
	bot := &Bot{store: store, client: client}
	message := Message{
		From: User{ID: 42}, Chat: Chat{ID: -100, Type: "supergroup"}, Text: "/start",
	}

	if err := bot.handleMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("ordinary group /start sent %d public messages, want 0", calls)
	}

	message.Text = "/start@branchybot"
	message.EphemeralMessageID = 77
	if err := bot.handleMessage(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ephemeral group /start sent %d messages, want 1", calls)
	}
	for _, want := range []string{`"receiver_user_id":42`, `"ephemeral_message_id":77`} {
		if !strings.Contains(body, want) {
			t.Fatalf("ephemeral response missing %q:\n%s", want, body)
		}
	}
}

func TestEphemeralStartRepliesBeforeCoreTouch(t *testing.T) {
	requestSeen := make(chan struct{})
	releaseTouch := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestSeen)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":-100,"type":"supergroup"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.apiBase = server.URL
	store := &touchStore{touch: func(context.Context, db.TouchArgs) error {
		<-releaseTouch
		return errors.New("database unavailable")
	}}
	bot := &Bot{store: store, client: client}
	done := make(chan error, 1)
	go func() {
		done <- bot.handleMessage(context.Background(), Message{
			From: User{ID: 42}, Chat: Chat{ID: -100, Type: "supergroup"},
			Text: "/start@branchybot", EphemeralMessageID: 77,
		})
	}()

	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("ephemeral response waited for core touch")
	}
	close(releaseTouch)
	if err := <-done; err != nil {
		t.Fatalf("post-response core touch failure should not retry the update: %v", err)
	}
}

type touchStore struct {
	Store
	touched int
	touch   func(context.Context, db.TouchArgs) error
}

func (s *touchStore) TouchCore(ctx context.Context, args db.TouchArgs) error {
	s.touched++
	if s.touch != nil {
		return s.touch(ctx, args)
	}
	return nil
}

func TestBranchModeLabelReadsAsAction(t *testing.T) {
	cases := []struct {
		mode     string
		branches []string
		want     string
	}{
		{"all", nil, "All branches"},
		{"default", nil, "Default branch"},
		{"selected", nil, "Specific branches"},
		{"selected", []string{"main", "dev", "main"}, "Specific branches · 2"},
	}
	for _, c := range cases {
		if got := branchModeLabel(c.mode, c.branches); got != c.want {
			t.Fatalf("branchModeLabel(%q, %v) = %q, want %q", c.mode, c.branches, got, c.want)
		}
	}
	if strings.Contains(branchModeLabel("selected", nil), "No branches selected") {
		t.Fatal("branch mode button should not read as a status")
	}
}

func TestNormalizeDraftForEventsFallsBackWhenNoBranchesSelected(t *testing.T) {
	got := normalizeDraftForEvents(subDraft{Events: []string{"push"}, BranchMode: "selected"})
	if got.BranchMode != "default" {
		t.Fatalf("empty selection should fall back to default branch, got %q", got.BranchMode)
	}
	if len(got.BranchNames) != 0 {
		t.Fatalf("fallback should leave no branch names, got %v", got.BranchNames)
	}

	kept := normalizeDraftForEvents(subDraft{Events: []string{"push"}, BranchMode: "selected", BranchNames: []string{"main"}})
	if kept.BranchMode != "selected" || len(kept.BranchNames) != 1 {
		t.Fatalf("non-empty selection must stay selected, got %q/%v", kept.BranchMode, kept.BranchNames)
	}
}

func TestCheckboxAndRadioUseDistinctGlyphs(t *testing.T) {
	if checkbox(true, "x") != "■ x" || checkbox(false, "x") != "□ x" {
		t.Fatalf("checkbox glyphs = %q/%q", checkbox(true, "x"), checkbox(false, "x"))
	}
	if radio(true, "x") != "● x" || radio(false, "x") != "○ x" {
		t.Fatalf("radio glyphs = %q/%q", radio(true, "x"), radio(false, "x"))
	}
	if checkbox(true, "x") == radio(true, "x") {
		t.Fatal("single-select and multi-select markers must differ")
	}
}

func TestInlineKeyboardButtonStyleOmitsWhenEmpty(t *testing.T) {
	plain, _ := json.Marshal(InlineKeyboardButton{Text: "Back", CallbackData: "home"})
	if strings.Contains(string(plain), "style") {
		t.Fatalf("unstyled button should not serialize a style field: %s", plain)
	}
	primary, _ := json.Marshal(InlineKeyboardButton{Text: "Done", CallbackData: "x", Style: stylePrimary})
	if !strings.Contains(string(primary), `"style":"primary"`) {
		t.Fatalf("primary button should serialize a style field: %s", primary)
	}
	styled, _ := json.Marshal(InlineKeyboardButton{Text: "Create", CallbackData: "x", Style: styleSuccess})
	if !strings.Contains(string(styled), `"style":"success"`) {
		t.Fatalf("styled button should serialize style: %s", styled)
	}
}

func TestCreationActionsConsumeCallbackTokens(t *testing.T) {
	for _, action := range []string{"sub.branch", "sub.branch.selected", "sub.create", "sub.edit.pr.save", "sub.edit.release.save"} {
		if !isConsumedAction(action) {
			t.Fatalf("%s should consume callback token", action)
		}
	}
}

func TestSettingTogglesDoNotConsumeCallbackTokens(t *testing.T) {
	for _, action := range []string{"sub.settings.branch.toggle", "sub.settings.pr.toggle", "sub.edit.branch.toggle", "sub.edit.pr.toggle"} {
		if isConsumedAction(action) {
			t.Fatalf("%s should not consume callback token", action)
		}
	}
}

func TestVisibleRepositoriesHidesArchivedRepositories(t *testing.T) {
	repos := []github.Repository{
		{FullName: "acme/active-admin", HasAdminPermission: true},
		{FullName: "acme/active-readonly"},
		{FullName: "acme/archived-admin", HasAdminPermission: true, Archived: true},
	}

	all := visibleRepositories(repos, false)
	if got, want := repoNames(all), []string{"acme/active-admin", "acme/active-readonly"}; !sameStrings(got, want) {
		t.Fatalf("visible repositories = %v, want %v", got, want)
	}

	subscribe := visibleRepositories(repos, true)
	if got, want := repoNames(subscribe), []string{"acme/active-admin"}; !sameStrings(got, want) {
		t.Fatalf("subscribe repositories = %v, want %v", got, want)
	}
}

func TestVisibleRepositoriesSinksReadOnlyToBottom(t *testing.T) {
	repos := []github.Repository{
		{FullName: "acme/readonly-1"},
		{FullName: "acme/admin-1", HasAdminPermission: true},
		{FullName: "acme/readonly-2"},
		{FullName: "acme/admin-2", HasAdminPermission: true},
	}
	got := repoNames(visibleRepositories(repos, false))
	want := []string{"acme/admin-1", "acme/admin-2", "acme/readonly-1", "acme/readonly-2"}
	if !sameStrings(got, want) {
		t.Fatalf("ordering = %v, want admin repos first, read-only last (%v)", got, want)
	}
}

func repoNames(repos []github.Repository) []string {
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.FullName)
	}
	return names
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// recordingStore implements just enough of Store to observe the callback token
// a button mints. The embedded nil interface satisfies the rest of Store; any
// unimplemented method would panic, but the navigation helpers under test only
// reach CreateCallbackToken.
type recordingStore struct {
	Store
	action  string
	payload any
}

func (s *recordingStore) CreateCallbackToken(_ context.Context, _ int64, _, action string, payload any, _ time.Duration) error {
	s.action = action
	s.payload = payload
	return nil
}

// TestEditNavigationRoutesBackToEditMenu pins the hub-and-spoke edit flow: the
// per-field editors (events, advanced, destination) hand "Back" off to the edit
// menu rather than jumping straight to the subscription view, while the create
// flow keeps its plain Back to the create target.
func TestEditNavigationRoutesBackToEditMenu(t *testing.T) {
	store := &recordingStore{}
	b := &Bot{store: store}
	ctx := context.Background()

	btn, err := b.editMenuButton(ctx, 42, "sub-1")
	if err != nil {
		t.Fatalf("editMenuButton: %v", err)
	}
	if btn.Text != "Back" {
		t.Fatalf("edit-menu back button text = %q, want %q", btn.Text, "Back")
	}
	if store.action != "sub.edit.menu" {
		t.Fatalf("editMenuButton action = %q, want sub.edit.menu", store.action)
	}
	if p, ok := store.payload.(subscriptionPayload); !ok || p.ID != "sub-1" {
		t.Fatalf("editMenuButton payload = %#v, want subscriptionPayload{ID: sub-1}", store.payload)
	}

	// In edit mode the step-back button routes through the edit menu...
	store.action = ""
	if _, err := b.stepBackButton(ctx, 42, true, "sub-1", "sub:new"); err != nil {
		t.Fatalf("stepBackButton(edit): %v", err)
	}
	if store.action != "sub.edit.menu" {
		t.Fatalf("stepBackButton(edit) action = %q, want sub.edit.menu", store.action)
	}

	// ...while the create flow stays a plain Back to the create target, minting no token.
	store.action = ""
	createBtn, err := b.stepBackButton(ctx, 42, false, "", "sub:new")
	if err != nil {
		t.Fatalf("stepBackButton(create): %v", err)
	}
	if createBtn.CallbackData != "sub:new" {
		t.Fatalf("stepBackButton(create) target = %q, want sub:new", createBtn.CallbackData)
	}
	if store.action != "" {
		t.Fatalf("create-flow back should not mint a token, got action %q", store.action)
	}

	// The edit menu is re-entered every time the user taps Back from a field
	// editor, so its token must stay re-usable rather than single-use.
	if isConsumedAction("sub.edit.menu") {
		t.Fatal("sub.edit.menu must be re-enterable, not consumed on first tap")
	}
}
