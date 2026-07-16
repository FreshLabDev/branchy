# Telegram Integration

## Commands

`/start` is the only command. It opens the main menu in DM.

All setup happens through inline keyboards. Other text messages are ignored so
the bot remains quiet and predictable.

## Groups

Subscription settings are managed in DM. Group delivery works after:

1. The bot is added to a group or supergroup.
2. Branchy receives a `my_chat_member` update and records the group.
3. The user selects that group in DM.
4. Branchy verifies the user is a group `creator` or `administrator` with
   `getChatMember`.

If Telegram cannot verify admin status, Branchy does not save the group
destination.

## Notifications

GitHub notifications use Telegram Bot API **Rich Messages**
(`sendRichMessage`) with Rich Markdown. Header and meta fields use HTML tags
with escaped user-controlled text; PR and release bodies are passed as GitHub
Flavored Markdown so Telegram renders tables, quotes, code, and media natively.

Short bodies are shown as Markdown blockquotes. Long or truncated bodies use a
collapsible `<details>` block. Bot UI (`/start`, settings, OAuth replies) still
uses classic HTML `sendMessage` / `editMessageText`.

Message content stays concise and includes:

- repository
- event type
- actor
- branch
- title or summary
- GitHub link
- optional PR/release body

## Subscription Settings

Subscription creation collects event-specific settings before saving:

- `push` and `pull_request` can use all branches, the default branch, or a
  selected set of branches.
- `pull_request` can deliver opened, merged, closed, or any combination of
  those actions.
- `release` can deliver stable releases, pre-releases, or both.

Release-only subscriptions do not ask for branch settings. Existing
subscriptions expose branch, pull request, and release controls under
`Advanced settings`; test notifications stay on the individual subscription
screen.
