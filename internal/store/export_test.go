package store

import (
	"fmt"
	"io/fs"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// Test hooks. This file is compiled only into this package's tests.

var PruneBackupsForTest = pruneBackups

// UseMigrationsForTest makes s use fsys as its migration folder.
func UseMigrationsForTest(s *Store, fsys fs.FS) { s.migrations = fsys }

// MigrationsUpToForTest returns engine's real migrations, up to and
// including version max, so a test can stop part-way and add old data.
func MigrationsUpToForTest(engine Engine, maxVersion int) fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations/"+string(engine))
	if err != nil {
		panic(err)
	}
	out := fstest.MapFS{}
	entries, _ := fs.ReadDir(sub, ".")
	for _, e := range entries {
		var v int
		if _, err := fmt.Sscanf(e.Name(), "%05d_", &v); err != nil || v > maxVersion {
			continue
		}
		data, _ := fs.ReadFile(sub, e.Name())
		out[e.Name()] = &fstest.MapFile{Data: data}
	}
	return out
}

// AllMigrationsForTest returns engine's real migrations.
func AllMigrationsForTest(engine Engine) fs.FS { return MigrationsUpToForTest(engine, 1<<30) }

// ProviderForTest exposes the migration provider, to run migrations down.
func (s *Store) ProviderForTest() (*goose.Provider, error) { return s.provider() }
