-- Upgrades are opt-in for movies already on disk (1.1.0).
--
-- versions.upgrades            FALSE: Reelkeeper keeps the file it has and never
--                              looks for a better copy. The version is still
--                              monitored, so a missing file is downloaded again.
--                              Only meaningful for movie versions; series
--                              versions keep TRUE and ignore it. Existing rows
--                              are TRUE: upgrading installs behave as before.
-- root_folders.upgrade_found   whether movies found in the folder (already on
--                              disk, or imported from Radarr) start with upgrades
--                              on. Existing folders are TRUE (today's behaviour);
--                              folders added from now on get the user's choice,
--                              which defaults to FALSE. Only meaningful for
--                              movie folders.

-- +goose Up
ALTER TABLE versions ADD COLUMN upgrades BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE root_folders ADD COLUMN upgrade_found BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE root_folders DROP COLUMN upgrade_found;
ALTER TABLE versions DROP COLUMN upgrades;
