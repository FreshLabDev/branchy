-- SPDX-License-Identifier: Apache-2.0

-- Records why a subscription is paused so the system can distinguish a user
-- pause (empty reason) from an automatic one. The system auto-pauses a
-- subscription when its Telegram destination becomes permanently unreachable
-- (bot blocked/removed/chat deleted), recording 'telegram_blocked'. The empty
-- default keeps every existing row a user pause.
ALTER TABLE subscriptions
  ADD COLUMN IF NOT EXISTS pause_reason TEXT NOT NULL DEFAULT '';
