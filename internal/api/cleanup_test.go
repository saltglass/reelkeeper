package api_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

func TestCleanupSettingsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()

	r := a.do("GET", "/api/v1/settings/cleanup", "")
	if r.status != 200 || r.body["mode"] != "dryrun" || r.body["seedHours"].(float64) != 24 {
		t.Fatalf("defaults: %d %s", r.status, r.raw)
	}
	body := `{"mode":"on","seedRatio":2,"seedHours":48,"stalledHours":0,"slowKBps":0,"slowHours":0,"metadataHours":2,"importFailedDays":3,"unregistered":false,"missingFiles":true,"emptyReplacedDays":0}`
	if r := a.do("PUT", "/api/v1/settings/cleanup", body); r.status != 200 || r.body["mode"] != "on" || r.body["unregistered"] != false {
		t.Fatalf("save: %d %s", r.status, r.raw)
	}
	if r := a.do("GET", "/api/v1/settings/cleanup", ""); r.body["seedRatio"].(float64) != 2 || r.body["stalledHours"].(float64) != 0 {
		t.Errorf("reload: %s", r.raw)
	}
	bad := strings.Replace(strings.Replace(body, `"mode":"on"`, `"mode":"sometimes"`, 1), `"slowKBps":0`, `"slowKBps":50`, 1)
	r = a.do("PUT", "/api/v1/settings/cleanup", bad)
	if r.status != 422 || !strings.Contains(r.raw, `"mode"`) || !strings.Contains(r.raw, `"slowHours"`) {
		t.Errorf("validation: %d %s", r.status, r.raw)
	}
}

func TestDownloadClientFolderAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	useFake(a, &fakeQB{})
	r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"u","password":"secret","folder":"/mnt/downloads/reelkeeper/"}`)
	if r.status != 200 || r.body["folder"] != "/mnt/downloads/reelkeeper" {
		t.Fatalf("save: %d %s", r.status, r.raw)
	}
	if r := a.do("GET", "/api/v1/settings/download-client", ""); r.body["folder"] != "/mnt/downloads/reelkeeper" {
		t.Errorf("get: %s", r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"u","folder":"relative"}`); r.status != 422 || !strings.Contains(r.raw, `"folder"`) {
		t.Errorf("relative folder: %d %s", r.status, r.raw)
	}
}

func TestCleanupActivityAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	f := &fakeQB{}
	useFake(a, f)
	ctx := context.Background()
	folder := t.TempDir()
	if _, err := a.settings.SaveDownloadClient(ctx, settings.DownloadClientUpdate{
		URL: "http://qb:8080", Username: "u", Password: ptr("secret"), Folder: folder}); err != nil {
		t.Fatal(err)
	}
	a.downloads.SetCleanup(a.settings.Cleanup, nil, nil)

	// Events: paging and the movie reference.
	titleID, _, dlID := queueFixture(t, db, a, "abc123")
	for i := 0; i < 3; i++ {
		ev := &download.CleanupEvent{CreatedAt: time.Now().UTC(), DownloadID: &dlID, TitleID: &titleID,
			ReleaseTitle: fmt.Sprintf("Release %d", i), Action: download.ActionWouldRemove, Rule: "stalled", Message: "No progress"}
		if _, err := db.DB.NewInsert().Model(ev).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DB.NewInsert().Model(&download.CleanupEvent{CreatedAt: time.Now().UTC(), ReleaseTitle: "",
		Action: download.ActionWouldEmpty, Rule: "replaced-files", Message: "old"}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	r := a.do("GET", "/api/v1/cleanup/events?limit=2", "")
	evs := r.body["events"].([]any)
	if r.status != 200 || len(evs) != 2 || r.body["nextBefore"] == nil {
		t.Fatalf("events page 1: %d %s", r.status, r.raw)
	}
	if first := evs[0].(map[string]any); first["rule"] != "replaced-files" || first["movie"] != nil {
		t.Errorf("newest first, no movie: %s", r.raw)
	}
	if m := evs[1].(map[string]any)["movie"].(map[string]any); m["title"] == "" {
		t.Errorf("movie ref: %s", r.raw)
	}
	r = a.do("GET", fmt.Sprintf("/api/v1/cleanup/events?before=%v", r.body["nextBefore"]), "")
	if len(r.body["events"].([]any)) != 2 || r.body["nextBefore"] != nil {
		t.Errorf("events page 2: %s", r.raw)
	}

	// Seeding: an imported download with a live torrent.
	if _, err := db.DB.NewUpdate().Model((*download.Download)(nil)).Set("status = ?", download.StatusImported).
		Set("imported_at = ?", time.Now().UTC()).Set("seed_ratio = 1.5").Where("id = ?", dlID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	f.torrents = []download.Torrent{{Hash: "abc123", Name: "Dune", State: torrent.Seeding, Progress: 1, Size: 100, Ratio: 0.2, SeedingTime: time.Hour}}
	r = a.do("GET", "/api/v1/cleanup/seeding", "")
	items := r.body["items"].([]any)
	if r.status != 200 || len(items) != 1 {
		t.Fatalf("seeding: %d %s", r.status, r.raw)
	}
	it := items[0].(map[string]any)
	tor := it["torrent"].(map[string]any)
	if it["seedRatio"].(float64) != 1.5 || it["seedTimeMinutes"] != nil || tor["ratio"].(float64) != 0.2 ||
		tor["seedingTimeSeconds"].(float64) != 3600 || tor["state"] != "seeding" || it["waiting"] == "" || it["movie"] == nil ||
		it["mediaType"] != "movie" || it["episodes"] != "" {
		t.Errorf("seeding item: %s", r.raw)
	}
	f.torrents = nil
	if r := a.do("GET", "/api/v1/cleanup/seeding", ""); r.body["items"].([]any)[0].(map[string]any)["torrent"] != nil {
		t.Errorf("missing torrent should be null: %s", r.raw)
	}

	// Orphans: an untracked torrent and a stray file.
	if err := os.WriteFile(filepath.Join(folder, "stray.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.torrents = []download.Torrent{
		{Hash: "abc123", Name: "Dune", Progress: 1, ContentPath: filepath.Join(folder, "Dune")},
		{Hash: "def456", Name: "Other", State: torrent.Paused, Progress: 0.5, Size: 9, ContentPath: filepath.Join(folder, "Other")},
	}
	r = a.do("GET", "/api/v1/cleanup/orphans", "")
	ts := r.body["torrents"].([]any)
	if r.status != 200 || len(ts) != 1 || ts[0].(map[string]any)["hash"] != "def456" || ts[0].(map[string]any)["state"] != "paused" ||
		fmt.Sprint(r.body["files"]) != "[stray.mkv]" || r.body["folder"] != folder || r.body["clientError"] != nil {
		t.Fatalf("orphans: %d %s", r.status, r.raw)
	}
	// A tracked torrent can't be removed from here.
	if r := a.do("POST", "/api/v1/cleanup/orphans/torrents/abc123/remove", ""); r.status != 422 || !strings.Contains(r.raw, "Queue") {
		t.Errorf("tracked: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/cleanup/orphans/torrents/def456/remove", ""); r.status != 204 || len(f.removed) != 1 || f.removed[0] != "def456" {
		t.Errorf("remove torrent: %d %s %v", r.status, r.raw, f.removed)
	}
	if r := a.do("POST", "/api/v1/cleanup/orphans/files/remove", `{"name":"../etc"}`); r.status != 422 {
		t.Errorf("path escape: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/cleanup/orphans/files/remove", `{"name":"stray.mkv"}`); r.status != 204 {
		t.Errorf("remove file: %d %s", r.status, r.raw)
	}
	if _, err := os.Stat(filepath.Join(folder, "stray.mkv")); !os.IsNotExist(err) {
		t.Errorf("file still there: %v", err)
	}

	// No client: a message, not an error.
	if _, err := a.settings.SaveDownloadClient(ctx, settings.DownloadClientUpdate{}); err != nil {
		t.Fatal(err)
	}
	r = a.do("GET", "/api/v1/cleanup/orphans", "")
	if r.status != 200 || r.body["clientError"] == nil || len(r.body["torrents"].([]any)) != 0 {
		t.Errorf("no client: %d %s", r.status, r.raw)
	}
}
