// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"branchy/internal/db"
	"branchy/internal/github"
	"branchy/internal/oauth"
	"branchy/internal/subscriptions"
)

type Store interface {
	UpsertTelegramUser(ctx context.Context, user db.TelegramUser) error
	UpsertTelegramChat(ctx context.Context, chat db.TelegramChat) error
	ListKnownGroups(ctx context.Context, telegramUserID int64) ([]db.TelegramChat, error)
	GetGitHubConnection(ctx context.Context, telegramUserID int64) (db.GitHubConnection, error)
	ListSubscriptionsByUser(ctx context.Context, telegramUserID int64) ([]db.Subscription, error)
	GetSubscriptionForUser(ctx context.Context, telegramUserID int64, id string) (db.Subscription, error)
	CreateCallbackToken(ctx context.Context, telegramUserID int64, token, action string, payload any, ttl time.Duration) error
	GetCallbackToken(ctx context.Context, telegramUserID int64, token string) (db.CallbackToken, error)
	ConsumeCallbackToken(ctx context.Context, telegramUserID int64, token string) (db.CallbackToken, error)
	GetRuntimeValue(ctx context.Context, key string) (string, error)
	SetRuntimeValue(ctx context.Context, key, value string) error
}

type OAuthService interface {
	CreateAuthURL(ctx context.Context, telegramUserID int64) (string, error)
}

type Bot struct {
	store        Store
	client       *Client
	oauth        OAuthService
	github       *github.Client
	sealer       *oauth.TokenSealer
	subs         *subscriptions.Service
	lastPollUnix atomic.Int64
	username     atomic.Value // string, the bot's @username, fetched lazily
}

const (
	repoPageSize   = 10
	branchPageSize = 20
)

func NewBot(store Store, client *Client, oauthSvc OAuthService, githubClient *github.Client, sealer *oauth.TokenSealer, subs *subscriptions.Service) *Bot {
	return &Bot{store: store, client: client, oauth: oauthSvc, github: githubClient, sealer: sealer, subs: subs}
}

func (b *Bot) Run(ctx context.Context) error {
	offset, err := b.loadOffset(ctx)
	if err != nil {
		slog.Warn("telegram offset load failed", "error", err)
	}
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
			slog.Error("telegram getUpdates failed", "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		b.lastPollUnix.Store(time.Now().Unix())
		for _, update := range updates {
			if err := b.handleUpdate(ctx, update); err != nil {
				slog.Error("telegram update failed", "update_id", update.UpdateID, "error", err)
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

func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
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

func (b *Bot) handleMessage(ctx context.Context, msg Message) error {
	if err := b.upsertUser(ctx, msg.From); err != nil {
		return err
	}
	if msg.Text == "/start" {
		if msg.Chat.Type != "private" {
			var markup *InlineKeyboardMarkup
			if username := b.botUsername(ctx); username != "" {
				markup = &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
					{{Text: "Open Branchy in DM", URL: "https://t.me/" + username}},
				}}
			}
			_, err := b.client.SendMessage(ctx, msg.Chat.ID, "Open a direct message with Branchy to configure notifications.", markup)
			return err
		}
		text, markup, err := b.mainMenu(ctx, msg.From.ID)
		if err != nil {
			return err
		}
		_, err = b.client.SendMessage(ctx, msg.Chat.ID, text, markup)
		return err
	}
	return nil
}

func (b *Bot) handleMyChatMember(ctx context.Context, upd ChatMemberUpdated) error {
	if err := b.upsertUser(ctx, upd.From); err != nil {
		return err
	}
	status := upd.NewChatMember.Status
	active := status == "member" || status == "administrator"
	return b.store.UpsertTelegramChat(ctx, db.TelegramChat{
		ID:          upd.Chat.ID,
		Type:        upd.Chat.Type,
		Title:       upd.Chat.Title,
		Username:    upd.Chat.Username,
		BotStatus:   status,
		Active:      active,
		AddedByUser: upd.From.ID,
	})
}

func (b *Bot) handleCallback(ctx context.Context, cq CallbackQuery) error {
	if err := b.upsertUser(ctx, cq.From); err != nil {
		return err
	}
	// Dispatch first, then answer the callback query exactly once with an
	// optional toast. Answering once (rather than a blank pre-answer) lets
	// confirmations surface as a toast while the underlying menu stays in place.
	toast, err := b.dispatchCallback(ctx, cq)
	if ackErr := b.client.AnswerCallbackQuery(ctx, cq.ID, toast, false); ackErr != nil {
		slog.Warn("answer callback failed", "error", ackErr)
	}
	return err
}

func (b *Bot) dispatchCallback(ctx context.Context, cq CallbackQuery) (string, error) {
	switch cq.Data {
	case "home":
		return "", b.renderHome(ctx, cq)
	case "sub:list":
		return "", b.renderSubscriptionList(ctx, cq)
	case "test:menu":
		return "", b.renderTestList(ctx, cq)
	}
	if page, ok := parsePage(cq.Data, "repo:list"); ok {
		return "", b.renderRepoList(ctx, cq, false, page)
	}
	if page, ok := parsePage(cq.Data, "sub:new"); ok {
		return "", b.renderRepoList(ctx, cq, true, page)
	}

	if !strings.HasPrefix(cq.Data, "t:") {
		return "", nil
	}
	tokenValue := strings.TrimPrefix(cq.Data, "t:")
	token, err := b.store.GetCallbackToken(ctx, cq.From.ID, tokenValue)
	if err != nil {
		return "This action expired.", b.renderHome(ctx, cq)
	}
	if isConsumedAction(token.Action) {
		token, err = b.store.ConsumeCallbackToken(ctx, cq.From.ID, tokenValue)
		if err != nil {
			return "This action already ran.", b.renderHome(ctx, cq)
		}
	}
	return b.handleToken(ctx, cq, token)
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

func (b *Bot) handleToken(ctx context.Context, cq CallbackQuery, token db.CallbackToken) (string, error) {
	switch token.Action {
	case "repo.info":
		var payload repoPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderRepoInfo(ctx, cq, payload.Repo)
	case "sub.repo":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return "", b.renderDestinationPicker(ctx, cq, draft, false, "")
	case "sub.dest":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, draft.DestinationChatID, cq.From.ID); err != nil {
				return b.groupAdminFailure(ctx, cq, err, func() error {
					return b.renderDestinationPicker(ctx, cq, draft, false, "")
				})
			}
		}
		return "", b.renderEventPicker(ctx, cq, draft)
	case "sub.events.toggle":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.ToggleEvent != "" {
			draft.Events = toggleEvent(draft.Events, draft.ToggleEvent)
			draft.ToggleEvent = ""
		}
		return "", b.renderEventPicker(ctx, cq, draft)
	case "sub.branch":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		if draft.BranchMode == "selected" && draft.BranchName == "" {
			return "", b.renderBranchList(ctx, cq, draft)
		}
		return b.createSubscription(ctx, cq, draft)
	case "sub.branch.selected":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return "", err
		}
		return b.createSubscription(ctx, cq, draft)
	case "sub.view":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderSubscription(ctx, cq, payload.ID)
	case "sub.status":
		var payload statusPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetStatus(ctx, cq.From.ID, payload.ID, payload.Status); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "update the subscription")), backHome())
		}
		toast := "Subscription paused."
		if payload.Status == "active" {
			toast = "Subscription resumed."
		}
		return toast, b.renderSubscription(ctx, cq, payload.ID)
	case "sub.delete":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.Delete(ctx, cq.From.ID, payload.ID); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "delete the subscription")), backHome())
		}
		return "Subscription deleted.", b.renderSubscriptionList(ctx, cq)
	case "sub.test":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SendTest(ctx, b.client, cq.From.ID, payload.ID); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "send the test notification")), backHome())
		}
		return "Test notification sent.", b.renderSubscription(ctx, cq, payload.ID)
	case "sub.edit.events":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditEvents(ctx, cq, payload.ID, payload.Events)
	case "sub.edit.events.toggle":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.ToggleEvent != "" {
			payload.Events = toggleEvent(payload.Events, payload.ToggleEvent)
			payload.ToggleEvent = ""
		}
		return "", b.renderEditEvents(ctx, cq, payload.ID, payload.Events)
	case "sub.edit.events.save":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetEvents(ctx, cq.From.ID, payload.ID, payload.Events); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "update the events")), backHome())
		}
		return "Events updated.", b.renderSubscription(ctx, cq, payload.ID)
	case "sub.edit.branch":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditBranch(ctx, cq, payload.ID)
	case "sub.edit.branch.save":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if err := b.subs.SetBranch(ctx, cq.From.ID, payload.ID, payload.BranchMode, payload.BranchName); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "update the branch filter")), backHome())
		}
		return "Branch filter updated.", b.renderSubscription(ctx, cq, payload.ID)
	case "sub.edit.branch.selected":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderEditBranchList(ctx, cq, payload.ID, payload.Page)
	case "sub.edit.dest":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		return "", b.renderDestinationPicker(ctx, cq, subDraft{EditSubscriptionID: payload.ID}, true, payload.ID)
	case "sub.edit.dest.save":
		var payload editDestinationPayload
		if err := decode(token.Payload, &payload); err != nil {
			return "", err
		}
		if payload.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, payload.DestinationChatID, cq.From.ID); err != nil {
				return b.groupAdminFailure(ctx, cq, err, func() error {
					return b.renderDestinationPicker(ctx, cq, subDraft{EditSubscriptionID: payload.ID}, true, payload.ID)
				})
			}
		}
		if err := b.subs.SetDestination(ctx, cq.From.ID, payload.ID, payload.DestinationType, payload.DestinationChatID); err != nil {
			return "", b.respond(ctx, cq, esc(b.userMessage(err, "update the destination")), backHome())
		}
		return "Destination updated.", b.renderSubscription(ctx, cq, payload.ID)
	}
	return "", nil
}

func (b *Bot) mainMenu(ctx context.Context, telegramUserID int64) (string, *InlineKeyboardMarkup, error) {
	connectURL, err := b.oauth.CreateAuthURL(ctx, telegramUserID)
	if err != nil {
		return "", nil, err
	}
	connected := false
	lines := []string{"<b>Branchy</b>"}
	if conn, err := b.store.GetGitHubConnection(ctx, telegramUserID); err == nil {
		connected = true
		lines = append(lines, "Connected as "+esc(conn.GitHubLogin))
	} else {
		lines = append(lines, "Not connected to GitHub")
	}
	connectLabel := "Connect GitHub"
	if connected {
		connectLabel = "Reconnect GitHub"
		subs, err := b.store.ListSubscriptionsByUser(ctx, telegramUserID)
		if err != nil {
			return "", nil, err
		}
		switch len(subs) {
		case 0:
			lines = append(lines, "No subscriptions yet. Tap New subscription to begin.")
		case 1:
			lines = append(lines, "1 subscription.")
		default:
			lines = append(lines, fmt.Sprintf("%d subscriptions.", len(subs)))
		}
	} else {
		lines = append(lines, "Connect GitHub to choose repositories and events.")
	}
	rows := [][]InlineKeyboardButton{
		{{Text: connectLabel, URL: connectURL}},
	}
	// The other actions all require a GitHub connection, so only offer them
	// once connected; until then the menu is just the connect button.
	if connected {
		rows = append(rows,
			[]InlineKeyboardButton{{Text: "Repositories", CallbackData: "repo:list"}, {Text: "New subscription", CallbackData: "sub:new"}},
			[]InlineKeyboardButton{{Text: "Subscriptions", CallbackData: "sub:list"}, {Text: "Test notification", CallbackData: "test:menu"}},
		)
	}
	return strings.Join(lines, "\n"), &InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (b *Bot) renderHome(ctx context.Context, cq CallbackQuery) error {
	text, markup, err := b.mainMenu(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	return b.respond(ctx, cq, text, markup)
}

func (b *Bot) renderRepoList(ctx context.Context, cq CallbackQuery, subscribeMode bool, page int) error {
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, "Connect GitHub first.", backHome())
	}
	repos, err := b.github.ListRepositories(ctx, token)
	if err != nil {
		return b.respond(ctx, cq, esc(b.userMessage(err, "list your repositories")), backHome())
	}
	repos = visibleRepositories(repos, subscribeMode)
	if len(repos) == 0 {
		if subscribeMode {
			return b.respond(ctx, cq, "No repositories where you can add a webhook. You need admin rights on a repository to subscribe.", backHome())
		}
		return b.respond(ctx, cq, "No repositories found for this GitHub account.", backHome())
	}

	prefix := "repo:list"
	title := "Repositories"
	if subscribeMode {
		prefix = "sub:new"
		title = "Choose a repository"
	}

	pages := (len(repos) + repoPageSize - 1) / repoPageSize
	if page >= pages {
		page = pages - 1
	}
	start := page * repoPageSize
	end := min(start+repoPageSize, len(repos))

	rows := [][]InlineKeyboardButton{}
	for _, repo := range repos[start:end] {
		action := "repo.info"
		if subscribeMode {
			action = "sub.repo"
		}
		text := repo.FullName
		if !subscribeMode && !repo.HasAdminPermission {
			text = text + "  ·  no hook access"
		}
		callback, err := b.token(ctx, cq.From.ID, action, repoPayload{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: text, CallbackData: callback}})
	}
	if nav := paginationRow(prefix, page, pages); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	header := "<b>" + esc(title) + "</b>"
	if pages > 1 {
		header += fmt.Sprintf("\nPage %d of %d", page+1, pages)
	}
	return b.respond(ctx, cq, header, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderRepoInfo(ctx context.Context, cq CallbackQuery, repo github.Repository) error {
	rows := [][]InlineKeyboardButton{}
	if repo.HasAdminPermission && !repo.Archived {
		callback, err := b.token(ctx, cq.From.ID, "sub.repo", subDraft{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Create subscription", CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "repo:list"}})
	text := "<b>" + esc(repo.FullName) + "</b>\nDefault branch: " + esc(repo.DefaultBranch)
	if repo.Archived {
		text += "\nThis repository is archived, so GitHub webhooks cannot be configured."
	} else if !repo.HasAdminPermission {
		text += "\nYou need admin rights here to add a webhook, so you cannot subscribe to this repository."
	}
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
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
	return filtered
}

func (b *Bot) renderDestinationPicker(ctx context.Context, cq CallbackQuery, draft subDraft, edit bool, editID string) error {
	rows := [][]InlineKeyboardButton{}
	if edit {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.dest.save", editDestinationPayload{ID: editID, DestinationType: "dm", DestinationChatID: cq.From.ID})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Direct message", CallbackData: callback}})
	} else {
		draft.DestinationType = "dm"
		draft.DestinationChatID = cq.From.ID
		callback, err := b.token(ctx, cq.From.ID, "sub.dest", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Direct message", CallbackData: callback}})
	}

	groups, err := b.store.ListKnownGroups(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	for _, group := range groups {
		label := group.Title
		if label == "" {
			label = fmt.Sprintf("Group %d", group.ID)
		}
		if edit {
			callback, err := b.token(ctx, cq.From.ID, "sub.edit.dest.save", editDestinationPayload{ID: editID, DestinationType: "group", DestinationChatID: group.ID})
			if err != nil {
				return err
			}
			rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
			continue
		}
		next := draft
		next.DestinationType = "group"
		next.DestinationChatID = group.ID
		callback, err := b.token(ctx, cq.From.ID, "sub.dest", next)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	backButton, err := b.stepBackButton(ctx, cq.From.ID, edit, editID, "sub:new")
	if err != nil {
		return err
	}
	rows = append(rows, []InlineKeyboardButton{backButton})
	text := "<b>Choose destination</b>\nGroups appear here after Branchy is added to them."
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEventPicker(ctx context.Context, cq CallbackQuery, draft subDraft) error {
	rows := [][]InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		next := draft
		next.ToggleEvent = event
		callback, err := b.token(ctx, cq.From.ID, "sub.events.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: checkbox(contains(draft.Events, event), eventLabel(event)), CallbackData: callback}})
	}
	text := "<b>Choose events</b>\nSelect at least one event, then choose a branch filter."
	if len(draft.Events) > 0 {
		for _, branchMode := range []string{"all", "default", "selected"} {
			next := draft
			next.BranchMode = branchMode
			next.BranchName = ""
			callback, err := b.token(ctx, cq.From.ID, "sub.branch", next)
			if err != nil {
				return err
			}
			rows = append(rows, []InlineKeyboardButton{{Text: branchLabel(branchMode, ""), CallbackData: callback}})
		}
	}
	// Back returns to the destination step, preserving the draft.
	backCB, err := b.token(ctx, cq.From.ID, "sub.repo", draft)
	if err != nil {
		return err
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: backCB}})
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderBranchList(ctx context.Context, cq CallbackQuery, draft subDraft) error {
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, "Connect GitHub first.", backHome())
	}
	branches, err := b.github.ListBranches(ctx, token, draft.Repo.FullName)
	if err != nil {
		return b.respond(ctx, cq, esc(b.userMessage(err, "list the branches")), backHome())
	}
	// Back returns to the event picker (ToggleEvent is empty, so it is a no-op
	// that simply re-renders), preserving the draft.
	backCB, err := b.token(ctx, cq.From.ID, "sub.events.toggle", draft)
	if err != nil {
		return err
	}
	backRow := []InlineKeyboardButton{{Text: "Back", CallbackData: backCB}}

	if len(branches) == 0 {
		allDraft := draft
		allDraft.BranchMode = "all"
		allDraft.BranchName = ""
		allCB, err := b.token(ctx, cq.From.ID, "sub.branch", allDraft)
		if err != nil {
			return err
		}
		rows := [][]InlineKeyboardButton{
			{{Text: "Use all branches", CallbackData: allCB}},
			backRow,
		}
		return b.respond(ctx, cq, "<b>Choose branch</b>\nThis repository has no branches to choose from.", &InlineKeyboardMarkup{InlineKeyboard: rows})
	}

	pages := (len(branches) + branchPageSize - 1) / branchPageSize
	page := clampPage(draft.BranchPage, pages)
	start := page * branchPageSize
	end := min(start+branchPageSize, len(branches))

	rows := [][]InlineKeyboardButton{}
	for _, branch := range branches[start:end] {
		next := draft
		next.BranchMode = "selected"
		next.BranchName = branch.Name
		callback, err := b.token(ctx, cq.From.ID, "sub.branch.selected", next)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: branch.Name, CallbackData: callback}})
	}
	var nav []InlineKeyboardButton
	if page > 0 {
		button, err := b.branchNavButton(ctx, cq.From.ID, draft, "‹ Prev", page-1)
		if err != nil {
			return err
		}
		nav = append(nav, button)
	}
	if page < pages-1 {
		button, err := b.branchNavButton(ctx, cq.From.ID, draft, "Next ›", page+1)
		if err != nil {
			return err
		}
		nav = append(nav, button)
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, backRow)
	header := "<b>Choose branch</b>"
	if pages > 1 {
		header += fmt.Sprintf("\nPage %d of %d", page+1, pages)
	}
	return b.respond(ctx, cq, header, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) createSubscription(ctx context.Context, cq CallbackQuery, draft subDraft) (string, error) {
	id, err := b.subs.Create(ctx, cq.From.ID, draft.Repo, draft.DestinationType, draft.DestinationChatID, draft.Events, draft.BranchMode, draft.BranchName)
	if err != nil {
		return "", b.respond(ctx, cq, esc(b.userMessage(err, "create the subscription")), backHome())
	}
	return "Subscription created.", b.renderSubscription(ctx, cq, id)
}

func (b *Bot) renderSubscriptionList(ctx context.Context, cq CallbackQuery) error {
	subs, err := b.store.ListSubscriptionsByUser(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return b.respond(ctx, cq, "No subscriptions yet.", backHome())
	}
	rows := [][]InlineKeyboardButton{}
	for _, sub := range subs {
		callback, err := b.token(ctx, cq.From.ID, "sub.view", subscriptionPayload{ID: sub.ID})
		if err != nil {
			return err
		}
		label := sub.RepoFullName
		if sub.Status == "paused" {
			label += "  ·  paused"
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Subscriptions</b>\nTap a subscription to view or edit it.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderTestList(ctx context.Context, cq CallbackQuery) error {
	subs, err := b.store.ListSubscriptionsByUser(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return b.respond(ctx, cq, "Create a subscription before sending a test notification.", backHome())
	}
	rows := [][]InlineKeyboardButton{}
	for _, sub := range subs {
		callback, err := b.token(ctx, cq.From.ID, "sub.test", subscriptionPayload{ID: sub.ID})
		if err != nil {
			return err
		}
		label := sub.RepoFullName + "  →  " + shortDestination(sub)
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Send test notification</b>\nTap a subscription to send a test to its destination.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderSubscription(ctx context.Context, cq CallbackQuery, id string) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, "Subscription not found.", backHome())
	}
	rows := [][]InlineKeyboardButton{}
	nextStatus := "paused"
	statusLabel := "Pause"
	if sub.Status == "paused" {
		nextStatus = "active"
		statusLabel = "Resume"
	}
	statusCB, err := b.token(ctx, cq.From.ID, "sub.status", statusPayload{ID: sub.ID, Status: nextStatus})
	if err != nil {
		return err
	}
	testCB, err := b.token(ctx, cq.From.ID, "sub.test", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	editEventsCB, err := b.token(ctx, cq.From.ID, "sub.edit.events", editEventsPayload{ID: sub.ID, Events: sub.Events})
	if err != nil {
		return err
	}
	editBranchCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	editDestCB, err := b.token(ctx, cq.From.ID, "sub.edit.dest", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	deleteCB, err := b.token(ctx, cq.From.ID, "sub.delete", subscriptionPayload{ID: sub.ID})
	if err != nil {
		return err
	}
	rows = append(rows,
		[]InlineKeyboardButton{{Text: statusLabel, CallbackData: statusCB}, {Text: "Test", CallbackData: testCB}},
		[]InlineKeyboardButton{{Text: "Edit events", CallbackData: editEventsCB}, {Text: "Edit branch", CallbackData: editBranchCB}},
		[]InlineKeyboardButton{{Text: "Edit destination", CallbackData: editDestCB}},
		[]InlineKeyboardButton{{Text: "Delete", CallbackData: deleteCB}},
		[]InlineKeyboardButton{{Text: "Back", CallbackData: "sub:list"}},
	)
	destLabel, destWarning := b.describeDestination(ctx, cq.From.ID, sub)
	text := fmt.Sprintf("<b>%s</b>\nStatus: %s\nDestination: %s\nEvents: %s\nBranch: %s",
		esc(sub.RepoFullName),
		esc(statusText(sub.Status)),
		esc(destLabel),
		esc(strings.Join(humanEvents(sub.Events), ", ")),
		esc(branchLabel(sub.BranchMode, sub.BranchName)),
	)
	if destWarning != "" {
		text += "\n" + esc(destWarning)
	}
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditEvents(ctx context.Context, cq CallbackQuery, id string, events []string) error {
	rows := [][]InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		payload := editEventsPayload{ID: id, Events: events, ToggleEvent: event}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.toggle", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: checkbox(contains(events, event), eventLabel(event)), CallbackData: callback}})
	}
	if len(events) > 0 {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.save", editEventsPayload{ID: id, Events: events})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Save", CallbackData: callback}})
	}
	backButton, err := b.viewButton(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, []InlineKeyboardButton{backButton})
	return b.respond(ctx, cq, "<b>Edit events</b>\nSelect at least one event, then tap Save.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditBranch(ctx context.Context, cq CallbackQuery, id string) error {
	rows := [][]InlineKeyboardButton{}
	for _, mode := range []string{"all", "default", "selected"} {
		action := "sub.edit.branch.save"
		payload := editBranchPayload{ID: id, BranchMode: mode}
		if mode == "selected" {
			action = "sub.edit.branch.selected"
		}
		callback, err := b.token(ctx, cq.From.ID, action, payload)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: branchLabel(mode, ""), CallbackData: callback}})
	}
	backButton, err := b.viewButton(ctx, cq.From.ID, id)
	if err != nil {
		return err
	}
	rows = append(rows, []InlineKeyboardButton{backButton})
	return b.respond(ctx, cq, "<b>Edit branch filter</b>", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditBranchList(ctx context.Context, cq CallbackQuery, id string, page int) error {
	sub, err := b.store.GetSubscriptionForUser(ctx, cq.From.ID, id)
	if err != nil {
		return b.respond(ctx, cq, "Subscription not found.", backHome())
	}
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, "Connect GitHub first.", backHome())
	}
	branches, err := b.github.ListBranches(ctx, token, sub.RepoFullName)
	if err != nil {
		return b.respond(ctx, cq, esc(b.userMessage(err, "list the branches")), backHome())
	}
	// Back returns to the branch-mode picker for this subscription.
	backCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch", subscriptionPayload{ID: id})
	if err != nil {
		return err
	}
	backRow := []InlineKeyboardButton{{Text: "Back", CallbackData: backCB}}

	if len(branches) == 0 {
		allCB, err := b.token(ctx, cq.From.ID, "sub.edit.branch.save", editBranchPayload{ID: id, BranchMode: "all"})
		if err != nil {
			return err
		}
		rows := [][]InlineKeyboardButton{
			{{Text: "Use all branches", CallbackData: allCB}},
			backRow,
		}
		return b.respond(ctx, cq, "<b>Choose branch</b>\nThis repository has no branches to choose from.", &InlineKeyboardMarkup{InlineKeyboard: rows})
	}

	pages := (len(branches) + branchPageSize - 1) / branchPageSize
	page = clampPage(page, pages)
	start := page * branchPageSize
	end := min(start+branchPageSize, len(branches))

	rows := [][]InlineKeyboardButton{}
	for _, branch := range branches[start:end] {
		payload := editBranchPayload{ID: id, BranchMode: "selected", BranchName: branch.Name}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch.save", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: branch.Name, CallbackData: callback}})
	}
	var nav []InlineKeyboardButton
	for _, step := range []struct {
		text string
		page int
		show bool
	}{
		{"‹ Prev", page - 1, page > 0},
		{"Next ›", page + 1, page < pages-1},
	} {
		if !step.show {
			continue
		}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch.selected", editBranchPayload{ID: id, BranchMode: "selected", Page: step.page})
		if err != nil {
			return err
		}
		nav = append(nav, InlineKeyboardButton{Text: step.text, CallbackData: callback})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, backRow)
	header := "<b>Choose branch</b>"
	if pages > 1 {
		header += fmt.Sprintf("\nPage %d of %d", page+1, pages)
	}
	return b.respond(ctx, cq, header, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) respond(ctx context.Context, cq CallbackQuery, text string, markup *InlineKeyboardMarkup) error {
	if cq.Message.MessageID != 0 {
		err := b.client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, markup)
		// "message is not modified" means the view already shows this state
		// (e.g. a toggle that produced identical text, or a double tap). Treat
		// it as success instead of posting a duplicate message.
		if err == nil || IsMessageNotModified(err) {
			return nil
		}
	}
	_, err := b.client.SendMessage(ctx, cq.From.ID, text, markup)
	return err
}

func (b *Bot) upsertUser(ctx context.Context, user User) error {
	return b.store.UpsertTelegramUser(ctx, db.TelegramUser{
		ID:        user.ID,
		Username:  user.Username,
		FirstName: user.FirstName,
		LastName:  user.LastName,
	})
}

func (b *Bot) accessToken(ctx context.Context, telegramUserID int64) (string, error) {
	conn, err := b.store.GetGitHubConnection(ctx, telegramUserID)
	if err != nil {
		return "", err
	}
	return b.sealer.Decrypt(conn.EncryptedAccessToken)
}

var errNotGroupAdmin = errors.New("not a group admin")

func (b *Bot) requireGroupAdmin(ctx context.Context, chatID, userID int64) error {
	member, err := b.client.GetChatMember(ctx, chatID, userID)
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
func (b *Bot) groupAdminFailure(ctx context.Context, cq CallbackQuery, err error, rerender func() error) (string, error) {
	toast := "You must be a group administrator."
	if !errors.Is(err, errNotGroupAdmin) {
		slog.Error("group admin check failed", "error", err)
		toast = "Couldn't verify group access. Please try again."
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

func backHome() *InlineKeyboardMarkup {
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "Back", CallbackData: "home"}},
	}}
}

func esc(value string) string {
	return html.EscapeString(value)
}

func branchLabel(mode, branch string) string {
	switch mode {
	case "all":
		return "All branches"
	case "default":
		return "Default branch"
	case "selected":
		if branch != "" {
			return branch
		}
		return "Selected branch"
	default:
		return mode
	}
}

// describeDestination returns a human label for a subscription's destination
// and an optional warning when a group destination is no longer reachable
// (the bot was removed or the group is unknown).
func (b *Bot) describeDestination(ctx context.Context, telegramUserID int64, sub db.Subscription) (string, string) {
	if sub.DestinationType == "dm" {
		return "Direct message", ""
	}
	groups, err := b.store.ListKnownGroups(ctx, telegramUserID)
	if err == nil {
		for _, group := range groups {
			if group.ID == sub.DestinationChatID {
				if group.Title != "" {
					return group.Title, ""
				}
				return fmt.Sprintf("Group %d", group.ID), ""
			}
		}
	}
	return "Group (unavailable)", "⚠ Branchy may no longer be in this group, so deliveries here can fail."
}

func shortDestination(sub db.Subscription) string {
	if sub.DestinationType == "dm" {
		return "DM"
	}
	return "group"
}

func statusText(status string) string {
	switch status {
	case "active":
		return "Active"
	case "paused":
		return "Paused"
	default:
		return status
	}
}

func eventLabel(event string) string {
	switch event {
	case "push":
		return "Push"
	case "pull_request":
		return "Pull requests"
	case "release":
		return "Releases"
	default:
		return event
	}
}

func humanEvents(events []string) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = eventLabel(event)
	}
	return out
}

func checkbox(on bool, label string) string {
	if on {
		return "☑ " + label
	}
	return "☐ " + label
}

// botUsername returns the bot's @username, fetched lazily and cached, for
// building t.me deep links. Returns "" if it cannot be resolved.
func (b *Bot) botUsername(ctx context.Context) string {
	if cached, ok := b.username.Load().(string); ok && cached != "" {
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

// userMessage maps an error to text safe to show the user: validation errors
// pass through, everything else is logged and shown as a generic message.
func (b *Bot) userMessage(err error, action string) string {
	var v *subscriptions.ValidationError
	if errors.As(err, &v) {
		return v.Error()
	}
	slog.Error("bot action failed", "action", action, "error", err)
	return "Something went wrong while trying to " + action + ". Please try again."
}

// viewButton builds a "Back" button that returns to a subscription's detail view.
func (b *Bot) viewButton(ctx context.Context, telegramUserID int64, id string) (InlineKeyboardButton, error) {
	callback, err := b.token(ctx, telegramUserID, "sub.view", subscriptionPayload{ID: id})
	if err != nil {
		return InlineKeyboardButton{}, err
	}
	return InlineKeyboardButton{Text: "Back", CallbackData: callback}, nil
}

// stepBackButton returns the destination-picker's Back button: to the
// subscription detail when editing, or to the repository list when creating.
func (b *Bot) stepBackButton(ctx context.Context, telegramUserID int64, edit bool, editID, createTarget string) (InlineKeyboardButton, error) {
	if edit {
		return b.viewButton(ctx, telegramUserID, editID)
	}
	return InlineKeyboardButton{Text: "Back", CallbackData: createTarget}, nil
}

func (b *Bot) branchNavButton(ctx context.Context, telegramUserID int64, draft subDraft, text string, page int) (InlineKeyboardButton, error) {
	next := draft
	next.BranchMode = "selected"
	next.BranchName = ""
	next.BranchPage = page
	callback, err := b.token(ctx, telegramUserID, "sub.branch", next)
	if err != nil {
		return InlineKeyboardButton{}, err
	}
	return InlineKeyboardButton{Text: text, CallbackData: callback}, nil
}

func paginationRow(prefix string, page, pages int) []InlineKeyboardButton {
	if pages <= 1 {
		return nil
	}
	var row []InlineKeyboardButton
	if page > 0 {
		row = append(row, InlineKeyboardButton{Text: "‹ Prev", CallbackData: fmt.Sprintf("%s:%d", prefix, page-1)})
	}
	if page < pages-1 {
		row = append(row, InlineKeyboardButton{Text: "Next ›", CallbackData: fmt.Sprintf("%s:%d", prefix, page+1)})
	}
	return row
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

func isConsumedAction(action string) bool {
	switch action {
	case "sub.branch",
		"sub.branch.selected",
		"sub.status",
		"sub.delete",
		"sub.test",
		"sub.edit.events.save",
		"sub.edit.branch.save",
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
	Repo               github.Repository `json:"repo,omitempty"`
	DestinationType    string            `json:"destination_type,omitempty"`
	DestinationChatID  int64             `json:"destination_chat_id,omitempty"`
	Events             []string          `json:"events,omitempty"`
	ToggleEvent        string            `json:"toggle_event,omitempty"`
	BranchMode         string            `json:"branch_mode,omitempty"`
	BranchName         string            `json:"branch_name,omitempty"`
	BranchPage         int               `json:"branch_page,omitempty"`
	EditSubscriptionID string            `json:"edit_subscription_id,omitempty"`
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
	ID         string `json:"id"`
	BranchMode string `json:"branch_mode"`
	BranchName string `json:"branch_name,omitempty"`
	Page       int    `json:"page,omitempty"`
}

type editDestinationPayload struct {
	ID                string `json:"id"`
	DestinationType   string `json:"destination_type"`
	DestinationChatID int64  `json:"destination_chat_id"`
}
