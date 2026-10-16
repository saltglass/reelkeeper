package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

// sqliteBackupsKept is how many pre-migration SQLite backups are kept.
// Older ones are deleted after a successful new backup.
const sqliteBackupsKept = 3

// MigrateResult describes what Migrate did.
type MigrateResult struct {
	From, To int64
	// Applied is the number of migrations run.
	Applied int
	// Backup is the path of the SQLite backup taken first, if any.
	Backup string
}

// MigrationState is one migration and whether it has been applied.
type MigrationState struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
}

func (s *Store) provider() (*goose.Provider, error) {
	dialect := goose.DialectSQLite3
	if s.Engine == Postgres {
		dialect = goose.DialectPostgres
	}
	return goose.NewProvider(dialect, s.DB.DB, s.migrations)
}

// Migrate applies all pending migrations.
//
// On SQLite, if the database already has a schema and migrations are
// pending, it first copies the database to backupDir so a failed or unwanted
// upgrade can be undone by restoring that file. Postgres isn't backed up here;
// that's the job of the database server's own backups (Databasus).
func (s *Store) Migrate(ctx context.Context, backupDir string) (MigrateResult, error) {
	p, err := s.provider()
	if err != nil {
		return MigrateResult{}, err
	}

	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("read schema version: %w", err)
	}
	res := MigrateResult{From: current, To: current}
	if current >= target {
		return res, nil
	}

	if s.Engine == SQLite && current > 0 && backupDir != "" {
		res.Backup, err = s.backupSQLite(ctx, backupDir, current)
		if err != nil {
			return res, fmt.Errorf("back up before migrating (nothing was changed): %w", err)
		}
	}

	results, err := p.Up(ctx)
	res.Applied = len(results)
	if len(results) > 0 {
		res.To = results[len(results)-1].Source.Version
	}
	if err != nil {
		if res.Backup != "" {
			return res, fmt.Errorf("migrate: %w (a copy of the database from before the upgrade is at %s)", err, res.Backup)
		}
		return res, fmt.Errorf("migrate: %w", err)
	}
	return res, nil
}

// MigrationStatus lists every known migration and whether it has run.
func (s *Store) MigrationStatus(ctx context.Context) ([]MigrationState, error) {
	p, err := s.provider()
	if err != nil {
		return nil, err
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MigrationState, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, MigrationState{
			Version:   st.Source.Version,
			Name:      filepath.Base(st.Source.Path),
			Applied:   st.State == goose.StateApplied,
			AppliedAt: st.AppliedAt,
		})
	}
	return out, nil
}

// backupSQLite writes a consistent copy of the database using VACUUM INTO,
// which is safe while the database is open, then prunes old backups.
func (s *Store) backupSQLite(ctx context.Context, dir string, version int64) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	name := fmt.Sprintf("reelkeeper-pre-migrate-v%d-%s.db", version, time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", err
	}
	pruneBackups(dir, "reelkeeper-pre-migrate-", sqliteBackupsKept)
	return path, nil
}

// pruneBackups keeps the newest `keep` files in dir that start with prefix.
// The names end in a timestamp, which is what they are sorted by. Failure
// to delete an old backup is harmless and ignored.
func pruneBackups(dir, prefix string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return
	}
	// Sort by the timestamp suffix, newest first.
	slices.SortFunc(names, func(a, b string) int {
		return strings.Compare(backupStamp(b), backupStamp(a))
	})
	for _, n := range names[keep:] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// backupStamp returns the "20060102-150405" part of a backup file name.
func backupStamp(name string) string {
	name = strings.TrimSuffix(name, ".db")
	if len(name) < 15 {
		return name
	}
	return name[len(name)-15:]
}

// SchemaVersion returns the newest migration applied to the database.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	p, err := s.provider()
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// MigrateTo applies pending migrations up to and including version, and no
// further. Restoring a backup from an older Reelkeeper uses it: the rows are
// loaded into a database at the backup's own schema version, then the
// remaining migrations bring them up to date.
func (s *Store) MigrateTo(ctx context.Context, version int64) error {
	p, err := s.provider()
	if err != nil {
		return err
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("migrate to version %d: %w", version, err)
	}
	return nil
}

// LatestVersion is the newest migration this build of Reelkeeper knows.
func (s *Store) LatestVersion(ctx context.Context) (int64, error) {
	p, err := s.provider()
	if err != nil {
		return 0, err
	}
	_, target, err := p.GetVersions(ctx)
	return target, err
}
