-- The backfill queue (Dan, 2026-10-12).
--
-- Shows lined up to have their older missing episodes filled in, one at a
-- time. One row per series version. position orders the queue; state is
-- queued, active (the one being worked on; at most one), decision (finished,
-- but episodes are still missing: the "Needs a decision" list) or done.
-- found and missing are the counts when it finished, for the summary;
-- hidden is "Hide" on a show in Needs a decision (its episodes stay wanted).

-- +goose Up
CREATE TABLE backfill (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id  INTEGER NOT NULL UNIQUE REFERENCES versions (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    state       TEXT NOT NULL,
    added_at    TIMESTAMP NOT NULL,
    started_at  TIMESTAMP,
    finished_at TIMESTAMP,
    found       INTEGER NOT NULL DEFAULT 0,
    missing     INTEGER NOT NULL DEFAULT 0,
    hidden      BOOLEAN NOT NULL DEFAULT FALSE
);

-- +goose Down
DROP TABLE backfill;
