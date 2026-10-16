package download

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeQbit mimics the parts of qBittorrent's Web API that Reelkeeper uses,
// including its login rules: a session cookie, 403 without one, and a
// Referer check.
type fakeQbit struct {
	// modern answers logins as qBittorrent 5.2 does (204/401) rather
	// than as 4.x (200 "Ok."/"Fails.").
	modern     bool
	mu         sync.Mutex
	srv        *httptest.Server
	sessions   map[string]bool
	categories map[string]string
	torrents   []map[string]any
	logins     int
	added      []map[string]string
	deleted    []string
	calls      []string // setAutoManagement and setCategory calls, in order
	topped     []string // hashes sent to topPrio
	noQueue    bool     // answer topPrio as qBittorrent does with queueing off
	// old4 makes it qBittorrent 4.x: it has pause and resume, not stop and
	// start.
	old4  bool
	power []string // pause/resume calls ("stop <hashes>"), in order
	limit []string // hashes sent to setShareLimits
	// For moving a torrent to another client: what export answers by hash
	// (a missing hash answers 409, no details yet), the file lists, and the
	// hashes sent to recheck.
	exports   map[string][]byte
	noExport  bool // a qBittorrent older than 4.5
	fileLists map[string][]map[string]any
	rechecked []string
}

func newFakeQbit(t *testing.T) *fakeQbit {
	t.Helper()
	f := &fakeQbit{sessions: map[string]bool{}, categories: map[string]string{"radarr": ""}}
	f.torrents = []map[string]any{{"hash": "ffff", "name": "Radarr's", "category": "radarr", "progress": 1.0}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeQbit) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Referer"), f.srv.URL) {
		http.Error(w, "bad referer", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/api/v2/auth/login" {
		_ = r.ParseForm()
		f.logins++
		if r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
			if f.modern {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "Fails.")
			return
		}
		f.sessions["sid1"] = true
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid1", Path: "/"})
		if f.modern {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "Ok.")
		return
	}
	if c, err := r.Cookie("SID"); err != nil || !f.sessions[c.Value] {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/api/v2/app/version":
		_, _ = io.WriteString(w, "v5.2.2")
	case "/api/v2/torrents/categories":
		out := map[string]any{}
		for k, v := range f.categories {
			out[k] = map[string]string{"name": k, "savePath": v}
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/api/v2/torrents/createCategory":
		_ = r.ParseForm()
		f.categories[r.Form.Get("category")] = r.Form.Get("savePath")
	case "/api/v2/torrents/add":
		_ = r.ParseMultipartForm(1 << 20)
		got := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			got[k] = v[0]
		}
		hash := ""
		if fh := r.MultipartForm.File["torrents"]; len(fh) > 0 {
			got["file"] = fh[0].Filename
			hash = strings.TrimSuffix(fh[0].Filename, ".torrent")
		} else if h, err := MagnetHash(got["urls"]); err == nil {
			hash = h
		}
		f.added = append(f.added, got)
		f.torrents = append(f.torrents, map[string]any{"hash": strings.ToUpper(hash), "name": "new", "category": got["category"],
			"progress": 0.5, "eta": 8640000, "completion_on": -1, "added_on": 1700000000})
		_, _ = io.WriteString(w, "Ok.")
	case "/api/v2/torrents/info":
		var out []map[string]any
		for _, t := range f.torrents {
			if c := r.URL.Query().Get("category"); c != "" && t["category"] != c {
				continue
			}
			if h := r.URL.Query().Get("hashes"); h != "" && !strings.EqualFold(t["hash"].(string), h) {
				continue
			}
			out = append(out, t)
		}
		if out == nil {
			out = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(out)
	case "/api/v2/torrents/setAutoManagement":
		_ = r.ParseForm()
		f.calls = append(f.calls, "auto="+r.Form.Get("enable")+" "+r.Form.Get("hashes"))
	case "/api/v2/torrents/setCategory":
		_ = r.ParseForm()
		f.calls = append(f.calls, "category="+r.Form.Get("category")+" "+r.Form.Get("hashes"))
		for _, t := range f.torrents {
			if strings.EqualFold(t["hash"].(string), r.Form.Get("hashes")) {
				t["category"] = r.Form.Get("category")
			}
		}
	case "/api/v2/torrents/topPrio":
		_ = r.ParseForm()
		if f.noQueue {
			http.Error(w, "Torrent queueing must be enabled", http.StatusConflict)
			return
		}
		f.topped = append(f.topped, r.Form.Get("hashes"))
	case "/api/v2/torrents/stop", "/api/v2/torrents/start", "/api/v2/torrents/pause", "/api/v2/torrents/resume":
		_ = r.ParseForm()
		name := strings.TrimPrefix(r.URL.Path, "/api/v2/torrents/")
		if new5 := name == "stop" || name == "start"; new5 == f.old4 {
			http.NotFound(w, r) // this version doesn't have that name
			return
		}
		f.power = append(f.power, name+" "+r.Form.Get("hashes"))
	case "/api/v2/torrents/export":
		if f.noExport {
			http.NotFound(w, r)
			return
		}
		b, ok := f.exports[r.URL.Query().Get("hash")]
		if !ok {
			http.Error(w, "no metadata", http.StatusConflict)
			return
		}
		_, _ = w.Write(b)
	case "/api/v2/torrents/files":
		_ = json.NewEncoder(w).Encode(f.fileLists[r.URL.Query().Get("hash")])
	case "/api/v2/torrents/recheck":
		_ = r.ParseForm()
		f.rechecked = append(f.rechecked, r.Form.Get("hashes"))
	case "/api/v2/torrents/setShareLimits":
		_ = r.ParseForm()
		f.limit = append(f.limit, r.Form.Get("hashes"))
	case "/api/v2/torrents/delete":
		_ = r.ParseForm()
		f.deleted = append(f.deleted, r.Form.Get("hashes")+" files="+r.Form.Get("deleteFiles"))
	default:
		http.NotFound(w, r)
	}
}

func TestQbitLoginAndVersion(t *testing.T) {
	for _, modern := range []bool{false, true} {
		f := newFakeQbit(t)
		f.modern = modern
		ctx := context.Background()
		q := &QBittorrent{URL: f.srv.URL + "/", Username: "admin", Password: "secret"}
		v, err := q.Version(ctx)
		if err != nil || v != "v5.2.2" {
			t.Fatalf("modern=%v: version = %q, %v", modern, v, err)
		}
		bad := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "nope"}
		if _, err := bad.Version(ctx); !errors.Is(err, ErrLogin) {
			t.Errorf("modern=%v: wrong password: %v", modern, err)
		}
	}
	ctx := context.Background()
	down := &QBittorrent{URL: "http://127.0.0.1:1", Username: "a", Password: "b"}
	if _, err := down.Version(ctx); err == nil || !strings.Contains(err.Error(), "can't reach qBittorrent") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestQbitSessionExpiry(t *testing.T) {
	f := newFakeQbit(t)
	ctx := context.Background()
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	// No login yet: the first request gets 403, logs in, and retries.
	if _, err := q.Torrents(ctx, "reelkeeper-movies"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.sessions = map[string]bool{} // qBittorrent restarted
	f.mu.Unlock()
	if _, err := q.Torrents(ctx, "reelkeeper-movies"); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 {
		t.Errorf("logins = %d", f.logins)
	}
}

func TestQbitCategoriesAndAdd(t *testing.T) {
	f := newFakeQbit(t)
	ctx := context.Background()
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	if err := q.EnsureCategory(ctx, "reelkeeper-movies", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.categories["reelkeeper-movies"]; !ok {
		t.Fatal("category not created")
	}
	f.categories["reelkeeper-movies"] = "/custom"
	if err := q.EnsureCategory(ctx, "reelkeeper-movies", "/other"); err != nil || f.categories["reelkeeper-movies"] != "/custom" {
		t.Error("an existing category's folder must be left alone")
	}

	file := []byte("d4:infod4:name4:testee")
	want, _ := InfoHash(file)
	hash, err := q.Add(ctx, Add{Torrent: file, Category: "reelkeeper-movies", Tags: []string{"reelkeeper", "Knaben"}})
	if err != nil || hash != want {
		t.Fatalf("add file = %q, %v", hash, err)
	}
	a := f.added[0]
	if a["category"] != "reelkeeper-movies" || a["tags"] != "reelkeeper,Knaben" || a["file"] != want+".torrent" || a["stopped"] != "false" {
		t.Errorf("sent %v", a)
	}
	// Adding the same one again is fine (already ours); nothing new is sent.
	if _, err := q.Add(ctx, Add{Torrent: file, Category: "reelkeeper-movies"}); err != nil || len(f.added) != 1 {
		t.Errorf("re-add: %v, %d adds", err, len(f.added))
	}
	// A magnet.
	hash, err = q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "reelkeeper-movies"})
	if err != nil || hash != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("add magnet = %q, %v", hash, err)
	}
	// Radarr's torrent: refused, untouched.
	f.torrents[0]["hash"] = "1111111111111111111111111111111111111111"
	_, err = q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111", Category: "reelkeeper-movies"})
	if err == nil || !strings.Contains(err.Error(), `category "radarr"`) {
		t.Errorf("someone else's torrent: %v", err)
	}
}

func TestQbitTorrentsAndRemove(t *testing.T) {
	f := newFakeQbit(t)
	ctx := context.Background()
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	hash, err := q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "reelkeeper-movies"})
	if err != nil {
		t.Fatal(err)
	}
	// Connected to 3 of the swarm's 63 seeds.
	added := f.torrents[len(f.torrents)-1]
	added["num_seeds"], added["num_complete"] = 3, 63
	ts, err := q.Torrents(ctx, "reelkeeper-movies")
	if err != nil || len(ts) != 1 {
		t.Fatalf("torrents = %+v, %v", ts, err)
	}
	if got := ts[0]; got.Hash != hash || got.ETA != 0 || got.CompletedOn != nil || got.Done() ||
		got.Seeds != 3 || got.SwarmSeeds != 63 {
		t.Errorf("torrent = %+v", got)
	}
	// qBittorrent says -1 when the tracker hasn't told it yet.
	added["num_complete"] = -1
	if ts, _ := q.Torrents(ctx, "reelkeeper-movies"); ts[0].SwarmSeeds != 0 {
		t.Errorf("unknown swarm = %d, want 0", ts[0].SwarmSeeds)
	}
	if _, err := q.Torrents(ctx, ""); err == nil {
		t.Error("listing every category must be refused")
	}
	// Someone else's torrent is never removed.
	if err := q.Remove(ctx, "reelkeeper-movies", []string{"ffff"}, true); err == nil {
		t.Error("removed a torrent outside Reelkeeper's category")
	}
	if err := q.Remove(ctx, "reelkeeper-movies", []string{hash}, true); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != hash+" files=true" {
		t.Errorf("deleted %v", f.deleted)
	}
}

// Moving categories turns automatic management off first, so qBittorrent
// doesn't move the files, and never touches another app's torrent.
func TestQbitMoveCategory(t *testing.T) {
	f := newFakeQbit(t)
	ctx := context.Background()
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	hash, err := q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "old-movies"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.MoveCategory(ctx, "old-movies", "reelkeeper-movies", []string{"ffff"}); err == nil {
		t.Error("moved a torrent that isn't in the old category")
	}
	if err := q.MoveCategory(ctx, "old-movies", "reelkeeper-movies", []string{hash}); err != nil {
		t.Fatal(err)
	}
	want := []string{"auto=false " + hash, "category=reelkeeper-movies " + hash}
	if strings.Join(f.calls, "; ") != strings.Join(want, "; ") {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if ts, _ := q.Torrents(ctx, "reelkeeper-movies"); len(ts) != 1 {
		t.Errorf("not in the new category: %+v", ts)
	}
}

// TopPriority sends the hashes to topPrio, treats "queueing is off" (409) as
// done, and never touches another app's torrent.
func TestQbitTopPriority(t *testing.T) {
	f := newFakeQbit(t)
	ctx := context.Background()
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	h1, err := q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "reelkeeper-movies"})
	if err != nil {
		t.Fatal(err)
	}

	if err := q.TopPriority(ctx, "reelkeeper-movies", nil); err != nil || len(f.topped) != 0 {
		t.Errorf("no hashes: err %v, sent %v", err, f.topped)
	}
	if err := q.TopPriority(ctx, "reelkeeper-movies", []string{h1}); err != nil {
		t.Fatal(err)
	}
	if len(f.topped) != 1 || !strings.EqualFold(f.topped[0], h1) {
		t.Errorf("sent %v, want [%s]", f.topped, h1)
	}

	// Another app's torrent (category "radarr") is refused, nothing sent.
	if err := q.TopPriority(ctx, "reelkeeper-movies", []string{"ffff"}); err == nil {
		t.Error("moved a torrent that isn't in Reelkeeper's category")
	}
	if len(f.topped) != 1 {
		t.Errorf("sent %v after a refused hash", f.topped)
	}

	// Queueing off in qBittorrent: nothing to reorder, not an error.
	f.noQueue = true
	if err := q.TopPriority(ctx, "reelkeeper-movies", []string{h1}); err != nil {
		t.Errorf("queueing off should count as done: %v", err)
	}
}

// Stop and Start use qBittorrent 5's endpoints, fall back to 4.x's pause
// and resume when those answer 404, do nothing for an empty list, and never
// touch another app's torrent.
func TestQbitStopAndStart(t *testing.T) {
	for _, old4 := range []bool{false, true} {
		f := newFakeQbit(t)
		f.old4 = old4
		ctx := context.Background()
		q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
		h1, err := q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "reelkeeper-movies"})
		if err != nil {
			t.Fatal(err)
		}

		if err := q.Stop(ctx, "reelkeeper-movies", nil); err != nil || len(f.power) != 0 {
			t.Errorf("old4=%v: no hashes: err %v, sent %v", old4, err, f.power)
		}
		if err := q.Start(ctx, "reelkeeper-movies", nil); err != nil || len(f.power) != 0 {
			t.Errorf("old4=%v: no hashes: err %v, sent %v", old4, err, f.power)
		}
		if err := q.Stop(ctx, "reelkeeper-movies", []string{h1}); err != nil {
			t.Fatalf("old4=%v: stop: %v", old4, err)
		}
		if err := q.Start(ctx, "reelkeeper-movies", []string{h1}); err != nil {
			t.Fatalf("old4=%v: start: %v", old4, err)
		}
		want := "stop " + h1 + "; start " + h1
		if old4 {
			want = "pause " + h1 + "; resume " + h1
		}
		if got := strings.ToLower(strings.Join(f.power, "; ")); got != want {
			t.Errorf("old4=%v: sent %q, want %q", old4, got, want)
		}

		// Another app's torrent (category "radarr") is refused, nothing sent.
		sent := len(f.power)
		if err := q.Stop(ctx, "reelkeeper-movies", []string{"ffff"}); err == nil {
			t.Errorf("old4=%v: stopped a torrent outside Reelkeeper's category", old4)
		}
		if err := q.Start(ctx, "reelkeeper-movies", []string{"ffff"}); err == nil {
			t.Errorf("old4=%v: started a torrent outside Reelkeeper's category", old4)
		}
		if len(f.power) != sent {
			t.Errorf("old4=%v: sent %v after a refused hash", old4, f.power[sent:])
		}
	}
}

// NoShareLimits still lifts the limits and then starts the torrents (with
// 4.x's name for it where needed), sharing its start code with Start.
func TestQbitNoShareLimitsStillStarts(t *testing.T) {
	for _, old4 := range []bool{false, true} {
		f := newFakeQbit(t)
		f.old4 = old4
		ctx := context.Background()
		q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
		h1, err := q.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01", Category: "reelkeeper-movies"})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.NoShareLimits(ctx, "reelkeeper-movies", []string{h1}); err != nil {
			t.Fatalf("old4=%v: %v", old4, err)
		}
		want := "start " + h1
		if old4 {
			want = "resume " + h1
		}
		if len(f.limit) != 1 || len(f.power) != 1 || strings.ToLower(f.power[0]) != want {
			t.Errorf("old4=%v: limits %v, power %v, want %q", old4, f.limit, f.power, want)
		}
		if err := q.NoShareLimits(ctx, "reelkeeper-movies", []string{"ffff"}); err == nil {
			t.Errorf("old4=%v: changed a torrent outside Reelkeeper's category", old4)
		}
	}
}
