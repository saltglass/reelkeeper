-- Indexers: the trackers and feeds Aerie searches (replacing Prowlarr).
--
-- indexers        one per configured tracker or feed.
--   kind          cardigann (a community tracker definition), torznab,
--                 newznab or knaben.
--   definition    for cardigann: the definition's id ("1337x").
--   settings      the user's values for the definition's settings (JSON),
--                 except secrets: passwords, cookies, API keys and passkeys
--                 are in secrets, encrypted with AERIE_SECRET_KEY.
--   use_solver    send every request through the Cloudflare solver.
--   categories    standard categories to search here (JSON); empty means
--                 movies and TV.
--   seed_ratio / seed_time_minutes / pack_seed_time_minutes
--                 the tracker's seeding rules; cleanup never removes a
--                 torrent before they're met. NULL means none.
--   query_limit / grab_limit
--                 optional daily caps. NULL means no cap.
--   failures, retry_after, last_error...
--                 health: a failing indexer is left alone until retry_after,
--                 longer each time it fails again.
-- indexer_usage   per indexer and day: searches, grabs, failures, and total
--                 response time, for limits and the stats on the page.

-- +goose Up
CREATE TABLE indexers (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT NOT NULL UNIQUE,
    kind                   TEXT NOT NULL,
    definition             TEXT NOT NULL DEFAULT '',
    base_url               TEXT NOT NULL DEFAULT '',
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    priority               INTEGER NOT NULL DEFAULT 25,
    use_solver             BOOLEAN NOT NULL DEFAULT FALSE,
    settings               TEXT NOT NULL DEFAULT '{}',
    secrets                TEXT NOT NULL DEFAULT '',
    categories             TEXT NOT NULL DEFAULT '[]',
    seed_ratio             REAL,
    seed_time_minutes      INTEGER,
    pack_seed_time_minutes INTEGER,
    query_limit            INTEGER,
    grab_limit             INTEGER,
    failures               INTEGER NOT NULL DEFAULT 0,
    retry_after            TIMESTAMP,
    last_error             TEXT NOT NULL DEFAULT '',
    last_success_at        TIMESTAMP,
    last_failure_at        TIMESTAMP,
    created_at             TIMESTAMP NOT NULL,
    updated_at             TIMESTAMP NOT NULL
);

CREATE TABLE indexer_usage (
    indexer_id INTEGER NOT NULL REFERENCES indexers (id) ON DELETE CASCADE,
    day        TEXT NOT NULL,
    queries    INTEGER NOT NULL DEFAULT 0,
    grabs      INTEGER NOT NULL DEFAULT 0,
    failures   INTEGER NOT NULL DEFAULT 0,
    total_ms   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (indexer_id, day)
);

-- +goose Down
DROP TABLE indexer_usage;
DROP TABLE indexers;
