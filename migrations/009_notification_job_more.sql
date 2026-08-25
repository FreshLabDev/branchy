-- SPDX-License-Identifier: Apache-2.0

-- PR More stores a webhook snapshot next to the outbox job. New jobs also
-- supply id from Go so the public card can embed m:<compact-uuid> before INSERT.
-- Existing rows stay valid: NULL more_json means no More button.
ALTER TABLE notification_jobs
  ADD COLUMN IF NOT EXISTS more_json JSONB;
