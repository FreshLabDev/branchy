# Changelog

All notable Branchy changes are documented here.

Branchy uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

## v1.0.1 - 2026-06-28

### Fixed

- In a group, branchy no longer answers a `/start` that was addressed to a
  different bot (`/start@otherbot`, which Telegram delivers to every bot in the
  group). It now responds only to the bare `/start` or its own
  `/start@<botname>`.

## v1.0.0 - 2026-06-26

First stable release. Branchy watches your GitHub repositories and delivers
push, pull-request and release notifications to Telegram — set up and managed
entirely through the bot, with GitHub OAuth, per-event and branch filters, and
reliable at-least-once delivery through a Postgres outbox.

Promoted from `v1.0.0-rc.1` unchanged, after a clean production soak (no errors,
a real release notification delivered end-to-end). This tag marks the 1.0
milestone after a hardening program across the alpha series and two independent
review passes.

## v1.0.0-rc.1 - 2026-06-25

Release candidate. A second independent review pass closed the last two
lost-notification gaps in the delivery path; everything else was clean.

### Reliability

- Release and pull-request notification bodies are now budgeted by their
  **visible** rendered length, not the raw Markdown slice. Rendering can expand
  the visible text (a Markdown image becomes an `Image:` prefix), so an
  image-heavy body could previously exceed Telegram's 4096-character limit and
  be rejected outright — silently losing the notification with no retry. The
  body is re-rendered to fit, and titles are bounded.
- Subscriptions are matched to incoming webhooks by the stable
  `github_repo_id` instead of the repository's full name. A GitHub repo
  **rename** no longer silently drops every notification for that repo (the hook
  keeps firing under the same id with a new name); the cached name is refreshed.

### Fixed

- `/start@botname` — the form Telegram delivers in groups — is now recognized,
  so the "open me in a DM" prompt fires there.
- A fatal startup error (e.g. the HTTP port already in use) is no longer masked
  as a clean exit when a shutdown signal arrives at the same instant.
- A notification job that exhausts its attempts purely through worker
  lease-expiry is now logged instead of being a silent terminal drop.

## v1.0.0-alpha.4 - 2026-06-25

A multi-dimension review pass: two delivery-path correctness blockers closed
plus a cluster of latent outbox/idempotency hardening, and a round of
notification and UX polish.

### Security

- The webhook rate limiter now runs **after** signature verification, so an
  unsigned flood can no longer drain the shared token bucket and return 429 to
  real GitHub deliveries. Unauthenticated traffic is rejected as 401 without
  touching the limiter.

### Reliability

- Recording a delivery and enqueuing its notification jobs is now a single
  atomic transaction. Previously a crash between the two could leave an
  idempotency marker with no jobs, so a GitHub retry was ignored as a duplicate
  and the notification was lost.
- Notification job terminal/retry updates are fenced on `status = 'processing'`;
  a stale write from a re-leased job is a safe no-op instead of overwriting a
  job another worker already finalized.
- A successful send is recorded under an uncancelable context, so a graceful
  shutdown immediately after delivery no longer leaves the job `processing` and
  re-sends it (a duplicate) on restart.
- A group→supergroup upgrade now auto-pauses the subscription instead of
  enqueuing jobs to the dead chat id forever (`migrate_to_chat_id` is detected).
- A failed Telegram update is re-delivered (the poll offset is not advanced)
  with a short backoff instead of being silently dropped; a persistently failing
  update is given up on after a few attempts so it cannot stall the poll loop.
- Migrations take a session-level advisory lock, so two instances starting
  together cannot double-run a non-idempotent migration.
- `PUBLIC_BASE_URL` is validated as an absolute http(s) URL at startup instead of
  failing far downstream on a malformed value.

### Changed

- Push notifications show as many commits as fit a text budget instead of a
  fixed cap of 10; since GitHub caps a push payload at ~20 commits, real pushes
  now list every commit, with "+N more" only for a pathologically long list.
- PR and release bodies collapse into an expandable quote only when long
  (over ~600 visible characters or ~10 lines, or truncated); short and medium
  notes render in full in a plain quote.
- Re-subscribing to a previously auto-paused configuration clears the stale
  `pause_reason`, so the subscription is no longer shown active with a pause
  warning.

### Fixed

- Deleting a subscription now asks for confirmation first, so a single mistaken
  tap can no longer destroy a subscription and its webhook.
- The bot registers its command menu (`setMyCommands`) and replies to
  unrecognized private-chat input with a hint, instead of staying silent and
  reading as a dead bot.
- A failed test notification keeps the subscription screen (surfaced as a toast)
  instead of ejecting the user to the home menu.
- Markdown inside a GitHub blockquote (links, emphasis, inline code) renders as
  formatted text instead of raw source.
- A declined GitHub OAuth consent surfaces the real reason instead of a
  misleading "missing code or state".

## v1.0.0-alpha.3 - 2026-06-25

Release notes now arrive in full far more often, and container logs no longer
grow without bound.

### Changed

- Release notifications render up to 3500 characters of notes before
  truncating, up from 1800. Telegram allows 4096 visible characters per
  message and the body already sits in an expandable quote, so the previous
  cap cut typical release notes off mid-sentence for no reason; anything past
  the cap still falls back to a "Full release notes" link.
- Both Compose services (`branchy`, `postgres`) rotate their container logs
  (`json-file`, `max-size: 10m`, `max-file: 3`) so logs stay bounded instead
  of accumulating in a single unbounded file — a noisy incident can no longer
  grow the log without limit.

## v1.0.0-alpha.2 - 2026-06-10

Resilience and security hardening prompted by a production DNS outage: the
Telegram polling loop now backs off instead of hammering the network, and the
bot token can no longer leak into container logs through transport errors.

### Security

- Transport-level Telegram errors (DNS failures, connection resets) are
  redacted before logging: net/http embeds the full request URL — including
  the bot token — in its error messages, and these previously reached the
  container log verbatim. API-level errors were already sanitized.
- The webhook endpoint is rate limited (token bucket, default 30 rps with a
  burst of 60; tune via `WEBHOOK_RATE_LIMIT` / `WEBHOOK_RATE_BURST`). Rejected
  requests are counted in the new `branchy_webhooks_rate_limited_total` metric.

### Reliability

- The Telegram polling loop retries with jittered exponential backoff
  (2s → 60s) instead of a constant 2-second sleep, and samples repeated
  identical errors after the first three instead of logging every attempt
  (a 5-minute DNS outage previously produced ~140 identical lines). A
  recovery line reports how many polls failed once the API is reachable again.
- Idempotent Telegram API reads (`getUpdates`, `getMe`) retry transient
  failures — DNS errors, connection resets, 429 and 5xx responses — up to 4
  attempts with a short jittered backoff, honoring `Retry-After`. Sends are
  deliberately not retried at this layer (the outbox already retries them
  with backoff) to avoid double-posting.
- `getUpdates` long polling now gets a deadline derived from the poll
  duration plus headroom; previously the flat 30-second client timeout left
  only 5 seconds of margin over the 25-second server-side poll.
- Graceful shutdown is no longer delayed by the polling retry sleep (it was
  an uninterruptible `time.Sleep`; now it observes context cancellation).

### Changed

- Oversized webhook payloads (over 5 MB, raised from 2 MB) are rejected with
  an explicit `413` and a warning log instead of failing signature
  verification on a silently truncated body.
- Telegram and GitHub API timeouts are tunable via `TELEGRAM_API_TIMEOUT`
  (default `30s`) and `GITHUB_API_TIMEOUT` (default `20s`).

## v1.0.0-alpha.1 - 2026-06-05

First `v1.0.0` pre-release. Hardens the MVP for production: CI/CD, build
provenance, observability, and automatic recovery from lost GitHub or Telegram
access. The supported event set (`push`, `pull_request`, `release`) and the
Telegram-first setup flow are unchanged.

### Added

- Continuous integration (GitHub Actions): build, vet, race tests, `go mod`
  verification, `govulncheck`, Docker image build, and compose validation on
  every push and pull request.
- Release workflow: tagging `v*` builds a version-stamped image, pushes it to
  GHCR, and publishes a GitHub Release from the matching changelog section
  (pre-release tags are marked as pre-releases).
- Build version, commit, and date are stamped into the binary and reported at
  startup and in `/healthz`.
- `/metrics` endpoint (Prometheus text format) with counters for webhook
  deliveries, notification outcomes, Telegram rate limits, and automatic
  subscription pauses.
- Tunable outbox via environment: `OUTBOX_POLL_INTERVAL`, `OUTBOX_BATCH_SIZE`,
  `OUTBOX_SEND_TIMEOUT`, `OUTBOX_LEASE`, `OUTBOX_RETENTION_DAYS`, and
  `NOTIFICATION_MAX_ATTEMPTS`.

### Changed

- Retry backoff is now jittered to avoid synchronized retry waves after an
  outage.
- More structured logs at the webhook, delivery, and subscription lifecycle
  boundaries (no secrets logged).
- Webhook-management errors are clearer: missing admin rights and inaccessible
  repositories now explain the fix.

### Reliability

- A subscription whose destination becomes permanently unreachable (bot blocked,
  kicked, or chat deleted) is paused automatically instead of retrying forever.
- A revoked or expired GitHub token (HTTP 401) is surfaced as a reconnect prompt
  instead of a generic error.

### Migrations

- `007_subscription_pause_reason.sql` adds `subscriptions.pause_reason`
  (defaults to empty; existing rows are unaffected).

## v0.2.0 - 2026-06-05

### Changed

- Promoted the v0.2.0 alpha line to the first stable `v0.2.0` release. The
  headline change since `v0.1.0` is event-specific subscription settings —
  multi-branch filters, pull request action filters, and release-type filters —
  grouped under Advanced settings. There are no runtime behavior changes from
  `v0.2.0-alpha.3` beyond the edit-menu navigation below.
- Editing a subscription now opens a single **Edit** menu — Events,
  Destination, and Advanced settings — replacing the three separate edit
  buttons on the subscription view; each editor returns to that menu on Back.
- The branch-filter screen now confirms with a blue "Done" button.

## v0.2.0-alpha.3 - 2026-06-03

### Fixed

- Opening "Specific branches" during subscription creation and choosing none no
  longer leaves the filter stuck on an empty selection; it falls back to the
  default branch.

### Changed

- Single-select options (branch mode, release type) now use round markers
  (`●`/`○`) and multi-select options (events, pull request actions, branches)
  use square markers (`■`/`□`), replacing the previous checkbox glyphs.

## v0.2.0-alpha.2 - 2026-06-03

### Added

- Accent colors on key inline buttons (Bot API 9.4 `style`): green for the final
  "Create subscription", blue for proceed/save actions, red for "Delete". One
  accented action per screen; older Telegram clients render plain buttons.

### Changed

- Branch-mode buttons now read as actions ("Specific branches", with a selected
  count) instead of the status-like "No branches selected".
- The repository list sorts repositories you can subscribe to first and sinks
  read-only ones to the bottom; their marker now reads "no access" instead of
  "no hook access".

## v0.2.0-alpha.1 - 2026-06-03

### Added

- Subscription setup now includes event-specific settings before creation:
  multi-branch filters for branch-based events, pull request action filters
  (`opened`, `merged`, `closed`), and release filters for stable releases,
  pre-releases, or both. The `opened` pull request filter also covers reopened
  pull requests.

### Changed

- The main Telegram menu no longer shows a separate test notification entry;
  test notifications remain available from each subscription.
- Editing a subscription into a configuration that already exists now shows a
  clear "you already have this subscription" message instead of a generic error.
- `New subscription` is now a full-width bottom action in the connected main
  menu.
- Release-only subscriptions no longer require a branch filter, and release
  webhook delivery is filtered by release type instead of `target_commitish`.
- Subscription detail screens now group branch, pull request, and release
  controls under `Advanced settings`.

### Migrations

- `006_subscription_event_settings.sql` adds multi-branch, pull request action,
  and release-mode columns to subscriptions and rebuilds subscription uniqueness
  around the expanded settings.

## v0.1.0 - 2026-06-03

### Changed

- Promoted the live-tested MVP from alpha to the first public `v0.1.0` release.
  There are no runtime behavior changes from `v0.1.0-alpha.2`.

## v0.1.0-alpha.2 - 2026-06-03

### Fixed

- Archived GitHub repositories are hidden from repository pickers and rejected
  defensively if an old inline callback tries to create a subscription for one.

## v0.1.0-alpha.1 - 2026-06-03

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

- GitHub notifications were redesigned for a clean, GitHub-like Telegram layout:
  one neutral leading icon per type, the repository shown once, branch metadata
  folded inline, the author shown once, and redundant fields removed.
- `push` notifications render as a compact commit list (linked short SHAs) with
  the branch inline, a single "Compare changes" link, and the pusher; tag refs,
  branch deletes, and zero-commit pushes are ignored.
- `pull_request` notifications show the PR number once as a link, the target
  branch, and the author, distinguish merged from closed, and — for
  opened/reopened/ready-for-review — render the PR description from Markdown in
  an expandable quote.
- `release` notifications lead with the linked version instead of tag/target
  plumbing, send only on the `published` action (no `created`/`prereleased`/
  `deleted` noise), and render release notes from common GitHub Markdown
  (headings, lists, task lists, links, autolinks, bold, italic, strikethrough,
  underline, inline code, code blocks, blockquotes, images as links). Long
  bodies collapse into an expandable quote.
- Inline menus no longer post duplicate messages on identical re-renders
  (Telegram "message is not modified" is treated as success).
- Outbox jobs left in `processing` after a worker crash now count the attempt
  on lease expiry and are marked `failed` at `max_attempts` instead of looping
  forever.
- Subscription creation no longer deletes a reactivated pre-existing
  subscription when webhook setup fails.
- Per-repository webhook synchronization is serialized (in-process per repo) so
  the GitHub hook cannot diverge from the database during concurrent edits.
- State cleanup no longer cascade-deletes notification jobs that are still
  pending or processing when their delivery record ages out.
- Group-admin checks are reported honestly: a transient lookup failure is no
  longer shown as "you must be an administrator", and the destination picker is
  re-rendered with fresh tokens so the action can be retried.
- Graceful shutdown waits for the worker and poller goroutines before the
  database pool closes.
- User-facing errors no longer leak raw Go/GitHub/Telegram error strings;
  confirmations are shown as callback toasts.
- Back navigation in create and edit flows returns to the correct previous
  screen.
- Humanized event labels, group titles, and status text in the UI and
  notifications.
- The main menu shows only the Connect GitHub button until a GitHub account is
  connected, instead of offering actions that require a connection.
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

- This is an alpha release intended for early live usage and feedback.
- GitHub App installation flow is intentionally out of scope for this MVP.
