// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/i18n"
	"branchy/internal/oauth"
	"github.com/FreshLabDev/tg"

	"branchy/internal/telegram"
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

	client := telegram.New("token", tg.WithAPIBase(server.URL))
	store := &touchStore{}
	bot := &Bot{store: store, client: client}
	message := tg.Message{
		From: &tg.User{ID: 42}, Chat: tg.Chat{ID: -100, Type: "supergroup"}, Text: "/start",
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
	for _, want := range []string{`"ephemeral_message_parameters":{"receiver_user_id":42}`, `"reply_parameters":{"ephemeral_message_id":77}`} {
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

	client := telegram.New("token", tg.WithAPIBase(server.URL))
	store := &touchStore{touch: func(context.Context, db.TouchArgs) error {
		<-releaseTouch
		return errors.New("database unavailable")
	}}
	bot := &Bot{store: store, client: client}
	done := make(chan error, 1)
	go func() {
		done <- bot.handleMessage(context.Background(), tg.Message{
			From: &tg.User{ID: 42}, Chat: tg.Chat{ID: -100, Type: "supergroup"},
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

// EffectiveLanguage answers "no preference recorded", which is what the shared
// hub returns for anybody who has never picked a language in any of the bots.
func (s *touchStore) EffectiveLanguage(context.Context, int64) (string, bool, error) {
	return "", false, nil
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
		if got := branchModeLabel(i18n.DefaultLang, c.mode, c.branches); got != c.want {
			t.Fatalf("branchModeLabel(%q, %v) = %q, want %q", c.mode, c.branches, got, c.want)
		}
	}
	if strings.Contains(branchModeLabel(i18n.DefaultLang, "selected", nil), "No branches selected") {
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
	// ◉/◎ is the family-wide chosen/unchosen pair; branchy used to draw ●/○.
	if radio(true, "x") != "◉ x" || radio(false, "x") != "◎ x" {
		t.Fatalf("radio glyphs = %q/%q", radio(true, "x"), radio(false, "x"))
	}
	if checkbox(true, "x") == radio(true, "x") {
		t.Fatal("single-select and multi-select markers must differ")
	}
}

func TestDisabledButtonSerializesWithoutCallbackData(t *testing.T) {
	raw, err := json.Marshal(disabledButton("Continue"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `"disabled":{}`) {
		t.Fatalf("disabled button missing empty object: %s", got)
	}
	if strings.Contains(got, "callback_data") || strings.Contains(got, `"url"`) {
		t.Fatalf("disabled button must not include an action field: %s", got)
	}
}

func TestPaginationRowDisablesUnavailableEdges(t *testing.T) {
	first := paginationRow(i18n.DefaultLang, "repo:list", 0, 3)
	if len(first) != 2 {
		t.Fatalf("first page nav = %#v, want prev+next", first)
	}
	if first[0].Disabled == nil || first[0].CallbackData != "" {
		t.Fatalf("prev on first page should be disabled: %#v", first[0])
	}
	if first[1].Disabled != nil || first[1].CallbackData != "repo:list:1" {
		t.Fatalf("next on first page should stay active: %#v", first[1])
	}

	last := paginationRow(i18n.DefaultLang, "repo:list", 2, 3)
	if last[1].Disabled == nil || last[1].CallbackData != "" {
		t.Fatalf("next on last page should be disabled: %#v", last[1])
	}
	if last[0].CallbackData != "repo:list:1" {
		t.Fatalf("prev on last page should stay active: %#v", last[0])
	}
}

func TestInlineKeyboardButtonStyleOmitsWhenEmpty(t *testing.T) {
	plain, _ := json.Marshal(tg.InlineKeyboardButton{Text: "Back", CallbackData: "home"})
	if strings.Contains(string(plain), "style") {
		t.Fatalf("unstyled button should not serialize a style field: %s", plain)
	}
	primary, _ := json.Marshal(tg.InlineKeyboardButton{Text: "Done", CallbackData: "x", Style: tg.StylePrimary})
	if !strings.Contains(string(primary), `"style":"primary"`) {
		t.Fatalf("primary button should serialize a style field: %s", primary)
	}
	styled, _ := json.Marshal(tg.InlineKeyboardButton{Text: "Create", CallbackData: "x", Style: tg.StyleSuccess})
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

	callback, err := b.editMenuCallback(ctx, 42, "sub-1")
	if err != nil {
		t.Fatalf("editMenuCallback: %v", err)
	}
	row := panelFooter(i18n.DefaultLang, tg.Chat{Type: "private"}, callback)
	if row[0].Text != i18n.T(i18n.DefaultLang, "btn.back") {
		t.Fatalf("edit-menu back button text = %q, want the btn.back translation", row[0].Text)
	}
	if row[0].CallbackData != callback {
		t.Fatalf("edit-menu back callback = %q, want %q", row[0].CallbackData, callback)
	}
	if store.action != "sub.edit.menu" {
		t.Fatalf("editMenuCallback action = %q, want sub.edit.menu", store.action)
	}
	if p, ok := store.payload.(subscriptionPayload); !ok || p.ID != "sub-1" {
		t.Fatalf("editMenuCallback payload = %#v, want subscriptionPayload{ID: sub-1}", store.payload)
	}

	// In edit mode the step-back button routes through the edit menu...
	store.action = ""
	if _, err := b.stepBackCallback(ctx, 42, true, "sub-1", "sub:new"); err != nil {
		t.Fatalf("stepBackCallback(edit): %v", err)
	}
	if store.action != "sub.edit.menu" {
		t.Fatalf("stepBackCallback(edit) action = %q, want sub.edit.menu", store.action)
	}

	// ...while the create flow stays a plain Back to the create target, minting no token.
	store.action = ""
	createCB, err := b.stepBackCallback(ctx, 42, false, "", "sub:new")
	if err != nil {
		t.Fatalf("stepBackCallback(create): %v", err)
	}
	if createCB != "sub:new" {
		t.Fatalf("stepBackCallback(create) target = %q, want sub:new", createCB)
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

func TestPRMoreCallbackDoesNotUseTokenAndScopesToChat(t *testing.T) {
	jobID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	compact := db.CompactUUID(jobID)
	snapshot, _ := json.Marshal(map[string]any{
		"number": 7, "title": "More", "url": "https://github.com/acme/repo/pull/7",
		"head_branch": "feat", "base_branch": "main",
	})
	store := &moreJobStore{
		job: db.NotificationJob{ID: jobID, DestinationChatID: -100, MoreJSON: snapshot},
	}
	var paths []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()
	client := telegram.New("token", tg.WithAPIBase(server.URL))
	bot := &Bot{store: store, client: client}

	cq := tg.CallbackQuery{
		ID:      "cq-more",
		From:    tg.User{ID: 42},
		Message: tg.Message{Chat: tg.Chat{ID: -100, Type: "supergroup"}},
		Data:    "m:" + compact,
	}
	if err := bot.handleCallback(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if store.tokenLookups != 0 {
		t.Fatalf("More must not look up callback_tokens, lookups=%d", store.tokenLookups)
	}
	if store.lookups != 1 || store.lastChatID != -100 || store.lastID != jobID {
		t.Fatalf("lookup = %+v chat=%d id=%q", store, store.lastChatID, store.lastID)
	}
	if len(paths) < 1 || !strings.HasSuffix(paths[0], "/sendRichMessage") {
		t.Fatalf("paths = %v, want sendRichMessage first", paths)
	}
	if !strings.Contains(bodies[0], `"callback_query_id":"cq-more"`) || strings.Contains(bodies[0], `"replace_callback_query_message"`) {
		t.Fatalf("ephemeral overlay JSON unexpected:\n%s", bodies[0])
	}

	store.lookups = 0
	paths = nil
	cq.Message.Chat.ID = -999
	if err := bot.handleCallback(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if store.lookups != 1 {
		t.Fatalf("wrong-chat tap should still query scoped lookup, got %d", store.lookups)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "/sendRichMessage") {
			t.Fatalf("wrong chat must not send the overlay: %v", paths)
		}
	}
	foundToast := false
	for _, body := range bodies {
		if strings.Contains(body, i18n.T(i18n.DefaultLang, "toast.snapshot_expired")) {
			foundToast = true
		}
	}
	if !foundToast {
		t.Fatalf("wrong chat should toast expired, bodies=%v", bodies)
	}
}

func TestPRMoreCallbackFetchesFilesWithOwnerToken(t *testing.T) {
	jobID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	snapshot, _ := json.Marshal(map[string]any{
		"number": 7, "title": "More", "url": "https://github.com/acme/repo/pull/7",
		"repo_full_name": "acme/repo", "head_branch": "feat", "base_branch": "main",
	})
	sealer := oauth.NewTokenSealer("test-secret")
	sealed, err := sealer.Encrypt("owner-token")
	if err != nil {
		t.Fatal(err)
	}
	store := &moreJobStore{
		job: db.NotificationJob{
			ID: jobID, SubscriptionID: "sub-owner", DestinationChatID: -100, MoreJSON: snapshot,
		},
		sub:  db.Subscription{ID: "sub-owner", TelegramUserID: 99},
		conn: db.GitHubConnection{TelegramUserID: 99, EncryptedAccessToken: sealed},
	}

	var ghAuth string
	var ghPath string
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghAuth = r.Header.Get("Authorization")
		ghPath = r.URL.Path
		_, _ = w.Write([]byte(`[{"filename":"internal/notify/more.go","status":"modified","additions":12,"deletions":3,"changes":15}]`))
	}))
	defer ghServer.Close()

	var tgBodies []string
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		tgBodies = append(tgBodies, string(raw))
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer tgServer.Close()

	client := telegram.New("token", tg.WithAPIBase(tgServer.URL))
	bot := &Bot{
		store:  store,
		client: client,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	cq := tg.CallbackQuery{
		ID:      "cq-more",
		From:    tg.User{ID: 42},
		Message: tg.Message{Chat: tg.Chat{ID: -100, Type: "supergroup"}},
		Data:    "m:" + db.CompactUUID(jobID),
	}
	if err := bot.handleCallback(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if store.connUserID != 99 {
		t.Fatalf("GitHub connection looked up for %d, want owner 99", store.connUserID)
	}
	if ghAuth != "Bearer owner-token" || ghPath != "/repos/acme/repo/pulls/7/files" {
		t.Fatalf("github call auth=%q path=%q", ghAuth, ghPath)
	}
	if len(tgBodies) < 1 || !strings.Contains(tgBodies[0], "internal/notify/more.go") || !strings.Contains(tgBodies[0], `\u003ctable`) {
		t.Fatalf("overlay should include file table: %v", tgBodies)
	}
	if strings.Contains(strings.Join(tgBodies, "\n"), i18n.T(i18n.DefaultLang, "toast.files_failed")) || strings.Contains(strings.Join(tgBodies, "\n"), i18n.T(i18n.DefaultLang, "toast.github_expired")) {
		t.Fatalf("successful fetch should not toast: %v", tgBodies)
	}
}

func TestPRMoreCallbackSendsOverlayWhenFilesFetchFails(t *testing.T) {
	jobID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	snapshot, _ := json.Marshal(map[string]any{
		"number": 7, "title": "More", "url": "https://github.com/acme/repo/pull/7",
		"repo_full_name": "acme/repo", "head_branch": "feat", "base_branch": "main",
		"additions": 10, "deletions": 2, "changed_files": 3, "commit_count": 1,
	})
	sealer := oauth.NewTokenSealer("test-secret")
	sealed, err := sealer.Encrypt("owner-token")
	if err != nil {
		t.Fatal(err)
	}
	store := &moreJobStore{
		job: db.NotificationJob{
			ID: jobID, SubscriptionID: "sub-owner", DestinationChatID: -100, MoreJSON: snapshot,
		},
		sub:  db.Subscription{ID: "sub-owner", TelegramUserID: 99},
		conn: db.GitHubConnection{TelegramUserID: 99, EncryptedAccessToken: sealed},
	}
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	defer ghServer.Close()
	var tgBodies []string
	var tgPaths []string
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tgPaths = append(tgPaths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		tgBodies = append(tgBodies, string(raw))
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer tgServer.Close()

	client := telegram.New("token", tg.WithAPIBase(tgServer.URL))
	bot := &Bot{
		store:  store,
		client: client,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	if err := bot.handleCallback(context.Background(), tg.CallbackQuery{
		ID:      "cq-more",
		From:    tg.User{ID: 42},
		Message: tg.Message{Chat: tg.Chat{ID: -100, Type: "supergroup"}},
		Data:    "m:" + db.CompactUUID(jobID),
	}); err != nil {
		t.Fatal(err)
	}
	var sentOverlay, toasted bool
	for i, path := range tgPaths {
		if strings.HasSuffix(path, "/sendRichMessage") {
			sentOverlay = true
			if strings.Contains(tgBodies[i], `"callback_query_id"`) {
				t.Fatalf("failed fetch should omit callback_query_id so the toast can answer: %s", tgBodies[i])
			}
			if !strings.Contains(tgBodies[i], "+10 · −2 · 3 files · 1 commit") {
				t.Fatalf("failed fetch should still send snapshot stats:\n%s", tgBodies[i])
			}
			if strings.Contains(tgBodies[i], `\u003ctable`) {
				t.Fatalf("failed fetch must not invent a file table:\n%s", tgBodies[i])
			}
		}
		if strings.Contains(tgBodies[i], i18n.T(i18n.DefaultLang, "toast.files_failed")) {
			toasted = true
		}
	}
	if !sentOverlay || !toasted {
		t.Fatalf("want overlay and toast, paths=%v bodies=%v", tgPaths, tgBodies)
	}
}

func TestPRMoreCallbackToastsExpiredGitHubToken(t *testing.T) {
	jobID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	snapshot, _ := json.Marshal(map[string]any{
		"number": 7, "title": "More", "url": "https://github.com/acme/repo/pull/7",
		"repo_full_name": "acme/repo",
	})
	sealer := oauth.NewTokenSealer("test-secret")
	sealed, err := sealer.Encrypt("owner-token")
	if err != nil {
		t.Fatal(err)
	}
	store := &moreJobStore{
		job: db.NotificationJob{
			ID: jobID, SubscriptionID: "sub-owner", DestinationChatID: -100, MoreJSON: snapshot,
		},
		sub:  db.Subscription{ID: "sub-owner", TelegramUserID: 99},
		conn: db.GitHubConnection{TelegramUserID: 99, EncryptedAccessToken: sealed},
	}
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ghServer.Close()
	var tgBodies []string
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		tgBodies = append(tgBodies, string(raw))
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer tgServer.Close()
	client := telegram.New("token", tg.WithAPIBase(tgServer.URL))
	bot := &Bot{
		store:  store,
		client: client,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	if err := bot.handleCallback(context.Background(), tg.CallbackQuery{
		ID:      "cq-more",
		From:    tg.User{ID: 42},
		Message: tg.Message{Chat: tg.Chat{ID: -100, Type: "supergroup"}},
		Data:    "m:" + db.CompactUUID(jobID),
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(tgBodies, "\n")
	if !strings.Contains(joined, i18n.T(i18n.DefaultLang, "toast.github_expired")) {
		t.Fatalf("401 should toast expired GitHub access: %v", tgBodies)
	}
	if strings.Contains(joined, i18n.T(i18n.DefaultLang, "toast.files_failed")) {
		t.Fatalf("401 should not use the generic files toast: %v", tgBodies)
	}
}

type moreJobStore struct {
	Store
	job          db.NotificationJob
	sub          db.Subscription
	conn         db.GitHubConnection
	lookups      int
	tokenLookups int
	connUserID   int64
	lastID       string
	lastChatID   int64
}

func (s *moreJobStore) TouchCore(context.Context, db.TouchArgs) error { return nil }

func (s *moreJobStore) EffectiveLanguage(context.Context, int64) (string, bool, error) {
	return "", false, nil
}

func (s *moreJobStore) GetNotificationJobForChat(_ context.Context, id string, chatID int64) (db.NotificationJob, error) {
	s.lookups++
	s.lastID = id
	s.lastChatID = chatID
	if id != s.job.ID || chatID != s.job.DestinationChatID {
		return db.NotificationJob{}, db.ErrNotFound
	}
	return s.job, nil
}

func (s *moreJobStore) GetSubscription(_ context.Context, id string) (db.Subscription, error) {
	if id != s.sub.ID {
		return db.Subscription{}, db.ErrNotFound
	}
	return s.sub, nil
}

func (s *moreJobStore) GetGitHubConnection(_ context.Context, telegramUserID int64) (db.GitHubConnection, error) {
	s.connUserID = telegramUserID
	if telegramUserID != s.conn.TelegramUserID {
		return db.GitHubConnection{}, db.ErrNotFound
	}
	return s.conn, nil
}

func (s *moreJobStore) GetCallbackToken(context.Context, int64, string) (db.CallbackToken, error) {
	s.tokenLookups++
	return db.CallbackToken{}, db.ErrNotFound
}

func TestPollRetryDelayBacksOffAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for failures := 1; failures <= 20; failures++ {
		d := pollRetryDelay(failures)
		if d < time.Second {
			t.Fatalf("failures=%d delay=%s, want >= 1s", failures, d)
		}
		if d > 90*time.Second {
			t.Fatalf("failures=%d delay=%s, want <= 90s (60s cap + jitter)", failures, d)
		}
		_ = prev
		prev = d
	}
}

// telegramStubResult answers the way Telegram does: a send returns the message
// it created, everything else returns true. The distinction matters now that
// the client decodes what a send gives back instead of ignoring it.
func telegramStubResult(path string) string {
	if strings.Contains(path, "send") {
		return `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":-100,"type":"supergroup"}}}`
	}
	return `{"ok":true,"result":true}`
}

func TestPanelFooterOffersCloseOnlyInGroups(t *testing.T) {
	cases := []struct {
		chatType string
		want     []string
	}{
		{"private", []string{"Back"}},
		{"", []string{"Back"}}, // unknown chat: never offer a Close that cannot work
		{"group", []string{"Back", "Close"}},
		{"supergroup", []string{"Back", "Close"}},
	}
	for _, c := range cases {
		row := panelFooter(i18n.DefaultLang, tg.Chat{Type: c.chatType}, "home")
		var got []string
		for _, button := range row {
			got = append(got, button.Text)
			if button.CallbackData == "" {
				t.Fatalf("chat %q: %q has no callback data", c.chatType, button.Text)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("panelFooter(%q) = %v, want %v", c.chatType, got, c.want)
		}
	}
}

func TestAboutCardStatesVersionAndLinksSourceInText(t *testing.T) {
	text := aboutText(i18n.DefaultLang, "v1.2.1-alpha.3")
	for _, want := range []string{
		"<b>Branchy</b> · <i>v1.2.1-alpha.3</i>",
		"Clean GitHub notifications in Telegram.",
		"<blockquote>Events · push, pull request, release",
		`Source · <a href="https://github.com/FreshLabDev/branchy">FreshLabDev/branchy</a> · Apache-2.0`,
		`Admin · <a href="https://t.me/amtiyo">@amtiyo</a></blockquote>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("about card missing %q:\n%s", want, text)
		}
	}

	// A build without the release ldflags has no version to claim.
	if got := (&Bot{}).buildVersion(); got != "dev" {
		t.Fatalf("unstamped build version = %q, want \"dev\"", got)
	}

	// The card is specified as exactly three rows, and the two facts people ask
	// for that it must NOT carry are a commit hash and a build timestamp:
	// neither answers a question somebody reading this card is asking.
	quote := text[strings.Index(text, "<blockquote>"):]
	if rows := strings.Count(quote, "\n") + 1; rows != 3 {
		t.Fatalf("About quote has %d rows, want exactly three:\n%s", rows, quote)
	}
	for _, forbidden := range []string{"commit", "Commit", "Built", "built"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("About card carries a %q row it should not:\n%s", forbidden, text)
		}
	}

	// The card renders in whatever language is answering; only the labels move.
	uk := aboutText("uk", "v1.2.1-alpha.3")
	if !strings.Contains(uk, "FreshLabDev/branchy") || !strings.Contains(uk, "@amtiyo") {
		t.Fatalf("About card lost its facts when translated:\n%s", uk)
	}
}

func TestGroupPanelOffersAboutAndCloseAndNoSourceButton(t *testing.T) {
	b := &Bot{}
	b.username.Store("branchybot")
	rows := b.groupPanel(i18n.DefaultLang).InlineKeyboard

	var labels []string
	for _, row := range rows {
		for _, button := range row {
			labels = append(labels, button.Text)
			// The repository is a link inside the About card, never a button:
			// two ways to reach one place is duplication, not convenience.
			if strings.Contains(button.URL, "github.com") {
				t.Fatalf("group panel should not link to the repository: %#v", button)
			}
		}
	}
	for _, want := range []string{"Open Branchy in DM", "About", "Close"} {
		if !slices.Contains(labels, want) {
			t.Fatalf("group panel buttons = %v, want %q", labels, want)
		}
	}

	// The DM link needs getMe; the rest of the panel must survive without it.
	fresh := &Bot{}
	for _, row := range fresh.groupPanel(i18n.DefaultLang).InlineKeyboard {
		for _, button := range row {
			if button.Text == "Open Branchy in DM" {
				t.Fatal("DM link offered before the bot username resolved")
			}
		}
	}
}

func TestAboutFromGroupPanelEditsTheEphemeralMessage(t *testing.T) {
	var paths []string
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/editEphemeralMessageText") {
			body = string(raw)
		}
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	b := &Bot{store: &touchStore{}, client: telegram.New("token", tg.WithAPIBase(server.URL)), version: "v9.9.9"}
	cq := tg.CallbackQuery{
		ID: "1", From: tg.User{ID: 42}, Data: "about",
		Message: tg.Message{
			MessageID: 5, EphemeralMessageID: 77,
			Chat: tg.Chat{ID: -100, Type: "supergroup"},
		},
	}
	if _, err := b.dispatchCallback(context.Background(), cq, i18n.DefaultLang); err != nil {
		t.Fatal(err)
	}

	for _, path := range paths {
		if strings.HasSuffix(path, "/editMessageText") || strings.HasSuffix(path, "/sendMessage") {
			t.Fatalf("ephemeral panel answered with %s; the group panel would freeze and the reply would land in DM", path)
		}
	}
	for _, want := range []string{
		`"ephemeral_message_id":77`,
		`"receiver_user_id":42`,
		"v9.9.9",
		`"text":"Close"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("about edit missing %q:\n%s", want, body)
		}
	}
}

func TestCloseOnlyDeletesEphemeralMessages(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	b := &Bot{store: &touchStore{}, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	group := tg.Chat{ID: -100, Type: "supergroup"}

	// Callback data can be sent for any visible message, not only for the
	// buttons a client was shown. "close" against a public message must not
	// delete Branchy's notification cards.
	public := tg.CallbackQuery{ID: "1", From: tg.User{ID: 42}, Data: "close", Message: tg.Message{MessageID: 5, Chat: group}}
	if _, err := b.dispatchCallback(context.Background(), public, i18n.DefaultLang); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.Contains(path, "elete") {
			t.Fatalf("close deleted a public group message via %s", path)
		}
	}

	paths = nil
	ephemeral := tg.CallbackQuery{ID: "2", From: tg.User{ID: 42}, Data: "close", Message: tg.Message{MessageID: 5, EphemeralMessageID: 77, Chat: group}}
	if _, err := b.dispatchCallback(context.Background(), ephemeral, i18n.DefaultLang); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/deleteEphemeralMessage") {
		t.Fatalf("close on the group panel called %v, want one deleteEphemeralMessage", paths)
	}
}

func TestGroupHomeCallbackDoesNotRebuildTheDirectMessageMenu(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/editEphemeralMessageText") {
			body = string(raw)
		}
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	// A store whose GitHub lookups would panic: renderHome must not reach for
	// the caller's connection while answering inside a shared chat.
	b := &Bot{store: &touchStore{}, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	cq := tg.CallbackQuery{
		ID: "1", From: tg.User{ID: 42}, Data: "home",
		Message: tg.Message{MessageID: 5, EphemeralMessageID: 77, Chat: tg.Chat{ID: -100, Type: "supergroup"}},
	}
	if _, err := b.dispatchCallback(context.Background(), cq, i18n.DefaultLang); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, i18n.T(i18n.DefaultLang, "home.group.body")) {
		t.Fatalf("group home did not return the group panel:\n%s", body)
	}
}

func TestPublicGroupMessagesAreNeverEditedIntoPanels(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	// Every panel Branchy shows in a group is ephemeral, so a public group
	// message from it is a delivered notification card. Callback data can be
	// sent for any visible message, so a forged one must not overwrite a card.
	b := &Bot{store: &touchStore{}, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	cq := tg.CallbackQuery{
		ID: "1", From: tg.User{ID: 42}, Data: "home",
		Message: tg.Message{MessageID: 5, Chat: tg.Chat{ID: -100, Type: "supergroup"}},
	}
	if _, err := b.dispatchCallback(context.Background(), cq, i18n.DefaultLang); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "/editMessageText") {
			t.Fatalf("a forged group callback edited a public message: %v", paths)
		}
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/sendMessage") {
		t.Fatalf("group callback answered with %v, want one DM sendMessage", paths)
	}
}

// TestPanelIsTheOnlyShape pins the family panel: a bold title, an italic
// one-line hint, then the substance in a blockquote — and nothing emitted for
// the parts a screen does not have. A screen whose keyboard already says
// everything must not grow an empty quote to satisfy the shape.
func TestPanelIsTheOnlyShape(t *testing.T) {
	full := panel("Subscriptions", "", "Tap one to edit it.", "Events: Push")
	if full != "<b>Subscriptions</b>\n<i>Tap one to edit it.</i>\n\n<blockquote>Events: Push</blockquote>" {
		t.Fatalf("panel = %q", full)
	}
	if got := panel("Language", "", "Shared with the other bots.", ""); strings.Contains(got, "blockquote") {
		t.Fatalf("a screen with no substance must not open a quote: %q", got)
	}
	if got := panel("Repositories", "", "", ""); got != "<b>Repositories</b>" {
		t.Fatalf("bare panel = %q", got)
	}
	// The badge is the About card's version and renders on the title line, which
	// is the one place the family card differs from every other screen.
	if got := panel("Branchy", "v1.0.0", "Tagline.", ""); got != "<b>Branchy</b> · <i>v1.0.0</i>\n<i>Tagline.</i>" {
		t.Fatalf("badge panel = %q", got)
	}
}

// TestEveryTranslationKeyExists reads the package's own source for i18n.T
// literals and checks each one against translations.json. Fifteen more
// languages are about to be keyed off that file: a key that only exists at a
// call site renders as "[key]" in every one of them, and no other test would
// catch it because the screen it lives on may never be exercised.
func TestEveryTranslationKeyExists(t *testing.T) {
	known := make(map[string]bool)
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	pattern := regexp.MustCompile(`i18n\.T\([^,]+,\s*"([^"]+)"`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(src), -1) {
			checked++
			if !known[match[1]] {
				t.Errorf("%s references missing translation key %q", entry.Name(), match[1])
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d i18n.T call sites found; the scan is not seeing the panels", checked)
	}
}

// TestValidationErrorsCarryRealKeys closes the other half of the same loop: the
// subscriptions layer names a sentence instead of carrying one, and a key it
// invents that translations.json does not have would reach the user as "[key]".
func TestValidationErrorsCarryRealKeys(t *testing.T) {
	known := make(map[string]bool)
	for _, key := range i18n.Keys() {
		known[key] = true
	}
	src, err := os.ReadFile("../subscriptions/service.go")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`invalid\("([^"]+)"`)
	matches := pattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 10 {
		t.Fatalf("only %d validation keys found; the scan is not seeing the service", len(matches))
	}
	for _, match := range matches {
		if !known[match[1]] {
			t.Errorf("subscriptions/service.go raises missing translation key %q", match[1])
		}
	}
}

// TestNoOrphanTranslationKeys is the reverse check, and it is aimed squarely at
// the translation pass: a key nobody renders is fifteen translations of nothing,
// paid for by a person who has no way to tell it is dead.
func TestNoOrphanTranslationKeys(t *testing.T) {
	var corpus strings.Builder
	for _, dir := range []string{".", "../subscriptions"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			src, err := os.ReadFile(dir + "/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			corpus.Write(src)
		}
	}
	source := corpus.String()
	for _, key := range i18n.Keys() {
		if !strings.Contains(source, `"`+key+`"`) {
			t.Errorf("translation key %q is never rendered", key)
		}
	}
}

// languageStore records what the language screen writes to the shared hub.
type languageStore struct {
	Store
	effective string
	set       string
	cleared   bool
	setErr    error
}

func (s *languageStore) TouchCore(context.Context, db.TouchArgs) error { return nil }

func (s *languageStore) EffectiveLanguage(context.Context, int64) (string, bool, error) {
	if s.effective == "" {
		return "", false, nil
	}
	return s.effective, true, nil
}

func (s *languageStore) SetLanguage(_ context.Context, _ int64, lang string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.set = lang
	return nil
}

func (s *languageStore) ClearLanguage(context.Context, int64) error {
	s.cleared = true
	return nil
}

// TestLanguageScreenMatchesTheFamilyGrid pins the shared screen: sixteen
// languages in one fixed order, two per row, every option marked and only the
// current one coloured, then "Follow Telegram" and the nav row. It is the same
// grid in every bot of the family, and "the same" is the whole point of it.
func TestLanguageScreenMatchesTheFamilyGrid(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			body = string(raw)
		}
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	b := &Bot{store: &languageStore{effective: "uk"}, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	cq := tg.CallbackQuery{
		ID: "1", From: tg.User{ID: 42}, Data: "lang",
		Message: tg.Message{MessageID: 5, Chat: tg.Chat{ID: 42, Type: "private"}},
	}
	if _, err := b.dispatchCallback(context.Background(), cq, "uk"); err != nil {
		t.Fatal(err)
	}

	var sent struct {
		Text   string `json:"text"`
		Markup struct {
			Keyboard [][]tg.InlineKeyboardButton `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("edit body did not parse: %v\n%s", err, body)
	}
	rows := sent.Markup.Keyboard
	if len(rows) != 10 {
		t.Fatalf("language keyboard has %d rows, want 8 language rows + follow + nav", len(rows))
	}
	var got []string
	for _, row := range rows[:8] {
		if len(row) != 2 {
			t.Fatalf("language rows must hold two buttons, got %d: %#v", len(row), row)
		}
		for _, button := range row {
			code := strings.TrimPrefix(button.CallbackData, "lang:")
			got = append(got, code)
			// Both states are shown, so the column has one left edge.
			if !strings.HasPrefix(button.Text, "◉ ") && !strings.HasPrefix(button.Text, "◎ ") {
				t.Fatalf("unmarked language button: %q", button.Text)
			}
			if !strings.HasSuffix(button.Text, i18n.LabelOf(code)) {
				t.Fatalf("button %q does not carry the family label for %q", button.Text, code)
			}
			if code == "uk" {
				if button.Style != tg.StyleSuccess || !strings.HasPrefix(button.Text, "◉ ") {
					t.Fatalf("current language must be marked and Success: %#v", button)
				}
			} else if button.Style != "" {
				t.Fatalf("only the current language carries a style: %#v", button)
			}
		}
	}
	if strings.Join(got, " ") != strings.Join(i18n.Codes(), " ") {
		t.Fatalf("language order = %v, want %v", got, i18n.Codes())
	}
	if rows[8][0].CallbackData != "lang:follow" {
		t.Fatalf("follow row = %#v", rows[8])
	}
	if rows[9][0].Text != i18n.T("uk", "btn.back") || rows[9][0].CallbackData != "home" {
		t.Fatalf("nav row = %#v", rows[9])
	}
	// A DM has nothing to close.
	if len(rows[9]) != 1 {
		t.Fatalf("a direct chat must not offer Close: %#v", rows[9])
	}
	// State lives on the buttons, never repeated as a list in the body.
	if strings.Contains(sent.Text, "blockquote") {
		t.Fatalf("language panel must not restate the keyboard: %s", sent.Text)
	}
}

// TestLanguageChoiceReachesTheSharedHub covers the two writes the screen makes
// and, for "Follow Telegram", that withdrawing the claim is not the same as
// picking English.
func TestLanguageChoiceReachesTheSharedHub(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	store := &languageStore{}
	b := &Bot{store: store, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	base := tg.Message{MessageID: 5, Chat: tg.Chat{ID: 42, Type: "private"}}

	toast, err := b.dispatchCallback(context.Background(),
		tg.CallbackQuery{ID: "1", From: tg.User{ID: 42}, Data: "lang:uk", Message: base}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if store.set != "uk" {
		t.Fatalf("set language = %q, want uk", store.set)
	}
	// The confirmation of a switch is itself in the language just chosen.
	if toast != i18n.T("uk", "toast.lang_set") {
		t.Fatalf("toast = %q, want the Ukrainian confirmation", toast)
	}

	// An unsupported code is not written: callback data is whatever a client
	// chose to send, not only what it was shown.
	store.set = ""
	if _, err := b.dispatchCallback(context.Background(),
		tg.CallbackQuery{ID: "2", From: tg.User{ID: 42}, Data: "lang:klingon", Message: base}, "en"); err != nil {
		t.Fatal(err)
	}
	if store.set != "" {
		t.Fatalf("unsupported code was written to the hub: %q", store.set)
	}

	// Follow Telegram withdraws the claim rather than setting a language.
	store.set = ""
	toast, err = b.dispatchCallback(context.Background(),
		tg.CallbackQuery{ID: "3", From: tg.User{ID: 42, LanguageCode: "de-DE"}, Data: "lang:follow", Message: base}, "uk")
	if err != nil {
		t.Fatal(err)
	}
	if !store.cleared || store.set != "" {
		t.Fatalf("follow should clear, not set: cleared=%v set=%q", store.cleared, store.set)
	}
	if toast != i18n.T("de", "toast.lang_follow") {
		t.Fatalf("toast = %q, want the German confirmation the Telegram hint implies", toast)
	}
}

// Follow Telegram withdraws Branchy's claim and nobody else's. If a sibling bot
// still holds a manual choice for this person, the hub keeps answering that —
// so the screen has to ask what won rather than assume the Telegram hint, which
// is what it used to do and what made the panel repaint in a language the very
// next update would replace.
func TestFollowTelegramRedrawsInWhatTheHubAnswersNotTheClientHint(t *testing.T) {
	// The hub keeps answering Russian: another bot's manual choice outlived the
	// clear. The Telegram client says German.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(telegramStubResult(r.URL.Path)))
	}))
	defer server.Close()

	store := &languageStore{effective: "ru"}
	b := &Bot{store: store, client: telegram.New("token", tg.WithAPIBase(server.URL))}
	base := tg.Message{MessageID: 7, Chat: tg.Chat{ID: 42, Type: "private"}}

	toast, err := b.dispatchCallback(context.Background(),
		tg.CallbackQuery{ID: "1", From: tg.User{ID: 42, LanguageCode: "de-DE"}, Data: "lang:follow", Message: base}, "uk")
	if err != nil {
		t.Fatal(err)
	}
	if !store.cleared {
		t.Fatal("follow did not withdraw the claim")
	}
	if toast != i18n.T("ru", "toast.lang_follow") {
		t.Fatalf("toast = %q, want the Russian the hub still answers, not the German hint", toast)
	}
}

// TestLanguageIsReachableFromHome guards the one thing that makes the screen
// worth having: a way in that does not require knowing a callback string.
func TestLanguageIsReachableFromHome(t *testing.T) {
	b := &Bot{store: &homeStore{}, oauth: stubOAuth{}}
	_, markup, err := b.mainMenu(context.Background(), 42, i18n.DefaultLang)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == "lang" {
				return
			}
		}
	}
	t.Fatalf("no way into the language screen from home: %#v", markup.InlineKeyboard)
}

type homeStore struct {
	Store
	connected bool
	subs      []db.Subscription
}

func (s homeStore) GetGitHubConnection(context.Context, int64) (db.GitHubConnection, error) {
	if !s.connected {
		return db.GitHubConnection{}, db.ErrNotFound
	}
	return db.GitHubConnection{TelegramUserID: 42, GitHubLogin: "octocat"}, nil
}

func (s homeStore) ListSubscriptionsByUser(context.Context, int64) ([]db.Subscription, error) {
	return s.subs, nil
}

type stubOAuth struct{}

func (stubOAuth) CreateAuthURL(context.Context, int64) (string, error) {
	return "https://github.com/login/oauth/authorize", nil
}

// assertStyleContract checks one screen's keyboard against the family colour
// rules: at most one Primary, Success only on the option you are currently on,
// and Close destructive and offered only where there is something to close.
func assertStyleContract(t *testing.T, screen string, group bool, rows [][]tg.InlineKeyboardButton) {
	t.Helper()
	primaries := 0
	for _, row := range rows {
		for _, button := range row {
			switch button.Style {
			case tg.StylePrimary:
				primaries++
			case tg.StyleSuccess:
				// Success reports state, never an action, so it only ever lands
				// on the option already marked as chosen.
				if !strings.HasPrefix(button.Text, "◉ ") {
					t.Errorf("%s: Success on %q, which is an action rather than the state you are in", screen, button.Text)
				}
			case tg.StyleDanger:
				if button.CallbackData != "close" && !strings.Contains(button.CallbackData, "t:") {
					t.Errorf("%s: Danger on %q, which destroys nothing", screen, button.Text)
				}
			}
			if button.CallbackData == "close" {
				if !group {
					t.Errorf("%s: a direct chat has nothing to close, yet offers %q", screen, button.Text)
				}
				if button.Style != tg.StyleDanger {
					t.Errorf("%s: Close must be painted destructive, got style %q", screen, button.Style)
				}
			}
		}
	}
	if primaries > 1 {
		t.Errorf("%s: %d Primary buttons; two primaries single out neither", screen, primaries)
	}
}

// TestScreensObeyTheColourContract walks the screens whose keyboards can be
// built without a live GitHub. The rule this pins hardest is the one Branchy
// broke: "Create subscription" was Primary on the repository screen and Success
// on the settings screen, so the same action changed colour depending on how
// you arrived at it.
func TestScreensObeyTheColourContract(t *testing.T) {
	ctx := context.Background()

	group := &Bot{}
	group.username.Store("branchybot")
	assertStyleContract(t, "group panel", true, group.groupPanel(i18n.DefaultLang).InlineKeyboard)

	for _, c := range []struct {
		name  string
		store homeStore
	}{
		{"home (disconnected)", homeStore{}},
		{"home (connected)", homeStore{connected: true, subs: []db.Subscription{{ID: "s1"}}}},
	} {
		b := &Bot{store: c.store, oauth: stubOAuth{}}
		_, markup, err := b.mainMenu(ctx, 42, i18n.DefaultLang)
		if err != nil {
			t.Fatal(err)
		}
		assertStyleContract(t, c.name, false, markup.InlineKeyboard)
	}

	// The language grid is the screen with the most Success buttons by far, and
	// exactly one of them is allowed.
	var rows [][]tg.InlineKeyboardButton
	for _, option := range i18n.LANGUAGE_OPTIONS {
		rows = append(rows, []tg.InlineKeyboardButton{languageButton(option, "uk")})
	}
	rows = append(rows, panelFooter(i18n.DefaultLang, tg.Chat{Type: "supergroup"}, "home"))
	assertStyleContract(t, "language", true, rows)

	// And the option a single-select screen is already on is state, not action.
	current := currentOption(radio(true, "All branches"))
	if current.Style != tg.StyleSuccess || current.Disabled == nil {
		t.Fatalf("current option = %#v, want a disabled Success button", current)
	}
}
