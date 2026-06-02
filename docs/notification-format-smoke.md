# Notification Format Smoke

This temporary document is used to exercise Branchy notifications during a
manual smoke run.

## Commit Formatting

Branchy should render a branch push as a commit notification, not as a generic
push event.

## Release Formatting

Release notifications should send only the published action and keep the tag,
target branch, release title, and GitHub link readable.

## Escaping Case

The smoke payload intentionally includes text like `<format> & "escape"` so the
Telegram HTML renderer proves that Branchy escapes user-controlled GitHub text.
