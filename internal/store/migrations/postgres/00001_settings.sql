-- Settings edited in the UI. One row per setting group (for example
-- "general" or "security"); value is that group's JSON document.

-- +goose Up
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE settings;
