-- History: what Aerie did and why (Activity → History).
--
-- history           one row per event, newest shown first.
--   event           searched     a search ran for a version (manual,
--                                automatic or the scheduled pass);
--                                data lists every release found and why
--                                each was or wasn't acceptable.
--                   rss          RSS sync found releases for a version.
--                   would-grab   dry run: the release Aerie would have
--                                grabbed.
--                   grabbed      sent to the download client (0.4.1).
--                   grab-skipped a release was chosen but not grabbed
--                                (for example the daily limit).
--                   (import events arrive in 0.4.1.)
--   title_id/version_id  what it was for; the history goes when the title
--                   does.
--   source_title    the release name, for grab events.
--   message         one plain sentence summing the event up.
--   data            details as JSON.
-- versions.last_search_at  when the version was last searched, so the
--                   scheduled pass searches the longest-waiting first.

-- +goose Up
CREATE TABLE history (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at   TIMESTAMPTZ NOT NULL,
    title_id     BIGINT REFERENCES titles (id) ON DELETE CASCADE,
    version_id   BIGINT REFERENCES versions (id) ON DELETE CASCADE,
    event        TEXT NOT NULL,
    source_title TEXT NOT NULL DEFAULT '',
    indexer      TEXT NOT NULL DEFAULT '',
    quality      TEXT NOT NULL DEFAULT '',
    score        INTEGER NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    data         JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX history_created ON history (created_at);
CREATE INDEX history_title ON history (title_id, created_at);
CREATE INDEX history_version_event ON history (version_id, event, created_at);

ALTER TABLE versions ADD COLUMN last_search_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE versions DROP COLUMN last_search_at;
DROP TABLE history;
