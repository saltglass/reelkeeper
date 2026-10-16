package store_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestEngineFor(t *testing.T) {
	cases := map[string]store.Engine{
		"postgres://u:p@h/db":           store.Postgres,
		"postgresql://u@h/db":           store.Postgres,
		"data/reelkeeper.db":            store.SQLite,
		"/config/reelkeeper.db":         store.SQLite,
		"postgres-backup.db":            store.SQLite,
		"C:\\reelkeeper\\reelkeeper.db": store.SQLite,
		"":                              store.SQLite,
		"./postgres://weird.db":         store.SQLite,
	}
	for dsn, want := range cases {
		if got := store.EngineFor(dsn); got != want {
			t.Errorf("EngineFor(%q) = %s, want %s", dsn, got, want)
		}
	}
}

func TestMigrateFromEmpty(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			s := storetest.OpenEmpty(t, engine)
			ctx := context.Background()

			res, err := s.Migrate(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if res.From != 0 || res.Applied == 0 || res.To == 0 {
				t.Errorf("unexpected result %+v", res)
			}
			if res.Backup != "" {
				t.Errorf("a brand-new database should not be backed up, got %s", res.Backup)
			}

			// Running again is a no-op.
			again, err := s.Migrate(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if again.Applied != 0 || again.From != res.To {
				t.Errorf("second run should do nothing, got %+v", again)
			}

			status, err := s.MigrationStatus(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range status {
				if !m.Applied {
					t.Errorf("migration %s not applied", m.Name)
				}
			}
		})
	}
}

func TestSettingsTableWorks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		ctx := context.Background()
		_, err := s.DB.NewRaw("INSERT INTO settings (key, value) VALUES (?, ?)", "general", `{"name":"Reelkeeper"}`).Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var key string
		if err := s.DB.NewRaw("SELECT key FROM settings").Scan(ctx, &key); err != nil {
			t.Fatal(err)
		}
		if key != "general" {
			t.Errorf("key = %q", key)
		}
	})
}

// An existing SQLite database is copied before new migrations run, and a
// failed migration names that copy so it can be restored.
func TestSQLiteBackupBeforeUpgrade(t *testing.T) {
	ctx := context.Background()
	backups := filepath.Join(t.TempDir(), "backups")
	s := storetest.OpenEmpty(t, store.SQLite)

	v1 := fstest.MapFS{
		"00001_a.sql": {Data: []byte("-- +goose Up\nCREATE TABLE a (id INTEGER);\n")},
	}
	store.UseMigrationsForTest(s, v1)
	if _, err := s.Migrate(ctx, backups); err != nil {
		t.Fatal(err)
	}

	// An app upgrade brings a second migration.
	v2 := fstest.MapFS{
		"00001_a.sql": v1["00001_a.sql"],
		"00002_b.sql": {Data: []byte("-- +goose Up\nCREATE TABLE b (id INTEGER);\n")},
	}
	store.UseMigrationsForTest(s, v2)
	res, err := s.Migrate(ctx, backups)
	if err != nil {
		t.Fatal(err)
	}
	if res.From != 1 || res.To != 2 || res.Applied != 1 {
		t.Errorf("unexpected result %+v", res)
	}
	if res.Backup == "" {
		t.Fatal("expected a backup")
	}
	if !strings.Contains(filepath.Base(res.Backup), "-v1-") {
		t.Errorf("backup name should record the old version: %s", res.Backup)
	}

	// The backup is a working database at the old version.
	old, err := store.Open(ctx, res.Backup)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var n int
	if err := old.DB.NewRaw("SELECT count(*) FROM sqlite_master WHERE name = 'b'").Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("backup should predate migration 2")
	}

	// A broken migration fails and points at the backup.
	v3 := fstest.MapFS{
		"00001_a.sql":   v1["00001_a.sql"],
		"00002_b.sql":   v2["00002_b.sql"],
		"00003_bad.sql": {Data: []byte("-- +goose Up\nTHIS IS NOT SQL;\n")},
	}
	store.UseMigrationsForTest(s, v3)
	_, err = s.Migrate(ctx, backups)
	if err == nil {
		t.Fatal("expected the broken migration to fail")
	}
	if !strings.Contains(err.Error(), backups) {
		t.Errorf("error should name the backup: %v", err)
	}
}

// A brand-new Postgres or SQLite database has nothing to back up.
func TestNoBackupForNewDatabase(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		// storetest.Open already migrated; nothing should be in this folder.
		dir := t.TempDir()
		if _, err := s.Migrate(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("unexpected backups: %v", entries)
		}
	})
}

func TestPruneKeepsNewestBackups(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"reelkeeper-pre-migrate-v1-20260101-000000.db",
		"reelkeeper-pre-migrate-v12-20260301-000000.db",
		"reelkeeper-pre-migrate-v3-20260201-000000.db",
		"reelkeeper-pre-migrate-v9-20260401-000000.db",
		"unrelated.db",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store.PruneBackupsForTest(dir, "reelkeeper-pre-migrate-", 3)

	entries, _ := os.ReadDir(dir)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{
		"reelkeeper-pre-migrate-v12-20260301-000000.db",
		"reelkeeper-pre-migrate-v3-20260201-000000.db",
		"reelkeeper-pre-migrate-v9-20260401-000000.db",
		"unrelated.db",
	}
	if !slices.Equal(got, want) {
		t.Errorf("after prune: %v, want %v", got, want)
	}
}

// Every migration must exist for both engines, with the same version and
// name, so the two schemas never drift apart.
func TestMigrationsMatch(t *testing.T) {
	list := func(engine string) []string {
		files, err := fs.Glob(os.DirFS("migrations/"+engine), "*.sql")
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	sqlite, pg := list("sqlite"), list("postgres")
	if len(sqlite) == 0 {
		t.Fatal("no migrations found")
	}
	if !slices.Equal(sqlite, pg) {
		t.Errorf("migration files differ between engines:\n  sqlite:   %v\n  postgres: %v", sqlite, pg)
	}
	for _, f := range sqlite {
		if !strings.Contains(f, "_") || len(f) < 10 {
			t.Errorf("%s: name should look like 00001_description.sql", f)
		}
	}
}

func TestOpenPostgresFailureHidesPassword(t *testing.T) {
	// Nothing listens on port 1; the error must not reveal the password.
	_, err := store.Open(context.Background(), "postgres://reelkeeper:hunter2secret@127.0.0.1:1/reelkeeper?connect_timeout=2")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2secret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

// Folder names containing URI special characters must not change which
// file is opened.
func TestSQLitePathWithSpecialCharacters(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "odd #1 ?x 100%")
	path := filepath.Join(dir, "reelkeeper.db")
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not created at the given path: %v", err)
	}
}

// Migration 16 marks routine runs from before jobs.quiet existed as quiet,
// and leaves every run that did something visible.
func TestQuietBackfill(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s := storetest.OpenEmpty(t, engine)
			store.UseMigrationsForTest(s, store.MigrationsUpToForTest(engine, 15))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			runs := []struct {
				kind, status, message string
				quiet                 bool
			}{
				{"download.check", "succeeded", "4 downloading.", true},
				{"download.check", "succeeded", "Nothing is downloading.", true},
				{"download.check", "succeeded", "3 downloading, 1 imported.", false},
				{"download.check", "failed", "4 downloading.", false},
				{"download.cleanup", "succeeded", "0 removed, 7 kept.", true},
				{"download.cleanup", "succeeded", "2 removed, 5 kept.", false},
				{"download.cleanup", "succeeded", `Dry run: 0 newly marked "would remove", 3 kept.`, true},
				{"grab.rss", "succeeded", "Read 63 new releases; 0 matches for 318 movie versions waiting.", true},
				{"grab.rss", "succeeded", "Read 63 new releases; 2 matches for 318 movie versions waiting.", false},
				{"grab.rss", "succeeded", "Read 0 new releases; 0 matches for 317 movie versions waiting. 2 indexers failed.", false},
				{"grab.search", "succeeded", "4 downloading.", false},
			}
			for _, r := range runs {
				if _, err := s.DB.ExecContext(ctx, "INSERT INTO jobs (kind, status, triggered_by, message, created_at) VALUES (?, ?, 'manual', ?, ?)",
					r.kind, r.status, r.message, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			store.UseMigrationsForTest(s, store.AllMigrationsForTest(engine))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			var got []struct {
				Kind, Status, Message string
				Quiet                 bool
			}
			if err := s.DB.NewSelect().Table("jobs").Column("kind", "status", "message", "quiet").OrderExpr("id").Scan(ctx, &got); err != nil {
				t.Fatal(err)
			}
			for i, r := range runs {
				if got[i].Quiet != r.quiet {
					t.Errorf("%s %q (%s): quiet = %v, want %v", r.kind, r.message, r.status, got[i].Quiet, r.quiet)
				}
			}
		})
	}
}

// Migration 17: series versions that already exist want only episodes
// from the day of the upgrade (2026-10-11); movie versions are left
// alone.
func TestTVMonitoringBackfill(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s := storetest.OpenEmpty(t, engine)
			store.UseMigrationsForTest(s, store.MigrationsUpToForTest(engine, 16))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			var root int64
			if err := s.DB.QueryRowContext(ctx, "INSERT INTO root_folders (path, media_type, default_profile_id, created_at) VALUES (?, 'series', 1, ?) RETURNING id",
				"/shows", now).Scan(&root); err != nil {
				t.Fatal(err)
			}
			for i, kind := range []string{"movie", "series"} {
				var title int64
				if err := s.DB.QueryRowContext(ctx, "INSERT INTO titles (media_type, tmdb_id, title, sort_title, year, created_at) VALUES (?, ?, ?, ?, 2020, ?) RETURNING id",
					kind, 100+i, kind, kind, now).Scan(&title); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB.ExecContext(ctx, "INSERT INTO versions (title_id, profile_id, root_folder_id, folder, monitored, created_at) VALUES (?, 1, ?, '', TRUE, ?)",
					title, root, now); err != nil {
					t.Fatal(err)
				}
			}
			store.UseMigrationsForTest(s, store.AllMigrationsForTest(engine))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			var got []struct {
				MediaType   string  `bun:"media_type"`
				MonitorFrom *string `bun:"monitor_from"`
			}
			if err := s.DB.NewSelect().TableExpr("versions AS v").ColumnExpr("t.media_type, v.monitor_from").
				Join("JOIN titles AS t ON t.id = v.title_id").OrderExpr("v.id").Scan(ctx, &got); err != nil {
				t.Fatal(err)
			}
			today := now.Format("2006-01-02")
			if len(got) != 2 || got[0].MonitorFrom != nil || got[1].MonitorFrom == nil || *got[1].MonitorFrom != today {
				t.Errorf("after migration: %+v (want the series from %s)", got, today)
			}
		})
	}
}

// Migration 18: cleanup's blocklist entries become temporary (30 days),
// except those the tracker deleted; everything else stays permanent. The
// reasons are the wording cleanup wrote (internal/cleanup/rules.go).
func TestBlocklistExpiryBackfill(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s := storetest.OpenEmpty(t, engine)
			store.UseMigrationsForTest(s, store.MigrationsUpToForTest(engine, 17))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			made := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
			titles := map[string]int64{}
			for i, kind := range []string{"movie", "series"} {
				var id int64
				if err := s.DB.QueryRowContext(ctx, "INSERT INTO titles (media_type, tmdb_id, title, sort_title, year, created_at) VALUES (?, ?, ?, ?, 2020, ?) RETURNING id",
					kind, 100+i, kind, kind, made).Scan(&id); err != nil {
					t.Fatal(err)
				}
				titles[kind] = id
			}
			entries := []struct {
				title  string
				reason string
				rule   string
				temp   bool
			}{
				{"movie", "Cleanup: No progress for 2 days. Strikes: none.", "stalled", true},
				{"movie", "Cleanup: Slower than 50 KB/s for 6 hours (now 3 KB/s).", "slow", true},
				{"movie", "Cleanup: It has been trying to get the torrent's details for 12 hours; nobody seems to be sharing it.", "metadata", true},
				{"movie", "Cleanup: qBittorrent reports an error or missing files for it. Seen 3 checks in a row.", "missing", true},
				{"movie", "Cleanup: Its import failed 3 days ago and hasn't been retried.", "import-failed", true},
				{"movie", "Cleanup: Something a later version says.", "cleanup", true},
				{"movie", "Cleanup: The tracker says this torrent no longer exists (deleted or replaced). Seen 3 checks in a row.", "unregistered", false},
				{"movie", "Removed from the queue by you", "removed", false},
				{"movie", "Blocklisted in Radarr: Fake", "radarr", false},
				{"series", "Blocklisted in Sonarr", "sonarr", false},
				{"series", "Blocklisted in Remux: Fake", "sonarr", false},
				{"movie", "Something else", "removed", false},
			}
			for _, e := range entries {
				if _, err := s.DB.ExecContext(ctx, "INSERT INTO blocklist (created_at, title_id, release_title, reason) VALUES (?, ?, 'Some.Release', ?)",
					made, titles[e.title], e.reason); err != nil {
					t.Fatal(err)
				}
			}
			store.UseMigrationsForTest(s, store.AllMigrationsForTest(engine))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			var got []struct {
				Rule      string     `bun:"rule"`
				ExpiresAt *time.Time `bun:"expires_at"`
			}
			if err := s.DB.NewSelect().Table("blocklist").Column("rule", "expires_at").OrderExpr("id").Scan(ctx, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(entries) {
				t.Fatalf("%d rows, want %d", len(got), len(entries))
			}
			for i, e := range entries {
				if got[i].Rule != e.rule {
					t.Errorf("%q: rule = %q, want %q", e.reason, got[i].Rule, e.rule)
				}
				switch {
				case e.temp && (got[i].ExpiresAt == nil || !got[i].ExpiresAt.Equal(made.Add(30*24*time.Hour))):
					t.Errorf("%q: expires %v, want %v", e.reason, got[i].ExpiresAt, made.Add(30*24*time.Hour))
				case !e.temp && got[i].ExpiresAt != nil:
					t.Errorf("%q: expires %v, want permanent", e.reason, got[i].ExpiresAt)
				}
			}
		})
	}
}

// Migration 20 (moving torrents between clients): downloads keep their data
// and gain empty move columns, the table for kept torrents works on both
// engines, and the migration goes back down cleanly.
func TestMoveTorrentsMigration(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s := storetest.OpenEmpty(t, engine)
			store.UseMigrationsForTest(s, store.MigrationsUpToForTest(engine, 19))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			made := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
			if _, err := s.DB.ExecContext(ctx, "INSERT INTO downloads (created_at, hash, category, release_title, status) VALUES (?, 'aa', 'reelkeeper-movies', 'Some.Release', 'imported')", made); err != nil {
				t.Fatal(err)
			}
			store.UseMigrationsForTest(s, store.AllMigrationsForTest(engine))
			res, err := s.Migrate(ctx, "")
			if err != nil || res.To < 20 || res.Applied != int(res.To-19) {
				t.Fatalf("migrate: %+v %v", res, err)
			}
			var d struct {
				Client, MoveState, MoveTo, MoveLayout string
				MoveOldBytes, SeedOffset, UpOffset    int64
				MoveWasPaused                         bool
				MovedAt, MoveStartedAt                *time.Time
				Status                                string
			}
			err = s.DB.QueryRowContext(ctx, `SELECT client, move_state, move_to, move_layout, move_old_bytes, seed_seconds_offset, uploaded_offset,
				move_was_paused, moved_at, move_started_at, status FROM downloads WHERE hash = 'aa'`).
				Scan(&d.Client, &d.MoveState, &d.MoveTo, &d.MoveLayout, &d.MoveOldBytes, &d.SeedOffset, &d.UpOffset, &d.MoveWasPaused, &d.MovedAt, &d.MoveStartedAt, &d.Status)
			if err != nil {
				t.Fatal(err)
			}
			if d.Client != "" || d.MoveState != "" || d.MoveTo != "" || d.MoveLayout != "" || d.MoveOldBytes != 0 || d.SeedOffset != 0 || d.UpOffset != 0 ||
				d.MoveWasPaused || d.MovedAt != nil || d.MoveStartedAt != nil || d.Status != "imported" {
				t.Errorf("existing download = %+v", d)
			}
			var id int64
			if err := s.DB.QueryRowContext(ctx, "SELECT id FROM downloads WHERE hash = 'aa'").Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, "INSERT INTO download_sources (download_id, kind, sealed, created_at) VALUES (?, 'torrent', 'v1:xx', ?)", id, made); err != nil {
				t.Fatalf("kept torrent: %v", err)
			}
			// One kept torrent per download, and it goes with the download.
			if _, err := s.DB.ExecContext(ctx, "INSERT INTO download_sources (download_id, kind, sealed, created_at) VALUES (?, 'magnet', 'v1:yy', ?)", id, made); err == nil {
				t.Error("two kept torrents for one download")
			}
			if _, err := s.DB.ExecContext(ctx, "DELETE FROM downloads WHERE id = ?", id); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM download_sources").Scan(&n); err != nil || n != 0 {
				t.Errorf("kept torrents after the download went: %d %v", n, err)
			}
			// Down again.
			p, err := s.ProviderForTest()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.DownTo(ctx, 19); err != nil {
				t.Fatalf("down: %v", err)
			}
			var cols int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM downloads").Scan(&cols); err != nil {
				t.Errorf("downloads after down: %v", err)
			}
		})
	}
}

// Migration 21 (Deluge as a third download client): downloads gain a
// "requested" note, false for every download that exists, which can be set
// afterwards, and the migration goes back down cleanly.
func TestDownloadRequestedMigration(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s := storetest.OpenEmpty(t, engine)
			store.UseMigrationsForTest(s, store.MigrationsUpToForTest(engine, 20))
			if _, err := s.Migrate(ctx, ""); err != nil {
				t.Fatal(err)
			}
			made := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
			if _, err := s.DB.ExecContext(ctx, "INSERT INTO downloads (created_at, hash, category, release_title, status) VALUES (?, 'aa', 'reelkeeper-movies', 'Some.Release', 'imported')", made); err != nil {
				t.Fatal(err)
			}
			store.UseMigrationsForTest(s, store.AllMigrationsForTest(engine))
			res, err := s.Migrate(ctx, "")
			if err != nil || res.To < 21 || res.Applied != int(res.To-20) {
				t.Fatalf("migrate: %+v %v", res, err)
			}
			var requested bool
			var status string
			if err := s.DB.QueryRowContext(ctx, "SELECT requested, status FROM downloads WHERE hash = 'aa'").Scan(&requested, &status); err != nil {
				t.Fatal(err)
			}
			if requested || status != "imported" {
				t.Errorf("existing download: requested %v, status %q", requested, status)
			}
			// A new download without the note is not a request; one with it is.
			if _, err := s.DB.ExecContext(ctx, "INSERT INTO downloads (created_at, hash, category, release_title, status) VALUES (?, 'bb', 'reelkeeper-movies', 'Other.Release', 'downloading')", made); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, "UPDATE downloads SET requested = ? WHERE hash = 'bb'", true); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM downloads WHERE requested").Scan(&n); err != nil || n != 1 {
				t.Errorf("requested downloads = %d, %v", n, err)
			}
			// Down again: the downloads stay.
			p, err := s.ProviderForTest()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.DownTo(ctx, 20); err != nil {
				t.Fatalf("down: %v", err)
			}
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM downloads").Scan(&n); err != nil || n != 2 {
				t.Errorf("downloads after down: %d %v", n, err)
			}
		})
	}
}
