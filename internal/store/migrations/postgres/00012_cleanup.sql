-- Cleanup engine (M5).
--
-- downloads.cleanup_track  what the stuck-download rules remember between
--                          checks (last progress, slow since, strikes),
--                          as JSON (internal/cleanup Track).
-- downloads.limits_set     qBittorrent's own seeding limits have been
--                          lifted for this torrent (Aerie decides when it
--                          stops seeding).
-- cleanup_events           what cleanup did, or would do in dry run, and
--                          why (Activity → Cleanup).
--   action                 removed, would-remove, emptied, would-empty.
--   rule                   seeded, stalled, slow, metadata, unregistered,
--                          missing, import-failed, replaced-files.

-- +goose Up
ALTER TABLE downloads ADD COLUMN cleanup_track JSONB NOT NULL DEFAULT '{}';
ALTER TABLE downloads ADD COLUMN limits_set BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE cleanup_events (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL,
    download_id   BIGINT REFERENCES downloads (id) ON DELETE SET NULL,
    title_id      BIGINT REFERENCES titles (id) ON DELETE SET NULL,
    release_title TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    rule          TEXT NOT NULL,
    message       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX cleanup_events_created ON cleanup_events (created_at);
CREATE INDEX cleanup_events_download ON cleanup_events (download_id, rule, action);

-- +goose Down
DROP TABLE cleanup_events;
ALTER TABLE downloads DROP COLUMN limits_set;
ALTER TABLE downloads DROP COLUMN cleanup_track;
