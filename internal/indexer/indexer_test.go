package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/solver"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// A made-up tracker definition with a password setting.
const testDef = `---
id: faketracker
name: Fake Tracker
type: private
links: [https://fake.example/]
caps:
  categorymappings:
    - {id: 1, cat: Movies/HD}
    - {id: 2, cat: TV/HD}
settings:
  - {name: username, type: text, label: Username}
  - {name: password, type: password, label: Password}
  - {name: sort, type: select, label: Sort, default: added, options: {added: Added, size: Size}}
  - {name: freeleech, type: checkbox, label: Freeleech only}
search:
  path: "browse/{{ .Keywords }}"
  rows: {selector: tr.t}
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
    size: {selector: td.size}
    seeders: {text: 5}
    leechers: {text: 1}
    category: {text: 1}
`

type fixture struct {
	svc     *Service
	tracker *httptest.Server
	hits    *atomic.Int32
	fail    *atomic.Bool
	solver  settings.Solver
}

func newFixture(t *testing.T, db *store.Store) *fixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "definitions", "custom"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "definitions", "custom", "fake.yml"), []byte(testDef), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.New(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New("test-secret-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{hits: &atomic.Int32{}, fail: &atomic.Bool{}}
	f.tracker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/dl/1": // a .torrent file
			_, _ = w.Write([]byte("d4:infod4:name4:testee"))
			return
		case "/dl/2": // a redirect to a magnet link
			http.Redirect(w, r, "magnet:?xt=urn:btih:abc", http.StatusFound)
			return
		case "/dl/3": // a login page instead of the file
			_, _ = w.Write([]byte("<html>Please log in</html>"))
			return
		}
		fmt.Fprintf(w, `<table><tr class="t"><td><a href="/dl/1">%s 1080p</a></td><td class="size">1 GB</td></tr></table>`, strings.TrimPrefix(r.URL.Path, "/browse/"))
	}))
	t.Cleanup(f.tracker.Close)
	f.svc = New(db, box, cat, func() settings.Solver { return f.solver }, slog.New(slog.DiscardHandler))
	// No pauses between requests in tests.
	f.svc.newSession = func(_ *Indexer, _ time.Duration, sol *solver.Client) *session { return newSession(0, "test", sol) }
	return f
}

func (f *fixture) input(name string) Input {
	return Input{
		Name: name, Kind: KindCardigann, Definition: "faketracker", BaseURL: f.tracker.URL + "/", Enabled: true,
		Settings: map[string]string{"username": "dan", "password": "hunter2", "sort": "size"},
	}
}

func TestCreateKeepsSecretsApart(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		ix, err := f.svc.Create(ctx, f.input("Fake"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ix.Settings, "hunter2") || strings.Contains(ix.Secrets, "hunter2") || ix.Secrets == "" {
			t.Errorf("the password must only be stored encrypted: settings %s", ix.Settings)
		}
		if got := ix.SettingsMap(); got["username"] != "dan" || got["sort"] != "size" || got["freeleech"] != "false" {
			t.Errorf("settings = %v", got)
		}
		if names := f.svc.SecretNames(ix); len(names) != 1 || names[0] != "password" {
			t.Errorf("secret names = %v", names)
		}
		// Saving without the password keeps it.
		in := f.input("Fake renamed")
		delete(in.Settings, "password")
		ix, err = f.svc.Update(ctx, ix.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if sec, _ := f.svc.secretsOf(ix); sec["password"] != "hunter2" || ix.Name != "Fake renamed" {
			t.Errorf("after update: %v %s", sec, ix.Name)
		}
		if ix.CategoryList()[0] != 2000 {
			t.Errorf("default categories = %v", ix.CategoryList())
		}
	})
}

func TestValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Create(ctx, f.input("Fake")); err != nil {
			t.Fatal(err)
		}
		bad := f.input("fake") // same name, any case
		bad.Priority = 99
		bad.Settings["sort"] = "nonsense"
		bad.BaseURL = "fake.example"
		one := -1
		bad.QueryLimit = &one
		_, err := f.svc.Create(ctx, bad)
		var errs validation.Errors
		if !errors.As(err, &errs) {
			t.Fatalf("err = %v", err)
		}
		for _, field := range []string{"name", "priority", "settings.sort", "baseUrl", "queryLimit"} {
			if errs[field] == "" {
				t.Errorf("no error for %s: %v", field, errs)
			}
		}
		if _, err := f.svc.Create(ctx, Input{Name: "X", Kind: KindCardigann, Definition: "nope", Enabled: true}); err == nil {
			t.Error("an unknown definition should be refused")
		}
		if _, err := f.svc.Create(ctx, Input{Name: "Feed", Kind: KindTorznab, Enabled: true}); err == nil {
			t.Error("a feed needs an address")
		}
	})
}

func TestSearchHealthAndLimits(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		a, err := f.svc.Create(ctx, f.input("A"))
		if err != nil {
			t.Fatal(err)
		}
		b := f.input("B")
		b.Categories = []int{5000} // TV only
		if _, err := f.svc.Create(ctx, b); err != nil {
			t.Fatal(err)
		}

		// A movie search goes to A only (B searches TV).
		res, failed, err := f.svc.Search(ctx, core.Query{Type: "movie-search", Keywords: "Heat", Categories: []int{2000}}, SearchOptions{})
		if err != nil || len(failed) != 0 || len(res) != 1 || res[0].Indexer != "A" || res[0].Title != "Heat 1080p" {
			t.Fatalf("search = %+v, %+v, %v", res, failed, err)
		}

		// A failure: reported, and A is left alone for a while.
		f.fail.Store(true)
		_, failed, _ = f.svc.Search(ctx, core.Query{Type: "movie-search", Keywords: "Heat", Categories: []int{2000}}, SearchOptions{})
		if len(failed) != 1 || !strings.Contains(failed[0].Message, "503") {
			t.Fatalf("failures = %+v", failed)
		}
		a, _ = f.svc.Get(ctx, a.ID)
		h := f.svc.HealthOf(a)
		if h.Status != "failing" || a.RetryAfter == nil || time.Until(*a.RetryAfter) < 4*time.Minute || !strings.Contains(h.Message, "try again") {
			t.Errorf("health = %+v, retry %v", h, a.RetryAfter)
		}
		before := f.hits.Load()
		res, failed, _ = f.svc.Search(ctx, core.Query{Type: "movie-search", Keywords: "Heat", Categories: []int{2000}}, SearchOptions{})
		if len(res) != 0 || f.hits.Load() != before {
			t.Error("a backing-off indexer must not be asked")
		}
		if len(failed) != 1 || !failed[0].Skipped || !strings.Contains(failed[0].Message, "resting after an error") {
			t.Errorf("the skipped indexer should be reported: %+v", failed)
		}

		// Asking for it by name tries it anyway; success clears the failure.
		f.fail.Store(false)
		res, _, _ = f.svc.Search(ctx, core.Query{Type: "search", Keywords: "Ran"}, SearchOptions{IndexerIDs: []int64{a.ID}})
		a, _ = f.svc.Get(ctx, a.ID)
		if len(res) != 1 || f.svc.HealthOf(a).Status != "ok" || a.Failures != 0 {
			t.Errorf("after recovery: %d results, %+v", len(res), f.svc.HealthOf(a))
		}

		stats, _ := f.svc.StatsOf(ctx)
		if st := stats[a.ID]; st.QueriesToday != 3 || st.Failures30d != 1 {
			t.Errorf("stats = %+v", st)
		}

		// A daily limit stops searches for the day.
		in := f.input("A")
		lim := 3
		in.QueryLimit = &lim
		if _, err := f.svc.Update(ctx, a.ID, in); err != nil {
			t.Fatal(err)
		}
		res, _, _ = f.svc.Search(ctx, core.Query{Type: "movie-search", Keywords: "Heat", Categories: []int{2000}}, SearchOptions{})
		if len(res) != 0 {
			t.Error("over its daily limit, A should be skipped")
		}
	})
}

func TestTestAndSolver(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		// Unsaved settings can be tested; nothing is stored.
		if err := f.svc.TestInput(ctx, 0, f.input("Draft")); err != nil {
			t.Errorf("test unsaved: %v", err)
		}
		if l, _ := f.svc.List(ctx); len(l) != 0 {
			t.Error("testing must not save")
		}
		ix, _ := f.svc.Create(ctx, f.input("A"))
		if err := f.svc.Test(ctx, ix.ID); err != nil {
			t.Errorf("test: %v", err)
		}
		// Wanting the solver without one set up is explained.
		in := f.input("A")
		in.UseSolver = true
		ix, _ = f.svc.Update(ctx, ix.ID, in)
		if err := f.svc.Test(ctx, ix.ID); !errors.Is(err, ErrNoSolver) {
			t.Errorf("no solver: %v", err)
		}
	})
}

// RSS sync reads an indexer behind the Cloudflare solver at most hourly,
// since every page costs the solver a browser visit. Searches aren't held
// back. (No solver is set up here, so a read shows up as a failure: that's
// enough to tell whether the indexer was asked.)
func TestSolverFeedHourly(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		now := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
		f.svc.now = func() time.Time { return now }
		in := f.input("Behind Cloudflare")
		in.UseSolver = true
		if _, err := f.svc.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Create(ctx, f.input("Plain")); err != nil {
			t.Fatal(err)
		}
		feed := core.Query{Type: "search", Categories: []int{2000}}
		asked := func(q core.Query, opt SearchOptions) (solver, plain bool) {
			t.Helper()
			// Clear the last failure's rest, so only the hourly rule is in play.
			if _, err := db.DB.NewUpdate().Model((*Indexer)(nil)).Set("retry_after = NULL").Where("1 = 1").Exec(ctx); err != nil {
				t.Fatal(err)
			}
			res, failed, err := f.svc.Search(ctx, q, opt)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range failed {
				solver = solver || (e.Indexer == "Behind Cloudflare" && !e.Skipped)
			}
			for _, r := range res {
				plain = plain || r.Indexer == "Plain"
			}
			return solver, plain
		}

		if s, p := asked(feed, SearchOptions{Feed: true}); !s || !p {
			t.Fatalf("first feed read: solver asked %v, plain asked %v", s, p)
		}
		now = now.Add(45 * time.Minute)
		if s, p := asked(feed, SearchOptions{Feed: true}); s || !p {
			t.Errorf("45 minutes later: solver asked %v (want false), plain asked %v", s, p)
		}
		if s, _ := asked(core.Query{Type: "search", Keywords: "Heat", Categories: []int{2000}}, SearchOptions{}); !s {
			t.Error("a search must still reach the solver-backed indexer")
		}
		now = now.Add(16 * time.Minute)
		if s, _ := asked(feed, SearchOptions{Feed: true}); !s {
			t.Error("over an hour later, the feed should be read again")
		}
	})
}

func TestErrorsHideSecretsInAddresses(t *testing.T) {
	s := newSession(0, "test", nil)
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/api?t=search&apikey=SECRET123", nil)
	_, err := s.Do(req, true)
	if err == nil || strings.Contains(err.Error(), "SECRET123") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("err = %v", err)
	}
}

func TestFetch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		in := f.input("A")
		lim := 2
		in.GrabLimit = &lim
		a, err := f.svc.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		rel := func(path string) Release {
			return Release{Release: core.Release{Title: "x", DownloadURL: f.tracker.URL + path}, IndexerID: a.ID, Indexer: "A"}
		}
		got, err := f.svc.Fetch(ctx, rel("/dl/1"))
		if err != nil || string(got.Torrent) != "d4:infod4:name4:testee" {
			t.Fatalf("torrent = %q, %v", got.Torrent, err)
		}
		got, err = f.svc.Fetch(ctx, rel("/dl/2"))
		if err != nil || got.Magnet != "magnet:?xt=urn:btih:abc" {
			t.Fatalf("magnet = %+v, %v", got, err)
		}
		stats, _ := f.svc.StatsOf(ctx)
		if stats[a.ID].GrabsToday != 2 {
			t.Errorf("grabs today = %d", stats[a.ID].GrabsToday)
		}
		// The daily grab limit is now used up.
		if _, err := f.svc.Fetch(ctx, rel("/dl/1")); !errors.Is(err, ErrGrabLimit) {
			t.Errorf("past the limit: %v", err)
		}
		// A web page instead of a torrent is an error that says so.
		in.GrabLimit = nil
		if _, err := f.svc.Update(ctx, a.ID, in); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Fetch(ctx, rel("/dl/3")); err == nil || !strings.Contains(err.Error(), "web page instead of a .torrent") {
			t.Errorf("login page: %v", err)
		}
	})
}
