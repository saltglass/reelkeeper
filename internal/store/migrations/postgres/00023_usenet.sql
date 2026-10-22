-- Usenet downloads (2.0).
--
-- downloads.protocol   "torrent" or "usenet". Every existing row is a
--                      torrent. For a Usenet download, downloads.hash holds
--                      the Usenet client's own job ID (SABnzbd's nzo_id)
--                      instead of a torrent's info hash, and downloads.client
--                      the Usenet client's type ("sabnzbd").

-- +goose Up
ALTER TABLE downloads ADD COLUMN protocol TEXT NOT NULL DEFAULT 'torrent';

-- +goose Down
ALTER TABLE downloads DROP COLUMN protocol;
