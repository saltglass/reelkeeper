-- Mark routine runs from before jobs.quiet existed (migration 15) as quiet,
-- so System -> Tasks doesn't show weeks of "4 downloading." until they age
-- out. Only messages that clearly mean "found nothing to do" match; runs that
-- imported, removed, matched or saw a failing indexer stay visible. Checked
-- against Dan's database first (2026-10-11): ~1,800 matched, the rest stayed.

-- +goose Up
UPDATE jobs SET quiet = TRUE
WHERE status = 'succeeded' AND NOT quiet AND (
  (kind = 'download.check' AND (
    message = 'Nothing is downloading.'
    OR (message LIKE '% downloading.' AND message NOT LIKE '%,%')))
  OR (kind = 'download.cleanup' AND (
    message LIKE '0 removed, % kept.'
    OR message LIKE 'Dry run: 0 newly marked%'
    OR message = 'Nothing to clean up.'
    OR message LIKE 'Cleanup is off%'))
  OR (kind = 'grab.rss' AND message LIKE 'Read % new releases; 0 matches for % waiting.')
);

-- +goose Down
-- Nothing to undo: quiet only hides runs from the default Tasks list.
SELECT 1;
