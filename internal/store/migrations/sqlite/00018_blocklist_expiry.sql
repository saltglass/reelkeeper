-- Temporary and permanent blocklist entries.
--
-- blocklist.expires_at: when a temporary entry stops blocking (NULL: the
-- entry is permanent). Dead swarms can come back to life, so releases
-- blocked because a download got stuck are tried again after 30 days; entries
-- the user made, and releases a tracker deleted, never expire (Dan,
-- 2026-10-12). A daily task deletes expired rows.
--
-- blocklist.rule: what blocked it, for the page's filter and its plain-words
-- label: stalled, slow, metadata, missing, import-failed, unregistered (the
-- cleanup rules), removed (the user removed it from the Queue), radarr or
-- sonarr (brought over by an import), or cleanup (cleanup, rule unknown).
--
-- Existing rows are sorted out from their reason text (the wording is in
-- internal/cleanup/rules.go). Cleanup's entries expire 30 days after they
-- were made, except those the tracker deleted; everything else is permanent.
-- Rows already past 30 days simply go on the next daily run.

-- +goose Up
ALTER TABLE blocklist ADD COLUMN expires_at TIMESTAMP;
ALTER TABLE blocklist ADD COLUMN rule TEXT NOT NULL DEFAULT '';

UPDATE blocklist SET rule = CASE
    WHEN reason LIKE 'Cleanup: The tracker says this torrent no longer exists%' THEN 'unregistered'
    WHEN reason LIKE 'Cleanup: qBittorrent reports an error or missing files%' THEN 'missing'
    WHEN reason LIKE 'Cleanup: It has been trying to get the torrent''s details%' THEN 'metadata'
    WHEN reason LIKE 'Cleanup: No progress for%' THEN 'stalled'
    WHEN reason LIKE 'Cleanup: Slower than%' THEN 'slow'
    WHEN reason LIKE 'Cleanup: Its import failed%' THEN 'import-failed'
    WHEN reason LIKE 'Cleanup: %' THEN 'cleanup'
    WHEN reason LIKE 'Blocklisted in %' AND
         (SELECT media_type FROM titles WHERE titles.id = blocklist.title_id) = 'series' THEN 'sonarr'
    WHEN reason LIKE 'Blocklisted in %' THEN 'radarr'
    ELSE 'removed' -- the Queue's remove, with or without the user's own reason
END;

UPDATE blocklist
SET expires_at = strftime('%Y-%m-%d %H:%M:%f', created_at, '+30 days') || '+00:00'
WHERE reason LIKE 'Cleanup: %' AND rule <> 'unregistered';

-- +goose Down
ALTER TABLE blocklist DROP COLUMN rule;
ALTER TABLE blocklist DROP COLUMN expires_at;
