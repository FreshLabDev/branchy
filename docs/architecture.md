# Architecture

Branchy is one Go service with three runtime surfaces:

- Telegram long polling for user interaction.
- HTTP routes for GitHub OAuth and GitHub repository webhooks.
- A notification outbox worker that sends queued Telegram messages.

The service stores its durable state in PostgreSQL. Branchy's own tables live in
a `branchy` schema (subscriptions, repositories, hooks, the notification outbox,
OAuth and runtime state), and Telegram identity/presence is delegated to a shared
`core` schema (`core.person`, `core.chat`) that Branchy upserts through the
`SECURITY DEFINER` `core.touch` function before any dependent write. In production
this is the shared `core-postgres` database; local `docker compose` seeds a
minimal `core` schema (`deploy/core-init.sql`) so migrations boot. GitHub webhook
handlers do not send Telegram messages directly; they create durable
`notification_jobs` rows and return quickly. The worker polls pending jobs with
`FOR UPDATE SKIP LOCKED`, sends Telegram messages, and updates job status.

## Packages

- `cmd/branchy`: process wiring and graceful shutdown.
- `internal/config`: environment parsing and required setting checks.
- `internal/db`: PostgreSQL store methods and data types.
- `internal/github`: small GitHub OAuth, REST, repository, branch, and webhook client.
- `internal/oauth`: OAuth state, PKCE, callback handling, and token encryption.
- `internal/telegram`: Telegram Bot API client and inline-button flows.
- `internal/subscriptions`: subscription mutation and repository webhook synchronization.
- `internal/webhooks`: GitHub webhook signature validation, event parsing, filtering, dedupe, and outbox enqueue.
- `internal/outbox`: durable Telegram notification job worker and retry classification.
- `internal/db`: PostgreSQL store methods and startup migration runner.
- `internal/notify`: GitHub event notification formatting.

## Data Flow

1. A user sends `/start` to the bot in DM.
2. The bot renders an inline main menu and creates a short-lived GitHub OAuth
   state for the connect button.
3. GitHub redirects to `/oauth/github/callback`.
4. Branchy exchanges the OAuth code, encrypts the access token, stores the
   connection, and sends a Telegram confirmation.
5. The user creates a subscription in DM with inline buttons.
6. Branchy creates or reuses one repository webhook for the selected repository.
7. GitHub posts events to `/webhooks/github`.
8. Branchy validates the signature, dedupes the GitHub delivery, parses the
   event, applies event-specific subscription filters, renders allowlisted Rich
   HTML plus a classic HTML fallback, creates `notification_jobs` (client-generated
   UUID, optional `more_json` for pull requests), and returns
   `200`.
9. The outbox worker locks pending jobs with `FOR UPDATE SKIP LOCKED`, sends
   Telegram messages, falls back through media-free Rich HTML, classic HTML, and
   plain text when Telegram rejects content, marks successes as `sent`, retries
   temporary failures, and marks permanent failures as `failed`.

## Decisions

- Long polling keeps local development simple and avoids exposing Telegram
  webhook routes.
- One service keeps the codebase small. The durable outbox lives in PostgreSQL
  instead of adding separate queue infrastructure.
- Branchy owns the `branchy` schema and, in production, connects to the shared
  `core-postgres` as a dedicated least-privilege role (`branchy_core`) with
  `search_path=branchy` on a single pool. Domain tables reference shared identity
  in the `core` schema by natural key (Telegram id) and call `core.touch`
  schema-qualified on that same pool — there is no separate `core` connection.
  The old standalone `branchy-postgres` container is retired (its volume kept for
  rollback).
- GitHub webhooks are owned per repository, not per subscription. Active
  subscription events are unioned into the repository hook configuration. When
  the active event union becomes empty, Branchy deletes its matching hook.
  The union and in-process lock are keyed by the stable GitHub repository id,
  so repository renames do not split synchronization state. If a subscription
  mutation cannot synchronize GitHub, Branchy compensates the database change
  and best-effort restores the previous hook configuration before returning the
  error to Telegram.
- GitHub webhook requests validate signature, dedupe delivery IDs, create
  durable notification jobs, and return `200` without waiting for Telegram.
- The notification worker retries Telegram `429` and temporary failures using
  `retry_at` and `attempts` (jittered backoff); permanent Telegram API errors are
  marked failed. Content, permission, size, and method-availability errors step
  through media-free Rich HTML, persisted classic HTML, and plain text. Each
  stage has a fresh timeout, while ambiguous transport failures never fall
  through and risk duplicate delivery.
- When a destination becomes permanently unreachable (bot blocked, kicked, or
  chat deleted), the worker auto-pauses the owning subscription
  (`pause_reason = 'telegram_blocked'`) so it stops queuing jobs that can only
  fail.
- A confirmed-invalid GitHub token (HTTP 401) is surfaced to the user as a
  reconnect prompt; no state is mutated, since a revoked authorization already
  stops GitHub deliveries.
- `/healthz` and `/metrics` report operational health and Prometheus counters
  with counts only (no secrets).
- Telegram API errors are reported by method name, not full bot URL, so the bot
  token is not copied into logs or UI errors. `retry_after` rate-limit responses
  are retried once.
- SQL migrations run at startup by default and are recorded in
  `schema_migrations`.
- Telegram update offset is persisted in `runtime_state`.
- Notification payloads are versioned in the outbox. Existing
  `rich_markdown_v1` rows from `v1.1.0-alpha.1/alpha.2` are converted through
  the current Rich HTML sanitizer at delivery time; new `rich_html_v1` rows
  store Rich HTML separately while the legacy `text` column remains a
  rollback-safe classic HTML payload.
- Group delivery is configured only in DM. Candidate groups are learned from
  `my_chat_member` updates when the bot is added to a group.
- The group-scoped `/start` command is ephemeral under Bot API 10.2. Its DM
  prompt targets only the invoking user via Bot API 10.3
  `ephemeral_message_parameters` and Branchy does not emit a public group
  fallback. Command registration retries independently after transient
  failures, and the reply precedes the best-effort `core.touch` presence write.
- Before saving a group destination, Branchy checks `getChatMember` and requires
  `creator` or `administrator`.
- Telegram callback data uses short tokens stored in PostgreSQL for dynamic
  settings actions. Static menu actions use short literal callback strings.
  Pull-request More uses a static `m:` prefix plus the compact notification
  job UUID and does not go through `callback_tokens` (those rows are bound to
  `core.person` and the tapping user). The handler looks up
  `notification_jobs` by id and `destination_chat_id`, then sends an extra
  ephemeral Rich Message to the tapping user. The public card is not replaced.
  Job ids are generated in Go so the More button can embed them before INSERT;
  `more_json` holds the PR webhook snapshot (NULL for push/release and older
  jobs).
