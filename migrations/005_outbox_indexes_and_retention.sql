-- SPDX-License-Identifier: Apache-2.0

-- The outbox claim query also scans rows whose lease expired
-- (status = 'processing' AND locked_until <= now()). The existing
-- idx_notification_jobs_pending partial index only covers the 'pending' arm,
-- so add a matching partial index for the 'processing' arm to avoid a full
-- scan as the table grows.
CREATE INDEX IF NOT EXISTS idx_notification_jobs_processing
  ON notification_jobs (locked_until)
  WHERE status = 'processing';

-- Retention cleanup deletes terminal jobs and old delivery records by time.
CREATE INDEX IF NOT EXISTS idx_notification_jobs_terminal
  ON notification_jobs (updated_at)
  WHERE status IN ('sent', 'failed');

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_received_at
  ON webhook_deliveries (received_at);
