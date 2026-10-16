-- TV: seasons, episodes and which files hold which episodes.
--
-- titles         series get their network, type (standard or daily) and
--                the episode order they follow: '' for TMDB's aired order,
--                or a TMDB episode group ID such as a DVD order.
-- seasons        TMDB's seasons (0 is Specials): names, dates, posters.
-- episodes       every episode TMDB knows. aired_season/aired_number are
--                TMDB's default numbers; season/number are the numbers in
--                the series' chosen order (the same unless another order is
--                chosen; NULL when that order leaves the episode out).
-- episode_files  which file holds which episode(s): a file can hold two
--                episodes ("S05E01-E02"), an episode can have several files
--                (one per version, or duplicates).
-- media_files    episode_claim: what the scanner read about the file's
--                episode (from its name or its season folder), kept so
--                files can be placed again when the order changes.
--                match_note: a plain-English problem with that placement,
--                or '' when it's fine.

-- +goose Up
ALTER TABLE titles ADD COLUMN network TEXT NOT NULL DEFAULT '';
ALTER TABLE titles ADD COLUMN series_type TEXT NOT NULL DEFAULT '';
ALTER TABLE titles ADD COLUMN last_air_date TEXT NOT NULL DEFAULT '';
ALTER TABLE titles ADD COLUMN episode_order TEXT NOT NULL DEFAULT '';
ALTER TABLE titles ADD COLUMN episode_order_name TEXT NOT NULL DEFAULT '';

CREATE TABLE seasons (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id      INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    season_number INTEGER NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    overview      TEXT NOT NULL DEFAULT '',
    air_date      TEXT NOT NULL DEFAULT '',
    poster_path   TEXT NOT NULL DEFAULT '',
    UNIQUE (title_id, season_number)
);

CREATE TABLE episodes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id      INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    tmdb_id       INTEGER NOT NULL,
    aired_season  INTEGER NOT NULL,
    aired_number  INTEGER NOT NULL,
    season        INTEGER,
    number        INTEGER,
    name          TEXT NOT NULL DEFAULT '',
    overview      TEXT NOT NULL DEFAULT '',
    air_date      TEXT NOT NULL DEFAULT '',
    runtime       INTEGER NOT NULL DEFAULT 0,
    still_path    TEXT NOT NULL DEFAULT '',
    episode_type  TEXT NOT NULL DEFAULT '',
    UNIQUE (title_id, tmdb_id)
);
CREATE INDEX episodes_order ON episodes (title_id, season, number);

CREATE TABLE episode_files (
    episode_id    INTEGER NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    media_file_id INTEGER NOT NULL REFERENCES media_files (id) ON DELETE CASCADE,
    PRIMARY KEY (episode_id, media_file_id)
);
CREATE INDEX episode_files_file ON episode_files (media_file_id);

ALTER TABLE media_files ADD COLUMN episode_claim TEXT NOT NULL DEFAULT '{}';
ALTER TABLE media_files ADD COLUMN match_note TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE media_files DROP COLUMN match_note;
ALTER TABLE media_files DROP COLUMN episode_claim;
DROP TABLE episode_files;
DROP TABLE episodes;
DROP TABLE seasons;
ALTER TABLE titles DROP COLUMN episode_order_name;
ALTER TABLE titles DROP COLUMN episode_order;
ALTER TABLE titles DROP COLUMN last_air_date;
ALTER TABLE titles DROP COLUMN series_type;
ALTER TABLE titles DROP COLUMN network;
