-- Quality profile settings (M4).
--
-- profiles.mode      "simple" (the editor's plain choices, in simple) or
--                    "advanced" (Radarr-style qualities, custom formats and
--                    scores, in advanced).
-- profiles.simple    the simple editor's settings (JSON). '{}' means none
--                    saved yet: Aerie then uses the preset matching the
--                    slug (remux -> Remux, otherwise HD).
-- profiles.advanced  the compiled profile (JSON), used when mode is
--                    "advanced". Switching to advanced copies what the
--                    simple settings compile to, so nothing changes.

-- +goose Up
ALTER TABLE profiles ADD COLUMN mode TEXT NOT NULL DEFAULT 'simple';
ALTER TABLE profiles ADD COLUMN simple JSONB NOT NULL DEFAULT '{}';
ALTER TABLE profiles ADD COLUMN advanced JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE profiles DROP COLUMN advanced;
ALTER TABLE profiles DROP COLUMN simple;
ALTER TABLE profiles DROP COLUMN mode;
