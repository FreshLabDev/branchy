<h1 align="center">Branchy</h1>

<p align="center"><strong>Clean GitHub notifications in Telegram.</strong><br/>Small Go service for repository events, subscriptions, and durable delivery.</p>

<p align="center">
  <a href="https://github.com/FreshLabDev/branchy/releases"><img src="https://img.shields.io/github/v/release/FreshLabDev/branchy?include_prereleases&sort=semver&style=for-the-badge&label=latest&labelColor=0f172a&color=4c8c4a" alt="latest version"></a>
  <a href="docs/versioning.md"><img src="https://img.shields.io/badge/stable-v1.1.1-4c8c4a?style=for-the-badge&labelColor=0f172a" alt="stable version"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/FreshLabDev/branchy?style=for-the-badge&logo=go&logoColor=white&label=go&labelColor=0f172a&color=00ADD8" alt="go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-334155?style=for-the-badge&labelColor=0f172a" alt="license"></a>
  <a href="https://t.me/branchy_bot"><img src="https://img.shields.io/badge/telegram-%40branchy__bot-26A5E4?style=for-the-badge&logo=telegram&logoColor=white&labelColor=0f172a" alt="telegram bot"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#using-branchy">Using Branchy</a> ·
  <a href="#how-it-works">How It Works</a> ·
  <a href="#configuration">Configuration</a> ·
  <a href="#security">Security</a> ·
  <a href="#docs">Docs</a>
</p>

---

## The Problem

GitHub notifications are useful, but raw webhook delivery becomes noisy fast:
too many event types, duplicated formatting, fragile direct sends, and unclear
group permissions.

Branchy keeps the MVP deliberately narrow:

| Need | Branchy approach |
|:--|:--|
| Focused event stream | Supports only `push`, `pull_request`, and `release` |
| Telegram-first setup | Uses `/start` plus inline buttons, with settings in DM |
| Safe group delivery | Enables groups only after admin or creator verification |
| Reliable sending | Stores notification jobs in PostgreSQL before delivery |
| Clean messages | Renders compact, sanitized Rich HTML with bounded GitHub media |

> **Result:** one small service that turns GitHub activity into readable
> Telegram updates without expanding beyond the MVP.

---

## Status

| Channel | Version | Meaning |
|:--|:--|:--|
| Latest | `v1.2.0-alpha.3` | Pre-release evaluating PR More file tables |
| Stable | `v1.1.1` | Latest non-prerelease tag |

The MVP has been live-tested with Telegram and GitHub. `v1.1.1` is the current
stable release; the `v1.2.0` alpha line evaluates Bot API 10.3 notification
cards, in-message buttons, PR More overlays, and disabled settings controls
before promotion.

---

## Preview

Branchy messages keep one event, one repository, and the useful links up front.

<table>
  <tr>
    <th align="left">Push</th>
    <th align="left">Pull request</th>
    <th align="left">Release</th>
  </tr>
  <tr>
    <td>
      <strong>2 new commits</strong><br/>
      pushed to FreshLabDev/branchy · <code>main</code> · <strong>amtiYo</strong><br/><br/>
      <code>f2a07de</code> fix Telegram layout<br/>
      <code>a4e7f27</code> clarify release flow<br/><br/>
      Open compare
    </td>
    <td>
      <strong>#42 Add branch filters</strong><br/>
      opened in FreshLabDev/branchy · <code>feat → main</code> · <strong>amtiYo</strong><br/><br/>
      Description is rendered as a compact quote.<br/><br/>
      Open pull request · More
    </td>
    <td>
      <strong>v0.1.0</strong><br/>
      Release in FreshLabDev/branchy · <code>v0.1.0</code> · <strong>amtiYo</strong><br/><br/>
      Release notes render from GitHub Markdown.<br/><br/>
      Open release
    </td>
  </tr>
</table>

---

## Quick Start

You need Docker, PostgreSQL, a Telegram bot token from
[BotFather](https://t.me/BotFather), a GitHub OAuth App, and a public HTTPS URL
for OAuth callbacks and webhooks.

```sh
# 1. Copy local configuration
cp .env.example .env

# 2. Fill the required secrets and public URL
$EDITOR .env

# 3. Start Branchy and PostgreSQL
docker compose up --build
```

Create the GitHub OAuth App callback URL with the same public base URL:

```text
${PUBLIC_BASE_URL}/oauth/github/callback
```

Branchy runs startup migrations from `migrations/` and records completed
versions in `schema_migrations`. Keep `AUTO_MIGRATE=true` for local
development.

The bundled `docker compose` is for local development only. It runs a local
PostgreSQL and seeds a minimal shared `core` schema (`deploy/core-init.sql`) so
migrations that reference `core.person` / `core.chat` boot cleanly. In the
shared production deployment Branchy instead connects to the existing
`core-postgres`; use the production deployment procedure in
[`docs/releases.md`](docs/releases.md).

---

## Using Branchy

All user setup is button-driven inside Telegram.

1. Open the bot in DM and send `/start`.
2. Connect GitHub through OAuth.
3. Pick repositories and subscribe to `push`, `pull_request`, or `release`.
4. Configure the selected events: branches for branch-based events, pull request
   actions, and release type.
5. Choose DM delivery or an eligible Telegram group.
6. View, pause, edit, delete, or test subscriptions from the inline menus.

Groups become available only after Branchy has seen the group. Before group
delivery is enabled, Branchy verifies that the Telegram user is a group
`creator` or `administrator`.

In groups, `/start` is registered as a Bot API 10.2 ephemeral command. Its DM
prompt is visible only to the user who invoked it; Branchy never posts a public
fallback into the group.

---

## How It Works

Branchy is one Go service with PostgreSQL as its only durable store. Branchy's
own tables — subscriptions, the notification outbox, OAuth and runtime state —
live in a `branchy` schema. Telegram identity and presence (users and chats) are
delegated to a shared `core` schema (`core.person`, `core.chat`), which Branchy
upserts via `core.touch` before any dependent write. In production that schema
lives in the shared `core-postgres` database; local `docker compose` seeds a
minimal `core` schema so development boots the same way.

```text
telegram poller  -> inline-button UI
http server      -> OAuth callback and GitHub webhooks
outbox worker    -> Telegram delivery and retries
```

Webhook handling is intentionally fast:

```text
verify signature -> dedupe delivery -> enqueue jobs -> return 200
```

Delivery happens outside the webhook request:

```text
poll pending jobs with FOR UPDATE SKIP LOCKED
  -> send Telegram
  -> mark sent, retry, or failed
```

Temporary Telegram or GitHub failures retry with `retry_at` and `attempts`.
Permanent delivery failures are marked `failed`. Subscription changes that
cannot synchronize the repository webhook are compensated back to their prior
database state, followed by a best-effort restoration of the prior hook.

---

## MVP Scope

| Included | Excluded |
|:--|:--|
| GitHub OAuth through an OAuth App | GitHub App installation flow |
| Telegram DM and verified groups | Non-Telegram delivery channels |
| `push`, `pull_request`, `release` | Issues, comments, deployments, workflow runs |
| PostgreSQL outbox delivery | Direct sends inside webhook handlers |
| `/healthz` and `/metrics` operational health | Billing or paid plans |

---

## Configuration

| Variable | Required | Default | Description |
|:--|:--:|:--|:--|
| `DATABASE_URL` | yes | - | PostgreSQL connection string; Branchy's tables live in the `branchy` schema, so a shared database needs `search_path=branchy` (append `options=-csearch_path%3Dbranchy`) |
| `PUBLIC_BASE_URL` | yes | - | Public HTTPS base URL |
| `TELEGRAM_BOT_TOKEN` | yes | - | Bot token from BotFather |
| `GITHUB_CLIENT_ID` | yes | - | GitHub OAuth App client ID |
| `GITHUB_CLIENT_SECRET` | yes | - | GitHub OAuth App client secret |
| `GITHUB_WEBHOOK_SECRET` | yes | - | Secret for GitHub webhook signatures |
| `APP_SECRET` | yes | - | Token encryption secret, 32+ characters |
| `GITHUB_OAUTH_SCOPE` | no | `repo read:user` | OAuth scopes |
| `HTTP_ADDR` | no | `:8080` | HTTP listen address |
| `MIGRATIONS_DIR` | no | `migrations` | Migration directory |
| `AUTO_MIGRATE` | no | `true` | Run migrations on startup |
| `OUTBOX_POLL_INTERVAL` | no | `2s` | Outbox poll interval (Go duration) |
| `OUTBOX_BATCH_SIZE` | no | `20` | Max jobs claimed per poll |
| `OUTBOX_SEND_TIMEOUT` | no | `20s` | Per-message Telegram send timeout |
| `OUTBOX_LEASE` | no | `2m` | Job processing lease duration |
| `OUTBOX_RETENTION_DAYS` | no | `7` | Days to keep terminal jobs and dedupe records |
| `NOTIFICATION_MAX_ATTEMPTS` | no | `5` | Delivery attempts before a job is failed |
| `TELEGRAM_API_TIMEOUT` | no | `30s` | Per-request Telegram API timeout (long polling adds its own headroom) |
| `GITHUB_API_TIMEOUT` | no | `20s` | Per-request GitHub API timeout |
| `WEBHOOK_RATE_LIMIT` | no | `30` | Webhook endpoint rate limit, requests per second |
| `WEBHOOK_RATE_BURST` | no | `60` | Webhook endpoint burst allowance |

The default `repo read:user` scope is broad, but it supports private repository
visibility and repository webhook management through the OAuth App flow.

---

## Security

- GitHub OAuth tokens are encrypted at rest with AES-GCM using `APP_SECRET`.
- GitHub webhook signatures are verified over the raw body before JSON parsing.
- GitHub delivery IDs are treated as idempotency keys.
- OAuth state is single-use and expires.
- GitHub notifications use Telegram Rich Messages (`sendRichMessage`). Branchy
  renders GitHub Markdown to allowlisted Rich HTML and strips unsafe or
  Telegram-specific tags, attributes, and URL schemes before delivery; bot UI
  keeps classic HTML parse mode.
- Notification links are restricted to `http(s)` URLs.
- Logs avoid Telegram bot tokens, GitHub tokens, webhook secrets, OAuth client
  secrets, raw authorization headers, and full Telegram Bot API URLs.

---

## Deployment

Only two public routes are required:

```text
/oauth/github/callback
/webhooks/github
```

Put `HTTP_ADDR` behind a TLS-terminating reverse proxy or tunnel and set
`PUBLIC_BASE_URL` to the matching HTTPS URL.

`/healthz` reports database status, Telegram polling freshness, worker
freshness, outbox counts, and the build version without exposing secrets.
`/metrics` exposes Prometheus counters (webhook deliveries, notification
outcomes, Telegram rate limits, automatic pauses). Both endpoints return counts
only; restrict them at your reverse proxy if you do not want them public.

---

## Testing

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.26.5-alpine go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.26.5-alpine go vet ./...
docker compose config
```

---

## Docs

| Document | Purpose |
|:--|:--|
| [Architecture](docs/architecture.md) | Service structure and core decisions |
| [GitHub integration](docs/github.md) | OAuth, scopes, and repository webhooks |
| [Telegram behavior](docs/telegram.md) | Bot interaction rules and group delivery |
| [Versioning](docs/versioning.md) | Pre-release and stable version line |
| [Release process](docs/releases.md) | Changelog and GitHub Release rules |

---

<p align="center">
  <a href="https://github.com/FreshLabDev/branchy/releases">Releases</a> ·
  <a href="CHANGELOG.md">Changelog</a> ·
  <a href="LICENSE">Apache-2.0</a> ·
  <a href="NOTICE">NOTICE</a>
</p>

<p align="center">
  Branchy is open source software by FreshLab.<br/>
  Copyright 2026 FreshLab.
</p>
