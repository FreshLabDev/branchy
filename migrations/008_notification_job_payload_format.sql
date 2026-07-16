-- Persist both the preferred Bot API Rich HTML payload and a classic HTML
-- fallback. Existing and rollback-era workers continue reading `text`, while
-- upgraded workers use `rich_text` only for explicitly versioned jobs. The
-- default is the alpha.1/alpha.2 format so pre-migration rows and rows inserted
-- by a rolled-back worker remain identifiable and are sanitized by upgraded
-- workers before delivery.
ALTER TABLE notification_jobs
    ADD COLUMN rich_text TEXT,
    ADD COLUMN payload_format TEXT NOT NULL DEFAULT 'rich_markdown_v1'
        CHECK (payload_format IN ('html_v1', 'rich_markdown_v1', 'rich_html_v1'));
