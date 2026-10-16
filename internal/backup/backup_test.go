package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

var ctx = context.Background()

// at is a timestamp with nanoseconds, which Postgres keeps only to the
// microsecond: the round trip must still compare equal.
var at = time.Date(2026, 10, 11, 21, 30, 15, 123456789, time.UTC)

// seed fills a database with a little of everything: booleans, JSON,
// floats, empty (NULL) values, timestamps, foreign keys, a session.
func seed(t *testing.T, s *store.Store) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.NewRaw(q, args...).Exec(ctx); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)", "dan", "$argon2id$v=19$x", at)
	var userID int64
	if err := s.DB.NewRaw("SELECT id FROM users WHERE username = ?", "dan").Scan(ctx, &userID); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"abc", userID, at, at.Add(time.Hour), at, "172.16.0.2", "test")
	exec("INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)", "general", `{"instanceName": "Reelkeeper", "logLevel": "info"}`, at)
	exec(`INSERT INTO indexers (name, kind, enabled, use_solver, settings, categories, seed_ratio, seed_time_minutes,
		retry_after, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"Knaben", "knaben", false, true, `{"apiKey": "", "nested": {"a": [1, 2]}}`, `[2000, 5000]`, 1.25, 1440, at, at, at)
	exec("INSERT INTO jobs (kind, args, status, triggered_by, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?)",
		"scan", `{"rootId": 3}`, "done", "schedule", at, at.Add(time.Minute))

	root := &library.RootFolder{Path: "/movies", MediaType: library.Movie, DefaultProfileID: 1, ManageFiles: true, CreatedAt: at}
	mustInsert(t, s, root)
	for i, name := range []string{"Heat", "Ronin", "Amélie \"quoted\" 'n' ? marks"} {
		title := &library.Title{MediaType: library.Movie, TMDBID: 100 + i, Title: name, Year: 1995 + i, Runtime: 120,
			Status: "Released", OriginalLanguage: "en", CreatedAt: at, Genres: `["Crime", "Drama"]`, AltTitles: "[]",
			Popularity: 12.5 + float64(i)}
		mustInsert(t, s, title)
		v := &library.Version{TitleID: title.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: i != 1, CreatedAt: at}
		mustInsert(t, s, v)
		mf := &library.MediaFile{VersionID: v.ID, RootFolderID: root.ID, Path: name + "/" + name + ".mkv", Size: 4 << 30,
			ModifiedAt: at, Resolution: 1080, Source: "WEB-DL", Quality: "WEBDL-1080p", ReleaseInfo: `{"group": "GRP"}`,
			MediaInfo: `{"video": {"codec": "h264"}}`, EpisodeClaim: "{}", AddedAt: at}
		mustInsert(t, s, mf)
		exec(`INSERT INTO history (created_at, title_id, version_id, event, message, score, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			at, title.ID, v.ID, "searched", "Searched for "+name+": 3 releases, 1 acceptable.", 42, `{"trigger": "manual", "releases": []}`)
	}
}

func mustInsert(t *testing.T, s *store.Store, model any) {
	t.Helper()
	if _, err := s.DB.NewInsert().Model(model).Exec(ctx); err != nil {
		t.Fatalf("insert %T: %v", model, err)
	}
}

func keyCheckOK(string) bool { return true }

// writeFile backs s up to a file and returns its path.
func writeFile(t *testing.T, s *store.Store, meta Meta) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.backup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(ctx, s, f, meta); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// dump reads every backed-up table into comparable form: each value
// encoded as a backup would, then made engine-neutral (flags as 0/1, JSON
// re-written in one canonical form), rows sorted.
func dump(t *testing.T, s *store.Store) map[string][]string {
	t.Helper()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	tables, err := readCatalog(ctx, tx, s.Engine)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	out := map[string][]string{}
	for _, tb := range tables {
		if skipped[tb.name] {
			continue
		}
		buf.Reset()
		if _, err := writeRows(ctx, tx, tb, &buf); err != nil {
			t.Fatal(err)
		}
		// Columns in name order, so engines that ordered them differently
		// (ALTER TABLE ADD COLUMN) still compare.
		idx := make([]int, len(tb.columns))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return tb.columns[idx[a]].Name < tb.columns[idx[b]].Name })
		rows := []string{}
		sc := bufio.NewScanner(&buf)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var vals []any
			dec := json.NewDecoder(strings.NewReader(sc.Text()))
			dec.UseNumber()
			if err := dec.Decode(&vals); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(vals))
			for n, i := range idx {
				parts[n] = tb.columns[i].Name + "=" + canonical(vals[i])
			}
			rows = append(rows, strings.Join(parts, "|"))
		}
		sort.Strings(rows)
		out[tb.name] = rows
	}
	return out
}

func canonical(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "1"
		}
		return "0"
	case json.Number:
		return x.String()
	case string:
		if strings.HasPrefix(x, "{") || strings.HasPrefix(x, "[") {
			var j any
			if json.Unmarshal([]byte(x), &j) == nil {
				b, _ := json.Marshal(j)
				return string(b)
			}
		}
		return fmt.Sprintf("%q", x)
	}
	return fmt.Sprint(v)
}

func compare(t *testing.T, want, got map[string][]string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("tables: want %d, got %d", len(want), len(got))
	}
	for name, rows := range want {
		if !reflect.DeepEqual(rows, got[name]) {
			t.Errorf("table %s differs:\n want %v\n  got %v", name, rows, got[name])
		}
	}
}

// Back up, restore into a fresh database on the same engine: every row of
// every table comes back the same.
func TestRoundTrip(t *testing.T) {
	storetest.Each(t, func(t *testing.T, src *store.Store) {
		seed(t, src)
		path := writeFile(t, src, Meta{App: "test", Kind: KindManual, KeyCheck: "sealed"})

		dst := storetest.Open(t, src.Engine)
		sum, err := Restore(ctx, dst, path, RestoreOptions{CheckKey: keyCheckOK, ScratchDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Rows["titles"] != 3 || sum.Rows["sessions"] != 0 {
			t.Errorf("summary rows = %v", sum.Rows)
		}
		compare(t, dump(t, src), dump(t, dst))
		// IDs carry on after the restored ones (Postgres sequences).
		mustInsert(t, dst, &library.Title{MediaType: library.Movie, TMDBID: 999, Title: "New", Year: 2026,
			CreatedAt: at, Genres: "[]", AltTitles: "[]"})
	})
}

// gate is a writer that stops at its first write until released, which
// holds a backup open halfway through.
type gate struct {
	reached chan struct{}
	release chan struct{}
	buf     bytes.Buffer
	once    bool
}

func (g *gate) Write(p []byte) (int, error) {
	if !g.once {
		g.once = true
		close(g.reached)
		<-g.release
	}
	return g.buf.Write(p)
}

// A SQLite backup must not stop the app writing while it runs, and the file
// must still hold the database as it was when the backup began.
func TestSQLiteBackupDoesNotBlockWrites(t *testing.T) {
	src := storetest.Open(t, store.SQLite)
	seed(t, src)
	before := dump(t, src)

	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := Write(ctx, src, g, Meta{App: "test", Kind: KindManual, KeyCheck: "sealed"})
		done <- err
	}()
	select {
	case <-g.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the backup never started")
	}

	// The backup's transaction is open. A write from another connection
	// goes through at once (a blocked one would wait out the 5 s busy
	// timeout and fail with "database is locked").
	start := time.Now()
	if _, err := src.DB.NewRaw("DELETE FROM history").Exec(ctx); err != nil {
		t.Fatalf("write during a backup: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("write during a backup took %v", took)
	}

	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.backup")
	if err := os.WriteFile(path, g.buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := storetest.Open(t, store.SQLite)
	if _, err := Restore(ctx, dst, path, RestoreOptions{CheckKey: keyCheckOK, ScratchDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	compare(t, before, dump(t, dst))
}

// A backup made on one engine restores into the other.
func TestAcrossEngines(t *testing.T) {
	if len(storetest.Engines()) < 2 {
		t.Skip("needs Postgres (just test-pg)")
	}
	for _, dir := range [][2]store.Engine{{store.SQLite, store.Postgres}, {store.Postgres, store.SQLite}} {
		t.Run(string(dir[0])+"→"+string(dir[1]), func(t *testing.T) {
			src := storetest.Open(t, dir[0])
			seed(t, src)
			path := writeFile(t, src, Meta{App: "test", Kind: KindManual})
			dst := storetest.Open(t, dir[1])
			if _, err := Restore(ctx, dst, path, RestoreOptions{CheckKey: keyCheckOK}); err != nil {
				t.Fatal(err)
			}
			compare(t, dump(t, src), dump(t, dst))
		})
	}
}

// Every table is backed up except the two left out on purpose, so a table
// added by a later migration can't be forgotten.
func TestEveryTableIsBackedUp(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		var buf bytes.Buffer
		sum, err := Write(ctx, s, &buf, Meta{})
		if err != nil {
			t.Fatal(err)
		}
		tables, err := readCatalog(ctx, s.DB, s.Engine)
		if err != nil {
			t.Fatal(err)
		}
		var left []string
		for _, tb := range tables {
			if _, ok := sum.Rows[tb.name]; !ok {
				left = append(left, tb.name)
			}
		}
		sort.Strings(left)
		if want := []string{"goose_db_version", "sessions"}; !reflect.DeepEqual(left, want) {
			t.Errorf("tables left out of backups = %v, want only %v", left, want)
		}
	})
}

// Restoring replaces what was there, and signs everyone out.
func TestRestoreReplaces(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		empty := writeFile(t, storetest.Open(t, s.Engine), Meta{})
		seed(t, s)
		if _, err := Restore(ctx, s, empty, RestoreOptions{CheckKey: keyCheckOK}); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"titles", "users", "sessions", "history"} {
			n, err := s.DB.NewSelect().Table(table).Count(ctx)
			if err != nil || n != 0 {
				t.Errorf("%s has %d rows after restoring an empty backup (%v)", table, n, err)
			}
		}
		// The seeded profiles came back from the backup, not doubled.
		if n, _ := s.DB.NewSelect().Table("profiles").Count(ctx); n != 2 {
			t.Errorf("profiles = %d", n)
		}
	})
}

// Refusals: nothing changes when a backup is damaged, from a newer
// Reelkeeper, made with another key, or fails halfway through.
func TestRefusalsChangeNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		seed(t, s)
		good := writeFile(t, s, Meta{KeyCheck: "sealed"})
		before := dump(t, s)
		opts := RestoreOptions{CheckKey: keyCheckOK}

		cases := []struct {
			name string
			path string
			opts RestoreOptions
			want error
		}{
			{"not a backup", writeRaw(t, []byte("hello")), opts, ErrNotBackup},
			{"cut short", truncated(t, good), opts, ErrDamaged},
			{"edited", rewrite(t, good, func(l string) string { return strings.Replace(l, `"Heat"`, `"Heap"`, 1) }, false), opts, ErrDamaged},
			{"newer schema", rewrite(t, good, func(l string) string { return bumpSchema(t, l) }, true), opts, ErrNewer},
			{"wrong key", good, RestoreOptions{CheckKey: func(string) bool { return false }}, ErrWrongKey},
			// A row that breaks a foreign key, in the last table restored:
			// everything before it was already written, and is undone.
			{"fails halfway", rewrite(t, good, breakForeignKey, true), opts, nil},
		}
		for _, c := range cases {
			_, err := Restore(ctx, s, c.path, c.opts)
			if err == nil {
				t.Errorf("%s: restored", c.name)
				continue
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
			}
			compare(t, before, dump(t, s))
		}
	})
}

// A backup from an older Reelkeeper is brought up to date by the
// migrations it hasn't had.
func TestOlderBackupIsUpgraded(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		old := storetest.OpenEmpty(t, s.Engine)
		if err := old.MigrateTo(ctx, 16); err != nil {
			t.Fatal(err)
		}
		if _, err := old.DB.NewRaw(`INSERT INTO root_folders (path, media_type, default_profile_id, manage_files, created_at)
			VALUES (?, ?, ?, ?, ?)`, "/shows", "series", 1, true, at).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := old.DB.NewRaw(`INSERT INTO titles (media_type, tmdb_id, title, sort_title, year, created_at, genres, alt_titles)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "series", 1399, "Some Show", "some show", 2011, at, "[]", "[]").Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := old.DB.NewRaw(`INSERT INTO versions (title_id, profile_id, root_folder_id, monitored, created_at)
			VALUES (1, 1, 1, ?, ?)`, true, at).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		// A blocklist entry from cleanup, made before blocklist entries could
		// expire (migration 18).
		if _, err := old.DB.NewRaw(`INSERT INTO blocklist (created_at, title_id, release_title, reason) VALUES (?, 1, ?, ?)`,
			at, "Some.Show.S01.WEB", "Cleanup: No progress for 2 days.").Exec(ctx); err != nil {
			t.Fatal(err)
		}
		path := writeFile(t, old, Meta{})

		sum, err := Restore(ctx, s, path, RestoreOptions{CheckKey: keyCheckOK, ScratchDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Header.SchemaVersion != 16 {
			t.Errorf("summary version = %d", sum.Header.SchemaVersion)
		}
		// Migration 17 ran on the restored rows: it gives existing series
		// versions "new episodes from today" (monitor_from).
		var v library.Version
		if err := s.DB.NewSelect().Model(&v).Where("id = 1").Scan(ctx); err != nil {
			t.Fatal(err)
		}
		// (The migration's SQL takes the database's own date: UTC or local.)
		utc, local := time.Now().UTC().Format("2006-01-02"), time.Now().Format("2006-01-02")
		if !v.Monitored || v.MonitorFrom == nil || (*v.MonitorFrom != utc && *v.MonitorFrom != local) {
			t.Errorf("version = %+v, want monitored from today", v)
		}
		if n, _ := s.DB.NewSelect().Table("titles").Where("title = ?", "Some Show").Count(ctx); n != 1 {
			t.Error("title lost")
		}
		// Migration 18 ran too: the cleanup entry is now temporary.
		var bl struct {
			Rule      string     `bun:"rule"`
			ExpiresAt *time.Time `bun:"expires_at"`
		}
		if err := s.DB.NewSelect().Table("blocklist").Column("rule", "expires_at").Scan(ctx, &bl); err != nil {
			t.Fatal(err)
		}
		// (SQLite's date functions keep milliseconds, so allow for that.)
		if bl.Rule != "stalled" || bl.ExpiresAt == nil || bl.ExpiresAt.Sub(at.Add(30*24*time.Hour)).Abs() > time.Second {
			t.Errorf("blocklist entry = %+v, want a temporary 'stalled' one", bl)
		}
	})
}

// ReadHeader reads just the header, for listing backups.
func TestReadHeader(t *testing.T) {
	s := storetest.Open(t, store.SQLite)
	path := writeFile(t, s, Meta{App: "0.8.1", Kind: KindScheduled, KeyCheck: "v1:x"})
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := ReadHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if h.App != "0.8.1" || h.Kind != KindScheduled || h.KeyCheck != "v1:x" || h.Engine != "sqlite" || h.SchemaVersion < 17 {
		t.Errorf("header = %+v", h)
	}
}

// --- helpers that make broken backups ---

func writeRaw(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "raw.backup")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func pack(t *testing.T, ls []string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, l := range ls {
		_, _ = gz.Write([]byte(l + "\n"))
	}
	_ = gz.Close()
	return writeRaw(t, buf.Bytes())
}

func truncated(t *testing.T, path string) string {
	ls := lines(t, path)
	return pack(t, ls[:len(ls)/2])
}

// rewrite changes every line but the last with fn. With fixSum, the
// checksum is recomputed, so only the change itself is wrong.
func rewrite(t *testing.T, path string, fn func(string) string, fixSum bool) string {
	ls := lines(t, path)
	body := ls[:len(ls)-1]
	for i := range body {
		body[i] = fn(body[i])
	}
	end := ls[len(ls)-1]
	if fixSum {
		sum := sha256.New()
		for _, l := range body {
			sum.Write([]byte(l + "\n"))
		}
		var e line
		if err := json.Unmarshal([]byte(end), &e); err != nil {
			t.Fatal(err)
		}
		e.End.SHA256 = hex.EncodeToString(sum.Sum(nil))
		b, _ := json.Marshal(e)
		end = string(b)
	}
	return pack(t, append(body, end))
}

func bumpSchema(t *testing.T, l string) string {
	if !strings.HasPrefix(l, `{"header"`) {
		return l
	}
	var x line
	if err := json.Unmarshal([]byte(l), &x); err != nil {
		t.Fatal(err)
	}
	x.Header.SchemaVersion += 100
	b, _ := json.Marshal(x)
	return string(b)
}

// breakForeignKey points every history row at a title that doesn't exist.
// history comes late in the restore order, after titles and versions.
var historyRow = false

func breakForeignKey(l string) string {
	if strings.HasPrefix(l, "{") {
		var x line
		_ = json.Unmarshal([]byte(l), &x)
		historyRow = x.Table == "history"
		return l
	}
	if !historyRow {
		return l
	}
	// history columns: id, created_at, title_id, ... — set title_id to 9999.
	var vals []any
	dec := json.NewDecoder(strings.NewReader(l))
	dec.UseNumber()
	_ = dec.Decode(&vals)
	vals[2] = json.Number("9999")
	b, _ := json.Marshal(vals)
	return string(b)
}
