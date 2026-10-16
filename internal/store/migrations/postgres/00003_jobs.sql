-- Background tasks (System → Tasks). Each row is one run of a task: a
-- library scan, a metadata refresh, ... Rows survive restarts: queued runs
-- stay queued, and a run that was interrupted by a restart is re-queued.
--
-- dedupe_key stops the same work being queued twice (for example two scans
-- of one folder); it is cleared when the run finishes.

-- +goose Up
CREATE TABLE jobs (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         TEXT NOT NULL,
    args         JSONB NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL,
    triggered_by TEXT NOT NULL,
    dedupe_key   TEXT,
    progress     INTEGER NOT NULL DEFAULT 0,
    message      TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL,
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX jobs_dedupe ON jobs (dedupe_key);
CREATE INDEX jobs_status ON jobs (status, id);
CREATE INDEX jobs_kind_finished ON jobs (kind, finished_at);

-- +goose Down
DROP TABLE jobs;
