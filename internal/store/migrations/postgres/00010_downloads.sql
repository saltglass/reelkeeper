-- Downloads and the blocklist (M4, 0.4.1).
--
-- downloads         one row per torrent Aerie sent to the download client.
--   hash            the torrent's info hash (lower-case hex), how
--                   qBittorrent knows it.
--   status          downloading, imported, import-failed, failed or
--                   removed. Live progress comes from qBittorrent itself.
--   seed_ratio / seed_time_minutes
--                   the tracker's seeding rules when it was grabbed, so
--                   cleanup (M5) never removes it before they're met.
--   imported_path   where the imported file went, relative to its media
--                   folder.
-- blocklist         releases Aerie must never grab again (failed or
--                   removed downloads, or added by hand), per title.

-- +goose Up
CREATE TABLE downloads (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at        TIMESTAMPTZ NOT NULL,
    title_id          BIGINT REFERENCES titles (id) ON DELETE SET NULL,
    version_id        BIGINT REFERENCES versions (id) ON DELETE SET NULL,
    hash              TEXT NOT NULL UNIQUE,
    category          TEXT NOT NULL,
    release_title     TEXT NOT NULL,
    indexer_id        BIGINT REFERENCES indexers (id) ON DELETE SET NULL,
    indexer           TEXT NOT NULL DEFAULT '',
    quality           TEXT NOT NULL DEFAULT '',
    score             INTEGER NOT NULL DEFAULT 0,
    size              BIGINT NOT NULL DEFAULT 0,
    upgrade           BOOLEAN NOT NULL DEFAULT FALSE,
    seed_ratio        DOUBLE PRECISION,
    seed_time_minutes INTEGER,
    status            TEXT NOT NULL,
    message           TEXT NOT NULL DEFAULT '',
    completed_at      TIMESTAMPTZ,
    imported_at       TIMESTAMPTZ,
    imported_path     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX downloads_status ON downloads (status);
CREATE INDEX downloads_version ON downloads (version_id);

CREATE TABLE blocklist (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at    TIMESTAMPTZ NOT NULL,
    title_id      BIGINT NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    release_title TEXT NOT NULL,
    hash          TEXT NOT NULL DEFAULT '',
    indexer       TEXT NOT NULL DEFAULT '',
    quality       TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX blocklist_title ON blocklist (title_id);

-- +goose Down
DROP TABLE blocklist;
DROP TABLE downloads;
