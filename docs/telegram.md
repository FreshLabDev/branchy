# Telegram Integration

## Commands

`/start` is the only command. It opens the main menu in DM. In groups and
supergroups it is registered as a Bot API 10.2 ephemeral command: Branchy's DM
prompt is visible only to the invoking user. An ordinary non-ephemeral group
`/start` gets no public response. Scope registration retries after transient
Telegram failures. The private reply is sent before the best-effort presence
write so database latency cannot consume the 15-second ephemeral reply window.

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
(`sendRichMessage`) with Rich HTML. Branchy parses GitHub Flavored Markdown,
keeps supported headings, lists, tables, quotes, links, code, task lists, and
media, then serializes a strict allowlist. Raw GitHub HTML and non-HTTP(S) URLs
do not pass through directly: safe formatting tags such as underline,
superscript, and subscript are retained, while scripts, Telegram-specific tags,
unsafe attributes, mentions, and non-HTTP(S) URLs are removed.

The first 50 valid HTTP(S) media items remain visible Rich Message blocks,
including linked Markdown images and supported raw image, video, and audio
tags; later items remain available as links. Rendering also enforces Telegram's
body, block, nesting, and 20-column table limits while keeping tags balanced
and removing implicit HTML5 table wrappers unsupported by Telegram. Short
bodies use Rich HTML blockquotes; long or truncated bodies use a collapsible
`<details>` block.

Each outbox job stores a bounded classic HTML fallback. If Telegram rejects the
preferred payload with a content, permission, size, or method error, Branchy
tries Rich HTML with media replaced by links, then classic HTML, then plain
text. Each attempt gets a fresh timeout. Rate limits, server failures, transport
errors, and unreachable chats keep their normal retry or auto-pause behavior
instead of falling through and risking duplicates. Pending alpha Rich Markdown
jobs are sanitized into Rich HTML before delivery. Bot UI (`/start`, settings,
OAuth replies) still uses classic HTML `sendMessage` / `editMessageText`.

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
