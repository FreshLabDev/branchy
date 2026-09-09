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

-- Local stand-in for the core language hub. The real one keeps per-bot
-- observations with manual/auto/client ranking across the whole bot fleet; one
-- row per (bot, subject) with manual-beats-client merge is enough here, and it
-- is what lets `docker-compose up` exercise the language screen.
CREATE TABLE IF NOT EXISTS core.user_language (
  bot text NOT NULL,
  subject_id bigint NOT NULL,
  language text NOT NULL,
  source text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (bot, subject_id)
);

CREATE OR REPLACE FUNCTION core.set_language(
  p_bot text, p_scope text, p_subject bigint, p_lang text, p_source text
) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF p_lang IS NULL OR btrim(p_lang) = '' THEN
    RETURN;
  END IF;
  INSERT INTO core.user_language AS ul (bot, subject_id, language, source, updated_at)
  VALUES (p_bot, p_subject, lower(split_part(btrim(p_lang), '-', 1)), p_source, now())
  ON CONFLICT (bot, subject_id) DO UPDATE SET
    language = EXCLUDED.language,
    source = EXCLUDED.source,
    updated_at = now();
END $$;

-- Dropping the manual observation has to leave the client hint behind, or
-- "Follow Telegram" would only mean "forget everything": the real hub keeps the
-- observations side by side and re-resolves, while this stand-in holds one row,
-- so it rebuilds the client row from the language_code core.touch recorded.
CREATE OR REPLACE FUNCTION core.clear_language(
  p_bot text, p_scope text, p_subject bigint
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE v_client text;
BEGIN
  DELETE FROM core.user_language WHERE bot = p_bot AND subject_id = p_subject;
  SELECT lower(split_part(btrim(pe.tg_language_code), '-', 1)) INTO v_client
  FROM core.person pe
  WHERE pe.telegram_user_id = p_subject AND btrim(coalesce(pe.tg_language_code, '')) <> '';
  IF v_client IS NOT NULL THEN
    INSERT INTO core.user_language (bot, subject_id, language, source)
    VALUES (p_bot, p_subject, v_client, 'client');
  END IF;
END $$;

CREATE OR REPLACE FUNCTION core.effective_language(
  p_user bigint, p_chat bigint DEFAULT NULL, p_prefer text DEFAULT 'user'
) RETURNS text LANGUAGE sql STABLE AS $$
  SELECT l.language
  FROM core.user_language l
  WHERE l.subject_id = p_user
  ORDER BY (l.source = 'manual') DESC, l.updated_at DESC
  LIMIT 1;
$$;
