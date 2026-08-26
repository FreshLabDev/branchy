# Versioning

Branchy uses SemVer-style versions. Stable releases use the `v1.x` line; alpha,
beta, and rc tags are reserved for changes that still need limited live
validation.

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

Current stable line:

```text
v0.1.1          bug fixes without new behavior
v0.2.0          notable UX, operations, or MVP-compatible feature improvements
v1.0.0          first stable production contract
v1.1.0          Telegram Rich Messages and Bot API 10.2 delivery
v1.1.1          webhook consistency and OAuth error-surface fixes
v1.2.0          Bot API 10.3 notification cards, PR More, disabled settings
```

Future releases:

```text
v1.2.x          backward-compatible fixes and operational hardening
v1.3.0          notable MVP-compatible product or operations improvements
v2.0.0          intentionally breaking contract changes
```

## Rules

- Use `alpha`, `beta`, or `rc` only while the corresponding live validation is
  still limited or incomplete.
- Use patch versions for fixes that do not change product behavior or runtime
  assumptions.
- Use minor versions for visible UX improvements, operational improvements, or
  supported event/delivery changes that remain in MVP scope.

## Breaking-Sensitive Areas

Breaking changes must be explicit when they affect:

- required environment variables
- PostgreSQL schema or migration requirements
- GitHub OAuth scopes
- webhook and outbox delivery behavior
- subscription semantics
- Docker Compose or deployment assumptions
