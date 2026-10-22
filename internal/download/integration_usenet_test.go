package download

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
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
// real SABnzbd and its made-up Usenet, fakenews (dev/fakenews): a Newznab
// indexer and the only news server SABnzbd is given. They run when
// REELKEEPER_TEST_SABNZBD_URL is set (http://localhost:8085 in the dev
// stack); the API key is REELKEEPER_TEST_SABNZBD_KEY or, when that's empty,
// read from the container. Nothing here touches the internet: SABnzbd's news
// servers are checked to be fakenews alone before anything is added.
//
// Releases are made with `docker exec` in the fakenews container, with a
// name of their own each run. SABnzbd's files are in the dev stack's
// volume, which this Mac can't see, so imports go to a fake importer and the
// "files visible" check is skipped.

const sabContainer, newsContainer = "reelkeeper-dev-sabnzbd", "reelkeeper-dev-fakenews"

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
}

func newRealUsenetFix(t *testing.T, db *store.Store) *realUsenetFix {
	t.Helper()
	cfg := realSABnzbd(t)
	f := &realUsenetFix{ufix: newUFix(t, db)}
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
		_ = (&SABnzbd{URL: f.ucfg.URL, APIKey: f.ucfg.APIKey}).RemoveJobs(context.Background(), d.Category, []string{d.Hash}, true)
	})
	return &d
}

// waitFor runs Check downloads until the download reaches one of the
// states, or the time is up.
func (f *realUsenetFix) waitFor(t *testing.T, d *Download, within time.Duration, states ...torrent.State) Torrent {
	t.Helper()
	sab := &SABnzbd{URL: f.ucfg.URL, APIKey: f.ucfg.APIKey}
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, _, err := f.svc.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		jobs, err := sab.Jobs(context.Background(), d.Category)
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

func TestRealUsenetDownloadImportAndCleanup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newRealUsenetFix(t, db)
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
				if !d.Usenet() || d.Client != settings.UsenetSABnzbd {
					t.Fatalf("download = %+v", d)
				}
				job := f.waitFor(t, d, time.Minute, torrent.Completed, torrent.Failed)
				if job.State != torrent.Completed {
					t.Fatalf("SABnzbd: %+v", job)
				}
				// The next check imports it from where SABnzbd put it.
				if _, _, err := f.svc.Check(ctx); err != nil {
					t.Fatal(err)
				}
				got := f.download(t, db, d.Hash)
				last := f.imp.reqs[len(f.imp.reqs)-1]
				if got.Status != StatusImported || !strings.HasPrefix(last.Source, "/mnt/downloads/usenet/reelkeeper-movies/"+d.ReleaseTitle) {
					t.Fatalf("after import: %s %q, import from %q", got.Status, got.Message, last.Source)
				}
				// Cleanup removes the job from SABnzbd.
				if _, _, err := f.svc.Cleanup(ctx); err != nil {
					t.Fatal(err)
				}
				if got := f.download(t, db, d.Hash); got.Status != StatusSeeded {
					t.Errorf("after cleanup: %s %q", got.Status, got.Message)
				}
				jobs, _ := (&SABnzbd{URL: f.ucfg.URL, APIKey: f.ucfg.APIKey}).Jobs(ctx, d.Category)
				for _, j := range jobs {
					if j.Hash == d.Hash {
						t.Errorf("SABnzbd still lists it: %+v", j)
					}
				}
			})
		}
	})
}

func TestRealUsenetFailedDownload(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newRealUsenetFix(t, db)
		d := f.grab(t, f.find(t, makeRelease(t, "Broken.Film", "-missing", "40")))
		job := f.waitFor(t, d, time.Minute, torrent.Failed, torrent.Completed)
		if job.State != torrent.Failed || job.Message == "" {
			t.Fatalf("SABnzbd: %+v", job)
		}
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		got := f.download(t, db, d.Hash)
		if got.Status != StatusRemoved || !strings.Contains(got.Message, "SABnzbd couldn't download it") {
			t.Errorf("after cleanup: %s %q", got.Status, got.Message)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked(d.ReleaseTitle, "") || len(f.again) != 1 {
			t.Errorf("blocklisted %v, searched again %v", blocked(d.ReleaseTitle, ""), f.again)
		}
	})
}

// The news server going away mid-download: Reelkeeper keeps the download
// (it isn't failed or removed), and SABnzbd finishes it once the server is
// back.
func TestRealUsenetServerGoesAway(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newRealUsenetFix(t, db)
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
		// SABnzbd rests a server that stopped answering for a few minutes
		// before trying it again: its timing, not Reelkeeper's.
		newsServerDown(t, false)
		if job := f.waitFor(t, d, 6*time.Minute, torrent.Completed, torrent.Failed); job.State != torrent.Completed {
			t.Fatalf("after the server came back: %+v", job)
		}
	})
}
