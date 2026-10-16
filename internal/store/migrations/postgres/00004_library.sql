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
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    applies_to TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
INSERT INTO profiles (name, slug, applies_to, created_at) VALUES
    ('HD', 'hd', 'both', now()),
    ('Remux', 'remux', 'movie', now());

CREATE TABLE root_folders (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    path               TEXT NOT NULL UNIQUE,
    media_type         TEXT NOT NULL,
    default_profile_id BIGINT NOT NULL REFERENCES profiles (id),
    manage_files       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL,
    last_scan_at       TIMESTAMPTZ
);

CREATE TABLE titles (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
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
    genres              JSONB NOT NULL DEFAULT '[]',
    collection          TEXT NOT NULL DEFAULT '',
    certification       TEXT NOT NULL DEFAULT '',
    release_date        TEXT NOT NULL DEFAULT '',
    theatrical_release  TEXT NOT NULL DEFAULT '',
    digital_release     TEXT NOT NULL DEFAULT '',
    physical_release    TEXT NOT NULL DEFAULT '',
    poster_path         TEXT NOT NULL DEFAULT '',
    backdrop_path       TEXT NOT NULL DEFAULT '',
    popularity          DOUBLE PRECISION NOT NULL DEFAULT 0,
    vote_average        DOUBLE PRECISION NOT NULL DEFAULT 0,
    alt_titles          JSONB NOT NULL DEFAULT '[]',
    metadata_updated_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL,
    UNIQUE (media_type, tmdb_id)
);
CREATE INDEX titles_sort ON titles (media_type, sort_title);

CREATE TABLE versions (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title_id       BIGINT NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    profile_id     BIGINT NOT NULL REFERENCES profiles (id),
    root_folder_id BIGINT NOT NULL REFERENCES root_folders (id),
    folder         TEXT NOT NULL DEFAULT '',
    monitored      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (title_id, profile_id)
);
CREATE INDEX versions_root ON versions (root_folder_id);

CREATE TABLE media_files (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    version_id     BIGINT NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    root_folder_id BIGINT NOT NULL REFERENCES root_folders (id) ON DELETE CASCADE,
    path           TEXT NOT NULL,
    size           BIGINT NOT NULL,
    modified_at    TIMESTAMPTZ NOT NULL,
    resolution     INTEGER NOT NULL DEFAULT 0,
    source         TEXT NOT NULL DEFAULT '',
    quality        TEXT NOT NULL DEFAULT '',
    edition        TEXT NOT NULL DEFAULT '',
    release_group  TEXT NOT NULL DEFAULT '',
    release_info   JSONB NOT NULL DEFAULT '{}',
    media_info     JSONB NOT NULL DEFAULT '{}',
    probe_error    TEXT NOT NULL DEFAULT '',
    added_at       TIMESTAMPTZ NOT NULL,
    probed_at      TIMESTAMPTZ,
    missing_since  TIMESTAMPTZ,
    UNIQUE (root_folder_id, path)
);
CREATE INDEX media_files_version ON media_files (version_id);

CREATE TABLE scan_items (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    root_folder_id BIGINT NOT NULL REFERENCES root_folders (id) ON DELETE CASCADE,
    path           TEXT NOT NULL,
    status         TEXT NOT NULL,
    title_guess    TEXT NOT NULL DEFAULT '',
    year_guess     INTEGER NOT NULL DEFAULT 0,
    reason         TEXT NOT NULL DEFAULT '',
    candidates     JSONB NOT NULL DEFAULT '[]',
    files          JSONB NOT NULL DEFAULT '[]',
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (root_folder_id, path)
);

-- +goose Down
DROP TABLE scan_items;
DROP TABLE media_files;
DROP TABLE versions;
DROP TABLE titles;
DROP TABLE root_folders;
DROP TABLE profiles;
