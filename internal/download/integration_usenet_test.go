package download

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// The tests below run Usenet downloads end to end against the dev stack's
// real SABnzbd and NZBGet and its made-up Usenet, fakenews (dev/fakenews): a
// Newznab indexer and the only news server either is given. Each client's
// tests run when its address is set: REELKEEPER_TEST_SABNZBD_URL
// (http://localhost:8085 in the dev stack; the API key is
// REELKEEPER_TEST_SABNZBD_KEY or, when that's empty, read from the
// container) and REELKEEPER_TEST_NZBGET_URL (http://localhost:6790; the login
// is REELKEEPER_TEST_NZBGET_USER and _PASS, reelkeeper/reelkeeper in the dev
// stack). Nothing here touches the internet: each client's news servers are
// checked to be fakenews alone before anything is added.
//
// Releases are made with `docker exec` in the fakenews container, with a
// name of their own each run. SABnzbd's files are in the dev stack's
// volume, which this Mac can't see, so imports go to a fake importer and the
// "files visible" check is skipped.

const sabContainer, newsContainer = "reelkeeper-dev-sabnzbd", "reelkeeper-dev-fakenews"

// realUsenetClients are the Usenet clients the end-to-end tests run against, each
// by the settings it is reached with. A client whose address isn't set is
// skipped.
var realUsenetClients = []struct {
	name string
	cfg  func(t *testing.T) settings.UsenetClient
	// folder is where it puts finished jobs (its category folders go in it).
	folder string
}{
	{settings.UsenetSABnzbd, realSABnzbd, "/mnt/downloads/usenet"},
	{settings.UsenetNZBGet, realNZBGet, "/mnt/downloads/nzbget"},
}

func realSABnzbd(t *testing.T) settings.UsenetClient {
	t.Helper()
	u := os.Getenv("REELKEEPER_TEST_SABNZBD_URL")
	if u == "" {
		t.Skip("set REELKEEPER_TEST_SABNZBD_URL to test against a real SABnzbd")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed; these tests make releases in the dev stack's fakenews")
	}
	key := os.Getenv("REELKEEPER_TEST_SABNZBD_KEY")
	if key == "" {
		out, err := exec.Command("docker", "exec", sabContainer, "sh", "-c", "grep -m1 '^api_key' /config/sabnzbd.ini | cut -d= -f2").Output()
		if err != nil {
			t.Fatalf("reading SABnzbd's API key: %v", err)
		}
		key = strings.TrimSpace(string(out))
	}
	cfg := settings.UsenetClient{Type: settings.UsenetSABnzbd, URL: u, APIKey: key}
	isolateSABnzbd(t, &SABnzbd{URL: u, APIKey: key})
	return cfg
}

// isolateSABnzbd makes fakenews SABnzbd's only news server, puts finished
// jobs on the volume the torrent clients share, and turns the speed limit
// off, then checks.
func isolateSABnzbd(t *testing.T, s *SABnzbd) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []url.Values{
		{"mode": {"set_config"}, "section": {"servers"}, "name": {"fakenews"}, "host": {"fakenews"}, "port": {"119"},
			"connections": {"4"}, "ssl": {"0"}, "enable": {"1"}, "username": {"dev"}, "password": {"dev"}},
		{"mode": {"set_config"}, "section": {"misc"}, "keyword": {"complete_dir"}, "value": {"/mnt/downloads/usenet"}},
		{"mode": {"config"}, "name": {"speedlimit"}, "value": {"0"}},
	} {
		if err := s.call(ctx, "set up the dev SABnzbd", q, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	var c struct {
		Config struct {
			Servers []struct {
				Host   string `json:"host"`
				Enable int    `json:"enable"`
			} `json:"servers"`
		} `json:"config"`
	}
	if err := s.call(ctx, "list its servers", url.Values{"mode": {"get_config"}, "section": {"servers"}}, nil, &c); err != nil {
		t.Fatal(err)
	}
	for _, srv := range c.Config.Servers {
		if srv.Enable == 1 && srv.Host != "fakenews" {
			t.Fatalf("SABnzbd has a news server other than fakenews (%s): refusing to download anything", srv.Host)
		}
	}
}

func realNZBGet(t *testing.T) settings.UsenetClient {
	t.Helper()
	u := os.Getenv("REELKEEPER_TEST_NZBGET_URL")
	if u == "" {
		t.Skip("set REELKEEPER_TEST_NZBGET_URL to test against a real NZBGet")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed; these tests make releases in the dev stack's fakenews")
	}
	user, pass := os.Getenv("REELKEEPER_TEST_NZBGET_USER"), os.Getenv("REELKEEPER_TEST_NZBGET_PASS")
	if user == "" {
		user, pass = "reelkeeper", "reelkeeper"
	}
	cfg := settings.UsenetClient{Type: settings.UsenetNZBGet, URL: u, Username: user, Password: pass}
	isolateNZBGet(t, &NZBGet{URL: u, Username: user, Password: pass})
	return cfg
}

// isolateNZBGet makes fakenews NZBGet's only news server, puts finished jobs
// on the volume the torrent clients share, takes off the speed limit and makes
// sure it isn't paused, then checks. NZBGet only saves its whole config file
// (and a new one takes a "reload" to apply), so this reads it, changes what
// differs and saves it only when something does. The first config a new
// NZBGet has points at a news server on the internet.
func isolateNZBGet(t *testing.T, n *NZBGet) {
	t.Helper()
	ctx := context.Background()
	var opts []struct{ Name, Value string }
	if err := n.call(ctx, "read its config", "loadconfig", nil, &opts); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Server1.Name": "fakenews", "Server1.Host": "fakenews", "Server1.Port": "119", "Server1.Encryption": "no",
		"Server1.Username": "dev", "Server1.Password": "dev", "Server1.Connections": "4", "Server1.Active": "yes",
		"DestDir": "/mnt/downloads/nzbget", "DownloadRate": "0", "KeepHistory": "30", "AppendCategoryDir": "yes",
		"HealthCheck": "park",
	}
	var save []map[string]string
	changed := false
	for _, o := range opts {
		// The read-only options come back too, but can't be saved.
		switch o.Name {
		case "ConfigFile", "AppBin", "AppDir", "Version":
			continue
		}
		v := o.Value
		if w, ok := want[o.Name]; ok {
			delete(want, o.Name)
			if v != w {
				v, changed = w, true
			}
		} else if strings.HasPrefix(o.Name, "Server") && strings.HasSuffix(o.Name, ".Active") && v != "no" {
			v, changed = "no", true // any other news server
		}
		save = append(save, map[string]string{"Name": o.Name, "Value": v})
	}
	for name, v := range want {
		save, changed = append(save, map[string]string{"Name": name, "Value": v}), true
	}
	if changed {
		var ok bool
		if err := n.call(ctx, "save its config", "saveconfig", []any{save}, &ok); err != nil || !ok {
			t.Fatalf("saving NZBGet's config: %v %v", ok, err)
		}
		if err := n.call(ctx, "reload", "reload", nil, &ok); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Sleep(time.Second); ; time.Sleep(time.Second) {
			if _, err := n.Version(ctx); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatalf("NZBGet didn't come back after reloading: %v", err)
			}
		}
	}
	// What it has now: fakenews alone.
	servers := map[string]bool{}
	var now []struct{ Name, Value string }
	if err := n.call(ctx, "read its config", "config", nil, &now); err != nil {
		t.Fatal(err)
	}
	for _, o := range now {
		if strings.HasPrefix(o.Name, "Server") && strings.HasSuffix(o.Name, ".Active") && o.Value == "yes" {
			servers[strings.TrimSuffix(o.Name, ".Active")] = true
		}
	}
	for _, o := range now {
		if host, ok := strings.CutSuffix(o.Name, ".Host"); ok && servers[host] && o.Value != "fakenews" {
			t.Fatalf("NZBGet has a news server other than fakenews (%s): refusing to download anything", o.Value)
		}
	}
	if len(servers) != 1 {
		t.Fatalf("NZBGet should have fakenews as its only active news server: %v", servers)
	}
	// A reload (or a bad config) can leave it paused.
	for _, m := range []string{"resumedownload", "resumepost", "resumescan"} {
		if err := n.call(ctx, m, m, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// makeRelease makes a made-up release in fakenews, with a name of its own.
func makeRelease(t *testing.T, base string, args ...string) string {
	t.Helper()
	name := fmt.Sprintf("%s.%d.1080p.WEB-DL.x264-FAKE", base, time.Now().UnixNano()%1_000_000_000)
	cmd := append([]string{"exec", newsContainer, "fakenews", "make", "-size", "6"}, append(args, name)...)
	if out, err := exec.Command("docker", cmd...).CombinedOutput(); err != nil {
		t.Fatalf("fakenews make: %v: %s", err, out)
	}
	return name
}

func newsServerDown(t *testing.T, down bool) {
	t.Helper()
	v := "0"
	if down {
		v = "1"
	}
	base := os.Getenv("REELKEEPER_TEST_FAKENEWS_URL")
	if base == "" {
		base = "http://localhost:8086"
	}
	resp, err := http.Post(base+"/control?down="+v, "text/plain", nil) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

// realUsenetFix is the download service with the real SABnzbd and a real
// Newznab indexer pointed at fakenews.
type realUsenetFix struct {
	*ufix
	idx *indexer.Service
	ix  *indexer.Indexer
	// folder is where the client puts finished jobs.
	folder string
}

// client is the real Usenet client, reached as Reelkeeper reaches it.
func (f *realUsenetFix) client() UsenetClient { return newUsenet(f.ucfg) }

func newRealUsenetFix(t *testing.T, db *store.Store, client int) *realUsenetFix {
	t.Helper()
	cfg := realUsenetClients[client].cfg(t)
	f := &realUsenetFix{ufix: newUFix(t, db), folder: realUsenetClients[client].folder}
	f.ucfg = cfg
	f.svc.NewUsenet = newUsenet // the real SABnzbd
	// The dev VM's disk is small; space checks have their own test.
	f.clean.KeepFreeGB = 0
	box, err := secrets.New("test-secret-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.New(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f.idx = indexer.New(db, box, cat, func() settings.Solver { return settings.Solver{} }, slog.New(slog.DiscardHandler))
	news := os.Getenv("REELKEEPER_TEST_FAKENEWS_URL")
	if news == "" {
		news = "http://localhost:8086"
	}
	if f.ix, err = f.idx.Create(context.Background(), indexer.Input{Name: "fakenews", Kind: indexer.KindNewznab, BaseURL: news,
		Enabled: true, Settings: map[string]string{"apiKey": "dev"}}); err != nil {
		t.Fatal(err)
	}
	f.svc.idx = f.idx
	return f
}

// find searches fakenews for a release by name.
func (f *realUsenetFix) find(t *testing.T, name string) indexer.Release {
	t.Helper()
	rels, failed, err := f.idx.Search(context.Background(), core.Query{Type: "search", Keywords: strings.ReplaceAll(name, ".", " ")}, indexer.SearchOptions{})
	if err != nil || len(failed) > 0 {
		t.Fatalf("search: %v %+v", err, failed)
	}
	for _, r := range rels {
		if r.Title == name {
			if !r.Usenet() {
				t.Fatalf("a Newznab release isn't Usenet: %+v", r)
			}
			return r
		}
	}
	t.Fatalf("fakenews didn't list %s: %+v", name, rels)
	return indexer.Release{}
}

func (f *realUsenetFix) grab(t *testing.T, rel indexer.Release) *Download {
	t.Helper()
	if err := f.svc.Grab(context.Background(), grab.Grabbed{Release: rel, Version: f.mv, Quality: "WEBDL-1080p"}); err != nil {
		t.Fatal(err)
	}
	var d Download
	if err := f.svc.db.DB.NewSelect().Model(&d).Where("release_title = ?", rel.Title).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.client().RemoveJobs(context.Background(), d.Category, []string{d.Hash}, true)
	})
	return &d
}

// waitFor runs Check downloads until the download reaches one of the
// states, or the time is up.
func (f *realUsenetFix) waitFor(t *testing.T, d *Download, within time.Duration, states ...torrent.State) Torrent {
	t.Helper()
	c := f.client()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, _, err := f.svc.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		jobs, err := c.Jobs(context.Background(), d.Category)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			if j.Hash == d.Hash {
				for _, s := range states {
					if j.State == s {
						return j
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s never reached %v", d.ReleaseTitle, states)
	return Torrent{}
}

// eachRealClient runs a test against each real Usenet client that is set up.
func eachRealClient(t *testing.T, run func(t *testing.T, db *store.Store, f *realUsenetFix)) {
	for i, c := range realUsenetClients {
		t.Run(c.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				run(t, db, newRealUsenetFix(t, db, i))
			})
		})
	}
}

func TestRealUsenetDownloadImportAndCleanup(t *testing.T) {
	eachRealClient(t, func(t *testing.T, db *store.Store, f *realUsenetFix) {
		ctx := context.Background()
		name := f.client().Name()
		for _, c := range []struct {
			name string
			args []string
		}{
			{"Clean.Film", nil},
			// A few articles missing: par2 repairs them.
			{"Repairable.Film", []string{"-missing", "2"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				d := f.grab(t, f.find(t, makeRelease(t, c.name, c.args...)))
				if !d.Usenet() || d.Client != f.ucfg.ClientType() {
					t.Fatalf("download = %+v", d)
				}
				job := f.waitFor(t, d, time.Minute, torrent.Completed, torrent.Failed)
				if job.State != torrent.Completed {
					t.Fatalf("%s: %+v", name, job)
				}
				// The next check imports it from where the client put it.
				if _, _, err := f.svc.Check(ctx); err != nil {
					t.Fatal(err)
				}
				got := f.download(t, db, d.Hash)
				last := f.imp.reqs[len(f.imp.reqs)-1]
				if got.Status != StatusImported || !strings.HasPrefix(last.Source, f.folder+"/reelkeeper-movies/"+d.ReleaseTitle) {
					t.Fatalf("after import: %s %q, import from %q", got.Status, got.Message, last.Source)
				}
				// Cleanup removes the job from the client.
				if _, _, err := f.svc.Cleanup(ctx); err != nil {
					t.Fatal(err)
				}
				if got := f.download(t, db, d.Hash); got.Status != StatusSeeded {
					t.Errorf("after cleanup: %s %q", got.Status, got.Message)
				}
				jobs, _ := f.client().Jobs(ctx, d.Category)
				for _, j := range jobs {
					if j.Hash == d.Hash {
						t.Errorf("%s still lists it: %+v", name, j)
					}
				}
			})
		}
	})
}

func TestRealUsenetFailedDownload(t *testing.T) {
	eachRealClient(t, func(t *testing.T, db *store.Store, f *realUsenetFix) {
		ctx := context.Background()
		name := f.client().Name()
		d := f.grab(t, f.find(t, makeRelease(t, "Broken.Film", "-missing", "40")))
		job := f.waitFor(t, d, time.Minute, torrent.Failed, torrent.Completed)
		if job.State != torrent.Failed || job.Message == "" {
			t.Fatalf("%s: %+v", name, job)
		}
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		got := f.download(t, db, d.Hash)
		if got.Status != StatusRemoved || !strings.Contains(got.Message, name+" couldn't download it") {
			t.Errorf("after cleanup: %s %q", got.Status, got.Message)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked(d.ReleaseTitle, "") || len(f.again) != 1 {
			t.Errorf("blocklisted %v, searched again %v", blocked(d.ReleaseTitle, ""), f.again)
		}
	})
}

// The news server going away mid-download: Reelkeeper keeps the download
// (it isn't failed or removed), and the client finishes it once the server is
// back.
func TestRealUsenetServerGoesAway(t *testing.T) {
	eachRealClient(t, func(t *testing.T, db *store.Store, f *realUsenetFix) {
		rel := f.find(t, makeRelease(t, "Patient.Film"))
		newsServerDown(t, true)
		t.Cleanup(func() { newsServerDown(t, false) })
		d := f.grab(t, rel)
		time.Sleep(3 * time.Second)
		if _, _, err := f.svc.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := f.download(t, db, d.Hash); got.Status != StatusDownloading {
			t.Fatalf("while the server is down: %s %q", got.Status, got.Message)
		}
		// The client rests a server that stopped answering for a while
		// before trying it again (SABnzbd for minutes): its timing, not
		// Reelkeeper's.
		newsServerDown(t, false)
		if job := f.waitFor(t, d, 6*time.Minute, torrent.Completed, torrent.Failed); job.State != torrent.Completed {
			t.Fatalf("after the server came back: %+v", job)
		}
	})
}

// NZBGet's own calls against the real thing, as the Queue's buttons make
// them, and what it does with a release it has seen before.
func TestRealNZBGetControls(t *testing.T) {
	cfg := realNZBGet(t)
	ctx := context.Background()
	n := newUsenet(cfg).(*NZBGet)
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newRealUsenetFix(t, db, 1)
		cat := settings.MovieCategory
		if v, err := n.Version(ctx); err != nil || v == "" {
			t.Fatalf("version %q: %v", v, err)
		}
		if w, r, err := n.Problems(ctx); err != nil || len(r) != 0 || len(w) != 0 {
			t.Errorf("the dev NZBGet should have no problems: %v %v %v", w, r, err)
		}
		if free, err := n.FreeSpace(ctx); err != nil || free <= 0 {
			t.Errorf("free space = %d, %v", free, err)
		}
		if err := n.EnsureCategory(ctx, cat); err != nil {
			t.Errorf("category: %v", err)
		}
		nzbOf := func(name string) []byte {
			fetched, err := f.idx.Fetch(ctx, f.find(t, name))
			if err != nil || len(fetched.NZB) == 0 {
				t.Fatalf("fetching %s: %v", name, err)
			}
			return fetched.NZB
		}
		names := []string{makeRelease(t, "Control.One"), makeRelease(t, "Control.Two")}
		var ids []string
		// Hold everything still while the queue is looked at.
		if err := n.call(ctx, "pause", "pausedownload", nil, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = n.call(ctx, "resume", "resumedownload", nil, nil) })
		for _, name := range names {
			id, err := n.AddNZB(ctx, nzbOf(name), name, cat, false)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		t.Cleanup(func() { _ = n.RemoveJobs(ctx, cat, ids, true) })
		state := func(id string) torrent.State {
			jobs, err := n.Jobs(ctx, cat)
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range jobs {
				if j.Hash == id {
					return j.State
				}
			}
			return ""
		}
		if state(ids[0]) != torrent.Queued || state(ids[1]) != torrent.Queued {
			t.Fatalf("added jobs: %q %q", state(ids[0]), state(ids[1]))
		}
		if err := n.PauseJobs(ctx, cat, ids[:1]); err != nil || state(ids[0]) != torrent.Paused {
			t.Errorf("pause: %v, now %q", err, state(ids[0]))
		}
		if err := n.ResumeJobs(ctx, cat, ids[:1]); err != nil || state(ids[0]) == torrent.Paused {
			t.Errorf("resume: %v, now %q", err, state(ids[0]))
		}
		if err := n.TopJobs(ctx, cat, ids[1:]); err != nil {
			t.Errorf("move to top: %v", err)
		}
		var first nzbgetGroup
		var groups []nzbgetGroup
		if err := n.call(ctx, "list", "listgroups", nil, &groups); err != nil || len(groups) == 0 {
			t.Fatalf("queue: %v", err)
		}
		first = groups[0]
		if strconv.Itoa(first.ID) != ids[1] {
			t.Errorf("top of the queue is %d, want %s", first.ID, ids[1])
		}
		// An ID NZBGet doesn't have is an error here, not a quiet yes.
		if err := n.PauseJobs(ctx, cat, []string{"99999999"}); err == nil {
			t.Error("pausing a job that doesn't exist worked")
		}
		// Removing a queued job leaves nothing behind, in the queue or the history.
		if err := n.RemoveJobs(ctx, cat, ids, true); err != nil {
			t.Fatal(err)
		}
		if jobs, _ := n.Jobs(ctx, cat); len(jobs) != 0 {
			for _, j := range jobs {
				if slices.Contains(ids, j.Hash) {
					t.Errorf("still listed: %+v", j)
				}
			}
		}
		// The same release added twice: NZBGet's duplicate check (on in the
		// dev NZBGet) would turn the second into a history entry that never
		// downloads; Reelkeeper adds with it off, so both download.
		if err := n.call(ctx, "resume", "resumedownload", nil, nil); err != nil {
			t.Fatal(err)
		}
		nzb := nzbOf(names[0])
		var twins []string
		for range 2 {
			id, err := n.AddNZB(ctx, nzb, names[0], cat, false)
			if err != nil {
				t.Fatal(err)
			}
			twins = append(twins, id)
		}
		t.Cleanup(func() { _ = n.RemoveJobs(ctx, cat, twins, true) })
		deadline := time.Now().Add(time.Minute)
		for done := 0; done < 2; {
			if time.Now().After(deadline) {
				t.Fatalf("the twins didn't both finish: %d", done)
			}
			time.Sleep(500 * time.Millisecond)
			jobs, err := n.Jobs(ctx, cat)
			if err != nil {
				t.Fatal(err)
			}
			done = 0
			for _, j := range jobs {
				if slices.Contains(twins, j.Hash) {
					if j.State == torrent.Failed {
						t.Fatalf("a twin failed: %+v", j)
					}
					if j.State == torrent.Completed {
						done++
					}
				}
			}
		}
		// Removing finished jobs takes them out of NZBGet's history. (Their
		// folders are in the dev stack's volume, which this Mac can't see, so
		// that part has its own test on a folder of its own.)
		if err := n.RemoveJobs(ctx, cat, twins, true); err != nil {
			t.Fatal(err)
		}
	})
}
