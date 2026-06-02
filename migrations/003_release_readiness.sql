-- SPDX-License-Identifier: Apache-2.0
ALTER TABLE callback_tokens
  ADD COLUMN IF NOT EXISTS consumed_at TIMESTAMPTZ;

DELETE FROM subscriptions a
USING subscriptions b
WHERE a.ctid < b.ctid
  AND a.telegram_user_id = b.telegram_user_id
  AND a.destination_type = b.destination_type
  AND a.destination_chat_id = b.destination_chat_id
  AND a.github_repo_id = b.github_repo_id
  AND a.events = b.events
  AND a.branch_mode = b.branch_mode
  AND a.branch_name = b.branch_name;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_unique_config
  ON subscriptions (
    telegram_user_id,
    destination_type,
    destination_chat_id,
    github_repo_id,
    events,
    branch_mode,
    branch_name
  );

ALTER TABLE notification_jobs
  DROP CONSTRAINT IF EXISTS notification_jobs_status_check;

ALTER TABLE notification_jobs
  ADD CONSTRAINT notification_jobs_status_check
  CHECK (status IN ('pending', 'processing', 'sent', 'failed'));

ALTER TABLE notification_jobs
  ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS runtime_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

