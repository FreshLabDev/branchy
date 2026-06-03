-- SPDX-License-Identifier: Apache-2.0

ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS branch_names TEXT[] NOT NULL DEFAULT '{}'::TEXT[],
  ADD COLUMN IF NOT EXISTS pull_request_actions TEXT[] NOT NULL DEFAULT ARRAY['opened', 'merged', 'closed']::TEXT[],
  ADD COLUMN IF NOT EXISTS release_mode TEXT NOT NULL DEFAULT 'all';

-- Drop the old uniqueness index up front. The normalization below rewrites
-- branch_name (e.g. trimming whitespace), which can transiently collide two
-- legacy rows on the old index before the dedup step removes them. Removing the
-- index first lets normalization and dedup run freely; the rebuilt index over
-- the expanded column set is recreated at the end.
DROP INDEX IF EXISTS idx_subscriptions_unique_config;

UPDATE subscriptions
SET branch_names = ARRAY[branch_name]::TEXT[]
WHERE branch_mode = 'selected'
  AND branch_name <> ''
  AND cardinality(branch_names) = 0;

UPDATE subscriptions
SET branch_mode = 'all'
WHERE branch_mode = 'selected'
  AND branch_name = ''
  AND cardinality(branch_names) = 0;

WITH normalized AS (
  SELECT
    id,
    ARRAY(
      SELECT DISTINCT trim(branch)
      FROM unnest(branch_names) AS branch
      WHERE trim(branch) <> ''
      ORDER BY trim(branch)
    )::TEXT[] AS norm_branch_names,
    ARRAY(
      SELECT allowed.action
      FROM (
        VALUES ('opened', 1), ('merged', 2), ('closed', 3)
      ) AS allowed(action, ord)
      WHERE allowed.action = ANY(pull_request_actions)
      ORDER BY allowed.ord
    )::TEXT[] AS norm_pull_request_actions
  FROM subscriptions
)
UPDATE subscriptions s
SET branch_names = CASE
      WHEN s.branch_mode = 'selected' THEN n.norm_branch_names
      ELSE '{}'::TEXT[]
    END,
    pull_request_actions = CASE
      WHEN cardinality(n.norm_pull_request_actions) > 0 THEN n.norm_pull_request_actions
      ELSE ARRAY['opened', 'merged', 'closed']::TEXT[]
    END,
    release_mode = CASE
      WHEN s.release_mode IN ('all', 'releases', 'prereleases') THEN s.release_mode
      ELSE 'all'
    END,
    branch_name = CASE
      WHEN s.branch_mode = 'selected' AND cardinality(n.norm_branch_names) > 0 THEN n.norm_branch_names[1]
      ELSE ''
    END,
    updated_at = now()
FROM normalized n
WHERE s.id = n.id;

-- Safety net: any 'selected' row that normalized down to zero branches (e.g. a
-- legacy whitespace-only branch_name) is demoted to 'all' so the branch_names
-- check constraint below cannot fail the migration.
UPDATE subscriptions
SET branch_mode = 'all',
    branch_name = '',
    branch_names = '{}'::TEXT[],
    updated_at = now()
WHERE branch_mode = 'selected'
  AND cardinality(branch_names) = 0;

WITH ranked AS (
  SELECT
    id,
    row_number() OVER (
      PARTITION BY
        telegram_user_id,
        destination_type,
        destination_chat_id,
        github_repo_id,
        events,
        branch_mode,
        branch_names,
        pull_request_actions,
        release_mode
      ORDER BY updated_at DESC, created_at DESC, id
    ) AS rn
  FROM subscriptions
)
DELETE FROM subscriptions s
USING ranked r
WHERE s.id = r.id
  AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_unique_config
  ON subscriptions (
    telegram_user_id,
    destination_type,
    destination_chat_id,
    github_repo_id,
    events,
    branch_mode,
    branch_names,
    pull_request_actions,
    release_mode
  );

ALTER TABLE subscriptions
  DROP CONSTRAINT IF EXISTS subscriptions_branch_names_check,
  DROP CONSTRAINT IF EXISTS subscriptions_pull_request_actions_check,
  DROP CONSTRAINT IF EXISTS subscriptions_release_mode_check;

ALTER TABLE subscriptions
  ADD CONSTRAINT subscriptions_branch_names_check
    CHECK (
      (branch_mode = 'selected' AND cardinality(branch_names) > 0)
      OR (branch_mode <> 'selected' AND cardinality(branch_names) = 0)
    ),
  ADD CONSTRAINT subscriptions_pull_request_actions_check
    CHECK (
      cardinality(pull_request_actions) > 0
      AND pull_request_actions <@ ARRAY['opened', 'merged', 'closed']::TEXT[]
    ),
  ADD CONSTRAINT subscriptions_release_mode_check
    CHECK (release_mode IN ('all', 'releases', 'prereleases'));
