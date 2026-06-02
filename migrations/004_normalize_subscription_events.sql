-- SPDX-License-Identifier: Apache-2.0
WITH normalized AS (
  SELECT
    id,
    telegram_user_id,
    destination_type,
    destination_chat_id,
    github_repo_id,
    branch_mode,
    branch_name,
    ARRAY(
      SELECT DISTINCT event
      FROM unnest(events) AS event
      WHERE event IN ('push', 'pull_request', 'release')
      ORDER BY event
    )::TEXT[] AS norm_events,
    created_at,
    updated_at
  FROM subscriptions
),
ranked AS (
  SELECT
    *,
    row_number() OVER (
      PARTITION BY
        telegram_user_id,
        destination_type,
        destination_chat_id,
        github_repo_id,
        norm_events,
        branch_mode,
        branch_name
      ORDER BY updated_at DESC, created_at DESC, id
    ) AS rn
  FROM normalized
)
DELETE FROM subscriptions s
USING ranked r
WHERE s.id = r.id
  AND r.rn > 1;

WITH normalized AS (
  SELECT
    id,
    ARRAY(
      SELECT DISTINCT event
      FROM unnest(events) AS event
      WHERE event IN ('push', 'pull_request', 'release')
      ORDER BY event
    )::TEXT[] AS norm_events
  FROM subscriptions
)
UPDATE subscriptions s
SET events = n.norm_events,
    updated_at = now()
FROM normalized n
WHERE s.id = n.id
  AND s.events IS DISTINCT FROM n.norm_events;
