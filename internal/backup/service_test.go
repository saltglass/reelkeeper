package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func newService(t *testing.T, s *store.Store, set *settings.Backups) *Service {
	t.Helper()
	data := t.TempDir()
	return New(Config{
		DB:       s,
		Settings: func() settings.Backups { return *set },
		KeyCheck: func(context.Context) (string, error) { return "sealed-with-key-A", nil },
		KeyOpens: func(sealed string) bool { return sealed == "sealed-with-key-A" },
		DataDir:  data,
		App:      "test",
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	files, _ := os.ReadDir(dir)
	var out []string
	for _, f := range files {
		out = append(out, f.Name())
	}
	sort.Strings(out)
	return out
}

func TestFolderDefaultsToDataFolder(t *testing.T) {
	set := settings.DefaultBackups()
	svc := newService(t, storetest.Open(t, store.SQLite), &set)
	if got, want := svc.Folder(), filepath.Join(svc.dataDir, "backups"); got != want {
		t.Errorf("folder = %s, want %s", got, want)
	}
	set.Folder = "/somewhere/else"
	if svc.Folder() != "/somewhere/else" {
		t.Errorf("folder = %s", svc.Folder())
	}
}

// Old scheduled and manual backups go past Keep; safety backups keep their
// own 3; uploaded ones and other files are never touched.
func TestPrune(t *testing.T) {
	set := settings.Backups{Scheduled: true, Time: "03:30", Keep: 2}
	svc := newService(t, storetest.Open(t, store.SQLite), &set)
	dir := svc.Folder()
	clock := time.Date(2026, 10, 1, 3, 30, 0, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"reelkeeper-20250101-000000-uploaded.backup", "reelkeeper-pre-migrate-v16-20250101-000000.db", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, other), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		clock = clock.Add(24 * time.Hour)
		kind := KindScheduled
		if i == 3 {
			kind = KindManual
		}
		if _, err := svc.Create(context.Background(), kind); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		clock = clock.Add(time.Hour)
		if _, err := svc.create(context.Background(), dir, KindSafety); err != nil {
			t.Fatal(err)
		}
	}
	got := names(t, dir)
	want := []string{
		"notes.txt",
		"reelkeeper-20250101-000000-uploaded.backup",
		"reelkeeper-20261005-033000-manual.backup",
		"reelkeeper-20261006-033000-scheduled.backup",
		"reelkeeper-20261006-053000-safety.backup",
		"reelkeeper-20261006-063000-safety.backup",
		"reelkeeper-20261006-073000-safety.backup",
		"reelkeeper-pre-migrate-v16-20250101-000000.db",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("folder =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDue(t *testing.T) {
	set := settings.Backups{Scheduled: true, Time: "03:30", Keep: 14}
	svc := newService(t, storetest.Open(t, store.SQLite), &set)
	loc := time.FixedZone("EDT", -4*3600)
	// Today's date: backups record the real time they were made, so a
	// made-up date in the past would make "the next day" come too early.
	// Before 03:30 a backup made now belongs to yesterday's schedule.
	day := time.Now().In(loc)
	if day.Hour()*60+day.Minute() < 3*60+30 {
		day = day.AddDate(0, 0, -1)
	}
	y, m, d := day.Date()
	clock := time.Date(y, m, d, 3, 0, 0, 0, loc)
	svc.now = func() time.Time { return clock }
	if svc.Due() {
		t.Error("due before 03:30")
	}
	clock = time.Date(y, m, d, 3, 45, 0, 0, loc)
	if !svc.Due() {
		t.Error("not due after 03:30 with no backup today")
	}
	if _, err := svc.Create(context.Background(), KindManual); err != nil {
		t.Fatal(err)
	}
	if !svc.Due() {
		t.Error("a manual backup shouldn't count as today's scheduled one")
	}
	if _, err := svc.Create(context.Background(), KindScheduled); err != nil {
		t.Fatal(err)
	}
	if svc.Due() {
		t.Error("due again after today's scheduled backup")
	}
	clock = clock.Add(24 * time.Hour)
	if !svc.Due() {
		t.Error("not due the next day")
	}
	set.Scheduled = false
	if svc.Due() {
		t.Error("due while scheduled backups are off")
	}
}

// Requests can only name plain backup files in the folder.
func TestNamesStayInTheFolder(t *testing.T) {
	set := settings.DefaultBackups()
	svc := newService(t, storetest.Open(t, store.SQLite), &set)
	e, err := svc.Create(context.Background(), KindManual)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "../secret.key", "/etc/passwd", "x/" + e.Name, ".writing-1.backup", "notes.txt"} {
		if _, err := svc.Open(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q) = %v, want not found", bad, err)
		}
		if err := svc.Delete(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q) = %v, want not found", bad, err)
		}
	}
	f, err := svc.Open(e.Name)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestListAndUpload(t *testing.T) {
	set := settings.DefaultBackups()
	s := storetest.Open(t, store.SQLite)
	seed(t, s)
	svc := newService(t, s, &set)
	made, err := svc.Create(context.Background(), KindManual)
	if err != nil {
		t.Fatal(err)
	}

	// A backup from another install, made with another key.
	other := storetest.Open(t, store.SQLite)
	var buf bytes.Buffer
	if _, err := Write(context.Background(), other, &buf, Meta{App: "0.8.0", Kind: KindScheduled, KeyCheck: "sealed-with-key-B"}); err != nil {
		t.Fatal(err)
	}
	up, err := svc.Upload(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if up.KeyMatches || !strings.HasSuffix(up.Name, "-uploaded.backup") {
		t.Errorf("uploaded = %+v", up)
	}
	if _, err := svc.Upload(strings.NewReader("not a backup")); !errors.Is(err, ErrNotBackup) {
		t.Errorf("junk upload: %v", err)
	}

	list, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	byName := map[string]Entry{}
	for _, e := range list {
		byName[e.Name] = e
	}
	if e := byName[made.Name]; !e.KeyMatches || e.Header.Kind != KindManual || e.Size == 0 {
		t.Errorf("own backup = %+v", e)
	}
	if _, err := svc.Check(context.Background(), up.Name); !errors.Is(err, ErrWrongKey) {
		t.Errorf("check with the wrong key = %v", err)
	}
}

// A cut-off or altered file must show its problem in the list, not only
// when someone tries to restore it; and files are read once, not each time.
func TestListChecksFilesInFull(t *testing.T) {
	set := settings.DefaultBackups()
	s := storetest.Open(t, store.SQLite)
	seed(t, s)
	svc := newService(t, s, &set)
	good, err := svc.Create(context.Background(), KindManual)
	if err != nil {
		t.Fatal(err)
	}
	dir := svc.Folder()
	data, err := os.ReadFile(filepath.Join(dir, good.Name))
	if err != nil {
		t.Fatal(err)
	}
	flipped := bytes.Clone(data)
	flipped[len(flipped)/2] ^= 0x01
	for name, content := range map[string][]byte{
		"reelkeeper-20260101-000000-truncated.backup": data[:len(data)*2/3],
		"reelkeeper-20260101-000001-flipped.backup":   flipped,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reads := 0
	svc.inspect = func(path string) (*Summary, error) {
		reads++
		return inspectFile(path)
	}
	check := func() {
		t.Helper()
		list, err := svc.List()
		if err != nil || len(list) != 3 {
			t.Fatalf("list = %+v, %v", list, err)
		}
		for _, e := range list {
			switch {
			case e.Name == good.Name:
				if e.Problem != "" || e.Header == nil || !e.KeyMatches {
					t.Errorf("good backup = %+v", e)
				}
			case e.Problem == "" || e.Header != nil:
				t.Errorf("%s should list with a problem: %+v", e.Name, e)
			}
		}
	}
	check()
	// The two hand-made files are read; the one Create made is already known.
	if reads != 2 {
		t.Errorf("first list read %d files, want 2", reads)
	}
	check()
	if reads != 2 {
		t.Errorf("second list read %d more files; the results should have been remembered", reads-2)
	}

	// A changed file is read again, and a deleted one is forgotten.
	if err := os.WriteFile(filepath.Join(dir, "reelkeeper-20260101-000001-flipped.backup"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete("reelkeeper-20260101-000000-truncated.backup"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("after changes: %+v, %v", list, err)
	}
	for _, e := range list {
		if e.Problem != "" {
			t.Errorf("%s still has a problem: %q", e.Name, e.Problem)
		}
	}
	if reads != 3 || len(svc.checked) != 2 {
		t.Errorf("reads = %d, remembered = %d", reads, len(svc.checked))
	}
}

// The whole path: stage a restore, "restart", restore before anything else
// runs; the result and a safety backup are left for the Backups page.
func TestStageAndRunPending(t *testing.T) {
	storetest.Each(t, func(t *testing.T, s *store.Store) {
		ctx := context.Background()
		set := settings.DefaultBackups()
		svc := newService(t, s, &set)
		restarted := make(chan struct{}, 1)
		svc.restart = func() { restarted <- struct{}{} }

		seed(t, s)
		e, err := svc.Create(ctx, KindManual)
		if err != nil {
			t.Fatal(err)
		}
		want := dump(t, s)
		// Changes after the backup, which the restore undoes.
		if _, err := s.DB.NewRaw("DELETE FROM history").Exec(ctx); err != nil {
			t.Fatal(err)
		}

		if _, err := svc.StageRestore(ctx, e.Name); err != nil {
			t.Fatal(err)
		}
		select {
		case <-restarted:
		case <-time.After(5 * time.Second):
			t.Fatal("no restart")
		}
		RunPending(ctx, s, svc.dataDir, "test", svc.keyCheck, svc.keyOpens, svc.logger)

		compare(t, want, dump(t, s))
		res := svc.LastResult()
		if res == nil || !res.OK || res.Safety == "" || !strings.Contains(res.Message, "Restored "+e.Name) {
			t.Fatalf("result = %+v", res)
		}
		if _, err := os.Stat(filepath.Join(svc.Folder(), res.Safety)); err != nil {
			t.Errorf("safety backup missing: %v", err)
		}
		// The request is used up: starting again restores nothing.
		if _, err := os.Stat(filepath.Join(svc.dataDir, pendingFile)); !errors.Is(err, os.ErrNotExist) {
			t.Error("restore request left behind")
		}
		// After the restart the Backups page reads neither file again: the
		// safety backup made by the restore is remembered too.
		b := New(Config{DB: s, Settings: svc.settings, KeyCheck: svc.keyCheck, KeyOpens: svc.keyOpens,
			DataDir: svc.dataDir, App: "test", Logger: svc.logger})
		reads := 0
		b.inspect = func(path string) (*Summary, error) {
			reads++
			return inspectFile(path)
		}
		if list, err := b.List(); err != nil || len(list) != 2 || reads != 0 {
			t.Errorf("after the restore: %d backups listed (%v), %d read again", len(list), err, reads)
		}
	})
}

// A restore that fails at startup leaves the database as it was, says why,
// and doesn't try again on the next start.
func TestRunPendingFailure(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, store.SQLite)
	set := settings.DefaultBackups()
	svc := newService(t, s, &set)
	seed(t, s)
	e, err := svc.Create(ctx, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StageRestore(ctx, e.Name); err != nil {
		t.Fatal(err)
	}
	// Between staging and the restart, the key changed.
	before := dump(t, s)
	RunPending(ctx, s, svc.dataDir, "test", svc.keyCheck, func(string) bool { return false }, svc.logger)
	compare(t, before, dump(t, s))
	res := svc.LastResult()
	if res == nil || res.OK || !strings.Contains(res.Message, "different REELKEEPER_SECRET_KEY") {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(svc.dataDir, pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("failed restore would be retried on every start")
	}
}

// The first-run screen's restore: into a fresh install with no account.
func TestSetupRestore(t *testing.T) {
	storetest.Each(t, func(t *testing.T, fresh *store.Store) {
		ctx := context.Background()
		old := storetest.Open(t, fresh.Engine)
		seed(t, old)
		var buf bytes.Buffer
		if _, err := Write(ctx, old, &buf, Meta{App: "0.8.1", Kind: KindScheduled, KeyCheck: "sealed-with-key-A"}); err != nil {
			t.Fatal(err)
		}
		set := settings.DefaultBackups()
		svc := newService(t, fresh, &set)

		// Another key's backup is refused before anything is staged.
		var other bytes.Buffer
		if _, err := Write(ctx, old, &other, Meta{KeyCheck: "sealed-with-key-B"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StageSetupRestore(ctx, bytes.NewReader(other.Bytes())); !errors.Is(err, ErrWrongKey) {
			t.Fatalf("other key: %v", err)
		}
		if _, err := os.Stat(filepath.Join(svc.dataDir, pendingFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("staged a restore it refused")
		}

		if _, err := svc.StageSetupRestore(ctx, bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatal(err)
		}
		RunPending(ctx, fresh, svc.dataDir, "test", svc.keyCheck, svc.keyOpens, svc.logger)
		compare(t, dump(t, old), dump(t, fresh))
		if res := svc.LastResult(); res == nil || !res.OK {
			t.Fatalf("result = %+v", res)
		}
		if _, err := os.Stat(filepath.Join(svc.dataDir, setupFile)); !errors.Is(err, os.ErrNotExist) {
			t.Error("the uploaded file was left in the data folder")
		}
	})
}

// If an account appears between the upload and the restart, the first-run
// restore doesn't run: it would replace an install someone has set up.
func TestSetupRestoreSkippedOnceAnAccountExists(t *testing.T) {
	ctx := context.Background()
	fresh := storetest.Open(t, store.SQLite)
	var buf bytes.Buffer
	if _, err := Write(ctx, storetest.Open(t, store.SQLite), &buf, Meta{KeyCheck: "sealed-with-key-A"}); err != nil {
		t.Fatal(err)
	}
	set := settings.DefaultBackups()
	svc := newService(t, fresh, &set)
	if _, err := svc.StageSetupRestore(ctx, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.DB.NewRaw("INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)", "someone", "x", at).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	before := dump(t, fresh)
	RunPending(ctx, fresh, svc.dataDir, "test", svc.keyCheck, svc.keyOpens, svc.logger)
	compare(t, before, dump(t, fresh))
	if res := svc.LastResult(); res == nil || res.OK || !strings.Contains(res.Message, "An account exists") {
		t.Fatalf("result = %+v", res)
	}
}

// Two backups made in the same second must both survive, in order.
func TestSameSecondBackupsKeepBoth(t *testing.T) {
	set := settings.DefaultBackups()
	s := storetest.Open(t, store.SQLite)
	seed(t, s)
	svc := newService(t, s, &set)
	svc.now = func() time.Time { return time.Date(2026, 10, 13, 15, 4, 5, 0, time.UTC) }

	var made []string
	for range 2 {
		e, err := svc.Create(context.Background(), KindManual)
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, e.Name)
	}
	var up []string
	for range 2 {
		var buf bytes.Buffer
		if _, err := Write(context.Background(), s, &buf, Meta{Kind: KindScheduled}); err != nil {
			t.Fatal(err)
		}
		e, err := svc.Upload(&buf)
		if err != nil {
			t.Fatal(err)
		}
		up = append(up, e.Name)
	}
	want := []string{
		"reelkeeper-20261013-150405-manual.backup",
		"reelkeeper-20261013-150405-uploaded.backup",
		"reelkeeper-20261013-150405_2-manual.backup",
		"reelkeeper-20261013-150405_2-uploaded.backup",
	}
	if got := names(t, svc.Folder()); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("files = %v, want %v", got, want)
	}
	if made[0] == made[1] || up[0] == up[1] {
		t.Errorf("names repeated: %v %v", made, up)
	}
	// The later one sorts as newer, which pruning relies on.
	if made[1] <= made[0] {
		t.Errorf("%s should sort after %s", made[1], made[0])
	}
}

// What a killed backup, upload or restore leaves behind is removed at
// startup; backups and the restore request and result files are not.
func TestRemoveLeftovers(t *testing.T) {
	set := settings.DefaultBackups()
	set.Folder = filepath.Join(t.TempDir(), "elsewhere")
	svc := newService(t, storetest.Open(t, store.SQLite), &set)
	data := svc.dataDir
	custom := svc.Folder()
	fallback := filepath.Join(data, "backups")

	files := func(dir string, names ...string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	files(custom, ".writing-123", ".upload-456", "reelkeeper-20261013-000000-manual.backup", "notes.txt")
	files(fallback, ".writing-789", ".upload-012", "reelkeeper-20261013-000001-safety.backup")
	files(data, ".setup-upload-345", ".backup-checks-678", pendingFile, resultFile, setupFile, checksFile)
	files(filepath.Join(data, "restore-901"), "scratch.db", "scratch.db-wal")
	files(filepath.Join(data, "restore-photos"), "scratch.db", "holiday.jpg")
	files(filepath.Join(data, "other-folder"), "keep.txt")

	svc.RemoveLeftovers()

	want := map[string][]string{
		custom:   {"notes.txt", "reelkeeper-20261013-000000-manual.backup"},
		fallback: {"reelkeeper-20261013-000001-safety.backup"},
		data:     {"backups", checksFile, "other-folder", pendingFile, "restore-photos", resultFile, setupFile},
	}
	for dir, w := range want {
		got := names(t, dir)
		sort.Strings(w)
		if strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("%s holds %v, want %v", dir, got, w)
		}
	}
	// Other folders, and a restore-* folder holding anything but a scratch
	// database, are left alone with their contents.
	for _, f := range []string{"other-folder/keep.txt", "restore-photos/holiday.jpg"} {
		if _, err := os.Stat(filepath.Join(data, f)); err != nil {
			t.Error(err)
		}
	}
}

// The first Backups page after a restart must not read every file again,
// and a damaged file keeps showing its problem.
func TestCheckedFilesSurviveARestart(t *testing.T) {
	set := settings.DefaultBackups()
	s := storetest.Open(t, store.SQLite)
	seed(t, s)
	svc := newService(t, s, &set)
	made, err := svc.Create(context.Background(), KindManual)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(svc.Folder(), made.Name))
	if err != nil {
		t.Fatal(err)
	}
	cut := "reelkeeper-20260101-000000-truncated.backup"
	if err := os.WriteFile(filepath.Join(svc.Folder(), cut), data[:len(data)*2/3], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(); err != nil {
		t.Fatal(err)
	}

	restarted := func() (*Service, *int) {
		b := New(Config{DB: s, Settings: svc.settings, KeyCheck: svc.keyCheck, KeyOpens: svc.keyOpens,
			DataDir: svc.dataDir, App: "test", Logger: svc.logger})
		reads := new(int)
		b.inspect = func(path string) (*Summary, error) {
			*reads++
			return inspectFile(path)
		}
		return b, reads
	}
	b, reads := restarted()
	list, err := b.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if *reads != 0 {
		t.Errorf("after a restart %d files were read again", *reads)
	}
	for _, e := range list {
		if (e.Name == cut) != (e.Problem != "") || (e.Name == made.Name && (e.Header == nil || !e.KeyMatches)) {
			t.Errorf("entry after restart = %+v", e)
		}
	}

	// Files that are gone are dropped from the saved results.
	if err := b.Delete(cut); err != nil {
		t.Fatal(err)
	}
	if _, err := b.List(); err != nil {
		t.Fatal(err)
	}
	if b2, _ := restarted(); len(b2.checked) != 1 {
		t.Errorf("remembered %d files, want 1", len(b2.checked))
	}

	// A damaged results file is ignored: start empty and read the files.
	if err := os.WriteFile(filepath.Join(svc.dataDir, checksFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b3, reads3 := restarted()
	if list, err := b3.List(); err != nil || len(list) != 1 || list[0].Problem != "" || *reads3 != 1 {
		t.Errorf("with a damaged results file: %+v, %v, %d reads", list, err, *reads3)
	}
}

// A restore that is killed leaves a result saying so, instead of the
// previous restore's. The note must exist before anything is changed.
func TestRunPendingNotesAnInterruptedRestore(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t, store.SQLite)
	set := settings.DefaultBackups()
	svc := newService(t, s, &set)
	seed(t, s)
	e, err := svc.Create(ctx, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	// The previous restore, which succeeded.
	old, _ := json.Marshal(Result{Backup: "older.backup", OK: true, Message: "Restored older.backup"})
	if err := os.WriteFile(filepath.Join(svc.dataDir, resultFile), old, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StageRestore(ctx, e.Name); err != nil {
		t.Fatal(err)
	}

	// What the result says at the moments the safety backup and the restore
	// itself begin (a kill at either would leave exactly that).
	var seenAt []string
	note := func(where string) {
		r := svc.LastResult()
		if r == nil || r.OK || r.Backup != e.Name || !strings.Contains(r.Message, "stopped during the restore") {
			t.Errorf("at %s the result was %+v", where, r)
		}
		seenAt = append(seenAt, where)
	}
	RunPending(ctx, s, svc.dataDir, "test",
		func(c context.Context) (string, error) { note("safety backup"); return svc.keyCheck(c) },
		func(sealed string) bool { note("restore"); return svc.keyOpens(sealed) }, svc.logger)
	if len(seenAt) < 2 {
		t.Errorf("checked only at %v", seenAt)
	}
	// The real outcome replaces the note.
	if r := svc.LastResult(); r == nil || !r.OK || r.Backup != e.Name {
		t.Errorf("final result = %+v", r)
	}
}
