-- SPDX-License-Identifier: Apache-2.0
-- LOCAL-DEV ONLY. Provisions a minimal shared `core` schema (person/chat/touch)
-- and the `branchy` schema inside the bundled docker-compose Postgres, so the
-- migrations (which FK into core.person/core.chat and call core.touch) can boot.
-- In production these live in the real shared core-postgres; this file is mounted
-- via /docker-entrypoint-initdb.d/ only for `docker-compose up` local development.
CREATE SCHEMA IF NOT EXISTS core;
CREATE SCHEMA IF NOT EXISTS branchy;

CREATE TABLE IF NOT EXISTS core.person (
  telegram_user_id bigint PRIMARY KEY,
  username text, first_name text, last_name text,
  is_bot boolean NOT NULL DEFAULT false,
  tg_language_code text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS core.chat (
  chat_id bigint PRIMARY KEY,
  type text, title text, username text,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION core.touch(
  p_bot text, p_user_id bigint,
  p_username text DEFAULT NULL, p_first_name text DEFAULT NULL, p_last_name text DEFAULT NULL,
  p_tg_lang text DEFAULT NULL, p_chat_id bigint DEFAULT NULL, p_chat_type text DEFAULT NULL,
  p_chat_title text DEFAULT NULL, p_chat_uname text DEFAULT NULL, p_is_bot boolean DEFAULT false,
  p_at timestamptz DEFAULT now()
) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO core.person AS pe (telegram_user_id, username, first_name, last_name, is_bot, tg_language_code, last_seen_at, updated_at)
  VALUES (p_user_id, p_username, p_first_name, p_last_name, p_is_bot, p_tg_lang, p_at, p_at)
  ON CONFLICT (telegram_user_id) DO UPDATE SET
    username = COALESCE(EXCLUDED.username, pe.username),
    first_name = COALESCE(EXCLUDED.first_name, pe.first_name),
    last_name = COALESCE(EXCLUDED.last_name, pe.last_name),
    is_bot = EXCLUDED.is_bot,
    tg_language_code = COALESCE(EXCLUDED.tg_language_code, pe.tg_language_code),
    last_seen_at = EXCLUDED.last_seen_at, updated_at = EXCLUDED.updated_at;
  IF p_chat_id IS NOT NULL THEN
    INSERT INTO core.chat AS ch (chat_id, type, title, username, last_seen_at, updated_at)
    VALUES (p_chat_id, p_chat_type, p_chat_title, p_chat_uname, p_at, p_at)
    ON CONFLICT (chat_id) DO UPDATE SET
      type = COALESCE(EXCLUDED.type, ch.type),
      title = COALESCE(EXCLUDED.title, ch.title),
      username = COALESCE(EXCLUDED.username, ch.username),
      last_seen_at = EXCLUDED.last_seen_at, updated_at = EXCLUDED.updated_at;
  END IF;
END $$;
