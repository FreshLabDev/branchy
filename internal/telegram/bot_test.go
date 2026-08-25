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
	"branchy/internal/oauth"
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
	first := paginationRow("repo:list", 0, 3)
	if len(first) != 2 {
		t.Fatalf("first page nav = %#v, want prev+next", first)
	}
	if first[0].Disabled == nil || first[0].CallbackData != "" {
		t.Fatalf("prev on first page should be disabled: %#v", first[0])
	}
	if first[1].Disabled != nil || first[1].CallbackData != "repo:list:1" {
		t.Fatalf("next on first page should stay active: %#v", first[1])
	}

	last := paginationRow("repo:list", 2, 3)
	if last[1].Disabled == nil || last[1].CallbackData != "" {
		t.Fatalf("next on last page should be disabled: %#v", last[1])
	}
	if last[0].CallbackData != "repo:list:1" {
		t.Fatalf("prev on last page should stay active: %#v", last[0])
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
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	client := NewClient("token")
	client.apiBase = server.URL
	bot := &Bot{store: store, client: client}

	cq := CallbackQuery{
		ID:   "cq-more",
		From: User{ID: 42},
		Message: Message{Chat: Chat{ID: -100, Type: "supergroup"}},
		Data: "m:" + compact,
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
		if strings.Contains(body, snapshotExpiredToast) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tgServer.Close()

	tg := NewClient("token")
	tg.apiBase = tgServer.URL
	bot := &Bot{
		store:  store,
		client: tg,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	cq := CallbackQuery{
		ID:   "cq-more",
		From: User{ID: 42},
		Message: Message{Chat: Chat{ID: -100, Type: "supergroup"}},
		Data: "m:" + db.CompactUUID(jobID),
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
	if strings.Contains(strings.Join(tgBodies, "\n"), filesLoadFailedToast) || strings.Contains(strings.Join(tgBodies, "\n"), githubExpiredToast) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tgServer.Close()

	tg := NewClient("token")
	tg.apiBase = tgServer.URL
	bot := &Bot{
		store:  store,
		client: tg,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	if err := bot.handleCallback(context.Background(), CallbackQuery{
		ID:   "cq-more",
		From: User{ID: 42},
		Message: Message{Chat: Chat{ID: -100, Type: "supergroup"}},
		Data: "m:" + db.CompactUUID(jobID),
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
		if strings.Contains(tgBodies[i], filesLoadFailedToast) {
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
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tgServer.Close()
	tg := NewClient("token")
	tg.apiBase = tgServer.URL
	bot := &Bot{
		store:  store,
		client: tg,
		github: github.NewClient(github.Config{UserAgent: "test", APIURL: ghServer.URL}),
		sealer: sealer,
	}
	if err := bot.handleCallback(context.Background(), CallbackQuery{
		ID:      "cq-more",
		From:    User{ID: 42},
		Message: Message{Chat: Chat{ID: -100, Type: "supergroup"}},
		Data:    "m:" + db.CompactUUID(jobID),
	}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(tgBodies, "\n")
	if !strings.Contains(joined, githubExpiredToast) {
		t.Fatalf("401 should toast expired GitHub access: %v", tgBodies)
	}
	if strings.Contains(joined, filesLoadFailedToast) {
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
