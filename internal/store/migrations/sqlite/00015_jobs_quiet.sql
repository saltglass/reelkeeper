-- jobs.quiet: a routine run that found nothing to do (a download check with
-- nothing finished, say). System -> Tasks hides these unless asked.

-- +goose Up
ALTER TABLE jobs ADD COLUMN quiet BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE jobs DROP COLUMN quiet;
