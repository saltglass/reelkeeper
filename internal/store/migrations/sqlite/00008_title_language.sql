-- titles.original_language: the movie's or series' original language as
-- TMDB gives it (ISO 639-1, such as "en" or "fr"). Quality profiles can
-- accept only the original language, and a release whose name states no
-- language is taken to be in it.
--
-- Existing titles are marked for a metadata refresh so the daily refresh
-- task fills it in.

-- +goose Up
ALTER TABLE titles ADD COLUMN original_language TEXT NOT NULL DEFAULT '';
UPDATE titles SET metadata_updated_at = NULL;

-- +goose Down
ALTER TABLE titles DROP COLUMN original_language;
