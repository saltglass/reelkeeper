package backup

import (
	"os"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// A real backup (REELKEEPER_REAL_BACKUP=<file>, opt-in: it holds personal
// data and is never committed) restores into a fresh SQLite and, with
// `just test-pg`, Postgres: every table's rows as counted in the backup,
// and both engines end up with the same rows.
func TestRealBackup(t *testing.T) {
	path := os.Getenv("REELKEEPER_REAL_BACKUP")
	if path == "" {
		t.Skip("set REELKEEPER_REAL_BACKUP to a backup file")
	}
	sum, err := inspectFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dumps := map[store.Engine]map[string][]string{}
	for _, engine := range storetest.Engines() {
		s := storetest.Open(t, engine)
		if _, err := Restore(ctx, s, path, RestoreOptions{ScratchDir: t.TempDir()}); err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		d := dump(t, s)
		for table, n := range sum.Rows {
			if len(d[table]) != n {
				t.Errorf("%s: %s has %d rows, the backup %d", engine, table, len(d[table]), n)
			}
		}
		dumps[engine] = d
	}
	if len(dumps) == 2 {
		compare(t, dumps[store.SQLite], dumps[store.Postgres])
	}
	t.Logf("%d rows in %d tables, from Reelkeeper %s (%s, schema %d)", sum.Total, len(sum.Rows),
		sum.Header.App, sum.Header.Engine, sum.Header.SchemaVersion)
}
