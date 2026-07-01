-- SPDX-License-Identifier: Apache-2.0
-- Identity + presence (telegram users/chats) live in the shared core schema
-- (core.person, core.chat); branchy's domain tables FK into them. This
-- connection points at core-postgres with search_path=branchy (set via
-- DATABASE_URL at deploy time), so unqualified names below resolve to the
-- branchy schema while core.* is schema-qualified.

CREATE TABLE IF NOT EXISTS chat_state (
  chat_id BIGINT PRIMARY KEY REFERENCES core.chat(chat_id),
  bot_status TEXT NOT NULL DEFAULT '',
  active BOOLEAN NOT NULL DEFAULT true,
  added_by_telegram_user_id BIGINT REFERENCES core.person(telegram_user_id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS github_connections (
  telegram_user_id BIGINT PRIMARY KEY REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  github_user_id BIGINT NOT NULL,
  github_login TEXT NOT NULL,
  encrypted_access_token BYTEA NOT NULL,
  token_scope TEXT NOT NULL DEFAULT '',
  connected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oauth_states (
  state TEXT PRIMARY KEY,
  telegram_user_id BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  code_verifier TEXT NOT NULL,
  redirect_uri TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repositories (
  github_repo_id BIGINT PRIMARY KEY,
  full_name TEXT NOT NULL UNIQUE,
  owner TEXT NOT NULL,
  name TEXT NOT NULL,
  private BOOLEAN NOT NULL DEFAULT false,
  default_branch TEXT NOT NULL DEFAULT '',
  html_url TEXT NOT NULL DEFAULT '',
  has_admin_permission BOOLEAN NOT NULL DEFAULT false,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS subscriptions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  telegram_user_id BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  destination_type TEXT NOT NULL CHECK (destination_type IN ('dm', 'group')),
  destination_chat_id BIGINT NOT NULL,
  github_repo_id BIGINT NOT NULL REFERENCES repositories(github_repo_id),
  repo_full_name TEXT NOT NULL,
  events TEXT[] NOT NULL,
  branch_mode TEXT NOT NULL CHECK (branch_mode IN ('all', 'default', 'selected')),
  branch_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repo_hooks (
  github_repo_id BIGINT PRIMARY KEY REFERENCES repositories(github_repo_id) ON DELETE CASCADE,
  full_name TEXT NOT NULL UNIQUE,
  hook_id BIGINT NOT NULL,
  events TEXT[] NOT NULL,
  payload_url TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
  delivery_id TEXT PRIMARY KEY,
  event TEXT NOT NULL,
  repo_full_name TEXT NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS callback_tokens (
  token TEXT PRIMARY KEY,
  telegram_user_id BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  payload JSONB NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_chat_state_added_by ON chat_state(added_by_telegram_user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON subscriptions(telegram_user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_repo_event ON subscriptions(repo_full_name, status);
CREATE INDEX IF NOT EXISTS idx_oauth_states_expiry ON oauth_states(expires_at);
CREATE INDEX IF NOT EXISTS idx_callback_tokens_expiry ON callback_tokens(expires_at);
