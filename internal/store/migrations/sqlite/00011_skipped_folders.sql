-- skipped_folders: folders in a media folder that scans leave alone,
-- because a movie or series was deleted from Aerie while its files were
-- kept. Without this the next scan would add it straight back.
--   path   the folder (or loose file) relative to the media folder, as
--          scans see it.
--   title  what it was, for the list under Settings → Media Folders.

-- +goose Up
CREATE TABLE skipped_folders (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    root_folder_id INTEGER NOT NULL REFERENCES root_folders (id) ON DELETE CASCADE,
    path           TEXT NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL,
    UNIQUE (root_folder_id, path)
);

-- +goose Down
DROP TABLE skipped_folders;
