# Versioning

Branchy uses SemVer-style versions with pre-release tags while the bot is still
before `v1.0.0`.

## Version Line

The first release line moved through these tags:

```text
v0.1.0-alpha.1  initial open-source MVP code
v0.1.0-alpha.2  archived repository picker guard
v0.1.0-beta.1   first real Telegram/GitHub beta
v0.1.0-rc.1     release candidate after beta fixes
v0.1.0          public MVP release
```

Branchy skipped `beta` and `rc` for this first line because the core Telegram
and GitHub flows were already live-tested during the alpha cycle.

After the public MVP release:

```text
v0.1.1          bug fixes without new behavior
v0.2.0          notable UX, operations, or MVP-compatible feature improvements
v1.0.0          stable production contract after real production usage
```

## Rules

- Use `alpha` while core flows are not proven with real Telegram and GitHub
  credentials.
- Use `beta` after the bot works end to end, but only for limited users.
- Use `rc` when the release is intended to become public and only fixes are
  expected.
- Use patch versions for fixes that do not change product behavior or runtime
  assumptions.
- Use minor versions for visible UX improvements, operational improvements, or
  supported event/delivery changes that remain in MVP scope.
- Do not use `v1.0.0` until the bot has real production history, stable
  deployment practices, and a clear behavior contract.

## Breaking Changes Before v1.0.0

Before `v1.0.0`, Branchy can still change faster than a mature product, but
breaking changes must be explicit when they affect:

- required environment variables
- PostgreSQL schema or migration requirements
- GitHub OAuth scopes
- webhook and outbox delivery behavior
- subscription semantics
- Docker Compose or deployment assumptions
