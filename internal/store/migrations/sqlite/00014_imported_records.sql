-- imported_records: records already brought over from another app (Radarr
-- history and blocklist entries), so running an import twice doesn't
-- duplicate them.
--   source       which app and kind, e.g. "Radarr/history".
--   external_id  the record's ID in that app.

-- +goose Up
CREATE TABLE imported_records (
    source      TEXT NOT NULL,
    external_id INTEGER NOT NULL,
    PRIMARY KEY (source, external_id)
);

-- +goose Down
DROP TABLE imported_records;
