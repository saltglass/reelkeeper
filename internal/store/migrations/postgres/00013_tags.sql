-- Tags (M6): labels on movie versions, as Radarr has them. Seerr tags each
-- request with the user who made it ("1 - dan") through the
-- Radarr-compatible API, and Aerie keeps them.
--
-- tags          label is stored lower-case, as Radarr does, and unique.
-- version_tags  which versions carry which tags.

-- +goose Up
CREATE TABLE tags (
    id    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label TEXT NOT NULL UNIQUE
);
CREATE TABLE version_tags (
    version_id BIGINT NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    tag_id     BIGINT NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (version_id, tag_id)
);

-- +goose Down
DROP TABLE version_tags;
DROP TABLE tags;
