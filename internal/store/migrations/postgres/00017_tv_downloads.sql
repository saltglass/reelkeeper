-- TV downloads (M7).
--
-- Which episodes a series version wants: episodes airing on or after
-- versions.monitor_from are wanted (NULL: none by default); rows in
-- episode_monitoring override that per episode, both ways (a season turned
-- on, an episode turned off). Specials (season 0) are only ever wanted
-- through a row. Existing series versions get today's date, so only new
-- episodes are wanted (Dan, 2026-10-11).
--
-- download_episodes records which episodes a download is for (one for a
-- single episode, several for a multi-episode release or a season pack).
--
-- episode_searches records when each wanted episode was last searched for
-- (a season pack search counts for every episode it was for), so the
-- scheduled pass spreads TV searches out the way versions.last_search_at
-- does for movies.

-- +goose Up
ALTER TABLE versions ADD COLUMN monitor_from TEXT;
UPDATE versions SET monitor_from = to_char(now(), 'YYYY-MM-DD')
WHERE title_id IN (SELECT id FROM titles WHERE media_type = 'series');

CREATE TABLE episode_monitoring (
    version_id BIGINT NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    episode_id BIGINT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    monitored  BOOLEAN NOT NULL,
    PRIMARY KEY (version_id, episode_id)
);

CREATE TABLE download_episodes (
    download_id BIGINT NOT NULL REFERENCES downloads (id) ON DELETE CASCADE,
    episode_id  BIGINT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    PRIMARY KEY (download_id, episode_id)
);
CREATE INDEX download_episodes_episode ON download_episodes (episode_id);
-- How the Queue names a TV download's episodes ("S02E05", "season 2").
ALTER TABLE downloads ADD COLUMN episodes_label TEXT NOT NULL DEFAULT '';

CREATE TABLE episode_searches (
    version_id  BIGINT NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    episode_id  BIGINT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    searched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (version_id, episode_id)
);

-- +goose Down
DROP TABLE episode_searches;
ALTER TABLE downloads DROP COLUMN episodes_label;
DROP TABLE download_episodes;
DROP TABLE episode_monitoring;
ALTER TABLE versions DROP COLUMN monitor_from;
