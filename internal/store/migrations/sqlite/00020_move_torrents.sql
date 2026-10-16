-- Moving torrents between download clients (1.3.0).
--
-- download_sources   what Reelkeeper sent to the download client when it grabbed a
--                    release, kept so another client can be given the same
--                    torrent: kind is "torrent" (the .torrent file) or "magnet".
--                    sealed is the content encrypted with the secret key (it holds
--                    the tracker's passkey), the same way indexer passwords are.
--                    A row goes when its torrent leaves the download client, so
--                    only active torrents are kept.
--
-- downloads.client    the type of client holding the torrent (qbittorrent or
--                    transmission). Empty on rows from before 1.3: the client
--                    that is set up.
-- downloads.move_*    how far a move to another client has got, so a restart
--                    can carry on (internal/download/move.go):
--   move_state        empty when the download isn't moving, else its step.
--   move_to           the client type it is moving to.
--   move_old_bytes    the bytes the old client had verified, once it stopped.
--   move_was_paused   whether the user had paused it before the move.
--   move_layout       JSON: each file's exact name and folder before Reelkeeper
--                    renamed anything, so a failed move can put them back.
--   move_started_at   when the download was marked to move (NULL when it isn't
--                    moving). A move that hasn't finished an hour later gives
--                    up, so a client that is gone for good can't hold up
--                    grabbing, imports and cleanup forever.
-- downloads.seed_seconds_offset / uploaded_offset
--                    what the old client(s) already had: seconds seeding and
--                    bytes uploaded. Cleanup adds them to what the new client
--                    reports, since no client lets anyone set its own counters.
-- downloads.moved_at when it last moved to another client (NULL: never).

-- +goose Up
CREATE TABLE download_sources (
    download_id INTEGER PRIMARY KEY REFERENCES downloads (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    sealed      TEXT NOT NULL,
    created_at  TIMESTAMP NOT NULL
);

ALTER TABLE downloads ADD COLUMN client TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN move_state TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN move_to TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN move_old_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN move_was_paused BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE downloads ADD COLUMN move_layout TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN move_started_at TIMESTAMP;
ALTER TABLE downloads ADD COLUMN seed_seconds_offset INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN uploaded_offset INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN moved_at TIMESTAMP;

-- +goose Down
ALTER TABLE downloads DROP COLUMN moved_at;
ALTER TABLE downloads DROP COLUMN uploaded_offset;
ALTER TABLE downloads DROP COLUMN seed_seconds_offset;
ALTER TABLE downloads DROP COLUMN move_started_at;
ALTER TABLE downloads DROP COLUMN move_layout;
ALTER TABLE downloads DROP COLUMN move_was_paused;
ALTER TABLE downloads DROP COLUMN move_old_bytes;
ALTER TABLE downloads DROP COLUMN move_to;
ALTER TABLE downloads DROP COLUMN move_state;
ALTER TABLE downloads DROP COLUMN client;
DROP TABLE download_sources;
