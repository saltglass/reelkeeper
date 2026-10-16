package api_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/backup"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

type backupEntry struct {
	Name          string  `json:"name"`
	Size          int64   `json:"size"`
	CreatedAt     *string `json:"createdAt"`
	Kind          *string `json:"kind"`
	App           *string `json:"app"`
	Engine        *string `json:"engine"`
	SchemaVersion *int64  `json:"schemaVersion"`
	KeyMatches    bool    `json:"keyMatches"`
	Problem       *string `json:"problem"`
}

type backupList struct {
	Folder      string        `json:"folder"`
	Backups     []backupEntry `json:"backups"`
	LastRestore *struct {
		Backup  string  `json:"backup"`
		Ok      bool    `json:"ok"`
		Message string  `json:"message"`
		Safety  *string `json:"safety"`
	} `json:"lastRestore"`
}

// makeBackup creates a manual backup through the API.
func makeBackup(t *testing.T, a *app) backupEntry {
	t.Helper()
	r := a.do("POST", "/api/v1/backups", "")
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.raw)
	}
	return backupEntry{Name: r.body["name"].(string)}
}

func TestBackupsListAndCreate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		var l backupList
		a.get("/api/v1/backups", &l)
		if len(l.Backups) != 0 || l.LastRestore != nil || l.Folder != filepath.Join(a.dataDir, "backups") {
			t.Fatalf("empty list: %+v", l)
		}
		if !strings.Contains(a.do("GET", "/api/v1/backups", "").raw, `"backups":[]`) {
			t.Error("an empty list should be [], not null")
		}

		r := a.do("POST", "/api/v1/backups", "")
		if r.status != 201 || r.body["kind"] != "manual" || r.body["keyMatches"] != true || r.body["engine"] != string(db.Engine) || r.body["size"].(float64) <= 0 {
			t.Fatalf("create: %d %s", r.status, r.raw)
		}
		name := r.body["name"].(string)
		if !strings.HasPrefix(name, "reelkeeper-") || !strings.HasSuffix(name, "-manual.backup") {
			t.Errorf("name = %q", name)
		}

		// A junk file shows up with its problem and no header fields.
		if err := os.WriteFile(filepath.Join(a.dataDir, "backups", "junk.backup"), []byte("not a backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		a.get("/api/v1/backups", &l)
		if len(l.Backups) != 2 {
			t.Fatalf("list: %+v", l)
		}
		var good, junk backupEntry
		for _, e := range l.Backups {
			if e.Name == name {
				good = e
			} else {
				junk = e
			}
		}
		if good.Kind == nil || *good.Kind != "manual" || good.CreatedAt == nil || good.App == nil || *good.App != "test" || good.SchemaVersion == nil || !good.KeyMatches || good.Problem != nil {
			t.Errorf("good = %+v", good)
		}
		if junk.Name != "junk.backup" || junk.Problem == nil || *junk.Problem == "" || junk.Kind != nil || junk.CreatedAt != nil {
			t.Errorf("junk = %+v", junk)
		}

		a.signOut()
		for _, p := range []string{"/api/v1/backups", "/api/v1/settings/backups"} {
			if r := a.do("GET", p, ""); r.status != 401 {
				t.Errorf("signed out %s: %d", p, r.status)
			}
		}
	})
}

func TestBackupDownloadDeleteAndUpload(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		name := makeBackup(t, a).Name

		r := a.do("GET", "/api/v1/backups/"+name, "")
		if r.status != 200 || r.header.Get("Content-Type") != "application/gzip" ||
			r.header.Get("Content-Disposition") != `attachment; filename="`+name+`"` {
			t.Fatalf("download: %d %v", r.status, r.header)
		}
		onDisk, err := os.ReadFile(filepath.Join(a.dataDir, "backups", name))
		if err != nil {
			t.Fatal(err)
		}
		if r.raw != string(onDisk) {
			t.Error("the downloaded bytes differ from the file")
		}

		// Uploading the same file back (as another installation would).
		up := a.do("POST", "/api/v1/backups/upload", r.raw, "Content-Type", "application/octet-stream")
		if up.status != 201 || !strings.HasSuffix(up.body["name"].(string), "-uploaded.backup") || up.body["keyMatches"] != true {
			t.Fatalf("upload: %d %s", up.status, up.raw)
		}
		var l backupList
		a.get("/api/v1/backups", &l)
		if len(l.Backups) != 2 {
			t.Fatalf("after upload: %+v", l)
		}

		// Junk is refused with the reason, and nothing is saved.
		bad := a.do("POST", "/api/v1/backups/upload", "this is not a backup", "Content-Type", "application/octet-stream")
		if bad.status != 422 || bad.body["code"] != "backup" || !strings.Contains(bad.body["message"].(string), "isn't a Reelkeeper backup") {
			t.Errorf("junk upload: %d %s", bad.status, bad.raw)
		}
		a.get("/api/v1/backups", &l)
		if len(l.Backups) != 2 {
			t.Errorf("junk was saved: %+v", l)
		}

		// A body larger than the usual 1 MiB API limit still gets through
		// to the backup check (and is refused there as not a backup).
		big := a.do("POST", "/api/v1/backups/upload", strings.Repeat("x", 2<<20), "Content-Type", "application/octet-stream")
		if big.status != 422 {
			t.Errorf("2 MiB upload: %d %s", big.status, big.raw)
		}

		if r := a.do("DELETE", "/api/v1/backups/"+name, ""); r.status != 204 {
			t.Errorf("delete: %d %s", r.status, r.raw)
		}
		if _, err := os.Stat(filepath.Join(a.dataDir, "backups", name)); !os.IsNotExist(err) {
			t.Errorf("the file is still there: %v", err)
		}
		if r := a.do("DELETE", "/api/v1/backups/"+name, ""); r.status != 404 {
			t.Errorf("delete again: %d", r.status)
		}
	})
}

func TestBackupCheckAndRestore(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		name := makeBackup(t, a).Name

		r := a.do("POST", "/api/v1/backups/"+name+"/check", "")
		if r.status != 200 || r.body["kind"] != "manual" || r.body["app"] != "test" || r.body["engine"] != string(db.Engine) {
			t.Fatalf("check: %d %s", r.status, r.raw)
		}
		rows := r.body["rows"].(map[string]any)
		var sum float64
		for _, n := range rows {
			sum += n.(float64)
		}
		if len(rows) == 0 || sum != r.body["total"].(float64) || rows["users"] == nil && rows["settings"] == nil {
			t.Errorf("rows = %v, total %v", rows, r.body["total"])
		}

		// A damaged file can't be checked or restored, and nothing is staged.
		if err := os.WriteFile(filepath.Join(a.dataDir, "backups", "junk.backup"), []byte("not a backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		pending := filepath.Join(a.dataDir, "restore-pending.json")
		for _, op := range []string{"check", "restore"} {
			r := a.do("POST", "/api/v1/backups/junk.backup/"+op, "")
			if r.status != 422 || r.body["code"] != "backup" || r.body["message"] == "" {
				t.Errorf("%s junk: %d %s", op, r.status, r.raw)
			}
		}
		if _, err := os.Stat(pending); !os.IsNotExist(err) {
			t.Fatalf("a restore was staged for a junk file: %v", err)
		}

		r = a.do("POST", "/api/v1/backups/"+name+"/restore", "")
		if r.status != 202 || r.body["message"] != "Reelkeeper is restarting to restore "+name+"." {
			t.Fatalf("restore: %d %s", r.status, r.raw)
		}
		b, err := os.ReadFile(pending)
		if err != nil || !strings.Contains(string(b), name) {
			t.Errorf("restore-pending.json: %v %s", err, b)
		}
	})
}

func TestBackupBadNames(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	makeBackup(t, a)
	// A real file that isn't a backup name must not be reachable.
	if err := os.WriteFile(filepath.Join(a.dataDir, "secret.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..%2Fsecret.txt", "..%2Fx", "x.txt", "secret.txt", "nope.backup", "..%2Fbackups%2Freelkeeper.backup"} {
		for _, c := range [][2]string{{"GET", ""}, {"DELETE", ""}, {"POST", "/check"}, {"POST", "/restore"}} {
			if r := a.do(c[0], "/api/v1/backups/"+name+c[1], ""); r.status != 404 {
				t.Errorf("%s %s%s: %d %s", c[0], name, c[1], r.status, r.raw)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, "restore-pending.json")); !os.IsNotExist(err) {
		t.Error("a restore was staged for a bad name")
	}
}

func TestBackupLastRestore(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	res := `{"at":"2026-10-11T04:00:00Z","backup":"reelkeeper-1-manual.backup","ok":true,"message":"Restored it.","safety":"reelkeeper-2-safety.backup"}`
	if err := os.WriteFile(filepath.Join(a.dataDir, "restore-result.json"), []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	var l backupList
	a.get("/api/v1/backups", &l)
	if l.LastRestore == nil || !l.LastRestore.Ok || l.LastRestore.Backup != "reelkeeper-1-manual.backup" ||
		l.LastRestore.Message != "Restored it." || l.LastRestore.Safety == nil || *l.LastRestore.Safety != "reelkeeper-2-safety.backup" {
		t.Errorf("lastRestore = %+v", l.LastRestore)
	}
}

func TestBackupSettingsAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		def := filepath.Join(a.dataDir, "backups")

		r := a.do("GET", "/api/v1/settings/backups", "")
		if r.status != 200 || r.body["scheduled"] != true || r.body["time"] != "03:30" || r.body["keep"].(float64) != 14 ||
			r.body["folder"] != "" || r.body["effectiveFolder"] != def {
			t.Fatalf("defaults: %d %s", r.status, r.raw)
		}

		for _, bad := range []struct{ body, field string }{
			{`{"folder":"relative/path","scheduled":true,"time":"03:30","keep":14}`, "folder"},
			{`{"folder":"","scheduled":true,"time":"25:99","keep":14}`, "time"},
			{`{"folder":"","scheduled":true,"time":"03:30","keep":0}`, "keep"},
			{`{"folder":"","scheduled":true,"time":"03:30","keep":1000}`, "keep"},
		} {
			r := a.do("PUT", "/api/v1/settings/backups", bad.body)
			if r.status != 422 || r.body["fields"].(map[string]any)[bad.field] == nil {
				t.Errorf("%s: %d %s", bad.body, r.status, r.raw)
			}
		}

		dir := t.TempDir()
		r = a.do("PUT", "/api/v1/settings/backups", `{"folder":"`+dir+`","scheduled":false,"time":"04:15","keep":3}`)
		if r.status != 200 || r.body["folder"] != dir || r.body["effectiveFolder"] != dir || r.body["scheduled"] != false || r.body["keep"].(float64) != 3 {
			t.Fatalf("save: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/settings/backups", ""); r.body["time"] != "04:15" || r.body["effectiveFolder"] != dir {
			t.Errorf("reload: %s", r.raw)
		}

		// Backups now land in the chosen folder.
		name := makeBackup(t, a).Name
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("not in the chosen folder: %v", err)
		}
		var l backupList
		a.get("/api/v1/backups", &l)
		if l.Folder != dir || len(l.Backups) != 1 {
			t.Errorf("list: %+v", l)
		}

		// Emptying the folder goes back to the data folder.
		r = a.do("PUT", "/api/v1/settings/backups", `{"folder":"","scheduled":true,"time":"03:30","keep":14}`)
		if r.status != 200 || r.body["effectiveFolder"] != def {
			t.Errorf("reset: %d %s", r.status, r.raw)
		}
	})
}

// The first-run screen can start from a backup instead of a new account:
// with no sign-in, only while no account exists, and only a usable file.
func TestSetupRestoreAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		// A backup made with this install's key (as one made before a move
		// to another database would be).
		e, err := a.backups.Create(context.Background(), backup.KindManual)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.ReadFile(filepath.Join(a.dataDir, "backups", e.Name))
		if err != nil {
			t.Fatal(err)
		}

		bad := a.do("POST", "/api/v1/auth/setup/restore", "not a backup", "Content-Type", "application/octet-stream")
		if bad.status != 422 || bad.body["code"] != "backup" {
			t.Errorf("junk: %d %s", bad.status, bad.raw)
		}
		ok := a.do("POST", "/api/v1/auth/setup/restore", string(file), "Content-Type", "application/octet-stream")
		if ok.status != 202 {
			t.Fatalf("restore: %d %s", ok.status, ok.raw)
		}
		b, err := os.ReadFile(filepath.Join(a.dataDir, "restore-pending.json"))
		if err != nil || !strings.Contains(string(b), `"setupOnly":true`) {
			t.Errorf("restore-pending.json: %v %s", err, b)
		}

		a.setup()
		a.signOut()
		if r := a.do("POST", "/api/v1/auth/setup/restore", string(file), "Content-Type", "application/octet-stream"); r.status != 409 {
			t.Errorf("with an account: %d %s", r.status, r.raw)
		}
	})
}
