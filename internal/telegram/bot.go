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
}

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
			_, err := b.client.SendMessage(ctx, msg.Chat.ID, "Open a DM with Branchy to configure notifications.", nil)
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
	if err := b.client.AnswerCallbackQuery(ctx, cq.ID, "", false); err != nil {
		slog.Warn("answer callback failed", "error", err)
	}

	switch cq.Data {
	case "home":
		return b.renderHome(ctx, cq)
	case "repo:list":
		return b.renderRepoList(ctx, cq, false)
	case "sub:new":
		return b.renderRepoList(ctx, cq, true)
	case "sub:list":
		return b.renderSubscriptionList(ctx, cq)
	case "test:menu":
		return b.renderTestList(ctx, cq)
	}

	if !strings.HasPrefix(cq.Data, "t:") {
		return nil
	}
	tokenValue := strings.TrimPrefix(cq.Data, "t:")
	token, err := b.store.GetCallbackToken(ctx, cq.From.ID, tokenValue)
	if err != nil {
		return b.respond(ctx, cq, "This action expired. Open the menu again.", backHome())
	}
	if isConsumedAction(token.Action) {
		token, err = b.store.ConsumeCallbackToken(ctx, cq.From.ID, tokenValue)
		if err != nil {
			return b.respond(ctx, cq, "This action already ran. Open the menu again.", backHome())
		}
	}
	return b.handleToken(ctx, cq, token)
}

func (b *Bot) handleToken(ctx context.Context, cq CallbackQuery, token db.CallbackToken) error {
	switch token.Action {
	case "repo.info":
		var payload repoPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderRepoInfo(ctx, cq, payload.Repo)
	case "repo.denied":
		var payload repoPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		text := "<b>" + esc(payload.Repo.FullName) + "</b>\nBranchy can list this repository, but GitHub did not report webhook admin permission for your OAuth token."
		return b.respond(ctx, cq, text, backHome())
	case "sub.repo":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return err
		}
		return b.renderDestinationPicker(ctx, cq, draft, false, "")
	case "sub.dest":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return err
		}
		if draft.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, draft.DestinationChatID, cq.From.ID); err != nil {
				return b.respond(ctx, cq, "Group delivery requires you to be a group administrator.", backHome())
			}
		}
		return b.renderEventPicker(ctx, cq, draft)
	case "sub.events.toggle":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return err
		}
		draft.Events = toggleEvent(draft.Events, draft.ToggleEvent)
		draft.ToggleEvent = ""
		return b.renderEventPicker(ctx, cq, draft)
	case "sub.branch":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return err
		}
		if draft.BranchMode == "selected" && draft.BranchName == "" {
			return b.renderBranchList(ctx, cq, draft, false)
		}
		return b.createSubscription(ctx, cq, draft)
	case "sub.branch.selected":
		var draft subDraft
		if err := decode(token.Payload, &draft); err != nil {
			return err
		}
		return b.createSubscription(ctx, cq, draft)
	case "sub.view":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderSubscription(ctx, cq, payload.ID)
	case "sub.status":
		var payload statusPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if err := b.subs.SetStatus(ctx, cq.From.ID, payload.ID, payload.Status); err != nil {
			return b.respond(ctx, cq, "Could not update subscription status: "+esc(err.Error()), backHome())
		}
		return b.renderSubscription(ctx, cq, payload.ID)
	case "sub.delete":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if err := b.subs.Delete(ctx, cq.From.ID, payload.ID); err != nil {
			return b.respond(ctx, cq, "Could not delete subscription: "+esc(err.Error()), backHome())
		}
		return b.respond(ctx, cq, "Subscription deleted.", backHome())
	case "sub.test":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if err := b.subs.SendTest(ctx, b.client, cq.From.ID, payload.ID); err != nil {
			return b.respond(ctx, cq, "Could not send test notification: "+esc(err.Error()), backHome())
		}
		return b.respond(ctx, cq, "Test notification sent.", backHome())
	case "sub.edit.events":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderEditEvents(ctx, cq, payload.ID, payload.Events)
	case "sub.edit.events.toggle":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		payload.Events = toggleEvent(payload.Events, payload.ToggleEvent)
		payload.ToggleEvent = ""
		return b.renderEditEvents(ctx, cq, payload.ID, payload.Events)
	case "sub.edit.events.save":
		var payload editEventsPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if err := b.subs.SetEvents(ctx, cq.From.ID, payload.ID, payload.Events); err != nil {
			return b.respond(ctx, cq, "Could not update events: "+esc(err.Error()), backHome())
		}
		return b.renderSubscription(ctx, cq, payload.ID)
	case "sub.edit.branch":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderEditBranch(ctx, cq, payload.ID)
	case "sub.edit.branch.save":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if err := b.subs.SetBranch(ctx, cq.From.ID, payload.ID, payload.BranchMode, payload.BranchName); err != nil {
			return b.respond(ctx, cq, "Could not update branch filter: "+esc(err.Error()), backHome())
		}
		return b.renderSubscription(ctx, cq, payload.ID)
	case "sub.edit.branch.selected":
		var payload editBranchPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderEditBranchList(ctx, cq, payload.ID)
	case "sub.edit.dest":
		var payload subscriptionPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		return b.renderDestinationPicker(ctx, cq, subDraft{EditSubscriptionID: payload.ID}, true, payload.ID)
	case "sub.edit.dest.save":
		var payload editDestinationPayload
		if err := decode(token.Payload, &payload); err != nil {
			return err
		}
		if payload.DestinationType == "group" {
			if err := b.requireGroupAdmin(ctx, payload.DestinationChatID, cq.From.ID); err != nil {
				return b.respond(ctx, cq, "Group delivery requires you to be a group administrator.", backHome())
			}
		}
		if err := b.subs.SetDestination(ctx, cq.From.ID, payload.ID, payload.DestinationType, payload.DestinationChatID); err != nil {
			return b.respond(ctx, cq, "Could not update destination: "+esc(err.Error()), backHome())
		}
		return b.renderSubscription(ctx, cq, payload.ID)
	}
	return nil
}

func (b *Bot) mainMenu(ctx context.Context, telegramUserID int64) (string, *InlineKeyboardMarkup, error) {
	connectURL, err := b.oauth.CreateAuthURL(ctx, telegramUserID)
	if err != nil {
		return "", nil, err
	}
	status := "GitHub: not connected"
	if conn, err := b.store.GetGitHubConnection(ctx, telegramUserID); err == nil {
		status = "GitHub: connected as " + conn.GitHubLogin
	}
	text := "<b>Branchy</b>\n" + esc(status)
	return text, &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "Connect GitHub", URL: connectURL}},
		{{Text: "Repositories", CallbackData: "repo:list"}, {Text: "New subscription", CallbackData: "sub:new"}},
		{{Text: "Subscriptions", CallbackData: "sub:list"}, {Text: "Test notification", CallbackData: "test:menu"}},
	}}, nil
}

func (b *Bot) renderHome(ctx context.Context, cq CallbackQuery) error {
	text, markup, err := b.mainMenu(ctx, cq.From.ID)
	if err != nil {
		return err
	}
	return b.respond(ctx, cq, text, markup)
}

func (b *Bot) renderRepoList(ctx context.Context, cq CallbackQuery, subscribeMode bool) error {
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, "Connect GitHub first.", backHome())
	}
	repos, err := b.github.ListRepositories(ctx, token)
	if err != nil {
		return b.respond(ctx, cq, "Could not list repositories: "+esc(err.Error()), backHome())
	}
	if len(repos) == 0 {
		return b.respond(ctx, cq, "No repositories found for this GitHub account.", backHome())
	}

	title := "Repositories"
	if subscribeMode {
		title = "Choose a repository"
	}
	rows := [][]InlineKeyboardButton{}
	limit := min(len(repos), 20)
	for i := 0; i < limit; i++ {
		repo := repos[i]
		action := "repo.info"
		if subscribeMode {
			action = "sub.repo"
		}
		if subscribeMode && !repo.HasAdminPermission {
			action = "repo.denied"
		}
		text := repo.FullName
		if !repo.HasAdminPermission {
			text = text + " (no hook access)"
		}
		callback, err := b.token(ctx, cq.From.ID, action, repoPayload{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: text, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>"+esc(title)+"</b>\nShowing up to 20 repositories.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderRepoInfo(ctx context.Context, cq CallbackQuery, repo github.Repository) error {
	rows := [][]InlineKeyboardButton{}
	if repo.HasAdminPermission {
		callback, err := b.token(ctx, cq.From.ID, "sub.repo", subDraft{Repo: repo})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Create subscription", CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "repo:list"}})
	text := "<b>" + esc(repo.FullName) + "</b>\nDefault branch: " + esc(repo.DefaultBranch)
	if !repo.HasAdminPermission {
		text += "\nWebhook permission: not available"
	}
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderDestinationPicker(ctx context.Context, cq CallbackQuery, draft subDraft, edit bool, editID string) error {
	rows := [][]InlineKeyboardButton{}
	if edit {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.dest.save", editDestinationPayload{ID: editID, DestinationType: "dm", DestinationChatID: cq.From.ID})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "DM", CallbackData: callback}})
	} else {
		draft.DestinationType = "dm"
		draft.DestinationChatID = cq.From.ID
		callback, err := b.token(ctx, cq.From.ID, "sub.dest", draft)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "DM", CallbackData: callback}})
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
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	text := "<b>Choose destination</b>\nGroups appear here after Branchy is added to them."
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEventPicker(ctx context.Context, cq CallbackQuery, draft subDraft) error {
	rows := [][]InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		next := draft
		next.ToggleEvent = event
		label := event
		if contains(next.Events, event) {
			label = "[x] " + label
		} else {
			label = "[ ] " + label
		}
		callback, err := b.token(ctx, cq.From.ID, "sub.events.toggle", next)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	if len(draft.Events) > 0 {
		for _, branchMode := range []string{"all", "default", "selected"} {
			next := draft
			next.BranchMode = branchMode
			next.BranchName = ""
			label := branchLabel(branchMode, "")
			callback, err := b.token(ctx, cq.From.ID, "sub.branch", next)
			if err != nil {
				return err
			}
			rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
		}
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Choose events</b>\nSelect at least one event, then choose a branch filter.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderBranchList(ctx context.Context, cq CallbackQuery, draft subDraft, edit bool) error {
	token, err := b.accessToken(ctx, cq.From.ID)
	if err != nil {
		return b.respond(ctx, cq, "Connect GitHub first.", backHome())
	}
	branches, err := b.github.ListBranches(ctx, token, draft.Repo.FullName)
	if err != nil {
		return b.respond(ctx, cq, "Could not list branches: "+esc(err.Error()), backHome())
	}
	rows := [][]InlineKeyboardButton{}
	limit := min(len(branches), 30)
	for i := 0; i < limit; i++ {
		next := draft
		next.BranchMode = "selected"
		next.BranchName = branches[i].Name
		callback, err := b.token(ctx, cq.From.ID, "sub.branch.selected", next)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: branches[i].Name, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Choose branch</b>\nShowing up to 30 branches.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) createSubscription(ctx context.Context, cq CallbackQuery, draft subDraft) error {
	id, err := b.subs.Create(ctx, cq.From.ID, draft.Repo, draft.DestinationType, draft.DestinationChatID, draft.Events, draft.BranchMode, draft.BranchName)
	if err != nil {
		return b.respond(ctx, cq, "Could not create subscription: "+esc(err.Error()), backHome())
	}
	return b.renderSubscription(ctx, cq, id)
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
		label := sub.RepoFullName + " - " + strings.Join(sub.Events, ",") + " - " + sub.Status
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Subscriptions</b>", &InlineKeyboardMarkup{InlineKeyboard: rows})
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
		rows = append(rows, []InlineKeyboardButton{{Text: sub.RepoFullName, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "home"}})
	return b.respond(ctx, cq, "<b>Send test notification</b>", &InlineKeyboardMarkup{InlineKeyboard: rows})
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
	text := fmt.Sprintf("<b>%s</b>\nStatus: %s\nDestination: %s\nEvents: %s\nBranch: %s",
		esc(sub.RepoFullName),
		esc(sub.Status),
		esc(destinationLabel(sub)),
		esc(strings.Join(sub.Events, ", ")),
		esc(branchLabel(sub.BranchMode, sub.BranchName)),
	)
	return b.respond(ctx, cq, text, &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditEvents(ctx context.Context, cq CallbackQuery, id string, events []string) error {
	rows := [][]InlineKeyboardButton{}
	for _, event := range []string{"push", "pull_request", "release"} {
		payload := editEventsPayload{ID: id, Events: events, ToggleEvent: event}
		label := "[ ] " + event
		if contains(events, event) {
			label = "[x] " + event
		}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.toggle", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: label, CallbackData: callback}})
	}
	if len(events) > 0 {
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.events.save", editEventsPayload{ID: id, Events: events})
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: "Save", CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "sub:list"}})
	return b.respond(ctx, cq, "<b>Edit events</b>", &InlineKeyboardMarkup{InlineKeyboard: rows})
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
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "sub:list"}})
	return b.respond(ctx, cq, "<b>Edit branch filter</b>", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) renderEditBranchList(ctx context.Context, cq CallbackQuery, id string) error {
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
		return b.respond(ctx, cq, "Could not list branches: "+esc(err.Error()), backHome())
	}
	rows := [][]InlineKeyboardButton{}
	limit := min(len(branches), 30)
	for i := 0; i < limit; i++ {
		payload := editBranchPayload{ID: id, BranchMode: "selected", BranchName: branches[i].Name}
		callback, err := b.token(ctx, cq.From.ID, "sub.edit.branch.save", payload)
		if err != nil {
			return err
		}
		rows = append(rows, []InlineKeyboardButton{{Text: branches[i].Name, CallbackData: callback}})
	}
	rows = append(rows, []InlineKeyboardButton{{Text: "Back", CallbackData: "sub:list"}})
	return b.respond(ctx, cq, "<b>Choose branch</b>\nShowing up to 30 branches.", &InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (b *Bot) respond(ctx context.Context, cq CallbackQuery, text string, markup *InlineKeyboardMarkup) error {
	if cq.Message.MessageID != 0 {
		if err := b.client.EditMessageText(ctx, cq.Message.Chat.ID, cq.Message.MessageID, text, markup); err == nil {
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

func (b *Bot) requireGroupAdmin(ctx context.Context, chatID, userID int64) error {
	member, err := b.client.GetChatMember(ctx, chatID, userID)
	if err != nil {
		return err
	}
	if member.Status == "creator" || member.Status == "administrator" {
		return nil
	}
	return errors.New("not a group admin")
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

func destinationLabel(sub db.Subscription) string {
	if sub.DestinationType == "dm" {
		return "DM"
	}
	return "Group " + fmt.Sprint(sub.DestinationChatID)
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
}

type editDestinationPayload struct {
	ID                string `json:"id"`
	DestinationType   string `json:"destination_type"`
	DestinationChatID int64  `json:"destination_chat_id"`
}
