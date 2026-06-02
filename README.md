# Branchy

Branchy is a minimal Telegram bot for GitHub repository notifications.

It connects a Telegram user to GitHub through OAuth, lets them choose repositories they can access, and delivers selected GitHub events to a direct message or to Telegram groups where the bot is installed.

Branchy is intentionally small: one Go service, PostgreSQL for state, Telegram long polling for bot interactions, and HTTP routes for GitHub OAuth callbacks and repository webhooks.

## What It Does

- Opens the setup UI with `/start`.
- Connects GitHub through OAuth.
- Lists GitHub repositories visible to the connected account.
- Creates subscriptions for repository events:
  - `push`
  - `pull_request`
  - `release`
- Delivers notifications to:
  - Telegram DM
  - Telegram groups where Branchy was added
- Supports branch filters:
  - all branches
  - default branch
  - selected branch
- Creates or reuses GitHub repository webhooks.
- Verifies GitHub webhook signatures before parsing payloads.
- Sends concise Telegram messages using HTML parse mode.
- Uses a durable PostgreSQL notification outbox for stable delivery and retries.

## Not In This MVP

Branchy does not implement issues, workflow runs, deployments, comments, billing, GitHub App installation flow, or non-Telegram delivery.

## Architecture

Branchy runs as a single Go service with three main loops:

- Telegram long polling for user interaction.
- An HTTP server for GitHub OAuth and repository webhooks.
- A notification worker that sends queued Telegram messages.

Webhook handling is intentionally fast:

```text
verify signature -> dedupe delivery -> enqueue notification jobs -> return 200
```

Telegram delivery happens through the durable outbox:

```text
poll pending jobs -> send Telegram -> mark sent, retry, or failed
```

More detail is documented in [docs/architecture.md](docs/architecture.md).

## Requirements

- Docker Desktop
- A Telegram bot token from BotFather
- A GitHub OAuth App
- PostgreSQL, provided locally by Docker Compose
- A public URL for local development, usually through a tunnel

The local machine used for this project does not currently have `go` in `PATH`, so the documented test commands run through Docker.

## Local Development

Copy the example environment file:

```sh
cp .env.example .env
```

Fill in the required values:

```text
TELEGRAM_BOT_TOKEN=
GITHUB_CLIENT_ID=
GITHUB_CLIENT_SECRET=
GITHUB_WEBHOOK_SECRET=
APP_SECRET=
PUBLIC_BASE_URL=
```

Create a GitHub OAuth App with this callback URL:

```text
${PUBLIC_BASE_URL}/oauth/github/callback
```

Start Branchy and PostgreSQL:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker compose up --build
```

Branchy applies SQL migrations from `migrations/` on startup and records completed versions in `schema_migrations`. Set `AUTO_MIGRATE=false` only when migrations are handled by release automation.

## Testing

Run the Go test suite through Docker:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go test ./...
```

Run static checks:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go vet ./...
```

Validate Compose configuration:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker compose config
```

## Security Notes

- GitHub OAuth tokens are encrypted with AES-GCM using a key derived from `APP_SECRET`.
- GitHub webhook signatures are verified with HMAC-SHA256 before JSON parsing.
- Telegram notification text is HTML-escaped before sending.
- OAuth state records are single-use and expire.
- Logs avoid Telegram bot tokens, GitHub tokens, webhook secrets, and OAuth secrets.
- GitHub OAuth uses the `repo read:user` scopes for the MVP. This is broad, but it allows private repository visibility and repository hook management through an OAuth App. A GitHub App flow is intentionally out of scope for this version.

## Product Behavior

- `/start` is the only command.
- Setup happens through inline buttons.
- Settings are configured in DM.
- Groups become selectable after Branchy sees that it was added to the group.
- Before enabling group delivery, Branchy verifies that the Telegram user is a group creator or administrator when Telegram provides enough information.
- Users can view, pause, edit, delete, and test subscriptions.

## Documentation

- [docs/architecture.md](docs/architecture.md) explains the service structure and main decisions.
- [docs/github.md](docs/github.md) documents OAuth, scopes, and repository webhooks.
- [docs/telegram.md](docs/telegram.md) documents Telegram interaction rules and group delivery.
- [docs/versioning.md](docs/versioning.md) documents the release version scheme.
- [docs/releases.md](docs/releases.md) documents changelog and GitHub Release rules.

## License

Branchy is open source under the [Apache License 2.0](LICENSE).

Copyright 2026 FreshLab.
