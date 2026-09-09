// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/i18n"
	"branchy/internal/notify"
	"branchy/internal/oauth"
	"branchy/internal/subscriptions"
	"github.com/FreshLabDev/tg"

	"branchy/internal/telegram"
)

type Store interface {
	TouchCore(ctx context.Context, a db.TouchArgs) error
	UpsertChatState(ctx context.Context, chat db.ChatState) error
	ListKnownGroups(ctx context.Context, telegramUserID int64) ([]db.ChatState, error)
	GetGitHubConnection(ctx context.Context, telegramUserID int64) (db.GitHubConnection, error)
	ListSubscriptionsByUser(ctx context.Context, telegramUserID int64) ([]db.Subscription, error)
	GetSubscriptionForUser(ctx context.Context, telegramUserID int64, id string) (db.Subscription, error)
	GetSubscription(ctx context.Context, id string) (db.Subscription, error)
	CreateCallbackToken(ctx context.Context, telegramUserID int64, token, action string, payload any, ttl time.Duration) error
	GetCallbackToken(ctx context.Context, telegramUserID int64, token string) (db.CallbackToken, error)
	ConsumeCallbackToken(ctx context.Context, telegramUserID int64, token string) (db.CallbackToken, error)
	GetRuntimeValue(ctx context.Context, key string) (string, error)
	SetRuntimeValue(ctx context.Context, key, value string) error
	GetNotificationJobForChat(ctx context.Context, id string, chatID int64) (db.NotificationJob, error)
	SetLanguage(ctx context.Context, telegramUserID int64, lang string) error
	ClearLanguage(ctx context.Context, telegramUserID int64) error
	EffectiveLanguage(ctx context.Context, telegramUserID int64) (string, bool, error)
}

type OAuthService interface {
	CreateAuthURL(ctx context.Context, telegramUserID int64) (string, error)
}

type Bot struct {
	store        Store
	client       *telegram.Client
	oauth        OAuthService
	github       *github.Client
	sealer       *oauth.TokenSealer
	subs         *subscriptions.Service
	lastPollUnix atomic.Int64
	username     atomic.Value // string, the bot's @username, fetched lazily
	// version is the same build string /healthz reports, stamped into the
	// binary at link time. The About card states it because "which version are
	// you running" is the first question a complaint has to answer.
	version string
}

const (
	repoPageSize   = 10
	branchPageSize = 20
)

func NewBot(store Store, client *telegram.Client, oauthSvc OAuthService, githubClient *github.Client, sealer *oauth.TokenSealer, subs *subscriptions.Service, version string) *Bot {
	return &Bot{store: store, client: client, oauth: oauthSvc, github: githubClient, sealer: sealer, subs: subs, version: version}
}

func (b *Bot) Run(ctx context.Context) error {
	// Username resolution is useful for the DM button but must not delay polling.
	// Retry it independently so a transient getMe failure heals without restart.
	go b.warmBotUsername(ctx)

	offset, err := b.loadOffset(ctx)
	if err != nil {
		slog.Warn("telegram offset load failed", "error", err)
	}
	var pollFailures int
	// Track consecutive re-delivery attempts of a single failing update so a
	// persistently failing (poison) update is dropped after a few tries instead
	// of stalling the whole poll loop forever.
	const maxUpdateRetries = 3
	var lastFailedUpdate int64
	var failedRetries int
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		updates, err := b.client.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			pollFailures++
			delay := pollRetryDelay(pollFailures)
			// A sustained outage (DNS down, Telegram unreachable) repeats the
			// same error for hours: log the first few, then sample.
			if pollFailures <= 3 || pollFailures%10 == 0 {
				slog.Error("telegram getUpdates failed", "error", err, "consecutive_failures", pollFailures, "retry_in", delay)
			}
			if sleep(ctx, delay) != nil {
				return nil
			}
			continue
		}
		if pollFailures > 0 {
			slog.Info("telegram polling recovered", "after_failures", pollFailures)
			pollFailures = 0
		}
		b.lastPollUnix.Store(time.Now().Unix())
		for _, update := range updates {
			if err := b.handleUpdate(ctx, update); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// Do not advance the offset past a failed update: leaving it
				// unconfirmed makes the next GetUpdates re-deliver it, giving a
				// transient DB/Telegram hiccup another chance instead of silently
				// dropping the user's command. Give up after a few attempts so a
				// poison update cannot stall the loop.
				if update.UpdateID == lastFailedUpdate {
					failedRetries++
				} else {
					lastFailedUpdate = update.UpdateID
					failedRetries = 1
				}
				if failedRetries <= maxUpdateRetries {
					slog.Error("telegram update failed; will retry", "update_id", update.UpdateID, "attempt", failedRetries, "error", err)
					// Brief backoff before the next GetUpdates re-delivers this
					// update: it stays unconfirmed so GetUpdates returns it
					// immediately, and a tiny pause lets a transient error clear
					// instead of spinning.
					if sleep(ctx, jitterDuration(250*time.Millisecond)) != nil {
						return nil
					}
					break
				}
				slog.Error("telegram update permanently failed; dropping", "update_id", update.UpdateID, "attempts", failedRetries, "error", err)
				lastFailedUpdate = 0
				failedRetries = 0
			} else if update.UpdateID == lastFailedUpdate {
				lastFailedUpdate = 0
				failedRetries = 0
			}
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
				if err := b.store.SetRuntimeValue(ctx, "telegram_update_offset", strconv.FormatInt(offset, 10)); err != nil {
					slog.Error("telegram offset persist failed", "error", err)
				}
			}
		}
	}
}

// pollRetryDelay backs off 2s → 60s (jittered) so a sustained Telegram or DNS
// outage does not hammer the network — or the logs — every two seconds.
func pollRetryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 6 {
		failures = 6
	}
	delay := 2 * time.Second << (failures - 1)
	if delay > time.Minute {
		delay = time.Minute
	}
	return jitterDuration(delay)
}

func (b *Bot) LastPoll() time.Time {
	unix := b.lastPollUnix.Load()
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func (b *Bot) loadOffset(ctx context.Context) (int64, error) {
	value, err := b.store.GetRuntimeValue(ctx, "telegram_update_offset")
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	return offset, nil
}

func (b *Bot) handleUpdate(ctx context.Context, update tg.Update) error {
	if update.MyChatMember != nil {
		return b.handleMyChatMember(ctx, *update.MyChatMember)
	}
	if update.Message != nil {
		return b.handleMessage(ctx, *update.Message)
	}
	if update.Callback != nil {
		return b.handleCallback(ctx, *update.Callback)
	}
	return nil
}

// startCommandTargetsBot reports whether text is a /start meant for THIS bot:
// the bare "/start" (Telegram delivers it to every bot in a group) or
// "/start@<self>". A "/start@<otherbot>" — which Telegram also delivers to us in
// a group — returns false, so branchy stays quiet when the /start was addressed
// to a different bot. self is resolved lazily and only consulted for the "@"
// form (so the common path makes no extra call); an empty self never matches a
// suffixed start.
func startCommandTargetsBot(text string, self func() string) bool {
	text = commandToken(text)
	if text == "/start" {
		return true
	}
	if suffix, ok := strings.CutPrefix(text, "/start@"); ok {
		s := self()
		return s != "" && strings.EqualFold(suffix, s)
	}
	return false
}

func commandToken(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// Telegram delivers an ephemeral command only to its target bot, so the
// suffixed form is safe to accept even while getMe username resolution is down.
func isEphemeralStartCommand(text string) bool {
	token := commandToken(text)
	return token == "/start" || (strings.HasPrefix(token, "/start@") && len(token) > len("/start@"))
}

func (b *Bot) handleMessage(ctx context.Context, msg tg.Message) error {
	// A message with no sender is a channel post or an anonymous group admin.
	// Branchy answers people, and every path below is keyed by a real user id;
	// the shared client models the absence honestly, where the old local type
	// handed over a zero-valued user.
	if msg.From == nil {
		return nil
	}
	if msg.Chat.Type != "private" && msg.EphemeralMessageID != 0 && isEphemeralStartCommand(msg.Text) {
		return b.handleEphemeralStart(ctx, msg)
	}
	if err := b.upsertUser(ctx, msg.From, &msg.Chat); err != nil {
		return err
	}
	resolveSelf := func() string { return b.botUsername(ctx) }
	if msg.Chat.Type != "private" {
		resolveSelf = b.cachedBotUsername
	}
	if startCommandTargetsBot(msg.Text, resolveSelf) {
		if msg.Chat.Type != "private" {
			// Bot API 10.2 group /start is registered as ephemeral. Never emit a
			// public fallback for an ordinary group message: settings belong in DM,
			// and the prompt must remain visible only to the invoking user.
			return nil
		}
		text, markup, err := b.mainMenu(ctx, msg.From.ID, b.resolveLang(ctx, msg.From))
		if err != nil {
			return err
		}
		_, err = b.client.SendMessage(ctx, msg.Chat.ID, text, markup)
		return err
	}
	// Nudge unrecognized private-chat input toward the menu instead of silently
	// ignoring it (which reads as a dead bot). Groups stay quiet to avoid noise.
	if msg.Chat.Type == "private" && strings.TrimSpace(msg.Text) != "" {
		_, err := b.client.SendMessage(ctx, msg.Chat.ID, i18n.T(b.resolveLang(ctx, msg.From), "home.nudge"), nil)
		return err
	}
	return nil
}

// resolveLang answers "which language does this person read Branchy in".
//
// The manual choice lives in the shared core hub, so a language picked in a
// sibling bot already answers here before Branchy has ever shown its own
// picker. Only when the hub has nothing does the Telegram profile hint decide.
// A hub that is slow or down must not cost a person their reply, so every
// failure falls through to the hint rather than propagating.
func (b *Bot) resolveLang(ctx context.Context, user *tg.User) string {
	if user == nil {
		return i18n.DefaultLang
	}
	fallback := i18n.LangOf(user.LanguageCode)
	if user.ID == 0 {
		return fallback
	}
	effective, ok, err := b.store.EffectiveLanguage(ctx, user.ID)
	if err != nil {
		slog.Warn("effective language lookup failed", "user_id", user.ID, "error", err)
		return fallback
	}
	if !ok {
		return fallback
	}
	return i18n.LangOf(effective)
}

// groupPanelText is the group door: Branchy does nothing configurable in a
// group, so the card's whole job is to send the reader to a direct message.
func groupPanelText(lang string) string {
	return panel(i18n.T(lang, "home.title"), "", i18n.T(lang, "home.hint"), i18n.T(lang, "home.group.body"))
}

// groupPanel is the whole of Branchy's group interface. Settings live in DM, so
// the panel only points there, plus About for the build string and Close to take
// the panel back out of the chat.
func (b *Bot) groupPanel(lang string) *tg.InlineKeyboardMarkup {
	var rows [][]tg.InlineKeyboardButton
	// The DM link needs the bot's own @username, which is resolved in the
	// background; until it lands the panel is still worth showing without it.
	if username := b.cachedBotUsername(); username != "" {
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.open_dm"), URL: "https://t.me/" + username, Style: tg.StylePrimary}})
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: i18n.T(lang, "btn.about"), CallbackData: "about"},
		{Text: i18n.T(lang, "btn.close"), CallbackData: "close", Style: tg.StyleDanger},
	})
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (b *Bot) handleEphemeralStart(ctx context.Context, msg tg.Message) error {
	// Telegram gives an ephemeral command a 15-second response window, so the
	// language lookup gets a short leash of its own: resolveLang falls back to
	// the Telegram hint on timeout, which is the right answer anyway when the
	// hub cannot be reached in time.
	langCtx, cancelLang := context.WithTimeout(ctx, 2*time.Second)
	lang := b.resolveLang(langCtx, msg.From)
	cancelLang()

	replyCtx, cancelReply := context.WithTimeout(ctx, 10*time.Second)
	_, err := b.client.SendEphemeralMessage(
		replyCtx,
		msg.Chat.ID,
		msg.From.ID,
		msg.EphemeralMessageID,
		groupPanelText(lang),
		b.groupPanel(lang),
	)
	cancelReply()
	if err != nil {
		return err
	}

	// The private response has already been delivered. Presence persistence is
	// best effort here so a slow or unavailable database cannot cause a duplicate
	// reply or consume Telegram's 15-second ephemeral response window.
	touchCtx, cancelTouch := context.WithTimeout(ctx, 2*time.Second)
	err = b.upsertUser(touchCtx, msg.From, &msg.Chat)
	cancelTouch()
	if err != nil && ctx.Err() == nil {
		slog.Warn("core touch failed after ephemeral start response", "user_id", msg.From.ID, "chat_id", msg.Chat.ID, "error", err)
	}
	return nil
}

func (b *Bot) handleMyChatMember(ctx context.Context, upd tg.ChatMemberUpdated) error {
	// upsertUser touches core.person (upd.From) and, since this is a non-private
	// chat, core.chat (upd.Chat identity) — both must exist before chat_state's
	// FK inserts below.
	if err := b.upsertUser(ctx, &upd.From, &upd.Chat); err != nil {
		return err
	}
	// Only groups/supergroups/channels are tracked in chat_state (its chat_id FKs
	// to core.chat, which upsertUser only populates for non-private chats). A
	// private my_chat_member (a user blocking/unblocking the bot in a DM) has no
	// group presence to record.
	if upd.Chat.Type == "private" {
		return nil
	}
	status := upd.NewChatMember.Status
	active := status == "member" || status == "administrator"
	return b.store.UpsertChatState(ctx, db.ChatState{
		ID:          upd.Chat.ID,
		BotStatus:   status,
		Active:      active,
		AddedByUser: upd.From.ID,
	})
}

func (b *Bot) handleCallback(ctx context.Context, cq tg.CallbackQuery) error {
	if err := b.upsertUser(ctx, &cq.From, &cq.Message.Chat); err != nil {
		return err
	}
	// Dispatch first, then answer the callback query exactly once with an
	// optional toast. Answering once (rather than a blank pre-answer) lets
	// confirmations surface as a toast while the underlying menu stays in place.
	lang := b.resolveLang(ctx, &cq.From)
	toast, err := b.dispatchCallback(ctx, cq, lang)
	if ackErr := b.client.AnswerCallbackQuery(ctx, cq.ID, toast, false); ackErr != nil {
		slog.Warn("answer callback failed", "error", ackErr)
	}
	return err
}

func (b *Bot) dispatchCallback(ctx context.Context, cq tg.CallbackQuery, lang string) (string, error) {
	switch cq.Data {
	case "home":
		return "", b.renderHome(ctx, cq, lang)
	case "about":
		return "", b.renderAbout(ctx, cq, lang)
	case "close":
		return "", b.closePanel(ctx, cq)
	case "lang":
		return "", b.renderLanguage(ctx, cq, lang)
	case "sub:list":
		return "", b.renderSubscriptionList(ctx, cq, lang)
	}
	if page, ok := parsePage(cq.Data, "repo:list"); ok {
		return "", b.renderRepoList(ctx, cq, lang, false, page)
	}
	if page, ok := parsePage(cq.Data, "sub:new"); ok {
		return "", b.renderRepoList(ctx, cq, lang, true, page)
	}
	if choice, ok := strings.CutPrefix(cq.Data, "lang:"); ok {
		return b.applyLanguage(ctx, cq, lang, choice)
	}
	if strings.HasPrefix(cq.Data, "m:") {
		return b.handlePRMore(ctx, cq, lang)
	}

	if !strings.HasPrefix(cq.Data, "t:") {
		return "", nil
	}
	tokenValue := strings.TrimPrefix(cq.Data, "t:")
	token, err := b.store.GetCallbackToken(ctx, cq.From.ID, tokenValue)
	if err != nil {
		return i18n.T(lang, "toast.action_expired"), b.renderHome(ctx, cq, lang)
	}
	if isConsumedAction(token.Action) {
		token, err = b.store.ConsumeCallbackToken(ctx, cq.From.ID, tokenValue)
		if err != nil {
			return i18n.T(lang, "toast.action_already_ran"), b.renderHome(ctx, cq, lang)
		}
	}
	return b.handleToken(ctx, cq, lang, token)
}

// parsePage matches a static callback prefix optionally suffixed with ":<page>".
func parsePage(data, prefix string) (int, bool) {
	if data == prefix {
		return 0, true
	}
	if rest, ok := strings.CutPrefix(data, prefix+":"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n >= 0 {
			return n, true
		}
	}
	return 0, false
}

const moreFilesTimeout = 8 * time.Second

func (b *Bot) handlePRMore(ctx context.Context, cq tg.CallbackQuery, lang string) (string, error) {
	expired := i18n.T(lang, "toast.snapshot_expired")
	compact := strings.TrimPrefix(cq.Data, "m:")
	jobID, ok := db.ExpandCompactUUID(compact)
	if !ok {
		return expired, nil
	}
	job, err := b.store.GetNotificationJobForChat(ctx, jobID, cq.Message.Chat.ID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return expired, nil
		}
		return "", err
	}
	if len(job.MoreJSON) == 0 {
		return expired, nil
	}
	var snapshot notify.PRMoreSnapshot
	if err := json.Unmarshal(job.MoreJSON, &snapshot); err != nil {
		return expired, nil
	}

	files, toast := b.loadPRMoreFiles(ctx, lang, job, snapshot)
	html := notify.PRMoreHTML(snapshot, files)
	if strings.TrimSpace(html) == "" {
		return expired, nil
	}
	callbackID := cq.ID
	if toast != "" {
		callbackID = ""
	}
	if err := b.client.SendEphemeralRichHTML(ctx, cq.Message.Chat.ID, cq.From.ID, callbackID, html); err != nil {
		return "", err
	}
	return toast, nil
}

func (b *Bot) loadPRMoreFiles(ctx context.Context, lang string, job db.NotificationJob, snapshot notify.PRMoreSnapshot) ([]notify.PRFile, string) {
	failed := i18n.T(lang, "toast.files_failed")
	if b.github == nil || b.sealer == nil || strings.TrimSpace(job.SubscriptionID) == "" {
		return nil, ""
	}
	if strings.TrimSpace(snapshot.RepoFullName) == "" || snapshot.Number <= 0 {
		return nil, failed
	}

	fetchCtx, cancel := context.WithTimeout(ctx, moreFilesTimeout)
	defer cancel()

	sub, err := b.store.GetSubscription(fetchCtx, job.SubscriptionID)
	if err != nil {
		slog.Warn("pr more subscription lookup failed", "error", err)
		return nil, failed
	}
	token, err := b.accessToken(fetchCtx, sub.TelegramUserID)
	if err != nil {
		slog.Warn("pr more token decrypt failed", "error", err)
		return nil, failed
	}
	raw, err := b.github.ListPullRequestFiles(fetchCtx, token, snapshot.RepoFullName, snapshot.Number)
	if err != nil {
		if github.IsAuthError(err) {
			return nil, i18n.T(lang, "toast.github_expired")
		}
		slog.Warn("pr more files fetch failed", "error", err)
		return nil, failed
	}
	return notifyPRFiles(raw), ""
}

func notifyPRFiles(files []github.PullRequestFile) []notify.PRFile {
	out := make([]notify.PRFile, 0, len(files))
	for _, file := range files {
		out = append(out, notify.PRFile{
			Filename:         file.Filename,
			PreviousFilename: file.PreviousFilename,
			Status:           file.Status,
			Additions:        file.Additions,
			Deletions:        file.Deletions,
			Changes:          file.Changes,
		})
	}
	return out
}

func (b *Bot) handleToken(ctx context.Context, cq tg.CallbackQuery, lang string, token db.CallbackToken) (string, error) {
	switch token.Action {
	case "repo.info":
		var payload repoPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderRepoInfo(ctx, cq, lang, payload.Repo)
	case "sub.repo":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderDestinationPicker(ctx, cq, lang, draft, false, "")
	case "sub.dest":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, draft.DestinationChatID, cq.From.ID); err != nil {
				return b.groupAdminFailure(ctx, cq, lang, err, func() error {
					return b.renderDestinationPicker(ctx, cq, lang, draft, false, "")
				})
			}
		}
		return "", b.renderEventPicker(ctx, cq, lang, draft)
	case "sub.events.toggle":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.ToggleEvent != "" {
			draft.Events = toggleEvent(draft.Events, draft.ToggleEvent)
			draft.ToggleEvent = ""
		}
		return "", b.renderEventPicker(ctx, cq, lang, draft)
	case "sub.settings":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderEventSettings(ctx, cq, lang, draft)
	case "sub.settings.branch":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderBranchSettings(ctx, cq, lang, draft)
	case "sub.settings.branch.mode":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.BranchMode == "selected" {
			return "", b.renderBranchList(ctx, cq, lang, draft)
		}
		draft.BranchNames = nil
		return "", b.renderEventSettings(ctx, cq, lang, draft)
	case "sub.settings.branch.list":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderBranchList(ctx, cq, lang, draft)
	case "sub.settings.branch.toggle":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.ToggleBranchName != "" {
			draft.BranchNames = toggleString(draft.BranchNames, draft.ToggleBranchName)
			draft.ToggleBranchName = ""
		}
		return "", b.renderBranchList(ctx, cq, lang, draft)
	case "sub.settings.pr":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderPullRequestSettings(ctx, cq, lang, draft)
	case "sub.settings.pr.toggle":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.TogglePullRequestAction != "" {
			draft.PullRequestActions = togglePullRequestAction(draft.PullRequestActions, draft.TogglePullRequestAction)
			draft.TogglePullRequestAction = ""
		}
		return "", b.renderPullRequestSettings(ctx, cq, lang, draft)
	case "sub.settings.release":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderReleaseSettings(ctx, cq, lang, draft)
	case "sub.settings.release.mode":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderEventSettings(ctx, cq, lang, draft)
	case "sub.create":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return b.createSubscription(ctx, cq, lang, draft)
	case "sub.branch":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.BranchName != "" && len(draft.BranchNames) == 0 {
			draft.BranchNames = []string{draft.BranchName}
		}
		if draft.BranchMode == "selected" && len(draft.BranchNames) == 0 {
			return "", b.renderBranchList(ctx, cq, lang, draft)
		}
		return b.createSubscription(ctx, cq, lang, draft)
	case "sub.branch.selected":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.BranchName != "" && len(draft.BranchNames) == 0 {
			draft.BranchNames = []string{draft.BranchName}
		}
		return b.createSubscription(ctx, cq, lang, draft)
	case "sub.view":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderSubscription(ctx, cq, lang, payload.ID)
	case "sub.status":
		var payload statusPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetStatus(ctx, cq.From.ID, payload.ID, payload.Status); err != nil {
			return "", b.githubError(ctx, cq, lang, err, "err.action.update_subscription")
		}
		toast := i18n.T(lang, "toast.sub_paused")
		if payload.Status == "active" {
			toast = i18n.T(lang, "toast.sub_resumed")
		}
		return toast, b.renderSubscription(ctx, cq, lang, payload.ID)
	case "sub.delete.confirm":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderDeleteConfirm(ctx, cq, lang, payload.ID)
	case "sub.delete":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.Delete(ctx, cq.From.ID, payload.ID); err != nil {
			return "", b.githubError(ctx, cq, lang, err, "err.action.delete_subscription")
		}
		return i18n.T(lang, "toast.sub_deleted"), b.renderSubscriptionList(ctx, cq, lang)
	case "sub.test":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SendTest(ctx, b.client, cq.From.ID, payload.ID); err != nil {
			// Keep the user on the subscription screen and surface the failure as
			// a toast, mirroring the success path, instead of ejecting them home.
			return b.userMessage(err, lang, "err.action.send_test"), b.renderSubscription(ctx, cq, lang, payload.ID)
		}
		return i18n.T(lang, "toast.test_sent"), b.renderSubscription(ctx, cq, lang, payload.ID)
	case "sub.edit.events":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditEvents(ctx, cq, lang, payload.ID, payload.Events)
	case "sub.edit.events.toggle":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.ToggleEvent != "" {
			payload.Events = toggleEvent(payload.Events, payload.ToggleEvent)
			payload.ToggleEvent = ""
		}
		return "", b.renderEditEvents(ctx, cq, lang, payload.ID, payload.Events)
	case "sub.edit.events.save":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetEvents(ctx, cq.From.ID, payload.ID, payload.Events); err != nil {
			return "", b.githubError(ctx, cq, lang, err, "err.action.update_events")
		}
		return i18n.T(lang, "toast.events_updated"), b.renderAdvancedSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.settings":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderAdvancedSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.menu":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderSubscriptionEditMenu(ctx, cq, lang, payload.ID)
	case "sub.edit.branch":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditBranch(ctx, cq, lang, payload.ID)
	case "sub.edit.branch.save":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.BranchName != "" && len(payload.BranchNames) == 0 {
			payload.BranchNames = []string{payload.BranchName}
		}
		if err := b.subs.SetBranch(ctx, cq.From.ID, payload.ID, payload.BranchMode, payload.BranchNames); err != nil {
			return "", b.respond(ctx, cq, errorPanel(lang, b.userMessage(err, lang, "err.action.update_branch")), backHome(lang, cq.Message.Chat))
		}
		return i18n.T(lang, "toast.branch_updated"), b.renderAdvancedSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.branch.selected":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditBranchList(ctx, cq, lang, payload)
	case "sub.edit.branch.toggle":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.ToggleBranchName != "" {
			payload.BranchNames = toggleString(payload.BranchNames, payload.ToggleBranchName)
			payload.ToggleBranchName = ""
		}
		return "", b.renderEditBranchList(ctx, cq, lang, payload)
	case "sub.edit.pr":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditPullRequestSettings(ctx, cq, lang, payload.ID, nil)
	case "sub.edit.pr.toggle":
		var payload editPullRequestPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.ToggleAction != "" {
			payload.Actions = togglePullRequestAction(payload.Actions, payload.ToggleAction)
			payload.ToggleAction = ""
		}
		return "", b.renderEditPullRequestSettings(ctx, cq, lang, payload.ID, payload.Actions)
	case "sub.edit.pr.save":
		var payload editPullRequestPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetPullRequestActions(ctx, cq.From.ID, payload.ID, payload.Actions); err != nil {
			return "", b.respond(ctx, cq, errorPanel(lang, b.userMessage(err, lang, "err.action.update_pr")), backHome(lang, cq.Message.Chat))
		}
		return i18n.T(lang, "toast.pr_updated"), b.renderAdvancedSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.release":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditReleaseSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.release.save":
		var payload editReleasePayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetReleaseMode(ctx, cq.From.ID, payload.ID, payload.ReleaseMode); err != nil {
			return "", b.respond(ctx, cq, errorPanel(lang, b.userMessage(err, lang, "err.action.update_release")), backHome(lang, cq.Message.Chat))
		}
		return i18n.T(lang, "toast.release_updated"), b.renderAdvancedSettings(ctx, cq, lang, payload.ID)
	case "sub.edit.dest":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderDestinationPicker(ctx, cq, lang, subDraft{EditSubscriptionID: payload.ID}, true, payload.ID)
	case "sub.edit.dest.save":
		var payload editDestinationPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, payload.DestinationChatID, cq.From.ID); err != nil {
				return b.groupAdminFailure(ctx, cq, lang, err, func() error {
					return b.renderDestinationPicker(ctx, cq, lang, subDraft{EditSubscriptionID: payload.ID}, true, payload.ID)
				})
			}
		}
		if err := b.subs.SetDestination(ctx, cq.From.ID, payload.ID, payload.DestinationType, payload.DestinationChatID); err != nil {
			return "", b.respond(ctx, cq, errorPanel(lang, b.userMessage(err, lang, "err.action.update_destination")), backHome(lang, cq.Message.Chat))
		}
		return i18n.T(lang, "toast.dest_updated"), b.renderSubscription(ctx, cq, lang, payload.ID)
	}
	return "", nil
}

func (b *Bot) mainMenu(ctx context.Context, telegramUserID int64, lang string) (string, *tg.InlineKeyboardMarkup, error) {
	connectURL, err := b.oauth.CreateAuthURL(ctx, telegramUserID)
	if err != nil {
		return "", nil, err
	}
	connected := false
	var lines []string
	if conn, err := b.store.GetGitHubConnection(ctx, telegramUserID); err == nil {
		connected = true
		lines = append(lines, i18n.T(lang, "home.connected", "login", esc(conn.GitHubLogin)))
	} else {
		lines = append(lines, i18n.T(lang, "home.not_connected"))
	}
	connectLabel := i18n.T(lang, "btn.connect")
	if connected {
		connectLabel = i18n.T(lang, "btn.reconnect")
		subs, err := b.store.ListSubscriptionsByUser(ctx, telegramUserID)
		if err != nil {
			return "", nil, err
		}
		switch len(subs) {
		case 0:
			lines = append(lines, i18n.T(lang, "home.subs.none"))
		case 1:
			lines = append(lines, i18n.T(lang, "home.subs.one"))
		default:
			lines = append(lines, i18n.T(lang, "home.subs.many", "count", strconv.Itoa(len(subs))))
		}
	} else {
		lines = append(lines, i18n.T(lang, "home.connect_hint"))
	}
	// When disconnected, connecting is the one call to action; once connected it
	// becomes a secondary "Reconnect" and "New subscription" is the accent.
	connectButton := tg.InlineKeyboardButton{Text: connectLabel, URL: connectURL}
	if !connected {
		connectButton.Style = tg.StylePrimary
	}
	rows := [][]tg.InlineKeyboardButton{{connectButton}}
	// The other actions all require a GitHub connection, so only offer them
	// once connected; until then the menu is just the connect button.
	if connected {
		rows = append(rows,
			[]tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.repositories"), CallbackData: "repo:list"}, {Text: i18n.T(lang, "btn.subscriptions"), CallbackData: "sub:list"}},
			[]tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.sub_new"), CallbackData: "sub:new", Style: tg.StylePrimary}},
		)
	}
	// Language and About sit last: they answer questions rather than doing
	// anything, so they should not compete with the call to action above them.
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: i18n.T(lang, "btn.language"), CallbackData: "lang"},
		{Text: i18n.T(lang, "btn.about"), CallbackData: "about"},
	})
	return panel(i18n.T(lang, "home.title"), "", i18n.T(lang, "home.hint"), strings.Join(lines, "\n")), &tg.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (b *Bot) renderHome(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	// A callback arriving from a group belongs to the group panel. Rebuilding
	// the DM menu here would put one person's GitHub login and subscription
	// count into a chat they share with everyone else, so the group gets the
	// group panel back instead.
	if inGroup(cq.Message.Chat) {
		return b.respond(ctx, cq, groupPanelText(lang), b.groupPanel(lang))
	}
	text, markup, err := b.mainMenu(ctx, cq.From.ID, lang)
	if err != nil {
		return err
	}
	return b.respond(ctx, cq, text, markup)
}

// renderAbout states what Branchy is and which build is answering.
func (b *Bot) renderAbout(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	rows := [][]tg.InlineKeyboardButton{panelFooter(lang, cq.Message.Chat, "home")}
	return b.respond(ctx, cq, aboutText(lang, b.buildVersion()), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// renderLanguage is the family's language screen: every language the fleet
// shares, two to a row, flag and native name. Which one is current lives on the
// buttons and nowhere else — a list in the body would say a second time what
// sixteen marked buttons already say, and the two would eventually disagree.
//
// "Follow Telegram" withdraws Branchy's claim in the shared hub rather than
// setting English, so the Telegram client's own language_code decides again.
// It is a different thing from picking English, and a person who has never
// chosen a language should be able to get back to it.
func (b *Bot) renderLanguage(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	options := i18n.LANGUAGE_OPTIONS
	rows := make([][]tg.InlineKeyboardButton, 0, len(options)/2+2)
	for i := 0; i < len(options); i += 2 {
		row := []tg.InlineKeyboardButton{languageButton(options[i], lang)}
		if i+1 < len(options) {
			row = append(row, languageButton(options[i+1], lang))
		}
		rows = append(rows, row)
	}
	rows = append(rows,
		[]tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.follow_telegram"), CallbackData: "lang:follow"}},
		panelFooter(lang, cq.Message.Chat, "home"),
	)
	text := panel(i18n.T(lang, "lang.title"), "", i18n.T(lang, "lang.hint"), "")
	return b.respond(ctx, cq, text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// languageButton marks every option so the column has one left edge, and styles
// only the current one: in a grid of sixteen, one coloured button answers "which
// am I on?" before the glyph is read.
func languageButton(option i18n.LangOption, lang string) tg.InlineKeyboardButton {
	button := tg.InlineKeyboardButton{
		Text:         radio(option.Code == lang, option.Label),
		CallbackData: "lang:" + option.Code,
	}
	if option.Code == lang {
		button.Style = tg.StyleSuccess
	}
	return button
}

// applyLanguage records or withdraws the choice in the shared hub, then redraws
// the screen in whatever language now answers — so the confirmation of a switch
// to Ukrainian is itself in Ukrainian.
func (b *Bot) applyLanguage(ctx context.Context, cq tg.CallbackQuery, lang, choice string) (string, error) {
	if choice == "follow" {
		if err := b.store.ClearLanguage(ctx, cq.From.ID); err != nil {
			slog.Error("clear language failed", "user_id", cq.From.ID, "error", err)
			return "", b.languageFailure(ctx, cq, lang)
		}
		// Ask the hub what answers now rather than assuming the client hint.
		// clear_language withdraws Branchy's claim and nobody else's, so a
		// sibling bot's manual choice survives it and then wins — and this
		// screen has to be drawn in the language the person is about to read,
		// not the one this bot would have picked on its own.
		lang = b.resolveLang(ctx, &cq.From)
		return i18n.T(lang, "toast.lang_follow"), b.renderLanguage(ctx, cq, lang)
	}
	code := i18n.Normalize(choice)
	if !i18n.IsSupported(code) {
		// Callback data can be anything a client cares to send; an unknown code
		// redraws rather than writing a language Branchy cannot render.
		return "", b.renderLanguage(ctx, cq, lang)
	}
	if err := b.store.SetLanguage(ctx, cq.From.ID, code); err != nil {
		slog.Error("set language failed", "user_id", cq.From.ID, "error", err)
		return "", b.languageFailure(ctx, cq, lang)
	}
	return i18n.T(code, "toast.lang_set"), b.renderLanguage(ctx, cq, code)
}

// languageFailure keeps a hub write failure on the same footing as every other
// failure: the standard error panel, still in the language that was answering
// before the write was attempted.
func (b *Bot) languageFailure(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	message := i18n.T(lang, "err.generic", "action", i18n.T(lang, "err.action.set_language"))
	return b.respond(ctx, cq, errorPanel(lang, message), backHome(lang, cq.Message.Chat))
}

// buildVersion reports exactly what /healthz reports. A binary built without the
// release ldflags has no version to state, and "dev" is the honest answer.
func (b *Bot) buildVersion() string {
	if b.version == "" {
		return "dev"
	}
	return b.version
}

// closePanel takes the group panel back out of the chat.
//
// Only an ephemeral message is closed. Telegram lets a client send any callback
// data for any message it can see, not only the buttons it was shown, so
// honouring "close" against a public message would let anyone in a group delete
// Branchy's notification cards. An ephemeral message is already visible to one
// person, so deleting it at that person's request destroys nothing that was not
// theirs to begin with.
func (b *Bot) closePanel(ctx context.Context, cq tg.CallbackQuery) error {
	if cq.Message.EphemeralMessageID == 0 {
		return nil
	}
	return b.client.DeleteEphemeralMessage(ctx, cq.Message.Chat.ID, cq.From.ID, cq.Message.EphemeralMessageID)
}

// inGroup reports whether a callback came from a shared chat. An unknown chat
// type counts as a DM: the group-only affordances all cost something when shown
// in the wrong place, so absence of evidence should not offer them.
func inGroup(chat tg.Chat) bool {
	return chat.Type != "" && chat.Type != "private"
}

// panelFooter is the navigation row a panel ends with. Close appears only in
// groups, where the panel sits in a feed shared with people who never asked for
// it and whoever summoned it needs a way to withdraw it. A DM has nothing to
// close: there the conversation is the panel. It is painted destructive because
// that is what it does — the panel goes away, and nothing brings that instance
// of it back.
func panelFooter(lang string, chat tg.Chat, backCallback string) []tg.InlineKeyboardButton {
	row := []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.back"), CallbackData: backCallback}}
	if inGroup(chat) {
		row = append(row, tg.InlineKeyboardButton{Text: i18n.T(lang, "btn.close"), CallbackData: "close", Style: tg.StyleDanger})
	}
	return row
}

// errorPanel is the shape a failure gets: the same title-hint-quote panel as
// every other screen, with the specific failure as its substance. An error is
// still a screen, and giving it a different shape is what makes a bot feel like
// it was assembled by several people.
func errorPanel(lang, message string) string {
	return panel(i18n.T(lang, "err.title"), "", "", message)
}

// currentOption is the choice a single-select screen is already on: disabled,
// because tapping it would change nothing, and Success, because Success is the
// family's word for "this is the state you are in". It is the same treatment
// the language grid gives the current language, and for the same reason — one
// coloured button in a column answers "which am I on?" at a glance.
func currentOption(label string) tg.InlineKeyboardButton {
	button := disabledButton(label)
	button.Style = tg.StyleSuccess
	return button
}

func (b *Bot) renderRepoList(ctx context.Context, cq tg.CallbackQuery, lang string, subscribeMode bool, page int) error {
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, notConnectedPanel(lang), backHome(lang, cq.Message.Chat))
	}
	repos, err := b.github.ListRepositories(ctx, token)
	if err != nil {
		return b.githubError(ctx, cq, lang, err, "err.action.list_repositories")
	}
	repos = visibleRepositories(repos, subscribeMode)
	if len(repos) == 0 {
		hintKey := "repo.empty.hint"
		if subscribeMode {
			hintKey = "repo.empty.subscribe.hint"
		}
		return b.respond(ctx, cq, panel(i18n.T(lang, "repo.empty.title"), "", i18n.T(lang, hintKey), ""), backHome(lang, cq.Message.Chat))
	}

	prefix := "repo:list"
	titleKey, hintKey := "repo.list.title", "repo.list.hint"
	if subscribeMode {
		prefix = "sub:new"
		titleKey, hintKey = "repo.choose.title", "repo.choose.hint"
	}

	pages := (len(repos) + repoPageSize - 1) / repoPageSize
	if page >= pages {
		page = pages - 1
	}
	start := page * repoPageSize
	end := min(start+repoPageSize, len(repos))

	rows := [][]tg.InlineKeyboardButton{}
	for _, repo := range repos[start:end] {
		action := "repo.info"
		if subscribeMode {
			action = "sub.repo"
		}
		text := repo.FullName
		if !subscribeMode && !repo.HasAdminPermission {
			text = text + "  ·  " + i18n.T(lang, "repo.no_access")
		}
		callback, err := b.token(ctx, cq.From.ID, action, repoPayload{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: text, CallbackData: callback}})
	}
	if nav := paginationRow(lang, prefix, page, pages); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, "home"))
	return b.respond(ctx, cq, panel(i18n.T(lang, titleKey), "", i18n.T(lang, hintKey), pageNote(lang, page, pages)), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderRepoInfo(ctx context.Context, cq tg.CallbackQuery, lang string, repo github.Repository) error {
	rows := [][]tg.InlineKeyboardButton{}
	if repo.HasAdminPermission && !repo.Archived {
		callback, err := b.token(ctx, cq.From.ID, "sub.repo", subDraft{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.sub_create"), CallbackData: callback, Style: tg.StylePrimary}})
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, "repo:list"))
	body := i18n.T(lang, "repo.default_branch", "branch", esc(repo.DefaultBranch))
	if repo.Archived {
		body += "\n" + i18n.T(lang, "repo.archived")
	} else if !repo.HasAdminPermission {
		body += "\n" + i18n.T(lang, "repo.no_admin")
	}
	return b.respond(ctx, cq, panel(esc(repo.FullName), "", i18n.T(lang, "repo.info.hint"), body), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func visibleRepositories(repos []github.Repository, subscribeMode bool) []github.Repository {
	filtered := repos[:0:0]
	for _, repo := range repos {
		if repo.Archived {
			continue
		}
		if subscribeMode && !repo.HasAdminPermission {
			continue
		}
		filtered = append(filtered, repo)
	}
	// Keep subscribable (admin) repositories first so the read-only ones the user
	// cannot act on sink to the bottom of the list. Stable to preserve GitHub's
	// ordering within each group. (In subscribe mode every repo is admin, so this
	// is a no-op there.)
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].HasAdminPermission && !filtered[j].HasAdminPermission
	})
	return filtered
}

func (b *Bot) renderDestinationPicker(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft, edit bool, editID string) error {
	rows := [][]tg.InlineKeyboardButton{}
	if edit {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.dest.save", editDestinationPayload{ID: editID, DestinationType: "dm", DestinationChatID: cq.From.ID})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "dest.dm"), CallbackData: callback}})
	} else {
		draft.DestinationType = "dm"
		draft.DestinationChatID = cq.From.ID
		callback, err := b.token(ctx, cq.From.ID, "sub.dest", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "dest.dm"), CallbackData: callback}})
	}

	groups, err := b.store.ListKnownGroups(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	for _, group := range groups {
		label := group.Title
		if label == "" {
			label = groupFallbackLabel(lang, group.ID)
		}
		if edit {
			callback, err := b.token(ctx, cq.From.ID, "sub.edit.dest.save", editDestinationPayload{ID: editID, DestinationType: "group", DestinationChatID: group.ID})
			if err != nil {
				return err
			}
			rows = append(rows, []tg.InlineKeyboardButton{{Text: label, CallbackData: callback}})
			continue
		}
		next := draft
		next.DestinationType = "group"
		next.DestinationChatID = group.ID
		callback, err := b.token(ctx, cq.From.ID, "sub.dest", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	backCB, err := b.stepBackCallback(ctx, cq.From.ID, edit, editID, "sub:new")
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "dest.title"), "", i18n.T(lang, "dest.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEventPicker(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = withDraftDefaults(draft)
	rows := [][]tg.InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		next := draft
		next.ToggleEvent = event
		callback, err := b.token(ctx, cq.From.ID, "sub.events.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(draft.Events, event), eventLabel(lang, event)), CallbackData: callback}})
	}
	if len(draft.Events) > 0 {
		callback, err := b.token(ctx, cq.From.ID, "sub.settings", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.continue"), CallbackData: callback, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.continue"))})
	}
	// Back returns to the destination step, preserving the draft.
	backCB, err := b.token(ctx, cq.From.ID, "sub.repo", draft)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.events.title"), "", i18n.T(lang, "sub.events.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEventSettings(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = normalizeDraftForEvents(draft)
	rows := [][]tg.InlineKeyboardButton{}
	if usesBranchFilter(draft.Events) {
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.branch", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.branch_filter"), CallbackData: callback}})
	}
	if contains(draft.Events, "pull_request") {
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.pr", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.pr_actions"), CallbackData: callback}})
	}
	if contains(draft.Events, "release") {
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.release", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.release_settings"), CallbackData: callback}})
	}
	backCB, err := b.token(ctx, cq.From.ID, "sub.dest", draft)
	if err != nil {
		return err
	}
	if settingsReady(draft) {
		createCB, err := b.token(ctx, cq.From.ID, "sub.create", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.sub_create"), CallbackData: createCB, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.sub_create"))})
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	body := settingsSummary(lang, draft.Events, draft.BranchMode, draft.BranchNames, draft.PullRequestActions, draft.ReleaseMode)
	if hint := settingsBlockingHint(lang, draft); hint != "" {
		body += "\n\n⚠ " + hint
	}
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.settings.title"), "", i18n.T(lang, "sub.settings.hint"), body), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// settingsBlockingHint explains why "Create subscription" stays disabled,
// so a greyed-out button never looks like a dead end. Returns "" when ready.
func settingsBlockingHint(lang string, draft subDraft) string {
	if settingsReady(draft) {
		return ""
	}
	if usesBranchFilter(draft.Events) && draft.BranchMode == "selected" && len(draft.BranchNames) == 0 {
		return i18n.T(lang, "sub.settings.need_branch")
	}
	if contains(draft.Events, "pull_request") && len(draft.PullRequestActions) == 0 {
		return i18n.T(lang, "sub.settings.need_pr")
	}
	return i18n.T(lang, "sub.settings.need_more")
}

func (b *Bot) renderBranchSettings(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = normalizeDraftForEvents(draft)
	rows := [][]tg.InlineKeyboardButton{}
	for _, mode := range []string{"all", "default", "selected"} {
		next := draft
		next.BranchMode = mode
		if mode != "selected" {
			next.BranchNames = nil
		}
		if draft.BranchMode == mode && mode != "selected" {
			rows = append(rows, []tg.InlineKeyboardButton{currentOption(radio(true, branchModeLabel(lang, mode, draft.BranchNames)))})
			continue
		}
		action := "sub.settings.branch.mode"
		callback, err := b.token(ctx, cq.From.ID, action, next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: radio(draft.BranchMode == mode, branchModeLabel(lang, mode, draft.BranchNames)), CallbackData: callback}})
	}
	backCB, err := b.token(ctx, cq.From.ID, "sub.settings", draft)
	if err != nil {
		return err
	}
	// Back, not Done: a branch mode is always set, this screen saves nothing of
	// its own, and the settings hub it returns to is the screen the user came
	// from. Its twin — release notifications — has always said Back.
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "branch.title"), "", i18n.T(lang, "branch.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderBranchList(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = normalizeDraftForEvents(draft)
	draft.BranchMode = "selected"
	draft.BranchNames = db.NormalizeBranchNames(draft.BranchNames)
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, notConnectedPanel(lang), backHome(lang, cq.Message.Chat))
	}
	branches, err := b.github.ListBranches(ctx, token, draft.Repo.FullName)
	if err != nil {
		return b.githubError(ctx, cq, lang, err, "err.action.list_branches")
	}
	backCB, err := b.token(ctx, cq.From.ID, "sub.settings.branch", draft)
	if err != nil {
		return err
	}
	backRow := panelFooter(lang, cq.Message.Chat, backCB)

	if len(branches) == 0 {
		allDraft := draft
		allDraft.BranchMode = "all"
		allDraft.BranchNames = nil
		allCB, err := b.token(ctx, cq.From.ID, "sub.settings.branch.mode", allDraft)
		if err != nil {
			return err
		}
		rows := [][]tg.InlineKeyboardButton{
			{{Text: i18n.T(lang, "btn.use_all_branches"), CallbackData: allCB, Style: tg.StylePrimary}},
			backRow,
		}
		return b.respond(ctx, cq, panel(i18n.T(lang, "branch.choose.title"), "", i18n.T(lang, "branch.choose.hint"), i18n.T(lang, "branch.empty")), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
	}

	pages := (len(branches) + branchPageSize - 1) / branchPageSize
	page := clampPage(draft.BranchPage, pages)
	start := page * branchPageSize
	end := min(start+branchPageSize, len(branches))

	rows := [][]tg.InlineKeyboardButton{}
	for _, branch := range branches[start:end] {
		next := draft
		next.BranchMode = "selected"
		next.ToggleBranchName = branch.Name
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.branch.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(draft.BranchNames, branch.Name), branch.Name), CallbackData: callback}})
	}
	if len(draft.BranchNames) > 0 {
		doneCB, err := b.token(ctx, cq.From.ID, "sub.settings", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.done"), CallbackData: doneCB, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.done"))})
	}
	var nav []tg.InlineKeyboardButton
	if pages > 1 {
		if page > 0 {
			button, err := b.branchNavButton(ctx, cq.From.ID, draft, i18n.T(lang, "btn.prev"), page-1, "sub.settings.branch.list")
			if err != nil {
				return err
			}
			nav = append(nav, button)
		} else {
			nav = append(nav, disabledButton(i18n.T(lang, "btn.prev")))
		}
		if page < pages-1 {
			button, err := b.branchNavButton(ctx, cq.From.ID, draft, i18n.T(lang, "btn.next"), page+1, "sub.settings.branch.list")
			if err != nil {
				return err
			}
			nav = append(nav, button)
		} else {
			nav = append(nav, disabledButton(i18n.T(lang, "btn.next")))
		}
		rows = append(rows, nav)
	}
	rows = append(rows, backRow)
	return b.respond(ctx, cq, panel(i18n.T(lang, "branch.choose.title"), "", i18n.T(lang, "branch.choose.hint"), pageNote(lang, page, pages)), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderPullRequestSettings(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = normalizeDraftForEvents(draft)
	rows := [][]tg.InlineKeyboardButton{}
	for _, action := range pullRequestActionOrder() {
		next := draft
		next.TogglePullRequestAction = action
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.pr.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(draft.PullRequestActions, action), pullRequestActionLabel(lang, action)), CallbackData: callback}})
	}
	// In the draft flow toggles already persist into the draft, so one button is
	// enough and it is a Back: it returns to the settings hub the user came
	// from, and nothing on this screen was committed for a "Done" to confirm.
	// The hub explains why Create stays disabled when no action is selected.
	backCB, err := b.token(ctx, cq.From.ID, "sub.settings", draft)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "pr.title"), "", i18n.T(lang, "pr.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderReleaseSettings(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) error {
	draft = normalizeDraftForEvents(draft)
	rows := [][]tg.InlineKeyboardButton{}
	for _, mode := range releaseModeOrder() {
		next := draft
		next.ReleaseMode = mode
		if draft.ReleaseMode == mode {
			rows = append(rows, []tg.InlineKeyboardButton{currentOption(radio(true, releaseModeLabel(lang, mode)))})
			continue
		}
		callback, err := b.token(ctx, cq.From.ID, "sub.settings.release.mode", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: radio(false, releaseModeLabel(lang, mode)), CallbackData: callback}})
	}
	backCB, err := b.token(ctx, cq.From.ID, "sub.settings", draft)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "release.title"), "", i18n.T(lang, "release.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) createSubscription(ctx context.Context, cq tg.CallbackQuery, lang string, draft subDraft) (string, error) {
	draft = normalizeDraftForEvents(draft)
	id, err := b.subs.Create(ctx, cq.From.ID, draft.Repo, draft.DestinationType, draft.DestinationChatID, draft.Events, draft.BranchMode, draft.BranchNames, draft.PullRequestActions, draft.ReleaseMode)
	if err != nil {
		return "", b.githubError(ctx, cq, lang, err, "err.action.create_subscription")
	}
	return i18n.T(lang, "toast.sub_created"), b.renderSubscription(ctx, cq, lang, id)
}

func (b *Bot) renderSubscriptionList(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	subs, err := b.store.ListSubscriptionsByUser(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return b.respond(ctx, cq, panel(i18n.T(lang, "sub.list.empty.title"), "", i18n.T(lang, "sub.list.empty.hint"), ""), backHome(lang, cq.Message.Chat))
	}
	rows := [][]tg.InlineKeyboardButton{}
	for _, sub := range subs {
		callback, err := b.token(ctx, cq.From.ID, "sub.view", subscriptionPayload{ID: sub.ID})
		if err != nil {
			return err
		}
		label := sub.RepoFullName
		if sub.Status == "paused" {
			label += "  ·  " + i18n.T(lang, "sub.list.paused")
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, "home"))
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.list.title"), "", i18n.T(lang, "sub.list.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderSubscription(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	rows := [][]tg.InlineKeyboardButton{}
	nextStatus := "paused"
	statusLabel := i18n.T(lang, "btn.pause")
	if sub.Status == "paused" {
		nextStatus = "active"
		statusLabel = i18n.T(lang, "btn.resume")
	}
	statusCB, err := b.token(ctx, cq.From.ID, "sub.status", statusPayload{ID: sub.ID, Status: nextStatus})
	if err != nil {
		return err
	}
	testCB, err := b.token(ctx, cq.From.ID, "sub.test", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	editMenuCB, err := b.token(ctx, cq.From.ID, "sub.edit.menu", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	deleteCB, err := b.token(ctx, cq.From.ID, "sub.delete.confirm", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	rows = append(rows,
		[]tg.InlineKeyboardButton{{Text: statusLabel, CallbackData: statusCB}, {Text: i18n.T(lang, "btn.test"), CallbackData: testCB}},
		[]tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.edit"), CallbackData: editMenuCB, Style: tg.StylePrimary}},
		[]tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.delete"), CallbackData: deleteCB, Style: tg.StyleDanger}},
		panelFooter(lang, cq.Message.Chat, "sub:list"),
	)
	destLabel, destWarning := b.describeDestination(ctx, lang, cq.From.ID, sub)
	body := strings.Join([]string{
		i18n.T(lang, "sub.field.status", "value", esc(statusText(lang, sub.Status))),
		i18n.T(lang, "sub.field.destination", "value", esc(destLabel)),
		settingsSummary(lang, sub.Events, sub.BranchMode, subscriptionBranchNames(sub), sub.PullRequestActions, sub.ReleaseMode),
	}, "\n")
	if note := pauseReasonNote(lang, sub.PauseReason); note != "" {
		body += "\n⚠ " + note
	}
	if destWarning != "" {
		body += "\n⚠ " + destWarning
	}
	return b.respond(ctx, cq, panel(esc(sub.RepoFullName), "", i18n.T(lang, "sub.view.hint"), body), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// renderDeleteConfirm asks for explicit confirmation before deleting a
// subscription: the destructive action is one fat-finger tap away on the detail
// screen, so it gets a dedicated yes/no hop. "Delete" issues a fresh consumed
// sub.delete token; the footer's "Back" returns to the subscription. Back, not
// "Cancel": the family uses one word for going up, whatever the screen.
func (b *Bot) renderDeleteConfirm(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	deleteCB, err := b.token(ctx, cq.From.ID, "sub.delete", subscriptionPayload{ID: id})
	if err != nil {
		return err
	}
	cancelCB, err := b.token(ctx, cq.From.ID, "sub.view", subscriptionPayload{ID: id})
	if err != nil {
		return err
	}
	rows := [][]tg.InlineKeyboardButton{
		{{Text: i18n.T(lang, "btn.delete"), CallbackData: deleteCB, Style: tg.StyleDanger}},
		panelFooter(lang, cq.Message.Chat, cancelCB),
	}
	text := panel(i18n.T(lang, "sub.delete.title"), "", i18n.T(lang, "sub.delete.body"), esc(sub.RepoFullName))
	return b.respond(ctx, cq, text, &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderSubscriptionEditMenu(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	editEventsCB, err := b.token(ctx, cq.From.ID, "sub.edit.events", editEventsPayload{ID: sub.ID, Events: sub.Events})
	if err != nil {
		return err
	}
	advancedCB, err := b.token(ctx, cq.From.ID, "sub.edit.settings", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	editDestCB, err := b.token(ctx, cq.From.ID, "sub.edit.dest", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	backCB, err := b.viewCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows := [][]tg.InlineKeyboardButton{
		{{Text: i18n.T(lang, "btn.events"), CallbackData: editEventsCB}},
		{{Text: i18n.T(lang, "btn.destination"), CallbackData: editDestCB}},
		{{Text: i18n.T(lang, "btn.advanced"), CallbackData: advancedCB}},
		panelFooter(lang, cq.Message.Chat, backCB),
	}
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.edit.title"), "", i18n.T(lang, "sub.edit.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditEvents(ctx context.Context, cq tg.CallbackQuery, lang string, id string, events []string) error {
	rows := [][]tg.InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		payload := editEventsPayload{ID: id, Events: events, ToggleEvent: event}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.toggle", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(events, event), eventLabel(lang, event)), CallbackData: callback}})
	}
	if len(events) > 0 {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.save", editEventsPayload{ID: id, Events: events})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.save"), CallbackData: callback, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.save"))})
	}
	backCB, err := b.editMenuCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.edit.events.title"), "", i18n.T(lang, "sub.edit.events.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderAdvancedSettings(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	rows := [][]tg.InlineKeyboardButton{}
	if usesBranchFilter(sub.Events) {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch", subscriptionPayload{ID: id})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.branch_filter"), CallbackData: callback}})
	}
	if contains(sub.Events, "pull_request") {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.pr", subscriptionPayload{ID: id})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.pr_actions"), CallbackData: callback}})
	}
	if contains(sub.Events, "release") {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.release", subscriptionPayload{ID: id})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.release_settings"), CallbackData: callback}})
	}
	backCB, err := b.editMenuCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	body := settingsSummary(lang, sub.Events, sub.BranchMode, subscriptionBranchNames(sub), sub.PullRequestActions, sub.ReleaseMode)
	return b.respond(ctx, cq, panel(i18n.T(lang, "sub.advanced.title"), "", i18n.T(lang, "sub.advanced.hint"), body), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditBranch(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	if !usesBranchFilter(sub.Events) {
		return b.renderAdvancedSettings(ctx, cq, lang, id)
	}
	currentBranches := subscriptionBranchNames(sub)
	rows := [][]tg.InlineKeyboardButton{}
	for _, mode := range []string{"all", "default", "selected"} {
		action := "sub.edit.branch.save"
		payload := editBranchPayload{ID: id, BranchMode: mode}
		if mode == "selected" {
			action = "sub.edit.branch.selected"
			payload.BranchNames = currentBranches
		}
		if sub.BranchMode == mode && mode != "selected" {
			rows = append(rows, []tg.InlineKeyboardButton{currentOption(radio(true, branchModeLabel(lang, mode, currentBranches)))})
			continue
		}
		callback, err := b.token(ctx, cq.From.ID, action, payload)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: radio(sub.BranchMode == mode, branchModeLabel(lang, mode, currentBranches)), CallbackData: callback}})
	}
	backCB, err := b.advancedCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "branch.edit.title"), "", i18n.T(lang, "branch.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditBranchList(ctx context.Context, cq tg.CallbackQuery, lang string, payload editBranchPayload) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, payload.ID)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	selected := payload.BranchNames
	if payload.BranchName != "" && len(selected) == 0 {
		selected = []string{payload.BranchName}
	}
	if selected == nil {
		selected = subscriptionBranchNames(sub)
	}
	selected = db.NormalizeBranchNames(selected)
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, notConnectedPanel(lang), backHome(lang, cq.Message.Chat))
	}
	branches, err := b.github.ListBranches(ctx, token, sub.RepoFullName)
	if err != nil {
		return b.githubError(ctx, cq, lang, err, "err.action.list_branches")
	}
	// Back returns to the branch-mode picker for this subscription.
	backCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch", subscriptionPayload{ID: payload.ID})
	if err != nil {
		return err
	}
	backRow := panelFooter(lang, cq.Message.Chat, backCB)

	if len(branches) == 0 {
		allCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch.save", editBranchPayload{ID: payload.ID, BranchMode: "all"})
		if err != nil {
			return err
		}
		rows := [][]tg.InlineKeyboardButton{
			{{Text: i18n.T(lang, "btn.use_all_branches"), CallbackData: allCB, Style: tg.StylePrimary}},
			backRow,
		}
		return b.respond(ctx, cq, panel(i18n.T(lang, "branch.choose.title"), "", i18n.T(lang, "branch.choose.hint"), i18n.T(lang, "branch.empty")), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
	}

	pages := (len(branches) + branchPageSize - 1) / branchPageSize
	page := clampPage(payload.Page, pages)
	start := page * branchPageSize
	end := min(start+branchPageSize, len(branches))

	rows := [][]tg.InlineKeyboardButton{}
	for _, branch := range branches[start:end] {
		next := editBranchPayload{ID: payload.ID, BranchMode: "selected", BranchNames: selected, ToggleBranchName: branch.Name, Page: page}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(selected, branch.Name), branch.Name), CallbackData: callback}})
	}
	if len(selected) > 0 {
		saveCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch.save", editBranchPayload{ID: payload.ID, BranchMode: "selected", BranchNames: selected})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.save"), CallbackData: saveCB, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.save"))})
	}
	var nav []tg.InlineKeyboardButton
	if pages > 1 {
		for _, step := range []struct {
			text    string
			page    int
			enabled bool
		}{
			{i18n.T(lang, "btn.prev"), page - 1, page > 0},
			{i18n.T(lang, "btn.next"), page + 1, page < pages-1},
		} {
			if !step.enabled {
				nav = append(nav, disabledButton(step.text))
				continue
			}
			callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch.selected", editBranchPayload{ID: payload.ID, BranchMode: "selected", BranchNames: selected, Page: step.page})
			if err != nil {
				return err
			}
			nav = append(nav, tg.InlineKeyboardButton{Text: step.text, CallbackData: callback})
		}
		rows = append(rows, nav)
	}
	rows = append(rows, backRow)
	return b.respond(ctx, cq, panel(i18n.T(lang, "branch.choose.title"), "", i18n.T(lang, "branch.choose.hint"), pageNote(lang, page, pages)), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditPullRequestSettings(ctx context.Context, cq tg.CallbackQuery, lang string, id string, actions []string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	if !contains(sub.Events, "pull_request") {
		return b.renderAdvancedSettings(ctx, cq, lang, id)
	}
	if actions == nil {
		actions = normalizedPullRequestActions(sub.PullRequestActions)
	}
	rows := [][]tg.InlineKeyboardButton{}
	for _, action := range pullRequestActionOrder() {
		payload := editPullRequestPayload{ID: id, Actions: actions, ToggleAction: action}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.pr.toggle", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: checkbox(contains(actions, action), pullRequestActionLabel(lang, action)), CallbackData: callback}})
	}
	if len(actions) > 0 {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.pr.save", editPullRequestPayload{ID: id, Actions: actions})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: i18n.T(lang, "btn.save"), CallbackData: callback, Style: tg.StylePrimary}})
	} else {
		rows = append(rows, []tg.InlineKeyboardButton{disabledButton(i18n.T(lang, "btn.save"))})
	}
	backCB, err := b.advancedCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "pr.title"), "", i18n.T(lang, "pr.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditReleaseSettings(ctx context.Context, cq tg.CallbackQuery, lang string, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, subNotFoundPanel(lang), backHome(lang, cq.Message.Chat))
	}
	if !contains(sub.Events, "release") {
		return b.renderAdvancedSettings(ctx, cq, lang, id)
	}
	rows := [][]tg.InlineKeyboardButton{}
	mode := normalizeReleaseMode(sub.ReleaseMode)
	for _, candidate := range releaseModeOrder() {
		if mode == candidate {
			rows = append(rows, []tg.InlineKeyboardButton{currentOption(radio(true, releaseModeLabel(lang, candidate)))})
			continue
		}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.release.save", editReleasePayload{ID: id, ReleaseMode: candidate})
		if err != nil {
			return err
		}
		rows = append(rows, []tg.InlineKeyboardButton{{Text: radio(false, releaseModeLabel(lang, candidate)), CallbackData: callback}})
	}
	backCB, err := b.advancedCallback(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, panelFooter(lang, cq.Message.Chat, backCB))
	return b.respond(ctx, cq, panel(i18n.T(lang, "release.title"), "", i18n.T(lang, "release.hint"), ""), &tg.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) respond(ctx context.Context, cq tg.CallbackQuery, text string, markup *tg.InlineKeyboardMarkup) error {
	switch {
	// An ephemeral message is addressed by its own id plus its receiver, never
	// by message_id, so editMessageText cannot reach one. Without this branch a
	// tap on the group panel would answer in DM and leave the panel frozen on
	// its previous screen.
	case cq.Message.EphemeralMessageID != 0:
		err := b.client.EditEphemeralMessageText(ctx, cq.Message.Chat.ID, cq.From.ID, cq.Message.EphemeralMessageID, text, markup)
		if err == nil {
			return nil
		}
		slog.Warn("ephemeral panel edit failed; answering in DM", "chat_id", cq.Message.Chat.ID, "error", err)
	// Every panel Branchy shows in a group is ephemeral, so a public group
	// message from Branchy is a delivered notification card. Callback data can
	// be sent for any visible message, so editing one here would let anyone
	// overwrite a notification with a panel. Answer in DM instead.
	case inGroup(cq.Message.Chat):
	case cq.Message.MessageID != 0:
		err := b.client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, markup)
		// "message is not modified" means the view already shows this state
		// (e.g. a toggle that produced identical text, or a double tap). Treat
		// it as success instead of posting a duplicate message.
		if err == nil || tg.IsMessageNotModified(err) {
			return nil
		}
	}
	_, err := b.client.SendMessage(ctx, cq.From.ID, text, markup)
	return err
}

// upsertUser records the caller's identity (and, when the caller is acting in a
// group, that chat's identity + presence) in the shared core hub via core.touch.
// It is awaited before any FK insert into branchy.* so core.person/core.chat
// exist first. chat may be nil; a private chat or an absent chat leaves ChatID
// nil (a DM's chat_id equals the user id and carries no group identity).
func (b *Bot) upsertUser(ctx context.Context, user *tg.User, chat *tg.Chat) error {
	if user == nil {
		return nil
	}
	args := db.TouchArgs{
		UserID:    user.ID,
		Username:  user.Username,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		IsBot:     user.IsBot,
	}
	if chat != nil && chat.ID != 0 && chat.Type != "private" {
		id := chat.ID
		args.ChatID = &id
		args.ChatType = chat.Type
		args.ChatTitle = chat.Title
		args.ChatUsername = chat.Username
	}
	return b.store.TouchCore(ctx, args)
}

func (b *Bot) accessToken(ctx context.Context, telegramUserID int64) (string, error) {
	conn, err := b.store.GetGitHubConnection(ctx, telegramUserID)
	if err != nil {
		return "", err
	}
	return b.sealer.Decrypt(conn.EncryptedAccessToken)
}

var errNotGroupAdmin = errors.New("not a group admin")

// groupAdminLookup bounds the admin check. It runs on the update loop, and the
// client honors a Retry-After verbatim -- a rate-limited lookup asked to wait
// thirty seconds would stall every other user's updates behind it, and long
// enough to fail the polling-freshness check in /healthz.
const groupAdminLookup = 8 * time.Second

func (b *Bot) requireGroupAdmin(ctx context.Context, chatID, userID int64) error {
	lookupCtx, cancel := context.WithTimeout(ctx, groupAdminLookup)
	defer cancel()
	member, err := b.client.GetChatMember(lookupCtx, chatID, userID)
	if err != nil {
		return err
	}
	if member.Status == "creator" || member.Status == "administrator" {
		return nil
	}
	return errNotGroupAdmin
}

// groupAdminFailure reports a failed group-admin check honestly: a genuine
// non-admin is told so, a transient lookup failure is not. It re-renders the
// destination picker so fresh callback tokens are issued (the tapped token may
// already be consumed), letting the user retry.
func (b *Bot) groupAdminFailure(ctx context.Context, cq tg.CallbackQuery, lang string, err error, rerender func() error) (string, error) {
	toast := i18n.T(lang, "toast.not_group_admin")
	if !errors.Is(err, errNotGroupAdmin) {
		slog.Error("group admin check failed", "error", err)
		toast = i18n.T(lang, "toast.group_check_failed")
	}
	return toast, rerender()
}

func (b *Bot) token(ctx context.Context, telegramUserID int64, action string, payload any) (string, error) {
	value, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := b.store.CreateCallbackToken(ctx, telegramUserID, value, action, payload, 24*time.Hour); err != nil {
		return "", err
	}
	return "t:" + value, nil
}

func randomToken() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decode(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}

func backHome(lang string, chat tg.Chat) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		panelFooter(lang, chat, "home"),
	}}
}

// notConnectedPanel and subNotFoundPanel are the two dead ends Branchy reaches
// from several screens at once. They are named rather than inlined so the same
// wording cannot drift apart across the six call sites.
func notConnectedPanel(lang string) string {
	return panel(i18n.T(lang, "err.not_connected.title"), "", i18n.T(lang, "err.not_connected.hint"), "")
}

func subNotFoundPanel(lang string) string {
	return panel(i18n.T(lang, "err.sub.not_found.title"), "", i18n.T(lang, "err.sub.not_found.hint"), "")
}

// pageNote is the one thing a paginated keyboard cannot say about itself. It is
// empty for a single page, which keeps the quote off screens that do not need
// one.
func pageNote(lang string, page, pages int) string {
	if pages <= 1 {
		return ""
	}
	return i18n.T(lang, "page.indicator", "page", strconv.Itoa(page+1), "pages", strconv.Itoa(pages))
}

// groupFallbackLabel names a group Branchy knows the id of but not the title.
func groupFallbackLabel(lang string, chatID int64) string {
	return i18n.T(lang, "dest.group_fallback", "id", strconv.FormatInt(chatID, 10))
}

func esc(value string) string {
	return html.EscapeString(value)
}

// branchLabel describes the current branch filter in summary text (e.g.
// "Branches: main, dev"). For tappable mode buttons use branchModeLabel, which
// reads as an action rather than a status.
func branchLabel(lang, mode string, branches []string) string {
	switch mode {
	case "all":
		return i18n.T(lang, "branch.all")
	case "default":
		return i18n.T(lang, "branch.default")
	case "selected":
		if len(branches) > 0 {
			return branchNamesLabel(lang, branches)
		}
		return i18n.T(lang, "branch.selected.empty")
	default:
		return mode
	}
}

// branchModeLabel is the action-oriented label for the branch-mode radio
// buttons. "Specific branches" invites the tap into the picker; a count keeps
// the button short and stable regardless of branch name lengths.
func branchModeLabel(lang, mode string, branches []string) string {
	switch mode {
	case "all":
		return i18n.T(lang, "branch.all")
	case "default":
		return i18n.T(lang, "branch.default")
	case "selected":
		branches = db.NormalizeBranchNames(branches)
		if len(branches) == 0 {
			return i18n.T(lang, "branch.selected")
		}
		return i18n.T(lang, "branch.selected.count", "count", strconv.Itoa(len(branches)))
	default:
		return mode
	}
}

func branchNamesLabel(lang string, branches []string) string {
	branches = db.NormalizeBranchNames(branches)
	switch len(branches) {
	case 0:
		return i18n.T(lang, "branch.selected.label")
	case 1, 2, 3:
		return joinList(lang, branches)
	default:
		return i18n.T(lang, "branch.names_more",
			"names", joinList(lang, branches[:3]),
			"count", strconv.Itoa(len(branches)-3))
	}
}

// describeDestination returns a human label for a subscription's destination
// and an optional warning when a group destination is no longer reachable
// (the bot was removed or the group is unknown).
func (b *Bot) describeDestination(ctx context.Context, lang string, telegramUserID int64, sub db.Subscription) (string, string) {
	if sub.DestinationType == "dm" {
		return i18n.T(lang, "dest.dm"), ""
	}
	groups, err := b.store.ListKnownGroups(ctx, telegramUserID)
	if err == nil {
		for _, group := range groups {
			if group.ID == sub.DestinationChatID {
				if group.Title != "" {
					return group.Title, ""
				}
				return groupFallbackLabel(lang, group.ID), ""
			}
		}
	}
	return i18n.T(lang, "dest.unavailable"), i18n.T(lang, "dest.unavailable.warning")
}

func statusText(lang, status string) string {
	switch status {
	case "active":
		return i18n.T(lang, "status.active")
	case "paused":
		return i18n.T(lang, "status.paused")
	default:
		return status
	}
}

// pauseReasonNote explains an automatic pause so the user knows what to fix
// before resuming. Manual pauses carry no reason and return "".
func pauseReasonNote(lang, reason string) string {
	switch reason {
	case "telegram_blocked":
		return i18n.T(lang, "sub.pause.telegram_blocked")
	default:
		return ""
	}
}

func eventLabel(lang, event string) string {
	switch event {
	case "push":
		return i18n.T(lang, "event.push")
	case "pull_request":
		return i18n.T(lang, "event.pull_request")
	case "release":
		return i18n.T(lang, "event.release")
	default:
		return event
	}
}

// joinList glues a list the way the language glues one. A comma-space is the
// separator in thirteen of the sixteen; Chinese and Japanese use an ideographic
// comma and Arabic its own, and a Latin comma inside those reads as a typo.
func joinList(lang string, parts []string) string {
	return strings.Join(parts, i18n.T(lang, "list.separator"))
}

func humanEvents(lang string, events []string) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = eventLabel(lang, event)
	}
	return out
}

// checkbox marks a multi-select option. Square glyph signals "pick any number".
func checkbox(on bool, label string) string {
	if on {
		return "■ " + label
	}
	return "□ " + label
}

// radio marks a single-select option. Round glyph signals "pick exactly one",
// distinguishing it from the square multi-select checkboxes. ◉/◎ is the
// family-wide pair for a chosen and an unchosen item.
func radio(on bool, label string) string {
	if on {
		return "◉ " + label
	}
	return "◎ " + label
}

func settingsSummary(lang string, events []string, branchMode string, branchNames []string, pullRequestActions []string, releaseMode string) string {
	events = db.NormalizeEvents(events)
	var lines []string
	lines = append(lines, i18n.T(lang, "sum.events", "value", esc(joinList(lang, humanEvents(lang, events)))))
	if usesBranchFilter(events) {
		lines = append(lines, i18n.T(lang, "sum.branches", "value", esc(branchLabel(lang, branchMode, branchNames))))
	}
	if contains(events, "pull_request") {
		lines = append(lines, i18n.T(lang, "sum.pull_requests", "value", esc(joinList(lang, humanPullRequestActions(lang, pullRequestActions)))))
	}
	if contains(events, "release") {
		lines = append(lines, i18n.T(lang, "sum.releases", "value", esc(releaseModeLabel(lang, releaseMode))))
	}
	return strings.Join(lines, "\n")
}

func withDraftDefaults(draft subDraft) subDraft {
	draft.Events = db.NormalizeEvents(draft.Events)
	if draft.BranchName != "" && len(draft.BranchNames) == 0 {
		draft.BranchNames = []string{draft.BranchName}
	}
	if draft.BranchMode == "" {
		draft.BranchMode = "all"
	}
	if draft.PullRequestActions == nil {
		draft.PullRequestActions = db.DefaultPullRequestActions()
	} else {
		draft.PullRequestActions = db.NormalizePullRequestActions(draft.PullRequestActions)
	}
	draft.ReleaseMode = normalizeReleaseMode(draft.ReleaseMode)
	return draft
}

func normalizeDraftForEvents(draft subDraft) subDraft {
	draft = withDraftDefaults(draft)
	if !usesBranchFilter(draft.Events) {
		draft.BranchMode = "all"
		draft.BranchNames = nil
	} else if draft.BranchMode != "selected" {
		draft.BranchNames = nil
	} else {
		draft.BranchNames = db.NormalizeBranchNames(draft.BranchNames)
		if len(draft.BranchNames) == 0 {
			// Opening "Specific branches" but choosing none must not stick as an
			// empty, unusable selection. Fall back to the default branch. (The
			// branch picker re-forces "selected" itself, so this only takes effect
			// once the user navigates away with nothing chosen.)
			draft.BranchMode = "default"
		}
	}
	return draft
}

func settingsReady(draft subDraft) bool {
	if len(draft.Events) == 0 {
		return false
	}
	if contains(draft.Events, "pull_request") && len(draft.PullRequestActions) == 0 {
		return false
	}
	if usesBranchFilter(draft.Events) && draft.BranchMode == "selected" && len(draft.BranchNames) == 0 {
		return false
	}
	return true
}

func usesBranchFilter(events []string) bool {
	return contains(events, "push") || contains(events, "pull_request")
}

func subscriptionBranchNames(sub db.Subscription) []string {
	if len(sub.BranchNames) > 0 {
		return db.NormalizeBranchNames(sub.BranchNames)
	}
	if sub.BranchName != "" {
		return []string{sub.BranchName}
	}
	return nil
}

func pullRequestActionOrder() []string {
	return []string{"opened", "merged", "closed"}
}

func normalizedPullRequestActions(actions []string) []string {
	actions = db.NormalizePullRequestActions(actions)
	if len(actions) == 0 {
		return db.DefaultPullRequestActions()
	}
	return actions
}

func pullRequestActionLabel(lang, action string) string {
	switch action {
	case "opened":
		return i18n.T(lang, "pr.opened")
	case "merged":
		return i18n.T(lang, "pr.merged")
	case "closed":
		return i18n.T(lang, "pr.closed")
	default:
		return action
	}
}

func humanPullRequestActions(lang string, actions []string) []string {
	actions = db.NormalizePullRequestActions(actions)
	if len(actions) == 0 {
		return []string{i18n.T(lang, "pr.none")}
	}
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		out = append(out, pullRequestActionLabel(lang, action))
	}
	return out
}

func releaseModeOrder() []string {
	return []string{"all", "releases", "prereleases"}
}

func normalizeReleaseMode(mode string) string {
	switch mode {
	case "releases", "prereleases":
		return mode
	default:
		return "all"
	}
}

func releaseModeLabel(lang, mode string) string {
	switch normalizeReleaseMode(mode) {
	case "releases":
		return i18n.T(lang, "release.releases")
	case "prereleases":
		return i18n.T(lang, "release.prereleases")
	default:
		return i18n.T(lang, "release.all")
	}
}

// botUsername returns the bot's @username, fetched lazily and cached, for
// building t.me deep links. Returns "" if it cannot be resolved.
func (b *Bot) botUsername(ctx context.Context) string {
	if cached := b.cachedBotUsername(); cached != "" {
		return cached
	}
	me, err := b.client.GetMe(ctx)
	if err != nil {
		slog.Warn("telegram getMe failed", "error", err)
		return ""
	}
	b.username.Store(me.Username)
	return me.Username
}

func (b *Bot) warmBotUsername(ctx context.Context) {
	delay := 30 * time.Second
	for {
		warmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		username := b.botUsername(warmCtx)
		cancel()
		if username != "" || ctx.Err() != nil {
			return
		}
		if sleep(ctx, delay) != nil {
			return
		}
		if delay < 5*time.Minute {
			delay *= 2
			if delay > 5*time.Minute {
				delay = 5 * time.Minute
			}
		}
	}
}

func (b *Bot) cachedBotUsername() string {
	cached, _ := b.username.Load().(string)
	return cached
}

// userMessage maps an error to text safe to show the user: validation errors
// pass through, everything else is logged and shown as a generic message.
func (b *Bot) userMessage(err error, lang, actionKey string) string {
	var v *subscriptions.ValidationError
	if errors.As(err, &v) {
		// The service names the sentence and supplies the data; only the data
		// can carry HTML metacharacters, so only the odd (value) slots are
		// escaped.
		args := make([]string, len(v.Args))
		for i, arg := range v.Args {
			if i%2 == 1 {
				arg = esc(arg)
			}
			args[i] = arg
		}
		return i18n.T(lang, v.Key, args...)
	}
	slog.Error("bot action failed", "action", actionKey, "error", err)
	return i18n.T(lang, "err.generic", "action", i18n.T(lang, actionKey))
}

// githubError renders the response for an error from a GitHub-backed action. A
// revoked token (401) gets the reconnect prompt; anything else falls back to the
// standard user message.
func (b *Bot) githubError(ctx context.Context, cq tg.CallbackQuery, lang string, err error, actionKey string) error {
	if github.IsAuthError(err) {
		return b.renderReconnect(ctx, cq, lang)
	}
	return b.respond(ctx, cq, errorPanel(lang, b.userMessage(err, lang, actionKey)), backHome(lang, cq.Message.Chat))
}

// renderReconnect shows a single reconnect call to action when a GitHub call
// fails with an invalid token. It mutates no state: a revoked authorization
// already stops GitHub deliveries, so no dead jobs accumulate, and reconnecting
// restores the connection (a later edit re-syncs the webhook).
func (b *Bot) renderReconnect(ctx context.Context, cq tg.CallbackQuery, lang string) error {
	connectURL, err := b.oauth.CreateAuthURL(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	text := panel(i18n.T(lang, "err.github.expired.title"), "", i18n.T(lang, "err.github.expired.hint"), "")
	markup := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: i18n.T(lang, "btn.reconnect"), URL: connectURL, Style: tg.StylePrimary}},
		panelFooter(lang, cq.Message.Chat, "home"),
	}}
	return b.respond(ctx, cq, text, markup)
}

// These mint the callback a "Back" button carries, not the button itself: the
// row it sits in is built by panelFooter, which is the only place that decides
// what "going up" looks like — one word, and a Close beside it only in a group.
func (b *Bot) viewCallback(ctx context.Context, telegramUserID int64, id string) (string, error) {
	return b.token(ctx, telegramUserID, "sub.view", subscriptionPayload{ID: id})
}

func (b *Bot) editMenuCallback(ctx context.Context, telegramUserID int64, id string) (string, error) {
	return b.token(ctx, telegramUserID, "sub.edit.menu", subscriptionPayload{ID: id})
}

func (b *Bot) advancedCallback(ctx context.Context, telegramUserID int64, id string) (string, error) {
	return b.token(ctx, telegramUserID, "sub.edit.settings", subscriptionPayload{ID: id})
}

// stepBackCallback is where the destination picker goes up to: the subscription
// edit menu when editing, or the repository list when creating.
func (b *Bot) stepBackCallback(ctx context.Context, telegramUserID int64, edit bool, editID, createTarget string) (string, error) {
	if edit {
		return b.editMenuCallback(ctx, telegramUserID, editID)
	}
	return createTarget, nil
}

func (b *Bot) branchNavButton(ctx context.Context, telegramUserID int64, draft subDraft, text string, page int, action string) (tg.InlineKeyboardButton, error) {
	next := draft
	next.BranchMode = "selected"
	next.BranchPage = page
	callback, err := b.token(ctx, telegramUserID, action, next)
	if err != nil {
		return tg.InlineKeyboardButton{}, err
	}
	return tg.InlineKeyboardButton{Text: text, CallbackData: callback}, nil
}

func paginationRow(lang, prefix string, page, pages int) []tg.InlineKeyboardButton {
	if pages <= 1 {
		return nil
	}
	prev, next := i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")
	var row []tg.InlineKeyboardButton
	if page > 0 {
		row = append(row, tg.InlineKeyboardButton{Text: prev, CallbackData: fmt.Sprintf("%s:%d", prefix, page-1)})
	} else {
		row = append(row, disabledButton(prev))
	}
	if page < pages-1 {
		row = append(row, tg.InlineKeyboardButton{Text: next, CallbackData: fmt.Sprintf("%s:%d", prefix, page+1)})
	} else {
		row = append(row, disabledButton(next))
	}
	return row
}

func disabledButton(text string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: text, Disabled: &tg.DisabledButton{}}
}

func clampPage(page, pages int) int {
	if pages <= 0 || page < 0 {
		return 0
	}
	if page >= pages {
		return pages - 1
	}
	return page
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func toggleEvent(events []string, event string) []string {
	if contains(events, event) {
		var out []string
		for _, existing := range events {
			if existing != event {
				out = append(out, existing)
			}
		}
		return db.NormalizeEvents(out)
	}
	return db.NormalizeEvents(append(events, event))
}

func toggleString(values []string, value string) []string {
	if contains(values, value) {
		out := make([]string, 0, len(values)-1)
		for _, existing := range values {
			if existing != value {
				out = append(out, existing)
			}
		}
		return db.NormalizeBranchNames(out)
	}
	return db.NormalizeBranchNames(append(values, value))
}

func togglePullRequestAction(actions []string, action string) []string {
	if actions == nil {
		actions = db.DefaultPullRequestActions()
	}
	if contains(actions, action) {
		out := make([]string, 0, len(actions)-1)
		for _, existing := range actions {
			if existing != action {
				out = append(out, existing)
			}
		}
		return db.NormalizePullRequestActions(out)
	}
	return db.NormalizePullRequestActions(append(actions, action))
}

func isConsumedAction(action string) bool {
	switch action {
	case "sub.branch",
		"sub.branch.selected",
		"sub.create",
		"sub.status",
		"sub.delete",
		"sub.test",
		"sub.edit.events.save",
		"sub.edit.branch.save",
		"sub.edit.pr.save",
		"sub.edit.release.save",
		"sub.edit.dest.save":
		return true
	default:
		return false
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type repoPayload struct {
	Repo github.Repository `json:"repo"`
}

type subDraft struct {
	Repo                    github.Repository `json:"repo,omitempty"`
	DestinationType         string            `json:"destination_type,omitempty"`
	DestinationChatID       int64             `json:"destination_chat_id,omitempty"`
	Events                  []string          `json:"events,omitempty"`
	ToggleEvent             string            `json:"toggle_event,omitempty"`
	BranchMode              string            `json:"branch_mode,omitempty"`
	BranchName              string            `json:"branch_name,omitempty"`
	BranchNames             []string          `json:"branch_names"`
	ToggleBranchName        string            `json:"toggle_branch_name,omitempty"`
	BranchPage              int               `json:"branch_page,omitempty"`
	PullRequestActions      []string          `json:"pull_request_actions"`
	TogglePullRequestAction string            `json:"toggle_pull_request_action,omitempty"`
	ReleaseMode             string            `json:"release_mode,omitempty"`
	EditSubscriptionID      string            `json:"edit_subscription_id,omitempty"`
}

type subscriptionPayload struct {
	ID string `json:"id"`
}

type statusPayload struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type editEventsPayload struct {
	ID          string   `json:"id"`
	Events      []string `json:"events"`
	ToggleEvent string   `json:"toggle_event,omitempty"`
}

type editBranchPayload struct {
	ID               string   `json:"id"`
	BranchMode       string   `json:"branch_mode"`
	BranchName       string   `json:"branch_name,omitempty"`
	BranchNames      []string `json:"branch_names"`
	ToggleBranchName string   `json:"toggle_branch_name,omitempty"`
	Page             int      `json:"page,omitempty"`
}

type editPullRequestPayload struct {
	ID           string   `json:"id"`
	Actions      []string `json:"actions"`
	ToggleAction string   `json:"toggle_action,omitempty"`
}

type editReleasePayload struct {
	ID          string `json:"id"`
	ReleaseMode string `json:"release_mode"`
}

type editDestinationPayload struct {
	ID                string `json:"id"`
	DestinationType   string `json:"destination_type"`
	DestinationChatID int64  `json:"destination_chat_id"`
}
