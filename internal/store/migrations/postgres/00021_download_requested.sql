-- Deluge as a third download client (1.4.0).
--
-- downloads.requested   whether somebody asked for this download (a Seerr
--                       request). Reelkeeper put "requested" on the torrent as a
--                       tag, and used the tag to send a moved torrent to the top
--                       of the new client's queue; Deluge has no tags, so
--                       Reelkeeper keeps this on its own record, for every client.
--                       Existing rows are FALSE: for qBittorrent and Transmission
--                       the tag is still read.

-- +goose Up
ALTER TABLE downloads ADD COLUMN requested BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE downloads DROP COLUMN requested;
