# Release Process

This document explains how Branchy uses `CHANGELOG.md` and GitHub Releases.

## Changelog Rules

- Keep `CHANGELOG.md` as the source of truth for human-readable release history.
- Put unreleased user-visible, operational, security, migration, or behavior
  changes under `## Unreleased`.
- Do not record every small refactor. Record changes that matter to users,
  operators, contributors, or future release decisions.
- Use these sections when relevant:
  - `Added`
  - `Changed`
  - `Fixed`
  - `Security`
  - `Migrations`
  - `Breaking`
  - `Known Limitations`
- Keep entries short and concrete.
- Mention migration filenames in `Migrations`.
- Mention required environment variable, OAuth scope, webhook behavior, outbox
  behavior, or deployment changes explicitly.

## Preparing A Release

1. Finish code and documentation changes.
2. Run the verification commands from `AGENTS.md`.
3. Run a real smoke test for `beta`, `rc`, and public releases.
4. Move relevant `Unreleased` entries into a version section:

   ```text
   ## v0.1.0-alpha.1 - 2026-06-02
   ```

5. Keep an empty `## Unreleased` section at the top for future changes.
6. Write release notes from the version section.
7. Create an annotated git tag.
8. Create a GitHub Release.

## GitHub Release Notes

Use this shape for release notes (same sections as `CHANGELOG.md`):

```text
### Changed

- Short concrete bullets from the version section of CHANGELOG.md.

### Fixed

- ...

### Operations

- Migration notes, env, or deploy notes when relevant.
```

GitHub Release **title** is the version only (`v1.1.1`), not
`Branchy v…`. Copy the matching `CHANGELOG.md` version section into the release
body (skip the `## vX.Y.Z` heading).

For `alpha`, `beta`, and `rc` versions, mark the GitHub Release as pre-release.
For stable tags, publish a normal GitHub Release.

## Commands

Create a pre-release:

```sh
git tag -a v0.1.0-alpha.1 -m "v0.1.0-alpha.1"
git push origin main
git push origin v0.1.0-alpha.1
gh release create v0.1.0-alpha.1 \
  --prerelease \
  --title "v0.1.0-alpha.1" \
  --notes-file /tmp/branchy-release-notes.md
```

Create a stable release:

```sh
git tag -a v1.1.1 -m "v1.1.1"
git push origin main
git push origin v1.1.1
gh release create v1.1.1 \
  --title "v1.1.1" \
  --notes-file /tmp/branchy-release-notes.md
```

Do not publish a release before the release notes, tag, and verification status
all match.

## Production Deployment

Production runs the `branchy` service from `/opt/stacks/branchy` and uses the
shared `core-postgres`. The root `docker-compose.yml` is local-development
configuration and must not be used as a production deployment source.

After the commit and tag are published, deploy only the Branchy stack with the
WS04 deployment workflow. The deploy command snapshots the current compose,
environment, and image ids, waits for health, and rolls back automatically on a
failed health check:

```sh
WS04_HOST=ssh.amdumo.fun ws04 deploy branchy --yes --health-timeout 120
WS04_HOST=ssh.amdumo.fun ws04 stack status branchy
WS04_HOST=ssh.amdumo.fun ws04 audit --stack branchy --since 1h
```

The final check must confirm the released version in `/healthz`, a healthy
container with no restart, fresh Telegram polling and outbox worker timestamps,
and zero pending, processing, or failed notification jobs.
