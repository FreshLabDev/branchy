# GitHub Integration

## OAuth

Branchy uses the GitHub OAuth App web application flow with:

- `state`
- PKCE verifier/challenge
- callback path `/oauth/github/callback`
- default scope `repo read:user`

The `repo` scope is broad. It is used for the MVP because an OAuth App needs
repository access to list private repositories and manage repository webhooks.
The future GitHub App installation flow is intentionally out of scope.

OAuth access tokens are encrypted at rest with AES-GCM. The key is derived from
`APP_SECRET`; changing `APP_SECRET` invalidates stored tokens.

## Repository Webhooks

Branchy creates or reuses one webhook per repository. Reuse is based on matching
the configured payload URL:

```text
${PUBLIC_BASE_URL}/webhooks/github
```

Webhook configuration:

- `content_type`: `json`
- `secret`: `GITHUB_WEBHOOK_SECRET`
- `active`: `true`
- events: active subscription event union for that repository

## Signature Validation

The webhook handler reads the raw request body and validates
`X-Hub-Signature-256` with HMAC-SHA256 and constant-time comparison before
parsing the payload.

## Supported Events

- `push`: branch is parsed from `ref` and matched against the subscription's
  branch filter.
- `pull_request`: branch filtering uses the PR base branch. Subscription
  settings can include opened, merged, closed, or any combination of those
  actions.
- `release`: delivery is filtered by release type (`release`, `pre-release`, or
  both). Branch filters do not apply to release events.

Unsupported events return success without notification.
