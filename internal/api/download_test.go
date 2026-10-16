package api_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// fakeQB is an in-memory download client. It records the settings it was
// built with, so tests can check which password was used.
type fakeQB struct {
	name     string // what Name answers; "qBittorrent" when empty
	cfg      settings.DownloadClient
	torrents []download.Torrent
	fail     error // returned by every call
	removed  []string
	calls    []string // Stop, Start and TopPriority calls, as "stop <hash>"
}

func (f *fakeQB) Name() string {
	if f.name == "" {
		return "qBittorrent"
	}
	return f.name
}
func (f *fakeQB) Version(context.Context) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	if f.cfg.Password != "secret" {
		return "", download.ErrLogin
	}
	return "v5.2.2", nil
}
func (f *fakeQB) EnsureCategory(context.Context, string, string) error         { return f.fail }
func (f *fakeQB) MoveCategory(context.Context, string, string, []string) error { return f.fail }
func (f *fakeQB) Add(context.Context, download.Add) (string, error)            { return "", f.fail }

// Torrents lists one category, as qBittorrent does. Test torrents with
// no category are taken to be movies.
func (f *fakeQB) Torrents(_ context.Context, cat string) ([]download.Torrent, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	var out []download.Torrent
	for _, t := range f.torrents {
		if t.Category == cat || (t.Category == "" && cat == settings.MovieCategory) {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeQB) NoShareLimits(context.Context, string, []string) error { return nil }
func (f *fakeQB) TopPriority(_ context.Context, _ string, hashes []string) error {
	return f.record("top", hashes)
}
func (f *fakeQB) Stop(_ context.Context, _ string, hashes []string) error {
	return f.record("stop", hashes)
}
func (f *fakeQB) Start(_ context.Context, _ string, hashes []string) error {
	return f.record("start", hashes)
}
func (f *fakeQB) record(what string, hashes []string) error {
	for _, h := range hashes {
		f.calls = append(f.calls, what+" "+h)
	}
	return f.fail
}
func (f *fakeQB) TrackerMessages(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (f *fakeQB) Source(_ context.Context, _, hash string) (download.Source, error) {
	return download.Source{Magnet: "magnet:?xt=urn:btih:" + hash}, nil
}
func (f *fakeQB) Layout(context.Context, string, string) (download.Layout, error) {
	return download.Layout{}, nil
}
func (f *fakeQB) Recheck(context.Context, string, string) error           { return nil }
func (f *fakeQB) SetFolder(context.Context, string, string, string) error { return nil }

func (f *fakeQB) Remove(_ context.Context, _ string, hashes []string, _ bool) error {
	f.removed = append(f.removed, hashes...)
	return f.fail
}

func useFake(a *app, f *fakeQB) {
	a.downloads.NewClient = func(c settings.DownloadClient) download.Client {
		f.cfg = c
		return f
	}
}

func TestDownloadClientAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	f := &fakeQB{}
	useFake(a, f)

	if r := a.do("GET", "/api/v1/settings/download-client", ""); r.status != 200 || r.body["url"] != "" || r.body["passwordSet"] != false {
		t.Errorf("empty: %d %s", r.status, r.raw)
	}
	// "On" needs a client.
	r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","queueLimit":0,"minSeeders":1}`)
	if r.status != 422 || !strings.Contains(r.raw, "Set up the download client first (below).") {
		t.Errorf("on without client: %d %s", r.status, r.raw)
	}
	r = a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"dryrun","tvMode":"on","queueLimit":0,"minSeeders":1}`)
	if r.status != 422 || !strings.Contains(r.raw, `"tvMode"`) {
		t.Errorf("TV on without client: %d %s", r.status, r.raw)
	}
	// TV starts in dry run, and saving without tvMode keeps it.
	if r := a.do("GET", "/api/v1/settings/grabbing", ""); r.body["tvMode"] != "dryrun" {
		t.Errorf("default TV mode: %s", r.raw)
	}
	a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"dryrun","tvMode":"off","queueLimit":0,"minSeeders":1}`)
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"dryrun","queueLimit":0,"minSeeders":1}`); r.status != 200 || r.body["tvMode"] != "off" {
		t.Errorf("tvMode left out: %d %s", r.status, r.raw)
	}
	// Bad values are field errors.
	if r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"nonsense","username":""}`); r.status != 422 || !strings.Contains(r.raw, `"url"`) || !strings.Contains(r.raw, `"username"`) {
		t.Errorf("validation: %d %s", r.status, r.raw)
	}
	// Save; the password never comes back.
	r = a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://192.168.1.20:8080/","username":"admin","password":"secret"}`)
	if r.status != 200 || r.body["url"] != "http://192.168.1.20:8080" || r.body["passwordSet"] != true || strings.Contains(r.raw, "secret") {
		t.Errorf("save: %d %s", r.status, r.raw)
	}
	// Omitting the password keeps it.
	r = a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://192.168.1.20:8080","username":"dan"}`)
	if r.status != 200 || r.body["username"] != "dan" || r.body["passwordSet"] != true {
		t.Errorf("keep password: %d %s", r.status, r.raw)
	}
	if got := a.settings.DownloadClient().Password; got != "secret" {
		t.Errorf("stored password = %q", got)
	}
	if r := a.do("GET", "/api/v1/settings/download-client", ""); strings.Contains(r.raw, "secret") || r.body["passwordSet"] != true {
		t.Errorf("get: %s", r.raw)
	}

	// Test: unsaved values, omitted password uses the stored one.
	if r := a.do("POST", "/api/v1/settings/download-client/test", `{"url":"http://other:8080","username":"x"}`); r.status != 200 || r.body["version"] != "v5.2.2" {
		t.Errorf("test ok: %d %s", r.status, r.raw)
	}
	if f.cfg.URL != "http://other:8080" {
		t.Errorf("test used %q, want the unsaved address", f.cfg.URL)
	}
	if got := a.settings.DownloadClient().URL; got != "http://192.168.1.20:8080" {
		t.Errorf("test must not save: %q", got)
	}
	r = a.do("POST", "/api/v1/settings/download-client/test", `{"url":"http://other:8080","username":"x","password":"wrong"}`)
	if r.status != 422 || !strings.Contains(r.raw, "refused the username or password") {
		t.Errorf("test wrong password: %d %s", r.status, r.raw)
	}
	f.fail = errors.New("can't reach qBittorrent: connection refused")
	if r := a.do("POST", "/api/v1/settings/download-client/test", `{"url":"http://other:8080","username":"x"}`); r.status != 422 || !strings.Contains(r.raw, "Can't reach qBittorrent") {
		t.Errorf("test unreachable: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/settings/download-client/test", `{"url":"","username":""}`); r.status != 422 {
		t.Errorf("test empty: %d %s", r.status, r.raw)
	}

	// With a client saved, "On" is accepted, and turning it on searches
	// straight away.
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","queueLimit":5,"minSeeders":1}`); r.status != 200 {
		t.Errorf("on with client: %d %s", r.status, r.raw)
	}
	queued := queuedPasses(t, a)
	if len(queued) != 1 {
		t.Fatalf("turning grabbing on queued %d search passes, want 1", len(queued))
	}
	if err := a.jobs.Cancel(context.Background(), queued[0]); err != nil {
		t.Fatal(err)
	}
	// Lowering the queue limit needs no search; raising or removing it does.
	a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","queueLimit":3,"minSeeders":1}`)
	if n := len(queuedPasses(t, a)); n != 0 {
		t.Errorf("lowering the limit queued %d passes", n)
	}
	a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","queueLimit":20,"minSeeders":1}`)
	if n := len(queuedPasses(t, a)); n != 1 {
		t.Errorf("raising the limit queued %d passes, want 1", n)
	}
	// Removing the client drops movies and TV back to a dry run.
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","tvMode":"on","queueLimit":20,"minSeeders":1}`); r.status != 200 {
		t.Errorf("TV on with client: %d %s", r.status, r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"","username":""}`); r.status != 200 || r.body["passwordSet"] != false {
		t.Errorf("remove: %d %s", r.status, r.raw)
	}
	if r := a.do("GET", "/api/v1/settings/grabbing", ""); r.body["mode"] != "dryrun" || r.body["tvMode"] != "dryrun" {
		t.Errorf("grabbing after removing the client: %s", r.raw)
	}
}

// queueFixture adds a movie and a downloading row for it.
func queueFixture(t *testing.T, db *store.Store, a *app, hash string) (titleID, versionID, downloadID int64) {
	t.Helper()
	root := addMovieFixture(t, a)
	r := a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d,"monitored":false}`, root))
	if r.status != 200 {
		t.Fatalf("add: %d %s", r.status, r.raw)
	}
	titleID = int64(r.body["id"].(float64))
	versionID = int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))
	d := &download.Download{
		CreatedAt: time.Now().UTC(), TitleID: &titleID, VersionID: &versionID, Hash: hash,
		Category: settings.MovieCategory, ReleaseTitle: "Dune.2021.1080p.WEB-DL", Indexer: "ix",
		Quality: "WEBDL-1080p", Score: 10, Size: 5 << 30, Status: download.StatusDownloading,
	}
	if _, err := db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	return titleID, versionID, d.ID
}

func TestQueueAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		f := &fakeQB{cfg: settings.DownloadClient{}}
		useFake(a, f)
		if _, err := a.settings.SaveDownloadClient(context.Background(), settings.DownloadClientUpdate{
			URL: "http://qb:8080", Username: "u", Password: ptr("secret")}); err != nil {
			t.Fatal(err)
		}

		if r := a.do("GET", "/api/v1/queue", ""); r.status != 200 || len(r.body["items"].([]any)) != 0 || r.body["clientError"] != nil {
			t.Fatalf("empty queue: %d %s", r.status, r.raw)
		}
		titleID, versionID, id := queueFixture(t, db, a, "abc123")
		f.torrents = []download.Torrent{{Hash: "abc123", State: torrent.Downloading, Progress: 0.5, Size: 100, Left: 50, Speed: 2048, ETA: 90 * time.Second, Seeds: 4, Peers: 7}}

		r := a.do("GET", "/api/v1/queue", "")
		items := r.body["items"].([]any)
		if r.status != 200 || len(items) != 1 {
			t.Fatalf("queue: %d %s", r.status, r.raw)
		}
		it := items[0].(map[string]any)
		movie := it["movie"].(map[string]any)
		tor := it["torrent"].(map[string]any)
		if int64(movie["id"].(float64)) != titleID || movie["title"] == "" || it["profile"] == nil || it["status"] != "downloading" ||
			tor["state"] != "downloading" || tor["progress"].(float64) != 0.5 || tor["etaSeconds"].(float64) != 90 || tor["speed"].(float64) != 2048 {
			t.Errorf("item: %s", r.raw)
		}
		_ = versionID
		// The page words its messages with the client's name.
		if r.body["clientName"] != "qBittorrent" {
			t.Errorf("clientName = %v", r.body["clientName"])
		}
		f.name = "Transmission"
		if r := a.do("GET", "/api/v1/queue", ""); r.body["clientName"] != "Transmission" {
			t.Errorf("clientName = %v, want Transmission", r.body["clientName"])
		}
		f.name = ""

		// The queue says whether it is full: grabbing On and the queue (one
		// download) at the limit. Dry run never is.
		for _, c := range []struct {
			mode  string
			limit int
			full  bool
		}{{"on", 1, true}, {"on", 2, false}, {"on", 0, false}, {"dryrun", 1, false}} {
			// Saved directly: the settings route would start a search.
			g := settings.Grabbing{Mode: c.mode, TVMode: settings.GrabOff, QueueLimit: c.limit, MinSeeders: 1}
			if _, err := a.settings.SaveGrabbing(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			r := a.do("GET", "/api/v1/queue", "")
			if r.body["limit"].(float64) != float64(c.limit) || r.body["full"] != c.full {
				t.Errorf("%s limit %d: queue limit/full = %v/%v, want full = %v", c.mode, c.limit, r.body["limit"], r.body["full"], c.full)
			}
		}

		// qBittorrent unreachable: the list stays, with a warning.
		f.fail = errors.New("can't reach qBittorrent: connection refused")
		r = a.do("GET", "/api/v1/queue", "")
		it = r.body["items"].([]any)[0].(map[string]any)
		if it["torrent"] != nil || !strings.Contains(fmt.Sprint(r.body["clientError"]), "Can't reach qBittorrent") {
			t.Errorf("client down: %s", r.raw)
		}
		f.fail = nil

		// A torrent that qBittorrent doesn't list has a null torrent.
		f.torrents = nil
		if r := a.do("GET", "/api/v1/queue", ""); r.body["items"].([]any)[0].(map[string]any)["torrent"] != nil {
			t.Errorf("missing torrent: %s", r.raw)
		}
		f.torrents = []download.Torrent{{Hash: "abc123", State: torrent.Seeding, Progress: 1}}

		// Remove with a new search: the release is blocklisted even
		// though blocklist wasn't asked for, or the search would grab it
		// again.
		path := fmt.Sprintf("/api/v1/queue/%d/remove", id)
		if r := a.do("POST", "/api/v1/queue/999/remove", `{"blocklist":false,"searchAgain":false}`); r.status != 404 {
			t.Errorf("unknown: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", path, `{"blocklist":false,"searchAgain":true}`); r.status != 204 {
			t.Fatalf("remove: %d %s", r.status, r.raw)
		}
		if len(f.removed) != 1 || f.removed[0] != "abc123" {
			t.Errorf("removed from client: %v", f.removed)
		}
		if got := searchJobs(t, a); len(got) != 1 || got[0] != fmt.Sprintf(`{"versionId":%d,"trigger":"manual"}`, versionID) {
			t.Errorf("search jobs = %v", got)
		}
		if r := a.do("GET", "/api/v1/queue", ""); len(r.body["items"].([]any)) != 0 {
			t.Errorf("queue after remove: %s", r.raw)
		}
		if r := a.do("POST", path, `{"blocklist":false,"searchAgain":false}`); r.status != 422 {
			t.Errorf("remove twice: %d %s", r.status, r.raw)
		}

		// Blocklist: the movie isn't monitored, so nothing is wanted for it
		// and the entry is dormant: hidden unless asked for.
		r = a.do("GET", "/api/v1/blocklist", "")
		if r.status != 200 || len(r.body["items"].([]any)) != 0 || r.body["total"].(float64) != 1 || r.body["dormant"].(float64) != 1 {
			t.Fatalf("blocklist: %d %s", r.status, r.raw)
		}
		r = a.do("GET", "/api/v1/blocklist?include=dormant", "")
		bl := r.body["items"].([]any)
		if r.status != 200 || len(bl) != 1 {
			t.Fatalf("blocklist with dormant: %d %s", r.status, r.raw)
		}
		b := bl[0].(map[string]any)
		if b["releaseTitle"] != "Dune.2021.1080p.WEB-DL" || b["indexer"] != "ix" || b["reason"] == "" || b["movie"].(map[string]any)["title"] == "" ||
			b["mediaType"] != "movie" || b["rule"] != "removed" || b["permanent"] != true || b["expiresAt"] != nil || b["dormant"] != true || b["episodes"] != "" {
			t.Errorf("blocklist item: %s", r.raw)
		}
		bid := int64(b["id"].(float64))
		if r := a.do("DELETE", fmt.Sprintf("/api/v1/blocklist/%d", bid), ""); r.status != 204 {
			t.Errorf("unblock: %d %s", r.status, r.raw)
		}
		if r := a.do("DELETE", fmt.Sprintf("/api/v1/blocklist/%d", bid), ""); r.status != 404 {
			t.Errorf("unblock again: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/blocklist?include=dormant", ""); len(r.body["items"].([]any)) != 0 || r.body["total"].(float64) != 0 {
			t.Errorf("blocklist after delete: %s", r.raw)
		}

		// Remove without blocklisting or searching changes nothing else.
		_, _, id2 := queueFixtureAgain(t, db, a, titleID, versionID, "def456")
		a.do("POST", fmt.Sprintf("/api/v1/queue/%d/remove", id2), `{"blocklist":false,"searchAgain":false}`)
		if r := a.do("GET", "/api/v1/blocklist?include=dormant", ""); len(r.body["items"].([]any)) != 0 {
			t.Errorf("unexpected blocklist: %s", r.raw)
		}
	})
}

func queueFixtureAgain(t *testing.T, db *store.Store, a *app, titleID, versionID int64, hash string) (int64, int64, int64) {
	t.Helper()
	d := &download.Download{
		CreatedAt: time.Now().UTC(), TitleID: &titleID, VersionID: &versionID, Hash: hash,
		Category: settings.MovieCategory, ReleaseTitle: "Dune.2021.2160p", Indexer: "ix", Status: download.StatusDownloading,
	}
	if _, err := db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	return titleID, versionID, d.ID
}

func TestRetryImportAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	f := &fakeQB{}
	useFake(a, f)
	if _, err := a.settings.SaveDownloadClient(context.Background(), settings.DownloadClientUpdate{
		URL: "http://qb:8080", Username: "u", Password: ptr("secret")}); err != nil {
		t.Fatal(err)
	}
	_, _, id := queueFixture(t, db, a, "abc123")
	if r := a.do("POST", "/api/v1/queue/999/retry-import", ""); r.status != 404 {
		t.Errorf("unknown: %d %s", r.status, r.raw)
	}
	// Still downloading: only a failed import can be retried.
	r := a.do("POST", fmt.Sprintf("/api/v1/queue/%d/retry-import", id), "")
	if r.status != 422 || !strings.Contains(r.raw, "only a failed import can be retried") && !strings.Contains(r.raw, "Only a failed import") {
		t.Errorf("retry while downloading: %d %s", r.status, r.raw)
	}
}

// Pause, resume and move to top: the routes, their errors, and that each
// success reaches the client and History.
func TestQueueControlsAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		f := &fakeQB{}
		useFake(a, f)
		if _, err := a.settings.SaveDownloadClient(context.Background(), settings.DownloadClientUpdate{
			URL: "http://qb:8080", Username: "u", Password: ptr("secret")}); err != nil {
			t.Fatal(err)
		}
		_, _, id := queueFixture(t, db, a, "abc123")
		f.torrents = []download.Torrent{{Hash: "abc123", State: torrent.Downloading, Progress: 0.5}}
		route := func(action string) string { return fmt.Sprintf("/api/v1/queue/%d/%s", id, action) }

		for _, action := range []string{"pause", "resume", "top"} {
			if r := a.do("POST", "/api/v1/queue/999/"+action, ""); r.status != 404 || !strings.Contains(r.raw, "That download isn't in the queue.") {
				t.Errorf("%s unknown: %d %s", action, r.status, r.raw)
			}
		}

		if r := a.do("POST", route("top"), ""); r.status != 204 {
			t.Fatalf("top: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", route("resume"), ""); r.status != 422 || !strings.Contains(r.raw, "resume_failed") || !strings.Contains(r.raw, "This download isn't paused.") {
			t.Errorf("resume a running download: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", route("pause"), ""); r.status != 204 {
			t.Fatalf("pause: %d %s", r.status, r.raw)
		}
		f.torrents[0].State = torrent.Paused
		if r := a.do("POST", route("pause"), ""); r.status != 422 || !strings.Contains(r.raw, "pause_failed") || !strings.Contains(r.raw, "This download is already paused.") {
			t.Errorf("pause twice: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", route("top"), ""); r.status != 422 || !strings.Contains(r.raw, "move_failed") || !strings.Contains(r.raw, "Resume this download first.") {
			t.Errorf("top while paused: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", route("resume"), ""); r.status != 204 {
			t.Fatalf("resume: %d %s", r.status, r.raw)
		}
		if want := []string{"top abc123", "stop abc123", "start abc123"}; !slices.Equal(f.calls, want) {
			t.Errorf("client calls = %v, want %v", f.calls, want)
		}

		// A failing client is the user's problem to read, not a crash.
		f.fail = errors.New("can't reach qBittorrent: connection refused")
		if r := a.do("POST", route("pause"), ""); r.status != 422 || !strings.Contains(r.raw, "Can't reach qBittorrent") {
			t.Errorf("client down: %d %s", r.status, r.raw)
		}
		f.fail = nil

		// Finished torrents refuse all three.
		f.torrents[0] = download.Torrent{Hash: "abc123", State: torrent.Seeding, Progress: 1}
		for _, action := range []string{"pause", "resume", "top"} {
			if r := a.do("POST", route(action), ""); r.status != 422 || !strings.Contains(r.raw, "This download has already finished.") {
				t.Errorf("%s a finished download: %d %s", action, r.status, r.raw)
			}
		}

		// A torrent the client has lost is reported by the client's name.
		f.name, f.torrents = "Transmission", nil
		if r := a.do("POST", route("pause"), ""); r.status != 422 || !strings.Contains(r.raw, "Transmission no longer has this download.") {
			t.Errorf("lost torrent: %d %s", r.status, r.raw)
		}
		f.name, f.torrents = "", []download.Torrent{{Hash: "abc123", State: torrent.Seeding, Progress: 1}}

		// History shows the three actions, filterable by kind.
		r := a.do("GET", "/api/v1/history?event=paused&event=resumed&event=moved-to-top", "")
		evs := r.body["events"].([]any)
		if r.status != 200 || len(evs) != 3 {
			t.Fatalf("history: %d %s", r.status, r.raw)
		}
		first := evs[2].(map[string]any) // newest first, so the oldest is last
		if first["event"] != "moved-to-top" || first["message"] != "Moved to the top of qBittorrent's queue by you." {
			t.Errorf("oldest event = %v", first)
		}
	})
}

func ptr[T any](v T) *T { return &v }

func TestHistoryFilterAcceptsDownloadEvents(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	if r := a.do("GET", "/api/v1/history?event=imported&event=import-failed&event=removed", ""); r.status != 200 {
		t.Errorf("history filter: %d %s", r.status, r.raw)
	}
}

// queuedPasses lists the queued scheduled-search jobs (search passes, not
// single-movie searches).
func queuedPasses(t *testing.T, a *app) []int64 {
	t.Helper()
	js, _, err := a.jobs.Recent(context.Background(), 50, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, j := range js {
		if j.Kind == grab.SearchKind && j.Status == jobs.Queued && !strings.Contains(j.Args, "versionId") {
			out = append(out, j.ID)
		}
	}
	return out
}

// The blocklist: what's listed (dormant entries only when asked), the
// counts, one title's entries, and unblocking the temporary ones.
func TestBlocklistAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()
		root := addMovieFixture(t, a)
		now := time.Now().UTC()

		// A wanted movie (monitored, released, no file), an idle one
		// (not monitored) and a series with nothing wanted.
		addTitle := func(kind, name string, tmdb int, monitored bool) int64 {
			title := &library.Title{MediaType: kind, TMDBID: tmdb, Title: name, Year: 2020, DigitalRelease: "2020-06-01",
				CreatedAt: now, Genres: "[]", AltTitles: "[]"}
			if _, err := db.DB.NewInsert().Model(title).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			v := &library.Version{TitleID: title.ID, ProfileID: 1, RootFolderID: root, Monitored: monitored, Upgrades: true, CreatedAt: now}
			if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			return title.ID
		}
		wanted := addTitle(library.Movie, "Wanted Movie", 9001, true)
		idle := addTitle(library.Movie, "Idle Movie", 9002, false)
		show := addTitle(library.Series, "Some Show", 9003, false)

		const hash = "cccccccccccccccccccccccccccccccccccccccc"
		d := &download.Download{CreatedAt: now, TitleID: &show, Hash: hash, Category: settings.TVCategory, ReleaseTitle: "Some.Show.S02.WEB",
			Status: download.StatusRemoved, EpisodesLabel: "season 2 (8 wanted episodes)"}
		if _, err := db.DB.NewInsert().Model(d).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		block := func(title int64, name, hash, rule string, temporary bool) {
			t.Helper()
			err := a.downloads.AddToBlocklist(ctx, download.BlockedRelease{TitleID: title, ReleaseTitle: name, Hash: hash, Indexer: "ix",
				Quality: "WEBDL-1080p", Reason: "Because " + rule, Rule: rule, Temporary: temporary})
			if err != nil {
				t.Fatal(err)
			}
		}
		block(wanted, "Wanted.Stuck", "", "stalled", true)
		block(wanted, "Wanted.Mine", "", download.BlockRemoved, false)
		block(idle, "Idle.Stuck", "", "slow", true)
		block(show, "Some.Show.S02.WEB", hash, "metadata", true)

		names := func(r response) []string {
			t.Helper()
			if r.status != 200 {
				t.Fatalf("status %d: %s", r.status, r.raw)
			}
			var out []string
			for _, it := range r.body["items"].([]any) {
				out = append(out, it.(map[string]any)["releaseTitle"].(string))
			}
			return out
		}
		counts := func(r response) [3]float64 {
			return [3]float64{r.body["total"].(float64), r.body["dormant"].(float64), r.body["temporary"].(float64)}
		}

		// By default only the wanted movie's entries, newest first; the
		// counts cover everything that still blocks.
		r := a.do("GET", "/api/v1/blocklist", "")
		if got := names(r); fmt.Sprint(got) != "[Wanted.Mine Wanted.Stuck]" || counts(r) != [3]float64{4, 2, 3} {
			t.Errorf("default: %v %v %s", got, counts(r), r.raw)
		}
		mine := r.body["items"].([]any)[0].(map[string]any)
		stuck := r.body["items"].([]any)[1].(map[string]any)
		if mine["permanent"] != true || mine["expiresAt"] != nil || mine["rule"] != "removed" || mine["dormant"] != false ||
			stuck["permanent"] != false || stuck["expiresAt"] == nil || stuck["rule"] != "stalled" || stuck["mediaType"] != "movie" {
			t.Errorf("entries: %s", r.raw)
		}

		// With dormant ones, including the series' episodes.
		r = a.do("GET", "/api/v1/blocklist?include=dormant", "")
		if got := names(r); fmt.Sprint(got) != "[Some.Show.S02.WEB Idle.Stuck Wanted.Mine Wanted.Stuck]" {
			t.Errorf("with dormant: %v", got)
		}
		ep := r.body["items"].([]any)[0].(map[string]any)
		if ep["mediaType"] != "series" || ep["episodes"] != "season 2 (8 wanted episodes)" || ep["dormant"] != true ||
			ep["movie"].(map[string]any)["title"] != "Some Show" {
			t.Errorf("series entry: %s", r.raw)
		}

		// One title's entries; its counts are its own.
		path := fmt.Sprintf("/api/v1/blocklist?titleId=%d", idle)
		r = a.do("GET", path, "")
		if got := names(r); len(got) != 0 || counts(r) != [3]float64{1, 1, 1} {
			t.Errorf("idle title: %v %v", got, counts(r))
		}
		r = a.do("GET", path+"&include=dormant", "")
		if got := names(r); fmt.Sprint(got) != "[Idle.Stuck]" {
			t.Errorf("idle title with dormant: %v", got)
		}
		r = a.do("GET", fmt.Sprintf("/api/v1/blocklist?titleId=%d", wanted), "")
		if got := names(r); len(got) != 2 || counts(r) != [3]float64{2, 0, 1} {
			t.Errorf("wanted title: %v %v", got, counts(r))
		}

		// Unblock the temporary ones of one title, then of all.
		r = a.do("POST", fmt.Sprintf("/api/v1/blocklist/unblock-temporary?titleId=%d", idle), "")
		if r.status != 200 || r.body["removed"].(float64) != 1 {
			t.Errorf("unblock one title: %d %s", r.status, r.raw)
		}
		r = a.do("POST", "/api/v1/blocklist/unblock-temporary", "")
		if r.status != 200 || r.body["removed"].(float64) != 2 {
			t.Errorf("unblock all: %d %s", r.status, r.raw)
		}
		r = a.do("POST", "/api/v1/blocklist/unblock-temporary", "")
		if r.status != 200 || r.body["removed"].(float64) != 0 {
			t.Errorf("unblock again: %d %s", r.status, r.raw)
		}
		r = a.do("GET", "/api/v1/blocklist?include=dormant", "")
		if got := names(r); fmt.Sprint(got) != "[Wanted.Mine]" || counts(r) != [3]float64{1, 0, 0} {
			t.Errorf("what's left: %v %v", got, counts(r))
		}
	})
}

// addDownloadRow inserts a download in the given status, without a movie.
func addDownloadRow(t *testing.T, db *store.Store, hash, status string) {
	t.Helper()
	d := &download.Download{
		CreatedAt: time.Now().UTC(), Hash: hash, Category: settings.MovieCategory, ReleaseTitle: "Release." + hash[:4],
		Indexer: "ix", Quality: "WEBDL-1080p", Size: 1 << 30, Status: status, CleanupTrack: "{}",
	}
	if _, err := db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func downloadStatus(t *testing.T, db *store.Store, hash string) (status, message string) {
	t.Helper()
	d := new(download.Download)
	if err := db.DB.NewSelect().Model(d).Where("hash = ?", hash).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d.Status, d.Message
}

// The type of download client can be chosen. Downloads in the Queue no longer
// stop a switch (they move to the new client, see
// TestDownloadClientSwitchStartsAMove); with nothing to move, or with the
// seeding ones left behind, the type simply changes.
func TestDownloadClientTypeAndSwitching(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		f := &fakeQB{}
		useFake(a, f)
		hash := func(c string) string { return strings.Repeat(c, 40) }

		// A client saved without a type (an older install, or a script) is qBittorrent.
		r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"admin","password":"secret"}`)
		if r.status != 200 || r.body["type"] != "qbittorrent" {
			t.Fatalf("save without a type: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/settings/download-client", ""); r.body["type"] != "qbittorrent" {
			t.Errorf("get: %s", r.raw)
		}
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"rtorrent","url":"http://qb:8080","username":"admin"}`); r.status != 422 || !strings.Contains(r.raw, `"type"`) {
			t.Errorf("bad type: %d %s", r.status, r.raw)
		}

		// Two downloads in the Queue (a failed import counts too), one seeding.
		addDownloadRow(t, db, hash("a"), download.StatusDownloading)
		addDownloadRow(t, db, hash("b"), download.StatusImportFailed)
		addDownloadRow(t, db, hash("c"), download.StatusImported)

		// Changing only the address, login or folder of the same client works as before,
		// with the type given or left out.
		for _, body := range []string{
			`{"type":"qbittorrent","url":"http://qb2:8080","username":"admin"}`,
			`{"url":"http://qb3:8080","username":"dan","folder":"/mnt/downloads/reelkeeper"}`,
		} {
			if r := a.do("PUT", "/api/v1/settings/download-client", body); r.status != 200 || r.body["type"] != "qbittorrent" {
				t.Errorf("same type %s: %d %s", body, r.status, r.raw)
			}
		}
		if st, _ := downloadStatus(t, db, hash("c")); st != download.StatusImported {
			t.Errorf("saving the same type changed a download: %q", st)
		}
		// So does removing the client, and that doesn't change the type...
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"","username":""}`); r.status != 200 || r.body["url"] != "" {
			t.Errorf("remove: %d %s", r.status, r.raw)
		}
		// ...and with the old client gone Reelkeeper can't reach the torrents in
		// the Queue to move them, so removing it first is no way around the rule.
		r = a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":""}`)
		if r.status != 422 || r.body["code"] != "download_client" ||
			!strings.Contains(r.raw, "can't reach the 2 downloads in the Queue to move them") {
			t.Errorf("switch after removing: %d %s", r.status, r.raw)
		}
		if got := a.settings.DownloadClient(); got.ClientType() != settings.ClientQBittorrent || got.URL != "" {
			t.Errorf("a refused switch changed the settings: %+v", got)
		}
		a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"admin","password":"secret"}`)

		// The Queue is empty: the switch goes ahead. The imported download
		// still seeding in qBittorrent is left there, untracked, as asked.
		for _, h := range []string{"a", "b"} {
			if _, err := db.DB.NewUpdate().Model((*download.Download)(nil)).Set("status = ?", download.StatusRemoved).Where("hash = ?", hash(h)).Exec(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		r = a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091/","username":"","password":"secret","leaveSeeding":true}`)
		if r.status != 200 || r.body["type"] != "transmission" || r.body["url"] != "http://tm:9091" || r.body["passwordSet"] != true {
			t.Fatalf("switch: %d %s", r.status, r.raw)
		}
		if got := a.settings.DownloadClient(); got.ClientName() != "Transmission" {
			t.Errorf("settings = %+v", got)
		}
		st, msg := downloadStatus(t, db, hash("c"))
		if st != download.StatusSeeded || msg != "Left seeding in qBittorrent when the download client changed to Transmission; Reelkeeper no longer tracks it." {
			t.Errorf("the seeding download = %q %q", st, msg)
		}
		if st, _ := downloadStatus(t, db, hash("a")); st != download.StatusRemoved {
			t.Errorf("a removed download changed to %q", st)
		}
		// Nothing is moving, so nothing runs.
		if a.downloads.Moving() {
			t.Error("a move is running with nothing to move")
		}
		// History says so.
		hr := a.do("GET", "/api/v1/history", "")
		if hr.status != 200 || !strings.Contains(hr.raw, "Left seeding in qBittorrent when the download client changed to Transmission") {
			t.Errorf("history: %d %s", hr.status, hr.raw)
		}
		// Nothing was deleted from the old client (the API has no way to).
		if len(f.removed) != 0 {
			t.Errorf("removed from the client: %v", f.removed)
		}

		// Transmission needs no username. Switching back works too (nothing is
		// seeding now), and qBittorrent needs its username again.
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":"tm","password":"x"}`); r.status != 200 || r.body["passwordSet"] != true {
			t.Errorf("transmission with a login: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"qbittorrent","url":"http://qb:8080","username":""}`); r.status != 422 || !strings.Contains(r.raw, `"username"`) {
			t.Errorf("qBittorrent without a username: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"qbittorrent","url":"http://qb:8080","username":"admin","password":"secret"}`); r.status != 200 || r.body["type"] != "qbittorrent" {
			t.Errorf("back to qBittorrent: %d %s", r.status, r.raw)
		}
	})
}

// Changing the download client with torrents in Activity → Queue moves them:
// the preview counts them, saving marks each as moving to the new client
// (the Queue says so and its buttons are refused), the move task is queued,
// the old client's login is kept for it, and until it is done the client
// can't be changed again. The seeding one moves too unless asked to stay.
func TestDownloadClientSwitchStartsAMove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()
		f := &fakeQB{}
		useFake(a, f)
		hash := func(c string) string { return strings.Repeat(c, 40) }
		for _, h := range []string{"a", "b", "c"} {
			f.torrents = append(f.torrents, download.Torrent{Hash: hash(h), Name: h, Category: settings.MovieCategory, State: torrent.Downloading, Progress: 0.5, Size: 100})
		}
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"admin","password":"secret"}`); r.status != 200 {
			t.Fatalf("setup: %d %s", r.status, r.raw)
		}
		addDownloadRow(t, db, hash("a"), download.StatusDownloading)
		addDownloadRow(t, db, hash("b"), download.StatusImportFailed)
		addDownloadRow(t, db, hash("c"), download.StatusImported)

		r := a.do("GET", "/api/v1/settings/download-client/move-preview", "")
		if r.status != 200 || r.body["total"] != 3.0 || r.body["downloading"] != 2.0 || r.body["seeding"] != 1.0 {
			t.Fatalf("preview: %d %s", r.status, r.raw)
		}
		if got, ok := r.body["cannotMove"].([]any); !ok || len(got) != 0 {
			t.Errorf("cannotMove = %v", r.body["cannotMove"])
		}

		// The new client can't be reached with that login: refused, nothing changed.
		r = a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":"tm","password":"wrong"}`)
		if r.status != 422 || r.body["code"] != "download_client" || !strings.Contains(r.raw, "Transmission can't be used yet") {
			t.Errorf("unreachable: %d %s", r.status, r.raw)
		}
		if got := a.settings.DownloadClient(); got.ClientType() != settings.ClientQBittorrent || a.downloads.Moving() {
			t.Errorf("a refused switch changed things: %+v moving=%v", got, a.downloads.Moving())
		}

		// Now it can.
		r = a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":"tm","password":"secret"}`)
		if r.status != 200 || r.body["type"] != "transmission" {
			t.Fatalf("switch: %d %s", r.status, r.raw)
		}
		if !a.downloads.Moving() {
			t.Fatal("no move is running")
		}
		mv := a.settings.DownloadMove()
		if !mv.Active || mv.Old.URL != "http://qb:8080" || mv.Old.Username != "admin" || mv.Old.Password != "secret" || mv.Old.ClientType() != settings.ClientQBittorrent {
			t.Errorf("the old client's connection = %+v", mv)
		}
		for _, h := range []string{"a", "b", "c"} {
			d := new(download.Download)
			if err := db.DB.NewSelect().Model(d).Where("hash = ?", hash(h)).Scan(ctx); err != nil {
				t.Fatal(err)
			}
			if d.MoveState != "queued" || d.MoveTo != settings.ClientTransmission || d.Client != settings.ClientQBittorrent || d.Status == download.StatusSeeded {
				t.Errorf("%s: %+v", h, d)
			}
		}
		// The move task is queued, and the Queue says where the downloads are going.
		tasks := a.do("GET", "/api/v1/system/tasks", "")
		if !strings.Contains(tasks.raw, `"download.move"`) || !strings.Contains(tasks.raw, `"status":"queued"`) {
			t.Errorf("tasks: %s", tasks.raw)
		}
		q := a.do("GET", "/api/v1/queue", "")
		items, _ := q.body["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("queue: %s", q.raw)
		}
		for _, it := range items {
			if m := it.(map[string]any); m["movingTo"] != "Transmission" {
				t.Errorf("queue item: %v", m)
			}
		}
		// Its buttons are refused meanwhile.
		id := int(items[0].(map[string]any)["id"].(float64))
		for _, op := range []string{"pause", "resume", "top", "retry-import"} {
			if r := a.do("POST", fmt.Sprintf("/api/v1/queue/%d/%s", id, op), ""); r.status != 422 {
				t.Errorf("%s while moving: %d %s", op, r.status, r.raw)
			}
		}
		if r := a.do("POST", fmt.Sprintf("/api/v1/queue/%d/remove", id), `{"blocklist":false,"searchAgain":false}`); r.status != 422 || !strings.Contains(r.raw, "moving to Transmission") {
			t.Errorf("remove while moving: %d %s", r.status, r.raw)
		}
		// Until it is done the client can't be changed or removed.
		for _, body := range []string{
			`{"type":"qbittorrent","url":"http://qb:8080","username":"admin","password":"secret"}`,
			`{"url":"","username":""}`,
		} {
			if r := a.do("PUT", "/api/v1/settings/download-client", body); r.status != 422 || r.body["code"] != "move_running" {
				t.Errorf("while moving %s: %d %s", body, r.status, r.raw)
			}
		}
		// The new client's own login can still be corrected.
		if r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":"tm2","password":"secret"}`); r.status != 200 {
			t.Errorf("fixing the login: %d %s", r.status, r.raw)
		}
		if len(f.removed) != 0 {
			t.Errorf("removed from a client: %v", f.removed)
		}
	})
}

// The user can leave the seeding torrents in the old client: they stop being
// tracked (as before 1.3) while the Queue's downloads still move.
func TestDownloadClientSwitchLeavesSeedingOnRequest(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	f := &fakeQB{}
	useFake(a, f)
	hash := func(c string) string { return strings.Repeat(c, 40) }
	for _, h := range []string{"a", "c"} {
		f.torrents = append(f.torrents, download.Torrent{Hash: hash(h), Name: h, Category: settings.MovieCategory, State: torrent.Downloading, Progress: 0.5, Size: 100})
	}
	a.do("PUT", "/api/v1/settings/download-client", `{"url":"http://qb:8080","username":"admin","password":"secret"}`)
	addDownloadRow(t, db, hash("a"), download.StatusDownloading)
	addDownloadRow(t, db, hash("c"), download.StatusImported)
	r := a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":"","password":"secret","leaveSeeding":true}`)
	if r.status != 200 {
		t.Fatalf("switch: %d %s", r.status, r.raw)
	}
	if st, _ := downloadStatus(t, db, hash("c")); st != download.StatusSeeded {
		t.Errorf("the seeding download = %q", st)
	}
	if !a.downloads.Moving() {
		t.Error("the download in the Queue isn't moving")
	}
	d := new(download.Download)
	if err := db.DB.NewSelect().Model(d).Where("hash = ?", hash("a")).Scan(context.Background()); err != nil || d.MoveState != "queued" {
		t.Errorf("a = %+v, %v", d, err)
	}
}

// Trying a connection builds the client the form asks for, and the messages
// use that client's words.
func TestDownloadClientTestUsesTheChosenType(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	var built []string
	a.downloads.NewClient = func(c settings.DownloadClient) download.Client {
		built = append(built, c.ClientType())
		return &fakeQB{cfg: c, name: c.ClientName()}
	}
	a.do("POST", "/api/v1/settings/download-client/test", `{"type":"transmission","url":"http://tm:9091","username":"","password":"secret"}`)
	a.do("POST", "/api/v1/settings/download-client/test", `{"url":"http://qb:8080","username":"admin","password":"secret"}`)
	a.do("POST", "/api/v1/settings/download-client/test", `{"type":"deluge","url":"http://dl:8112","username":"left over","password":"secret"}`)
	if len(built) != 3 || built[0] != "transmission" || built[1] != "qbittorrent" || built[2] != "deluge" {
		t.Errorf("clients built: %v", built)
	}
	if got := a.settings.DownloadClient(); got.Username == "left over" {
		t.Errorf("trying a connection saved something: %+v", got)
	}
	r := a.do("POST", "/api/v1/settings/download-client/test", `{"type":"deluge","url":"","username":""}`)
	if r.status != 422 || r.body["message"] != "Enter Deluge's web UI address first." || !strings.Contains(r.raw, "Enter Deluge's web UI address, like http://192.168.1.20:8112.") {
		t.Errorf("empty Deluge address: %d %s", r.status, r.raw)
	}
	r = a.do("POST", "/api/v1/settings/download-client/test", `{"type":"transmission","url":"","username":""}`)
	if r.status != 422 || r.body["message"] != "Enter Transmission's RPC address first." || !strings.Contains(r.raw, "Enter Transmission's RPC address, like http://192.168.1.20:9091.") {
		t.Errorf("empty Transmission address: %d %s", r.status, r.raw)
	}
	r = a.do("POST", "/api/v1/settings/download-client/test", `{"url":"","username":""}`)
	if r.status != 422 || r.body["message"] != "Enter qBittorrent's Web UI address first." {
		t.Errorf("empty qBittorrent address: %d %s", r.status, r.raw)
	}
	// Saving Deluge: it has no username, so one that came along is dropped.
	r = a.do("PUT", "/api/v1/settings/download-client", `{"type":"deluge","url":"http://dl:8112/","username":"left over","password":"secret"}`)
	if r.status != 200 || r.body["type"] != "deluge" || r.body["username"] != "" || r.body["url"] != "http://dl:8112" || r.body["passwordSet"] != true {
		t.Errorf("save Deluge: %d %s", r.status, r.raw)
	}
	if got := a.settings.DownloadClient(); got.ClientName() != "Deluge" || got.Username != "" {
		t.Errorf("saved: %+v", got)
	}
}
