-- The library: what Aerie knows about the movies (and later series) on disk.
--
-- profiles      quality profiles; in M1 just names (rules arrive with the
--               decision engine). Seeded with HD and Remux.
-- root_folders  folders Aerie scans. manage_files stays off while another
--               app (Radarr) still manages them: Aerie then changes nothing.
-- titles        a movie or series and its TMDB metadata, shared by versions
-- versions      one copy of a title in one profile (HD, Remux...)
-- media_files   the video files of a version, with what the name claims and
--               what the file's headers say (JSON)
-- scan_items    folders the scanner couldn't identify for sure; the review
--               screen lists them

-- +goose Up
CREATE TABLE profiles (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    applies_to TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);
INSERT INTO profiles (name, slug, applies_to, created_at) VALUES
    ('HD', 'hd', 'both', CURRENT_TIMESTAMP),
    ('Remux', 'remux', 'movie', CURRENT_TIMESTAMP);

CREATE TABLE root_folders (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    path               TEXT NOT NULL UNIQUE,
    media_type         TEXT NOT NULL,
    default_profile_id INTEGER NOT NULL REFERENCES profiles (id),
    manage_files       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMP NOT NULL,
    last_scan_at       TIMESTAMP
);

CREATE TABLE titles (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type          TEXT NOT NULL,
    tmdb_id             INTEGER NOT NULL,
    imdb_id             TEXT NOT NULL DEFAULT '',
    tvdb_id             INTEGER NOT NULL DEFAULT 0,
    title               TEXT NOT NULL,
    original_title      TEXT NOT NULL DEFAULT '',
    sort_title          TEXT NOT NULL,
    year                INTEGER NOT NULL DEFAULT 0,
    overview            TEXT NOT NULL DEFAULT '',
    runtime             INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT '',
    genres              TEXT NOT NULL DEFAULT '[]',
    collection          TEXT NOT NULL DEFAULT '',
    certification       TEXT NOT NULL DEFAULT '',
    release_date        TEXT NOT NULL DEFAULT '',
    theatrical_release  TEXT NOT NULL DEFAULT '',
    digital_release     TEXT NOT NULL DEFAULT '',
    physical_release    TEXT NOT NULL DEFAULT '',
    poster_path         TEXT NOT NULL DEFAULT '',
    backdrop_path       TEXT NOT NULL DEFAULT '',
    popularity          REAL NOT NULL DEFAULT 0,
    vote_average        REAL NOT NULL DEFAULT 0,
    alt_titles          TEXT NOT NULL DEFAULT '[]',
    metadata_updated_at TIMESTAMP,
    created_at          TIMESTAMP NOT NULL,
    UNIQUE (media_type, tmdb_id)
);
CREATE INDEX titles_sort ON titles (media_type, sort_title);

CREATE TABLE versions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id       INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    profile_id     INTEGER NOT NULL REFERENCES profiles (id),
    root_folder_id INTEGER NOT NULL REFERENCES root_folders (id),
    folder         TEXT NOT NULL DEFAULT '',
    monitored      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMP NOT NULL,
    UNIQUE (title_id, profile_id)
);
CREATE INDEX versions_root ON versions (root_folder_id);

CREATE TABLE media_files (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id     INTEGER NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    root_folder_id INTEGER NOT NULL REFERENCES root_folders (id) ON DELETE CASCADE,
    path           TEXT NOT NULL,
    size           INTEGER NOT NULL,
    modified_at    TIMESTAMP NOT NULL,
    resolution     INTEGER NOT NULL DEFAULT 0,
    source         TEXT NOT NULL DEFAULT '',
    quality        TEXT NOT NULL DEFAULT '',
    edition        TEXT NOT NULL DEFAULT '',
    release_group  TEXT NOT NULL DEFAULT '',
    release_info   TEXT NOT NULL DEFAULT '{}',
    media_info     TEXT NOT NULL DEFAULT '{}',
    probe_error    TEXT NOT NULL DEFAULT '',
    added_at       TIMESTAMP NOT NULL,
    probed_at      TIMESTAMP,
    missing_since  TIMESTAMP,
    UNIQUE (root_folder_id, path)
);
CREATE INDEX media_files_version ON media_files (version_id);

CREATE TABLE scan_items (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    root_folder_id INTEGER NOT NULL REFERENCES root_folders (id) ON DELETE CASCADE,
    path           TEXT NOT NULL,
    status         TEXT NOT NULL,
    title_guess    TEXT NOT NULL DEFAULT '',
    year_guess     INTEGER NOT NULL DEFAULT 0,
    reason         TEXT NOT NULL DEFAULT '',
    candidates     TEXT NOT NULL DEFAULT '[]',
    files          TEXT NOT NULL DEFAULT '[]',
    created_at     TIMESTAMP NOT NULL,
    updated_at     TIMESTAMP NOT NULL,
    UNIQUE (root_folder_id, path)
);

-- +goose Down
DROP TABLE scan_items;
DROP TABLE media_files;
DROP TABLE versions;
DROP TABLE titles;
DROP TABLE root_folders;
DROP TABLE profiles;
