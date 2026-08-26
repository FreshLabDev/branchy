# Changelog

All notable Branchy changes are documented here.

Branchy uses SemVer-style versions with pre-release tags before `v1.0.0`. Release
notes should be copied from the relevant changelog section and lightly edited for
GitHub Releases.

## Unreleased

Use this section for changes that are merged but not released yet.

## v1.2.0-alpha.4 - 2026-08-26

Fourth Bot API 10.3 alpha. Long PR and release notes stay inside the folded
body; cards no longer add a second GitHub link after truncation.

### Changed

- Pull request **Read more** and release **Full release notes** links are
  gone. Long notes already collapse into `<details>` or an expandable quote;
  **Open pull request** / **Open release** remain the GitHub links.
- PR and release bodies share the Rich Message sanitizer budget (24k HTML
  runes, 470 blocks, 13 depth, 50 media, 20 table columns, 32_768 for the
  whole card) instead of the tighter 2500/10000 source cuts. No new migration
  or OAuth scope.

## v1.2.0-alpha.3 - 2026-08-25

Third Bot API 10.3 alpha. Pull-request More shows a file diff table instead of
repeating the description.

### Changed

- The More overlay is a thin `#N Title` header, non-zero diff stats (including
  commit count), and a compact `File` / `+` / `−` table. Description, labels,
  reviewers, Commits, and Checks are gone; Open and Files remain.
- File rows are loaded live on tap from GitHub
  `GET /repos/{owner}/{repo}/pulls/{n}/files` with the subscription owner's
  OAuth token. The webhook handler still only enqueues the job. New `more_json`
  rows omit the PR body. No new migration or OAuth scope.

### Security

- More lookup stays scoped to `destination_chat_id`. Patch text is not stored
  or logged. A failed files fetch still sends the overlay header and toasts
  `Could not load the file list.` (or `GitHub access expired.` on GitHub 401)
  without opening settings.

## v1.2.0-alpha.2 - 2026-08-25

Second Bot API 10.3 alpha. Notification cards become title-first, and pull
requests gain a tap-to-open More overlay that is visible only to the tapping
user.

### Added

- Pull request cards include a **More** in-message callback (`style="link"`).
  Tapping it sends a separate ephemeral Rich Message to that user with the
  webhook snapshot (branches, draft, non-zero diff counts, labels/reviewers,
  description) and Open / Files / Commits / Checks URL buttons.
- `notification_jobs.more_json` stores that snapshot. Job ids are generated in
  Go so the More callback can embed `m:` plus a compact UUID.

### Changed

- Notification cards put the event in `h2` (`#7 Title`, `N new commits`,
  release name) and move repository, action, branches, and author into one
  quiet line. The divider is gone.
- Copy is only **Copy SHA** on a single-commit push. Pull request **Copy #N**
  and release **Copy tag** are removed; the release tag stays in the quiet
  line.
- Test notifications stay a short title-first card without More.

### Security

- More lookup is scoped to `destination_chat_id`. Missing, foreign-chat, or
  expired snapshots toast `This snapshot expired.` and do not open settings.
  GitHub body HTML still cannot inject `tg-button` callbacks.

### Operations

- Migration `009_notification_job_more.sql` adds nullable `more_json`. Outbox
  payload format remains `rich_html_v1`. No new environment variables or OAuth
  scopes. Old jobs without `more_json` simply have no More button.

## v1.2.0-alpha.1 - 2026-08-25

First Bot API 10.3 alpha. Notification cards gain in-message Open/Copy buttons
and richer HTML chrome; settings keep incomplete actions visible as disabled
controls.

### Added

- GitHub notifications use Bot API 10.3 in-message action buttons (`<tg-button-row>`)
  for Open compare / Open pull request / Open release and Copy SHA, PR number, or
  tag. Classic HTML fallback still uses ordinary links.
- Settings keyboards show disabled Continue, Create, Save, Done, pagination, and
  the current all/default/release radio instead of hiding those controls
  (Bot API 10.3 `disabled`). The selected-branch radio stays tappable.
- Test notifications use the same Rich HTML card chrome as live events.

### Changed

- Notification cards use Rich HTML headings, dividers, and footers.
- GFM tables are serialized with `bordered striped compact`.
- Long flat PR/release notes fold into `<blockquote expandable>` using inline
  text and `<br>`; truncated or structured bodies still use `<details>`.
- Group `/start` replies send Bot API 10.3 `ephemeral_message_parameters`.
- Classic `sendMessage` / `editMessageText` disable link previews via
  `link_preview_options`.

### Fixed

- Rich bodies are no longer marked truncated just because the shorter classic
  HTML fallback was cut, so complete release notes do not get a false
  “Full release notes” link.
- Unclosed `tg-*` tags in GitHub HTML no longer swallow the rest of the body.
- PR titles in Rich HTML are plain text when an Open button is present, so the
  card does not show two competing GitHub links.
- Commit lists and PR subtitle lines use `<p>` / `<br>` instead of raw newlines.
- Incomplete Done / Save branches controls stay visible as disabled buttons.
- Test-notification fallback no longer retries classic HTML when the chat is
  permanently unreachable.

### Security

- Untrusted GitHub HTML strips `tg-*` tags while keeping their inner text, so
  injected in-message buttons cannot reach delivery and cannot hide later notes.

### Operations

- No database migration, new environment variables, or OAuth scope changes.
- Outbox payload format remains `rich_html_v1`. Classic HTML stays in
  `notification_jobs.text` as the rollback-safe fallback.

## v1.1.1 - 2026-08-09

Patch release for subscription webhook consistency and safer OAuth failure
handling.

### Fixed

- Subscription create, pause, edit, and delete operations now compensate the
  database mutation when GitHub webhook synchronization fails, then attempt to
  restore the previous repository hook configuration.
- Repository webhook event unions and synchronization locks now use the stable
  GitHub repository id, so repository renames cannot split hook state.

### Security

- OAuth callback failures are logged server-side and no longer expose internal
  provider or transport error details in the browser response.

### Operations

- Production deployment documentation now distinguishes the local Compose
  stack from the shared `core-postgres` WS04 deployment.

## v1.1.0 - 2026-07-18

Stable Telegram Bot API 10.2 release. Promoted from `v1.1.0-alpha.3` after a
40-hour error-free production soak and live release-notification tests covering
mixed media, the 50-media boundary, unsafe input sanitizing, and rejected-media
fallback.

### Added

- **Private group `/start`.** The group-scoped command is ephemeral: Branchy's
  DM prompt is visible only to the invoking user. Registration retries after
  transient Telegram failures without delaying HTTP startup or long polling.

### Changed

- GitHub PR and release bodies are rendered as sanitized Bot API Rich HTML.
  GFM headings, lists, tables, quotes, links, code, task lists, and safe inline
  HTML remain available.
- Up to 50 valid HTTP(S) images, videos, or audio items remain visible as Rich
  Message blocks. Overflow media remains reachable as links.
- New outbox jobs retain both preferred Rich HTML and bounded classic HTML, so
  content failures and rollback paths do not lose the notification.

### Fixed

- Rich output is structurally bounded by message size, block count, nesting
  depth, media count, and table width, with balanced HTML after truncation.
- Telegram content or method errors fall back through Rich HTML without media,
  classic HTML, and plain text. Temporary transport, server, and rate-limit
  failures keep normal retry behavior without risking duplicate fallback sends.
- Notification headers preserve the visual line break between repository name
  and event details.
- Ephemeral group replies are sent before the best-effort `core.touch`, so a
  slow database cannot consume Telegram's reply window or cause duplicates.

### Security

- Unsafe tags, Telegram-specific tags, event attributes, mentions, malformed
  structures, and non-HTTP(S) URL schemes are removed before delivery.
- Build and CI toolchains require Go 1.26.5, including the standard-library fix
  for `GO-2026-5856` in `crypto/tls`.

### Migrations

- `008_notification_job_payload_format.sql` adds `rich_text` and versioned
  payload metadata. Existing alpha jobs remain identifiable and are sanitized
  before delivery; rollback-era workers continue using classic `text`.

### Operations

- No new environment variables, OAuth scopes, or webhook-event requirements.
- `AUTO_MIGRATE=true` applies migration `008` during upgrade from `v1.0.3`.
- WS04 production Compose no longer declares the retired standalone
  `branchy-postgres` service; shared `core-postgres` remains the only durable
  store. Historical volume data was preserved for rollback.

## v1.1.0-alpha.3 - 2026-07-16

### Added

- **Private group `/start`.** The group-scoped command now uses Bot API 10.2
  ephemeral commands and replies only to the invoking user with the DM prompt.
  Branchy no longer posts a public group fallback for `/start`. Command scope
  registration and bot username discovery retry after transient Telegram
  failures without delaying HTTP startup or long polling.

### Changed

- **GitHub bodies are rendered to safe Rich HTML inside Branchy.** GFM
  headings, lists, tables, quotes, links, code, task lists, and media remain
  available. Safe inline GFM HTML such as underline, superscript, and subscript
  is retained, while unsafe or Telegram-specific tags, attributes, and URL
  schemes are removed before Telegram sees the payload.
- Up to 50 valid HTTP(S) images, videos, or audio items remain visible as Rich
  Message blocks, including linked Markdown images and supported raw
  `<img>`/`<video>`/`<audio>` media. Media beyond the Telegram limit remains
  available as links.

### Fixed

- Rich content is truncated structurally with balanced HTML and explicit caps
  for message size, block count, nesting depth, media, and table columns.
- A final HTML5 bounds pass no longer leaks implicit `<tbody>`, `<thead>`, or
  `<tfoot>` tags that Telegram Rich HTML does not support.
- Telegram content or method errors now fall back through Rich HTML without
  media, bounded classic HTML, and plain text. Every stage receives a fresh
  timeout; rate limits and temporary transport/server failures still retry
  without risking duplicate fallback sends.
- Ephemeral group `/start` replies are sent before the best-effort `core.touch`,
  so a slow or unavailable database cannot consume Telegram's 15-second reply
  window or trigger duplicate private replies.

### Security

- PR authors and release body contributors can no longer inject
  Telegram-specific rich tags, unsafe links, mentions, or malformed structures
  through webhook body Markdown.
- Build and CI toolchains now require Go 1.26.5, which includes the standard
  library fix for `GO-2026-5856` in `crypto/tls`.

### Migrations

- `008_notification_job_payload_format.sql` adds versioned payload metadata and
  `rich_text`. Existing `v1.1.0-alpha.1/alpha.2` rows remain identifiable as
  Rich Markdown but are converted through the safe Rich HTML sanitizer before
  delivery. New jobs keep classic HTML in `text` for fallback and rollback.

## v1.1.0-alpha.2 - 2026-07-16

### Fixed

- **Notification headers keep a line break under the repo name.** Rich Markdown
  collapses a single `\n`, so the event line (`Release · …`, commit summary,
  PR action) sat on the same visual row as `📦 repo`. Headers now use a blank
  line between the title row and the event row.

## v1.1.0-alpha.1 - 2026-07-16

### Changed

- **GitHub notifications use Telegram Rich Messages.** Delivery switched from
  classic HTML `sendMessage` to Bot API `sendRichMessage` (Rich Markdown).
  Headers still use escaped HTML tags; PR and release bodies are passed as
  GitHub Flavored Markdown so Telegram natively renders headings, lists, tables,
  quotes, code, and media (`![](url)`).
- **Long bodies use `<details>`.** Short notes stay Markdown blockquotes; long
  or truncated notes collapse under a collapsible details summary instead of
  classic `<blockquote expandable>`.
- Soft body caps raised for the rich-message limit (PR ~2500 runes, release
  ~10000 runes) while keeping group notifications scannable.
- Bot UI (`/start`, settings, OAuth replies) is unchanged and still uses HTML
  parse mode.

### Breaking

- **Hard cutover for the outbox payload format.** `notification_jobs.text` is
  now Rich Markdown. Pending jobs enqueued as classic HTML before this deploy
  can fail permanently; drain or fail pending rows before upgrading.

### Operations

- No database migration.
- Ensure the bot can send media in destination groups if release notes include
  remote images (Telegram may embed media blocks from Markdown).

## v1.0.3 - 2026-07-01

### Fixed

- **Local `docker-compose` stack could not boot after the core consolidation.**
  Migration `001` now references the shared `core.person`/`core.chat`, but the
  bundled dev Postgres had no `core` schema, so `docker-compose up` crash-looped
  (`schema "core" does not exist`). Added `deploy/core-init.sql` (a minimal local
  `core` schema + `core.touch`) mounted via `docker-entrypoint-initdb.d`, and set
  `search_path=branchy` on the compose `DATABASE_URL`. Production is unaffected
  (it uses the real shared core-postgres); this only fixes local development.

## v1.0.2 - 2026-07-01

### Changed

- **Shared `core` database.** Branchy's data now lives in the shared
  `core-postgres` under a `branchy` schema, and identity/presence (Telegram
  users and chats) are delegated to the central `core` hub (`core.person` /
  `core.chat`), keyed on the global Telegram id. Domain tables reference
  `core.person(telegram_user_id)` directly; the local `telegram_users` and
  `telegram_chats` tables are dropped. Chat-specific state (bot status, active,
  added-by) moves to a small `chat_state` table that FKs into `core.chat`.
  Identity/presence is upserted via the shared `core.touch` before any dependent
  insert. The bot connects to core-postgres with `search_path=branchy`; its
  dedicated `branchy-postgres` is retired.

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
