# AGENTS.md

This file is for coding agents working on Branchy. Keep the project minimal, secure, and production-minded.

## Project Shape

- Branchy is one Go service.
- PostgreSQL is the only durable store.
- Telegram uses long polling in the MVP.
- GitHub OAuth callbacks and repository webhooks are served by the same HTTP process.
- Notification delivery uses the durable PostgreSQL outbox, not direct sending inside the webhook handler.

## Hard Product Boundaries

- English UI only.
- `/start` is the only Telegram command.
- All setup and settings use inline buttons.
- Settings are configured in DM.
- Group delivery is allowed only after the bot has seen the group and the user is verified as `creator` or `administrator`.
- MVP events are only `push`, `pull_request`, and `release`.
- Do not add issues, workflow runs, deployments, comments, billing, GitHub App installation flow, or delivery channels other than Telegram unless explicitly requested.

## Security Invariants

- GitHub OAuth tokens must remain encrypted at rest with AES-GCM using `APP_SECRET`.
- Never log Telegram bot tokens, GitHub tokens, OAuth client secrets, webhook secrets, raw Authorization headers, or full Telegram Bot API URLs.
- Verify `X-Hub-Signature-256` over the raw GitHub webhook body before JSON parsing.
- Treat GitHub delivery IDs as idempotency keys.
- OAuth state must be single-use and expire.
- Escape user-controlled text before Telegram HTML formatting.
- Keep Telegram parse mode as HTML and avoid noisy formatting.

## Webhook And Outbox Flow

Webhook handlers should do this:

```text
verify signature -> dedupe GitHub delivery -> create notification_jobs -> return 200
```

The worker should do this:

```text
poll pending jobs with FOR UPDATE SKIP LOCKED -> send Telegram -> mark sent, retry, or failed
```

Temporary Telegram/GitHub failures should retry with `retry_at` and `attempts`. Permanent delivery failures should be marked `failed`.

## Code Style

- Prefer small packages and simple interfaces.
- Use the standard library unless a dependency materially reduces complexity.
- Keep SQL explicit and close to the store method that uses it.
- Avoid broad refactors while changing behavior.
- Add comments only when they explain non-obvious security, concurrency, or protocol behavior.
- Do not introduce generated code or framework glue for simple Telegram/GitHub HTTP calls.

## Migrations

- Add new SQL migrations under `migrations/` with the next numeric prefix.
- Migrations must be safe to run once and tracked by `schema_migrations`.
- Do not edit old migrations after they may have been applied, unless the repo is still explicitly pre-release and the user asks for it.
- Keep `AUTO_MIGRATE=true` useful for local development.

## Versioning

- Follow `docs/versioning.md` for release tags.
- Keep the first release line as `v0.1.0-alpha.1`, `v0.1.0-beta.1`, `v0.1.0-rc.1`, then `v0.1.0`.
- Use patch versions for fixes, minor versions for MVP-compatible product or operations improvements, and reserve `v1.0.0` for a stable production contract.
- Treat required env vars, PostgreSQL schema requirements, OAuth scopes, webhook/outbox semantics, subscription behavior, and deployment assumptions as breaking-sensitive areas before `v1.0.0`.

## Changelog And Releases

- Keep `CHANGELOG.md` updated for notable user-visible, operational, security, migration, and behavior changes.
- When making such changes, add or adjust the matching `CHANGELOG.md` entry in `## Unreleased` as part of the same work.
- If the user explicitly says not to touch the changelog, obey that request and mention the skipped changelog update in the final response.
- Add new unreleased notes under `## Unreleased`; move them into a version section only when preparing the tag.
- Follow `docs/releases.md` for changelog sections, release note shape, and GitHub Release commands.
- Mark `alpha`, `beta`, and `rc` GitHub Releases as pre-release.
- Do not publish a GitHub Release until verification results and release notes match the tagged code.

## Testing

The local machine may not have `go` in `PATH`. Use Docker for verification:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go test ./...
```

Run vet when changing service logic:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go vet ./...
```

For Docker changes, also run:

```sh
/Applications/Docker.app/Contents/Resources/bin/docker compose config
```

## Release Checklist

- OAuth connect works end to end.
- `/start` works without an existing GitHub connection.
- Repo listing works for connected users.
- Subscription create, pause, edit, delete, and test notification work.
- GitHub webhook signature rejection happens before parsing.
- Duplicate GitHub deliveries are skipped.
- Webhook request returns quickly after enqueueing jobs.
- Outbox retries Telegram `429` and temporary failures.
- Deleting a subscription updates or removes the repository webhook event set.
- `/healthz` reports database, Telegram polling freshness, worker freshness, and outbox counts.

## License

Branchy is licensed under Apache-2.0. Preserve the root `LICENSE` and `NOTICE` files and keep public documentation consistent with that license.
