# Changelog

All notable Branchy changes are documented here.

Branchy uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

### Added

- Initial Branchy MVP implementation:
  - Telegram `/start` main menu with inline-button setup.
  - GitHub OAuth connection with state, PKCE, and encrypted token storage.
  - GitHub repository listing through OAuth.
  - Subscription creation for DM or Telegram group destinations.
  - Event selection for `push`, `pull_request`, and `release`.
  - Branch filters for all branches, default branch, or selected branch.
  - GitHub repository webhook create, reuse, update, and delete behavior.
  - GitHub webhook signature validation with `X-Hub-Signature-256`.
  - Durable PostgreSQL notification outbox and Telegram delivery worker.
  - Subscription view, pause, edit, delete, and test notification actions.
  - Startup SQL migrations tracked in `schema_migrations`.
  - `/healthz` runtime health endpoint.
- Apache-2.0 license under FreshLab.
- Project documentation for architecture, GitHub integration, Telegram behavior,
  versioning, and release process.
- Pagination for the repository and branch pickers (Prev/Next) instead of
  silent truncation.
- Post-OAuth message with an "Open Branchy" button into the connected main menu.
- Periodic cleanup of expired OAuth states and callback tokens, old webhook
  deliveries, and terminal notification jobs.
- App container healthcheck and `restart: unless-stopped` in Docker Compose;
  `ca-certificates` in the runtime image.

### Fixed

- Inline menus no longer post duplicate messages on identical re-renders
  (Telegram "message is not modified" is treated as success).
- Outbox jobs left in `processing` after a worker crash now count the attempt
  on lease expiry and are marked `failed` at `max_attempts` instead of looping
  forever.
- Subscription creation no longer deletes a reactivated pre-existing
  subscription when webhook setup fails.
- Per-repository webhook synchronization is serialized with a PostgreSQL
  advisory lock so the GitHub hook cannot diverge from the database.
- Graceful shutdown waits for the worker and poller goroutines before the
  database pool closes.
- User-facing errors no longer leak raw Go/GitHub/Telegram error strings;
  confirmations are shown as callback toasts.
- Back navigation in create and edit flows returns to the correct previous
  screen.
- Humanized event labels, group titles, and status text in the UI and
  notifications.

- Callback tokens for final subscription creation actions are consumed after use.
- Subscription event arrays are normalized so equivalent event sets are stored in
  one stable order.
- GitHub delivery dedupe runs before webhook JSON parsing and subscription
  lookup.

### Security

- GitHub OAuth tokens are encrypted at rest with AES-GCM.
- GitHub webhook signatures are checked before JSON parsing.
- Telegram notification content is HTML-escaped before sending.
- Telegram API errors avoid exposing bot-token URLs.
- Notification links are restricted to `http(s)` schemes before being placed in
  Telegram messages.
- `/healthz` no longer returns raw database error strings on its public port.
- The `.env.example` `APP_SECRET` placeholder is intentionally too short so an
  unedited copy fails startup validation instead of booting with a known key.

### Migrations

- `001_init.sql` creates the initial Branchy schema.
- `002_notification_jobs.sql` adds the durable notification outbox.
- `003_release_readiness.sql` adds callback token consumption, runtime state,
  and subscription uniqueness.
- `004_normalize_subscription_events.sql` normalizes existing subscription event
  arrays.
- `005_outbox_indexes_and_retention.sql` adds indexes for the outbox
  `processing` claim arm and for retention cleanup.

### Known Limitations

- A real Telegram and GitHub end-to-end smoke test is still required before the
  public MVP announcement.
- GitHub App installation flow is intentionally out of scope for this MVP.

## v0.1.0-alpha.1 - TBD

Planned first open-source MVP code release. Move the relevant `Unreleased`
entries here when the tag is created.
