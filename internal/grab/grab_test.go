package grab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeLib serves versions from the test database rows it was given.
type fakeLib struct {
	versions   []library.MovieVersion
	series     []library.SeriesVersion
	profile    decision.Profile
	searched   map[int64]time.Time
	epSearched map[int64]map[int64]time.Time // version → episode → when
	unmanaged  map[int64]bool                // root folders with "Manage files" off
	monitored  map[int64]map[int64]bool      // version → episode → turned on or off by SetEpisodesMonitored
}

func (f *fakeLib) MonitoredMovieVersions(_ context.Context, ids ...int64) ([]library.MovieVersion, error) {
	var out []library.MovieVersion
	for _, v := range f.versions {
		if len(ids) == 0 || v.Version.ID == ids[0] {
			if t, ok := f.searched[v.Version.ID]; ok {
				v.Version.LastSearchAt = &t
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeLib) CompiledProfile(context.Context, int64) (*decision.Profile, error) {
	p := f.profile
	return &p, nil
}

func (f *fakeLib) Profiles(context.Context) ([]library.Profile, error) {
	return []library.Profile{{ID: 1, Name: "HD"}}, nil
}

func (f *fakeLib) MarkSearched(_ context.Context, id int64, at time.Time) error {
	f.searched[id] = at
	return nil
}

func (f *fakeLib) SeriesVersions(_ context.Context, ids ...int64) ([]library.SeriesVersion, error) {
	var out []library.SeriesVersion
	for _, v := range f.series {
		if len(ids) == 0 || v.Version.ID == ids[0] {
			v.Searched = map[int64]time.Time{}
			for id, t := range f.epSearched[v.Version.ID] {
				v.Searched[id] = t
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// SetEpisodesMonitored remembers the choice and, like the real library,
// makes an episode that has aired and has no file wanted.
func (f *fakeLib) SetEpisodesMonitored(_ context.Context, versionID int64, ids []int64, on bool) error {
	if f.monitored == nil {
		f.monitored = map[int64]map[int64]bool{}
	}
	if f.monitored[versionID] == nil {
		f.monitored[versionID] = map[int64]bool{}
	}
	for _, id := range ids {
		f.monitored[versionID][id] = on
	}
	for i := range f.series {
		sv := &f.series[i]
		if sv.Version.ID != versionID {
			continue
		}
		for _, e := range sv.Episodes {
			wanted := slices.ContainsFunc(sv.Wanted, func(w library.Episode) bool { return w.ID == e.ID })
			if slices.Contains(ids, e.ID) && on && !wanted && !sv.HasFile[e.ID] && e.AirDate != "" && e.AirDate <= "2026-10-09" {
				sv.Wanted = append(sv.Wanted, e)
			}
		}
	}
	return nil
}

func (f *fakeLib) RootFolder(_ context.Context, id int64) (*library.RootFolder, error) {
	return &library.RootFolder{ID: id, Path: "/movies", ManageFiles: !f.unmanaged[id]}, nil
}

func (f *fakeLib) MarkEpisodesSearched(_ context.Context, versionID int64, ids []int64, at time.Time) error {
	if f.epSearched == nil {
		f.epSearched = map[int64]map[int64]time.Time{}
	}
	if f.epSearched[versionID] == nil {
		f.epSearched[versionID] = map[int64]time.Time{}
	}
	for _, id := range ids {
		f.epSearched[versionID][id] = at
	}
	return nil
}

type fakeIdx struct {
	releases []indexer.Release
	failures []indexer.IndexerError
	queries  []core.Query
	opts     []indexer.SearchOptions
}

func (f *fakeIdx) Search(_ context.Context, q core.Query, opt indexer.SearchOptions) ([]indexer.Release, []indexer.IndexerError, error) {
	f.queries = append(f.queries, q)
	f.opts = append(f.opts, opt)
	return f.releases, f.failures, nil
}

type fakeGrabber struct {
	got   []string
	grabs []Grabbed
	err   error // returned instead of grabbing
	// q, when set, gets a download for every grab, as the real grabber's
	// download row does (the queue limit counts them).
	q *fakeQueue
}

func (f *fakeGrabber) Grab(_ context.Context, g Grabbed) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, g.Release.Title)
	f.grabs = append(f.grabs, g)
	if f.q != nil {
		f.q.size++
	}
	return nil
}

func rel(name string, seeders int) indexer.Release {
	return indexer.Release{Release: core.Release{Title: name, Size: 8 << 30, Seeders: seeders, DownloadFactor: 1, InfoHash: name},
		IndexerID: 1, Indexer: "Knaben"}
}

type fixture struct {
	svc  *Service
	lib  *fakeLib
	idx  *fakeIdx
	hist *History
	mode settings.Grabbing
	now  time.Time
}

// newFixture stores two movies (Heat with a WEB-DL file, Ronin with none)
// so history rows have real titles and versions to point at.
func newFixture(t *testing.T, db *store.Store) *fixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	root := &library.RootFolder{Path: "/movies", MediaType: library.Movie, DefaultProfileID: 1, CreatedAt: now}
	if _, err := db.DB.NewInsert().Model(root).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var mvs []library.MovieVersion
	for i, m := range []struct {
		title string
		year  int
		file  string
	}{{"Heat", 1995, "Heat.1995.1080p.WEB-DL.H.264-GRP.mkv"}, {"Ronin", 1998, ""}} {
		title := &library.Title{MediaType: library.Movie, TMDBID: 100 + i, Title: m.title, Year: m.year, Runtime: 120,
			Status: "Released", OriginalLanguage: "en", CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(title).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		v := &library.Version{TitleID: title.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: true, Upgrades: true, CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		mv := library.MovieVersion{Version: *v, Title: *title}
		if m.file != "" {
			mv.Files = []library.MediaFile{{VersionID: v.ID, Path: m.title + "/" + m.file, Size: 4 << 30, Resolution: 1080, Source: "WEB-DL"}}
		}
		mvs = append(mvs, mv)
	}
	hd, _ := decision.PresetByID("hd")
	p, err := hd.Settings.Compile()
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		lib: &fakeLib{versions: mvs, profile: p, searched: map[int64]time.Time{}},
		idx: &fakeIdx{}, hist: NewHistory(db), mode: settings.DefaultGrabbing(), now: now,
	}
	f.hist.now = func() time.Time { return f.now }
	f.svc = New(f.lib, f.idx, func() settings.Grabbing { return f.mode }, f.hist, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.svc.now = func() time.Time { return f.now }
	f.svc.loc = time.UTC // midnight for the grabs-today counts, fixed for tests
	return f
}

// setDownloads connects a fake download client whose grabs fill the fake
// queue, so the queue limit sees them.
func (f *fixture) setDownloads(g *fakeGrabber, q *fakeQueue) {
	g.q = q
	f.svc.SetDownloads(g, q)
}

func (f *fixture) events(t *testing.T, kinds ...string) []Event {
	t.Helper()
	evs, err := f.hist.List(context.Background(), ListOptions{Events: kinds, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestSearchDryRun(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		ronin := f.lib.versions[1]
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.720p.BluRay.x264-GRP", 50),
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
			rel("Ronin.1998.1080p.WEB-DL.H.264-GRP", 400),
			rel("Ronin.2023.1080p.WEB-DL.H.264-GRP", 400),
		}
		msg, err := f.svc.SearchVersion(ctx, ronin.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "4 releases, 2 acceptable") || !strings.Contains(msg, "Would grab Ronin.1998.1080p.BluRay.x264-AMIABLE") {
			t.Errorf("message = %q", msg)
		}
		if q := f.idx.queries[0]; q.Keywords != "Ronin" || q.Year != 1998 || q.Type != "movie-search" {
			t.Errorf("query = %+v", q)
		}
		if _, ok := f.lib.searched[ronin.Version.ID]; !ok {
			t.Error("search time not recorded")
		}
		searched := f.events(t, EventSearched)
		if len(searched) != 1 {
			t.Fatalf("searched events = %d", len(searched))
		}
		var data EventData
		_ = json.Unmarshal([]byte(searched[0].Data), &data)
		if len(data.Releases) != 4 || !data.Releases[0].Accepted || data.Releases[3].Accepted ||
			len(data.Releases[3].Rejections) == 0 {
			t.Errorf("recorded releases = %+v", data.Releases)
		}
		would := f.events(t, EventWouldGrab)
		if len(would) != 1 || would[0].SourceTitle != "Ronin.1998.1080p.BluRay.x264-AMIABLE" || would[0].Quality != "Bluray-1080p" {
			t.Errorf("would-grab = %+v", would)
		}
	})
}

func TestNothingAcceptableSaysWhy(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		f.idx.releases = []indexer.Release{rel("Ronin.1998.720p.BluRay.x264-GRP", 5), rel("Ronin.1998.720p.WEB-DL.x264-GRP", 5)}
		msg, err := f.svc.SearchVersion(context.Background(), f.lib.versions[1].Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "0 acceptable") || !strings.Contains(msg, "Most common reason") {
			t.Errorf("message = %q", msg)
		}
		if len(f.events(t, EventWouldGrab, EventGrabbed)) != 0 {
			t.Error("grabbed something unacceptable")
		}
	})
}

func TestGrabbingOn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1, QueueLimit: 1}
		ronin := f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}

		// No download client yet (0.4.0): chosen, but not grabbed.
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if ev := f.events(t, EventGrabSkipped); len(ev) != 1 || !strings.Contains(ev[0].Message, "no download client") {
			t.Fatalf("skipped = %+v", ev)
		}

		g := &fakeGrabber{}
		f.setDownloads(g, &fakeQueue{})
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 || len(f.events(t, EventGrabbed)) != 1 {
			t.Fatalf("grabbed %v", g.got)
		}
		// The queue is at its limit of one, which stops the second grab.
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 {
			t.Error("grabbed past the queue limit")
		}
		if ev := f.events(t, EventGrabSkipped); len(ev) != 2 || !strings.Contains(ev[0].Message, "the queue is full (1 download,") {
			t.Errorf("skipped = %+v", ev)
		}
	})
}

// A version whose media folder Reelkeeper may not write to gets nothing
// grabbed: the download could never be imported. History says why, and the
// search still counts, so it isn't retried every hour.
func TestNoGrabIntoUnmanagedFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1, QueueLimit: 5}
		g := &fakeGrabber{}
		f.svc.grabber = g
		ronin := f.lib.versions[1].Version
		f.lib.unmanaged = map[int64]bool{ronin.RootFolderID: true}
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}

		msg, err := f.svc.SearchVersion(ctx, ronin.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 0 {
			t.Fatalf("grabbed %v into an unmanaged folder", g.got)
		}
		if !strings.Contains(msg, `"Manage files" is off for /movies`) {
			t.Errorf("message = %q", msg)
		}
		if ev := f.events(t, EventGrabSkipped); len(ev) != 1 || !strings.Contains(ev[0].Message, "Settings → Media Folders") {
			t.Errorf("skipped = %+v", ev)
		}
		if _, ok := f.lib.searched[ronin.ID]; !ok {
			t.Error("search time not recorded")
		}

		// Turned on: the same search grabs.
		f.lib.unmanaged = nil
		if _, err := f.svc.SearchVersion(ctx, ronin.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 {
			t.Errorf("grabbed %v once managed", g.got)
		}
	})
}

func TestQueueLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1, QueueLimit: 1}
		g, q := &fakeGrabber{}, &fakeQueue{}
		f.setDownloads(g, q)
		heat, ronin := f.lib.versions[0].Version.ID, f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
			rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40),
		}
		// Below the limit (an empty queue), a search grabs.
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 || q.size != 1 {
			t.Fatalf("grabbed %d, queue %d; want 1 and 1", len(g.got), q.size)
		}
		// The queue is now full: Heat's grab is blocked, and Heat doesn't
		// count as searched, so it stays first in line.
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 {
			t.Fatal("grabbed with the queue full")
		}
		ev := f.events(t, EventGrabSkipped)
		want := "the queue is full (1 download, the limit in Settings → Download Client); Reelkeeper grabs again when downloads finish."
		if len(ev) != 1 || !strings.Contains(ev[0].Message, want) || strings.Contains(ev[0].Message, "midnight") {
			t.Errorf("skipped = %+v", ev)
		}
		if _, marked := f.lib.searched[heat]; marked {
			t.Error("a search the limit blocked counted as a search")
		}
		if _, marked := f.lib.searched[ronin]; !marked {
			t.Error("a search that grabbed didn't count")
		}

		// While the queue is full, the hourly pass and RSS sync don't
		// query the indexers.
		queries := len(f.idx.queries)
		f.now = f.now.Add(time.Hour)
		msg, err := f.svc.ScheduledPass(ctx, 3, searchEvery)
		if err != nil || !strings.Contains(msg, "Paused: the queue is full (1 of 1 downloads). Searching resumes when downloads finish") ||
			strings.Contains(msg, "midnight") {
			t.Errorf("pass = %q, %v", msg, err)
		}
		if msg, _, err := f.svc.RSSSync(ctx); err != nil || !strings.Contains(msg, "Paused") {
			t.Errorf("rss = %q, %v", msg, err)
		}
		if len(f.idx.queries) != queries {
			t.Error("searched while the queue was full")
		}

		// A download finishes: room again, so the pass searches and Heat
		// is grabbed.
		q.size = 0
		f.now = f.now.Add(time.Hour)
		if _, err := f.svc.ScheduledPass(ctx, 3, searchEvery); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 2 || g.got[1] != "Heat.1995.1080p.BluRay.x264-AMIABLE" {
			t.Fatalf("after a download finished grabbed %+v", g.got)
		}

		// Raising the limit lets grabs through at once.
		f.mode.QueueLimit = 5
		f.lib.searched = map[int64]time.Time{}
		f.now = f.now.Add(time.Hour)
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 3 {
			t.Fatalf("under a raised limit grabbed %d, want 3", len(g.got))
		}
		// Lowering it below the queue's size stops grabbing at once.
		f.mode.QueueLimit = 2
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 3 {
			t.Error("grabbed after the limit was lowered below the queue's size")
		}
		// No limit: never full.
		f.mode.QueueLimit = 0
		q.size = 500
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 4 {
			t.Errorf("with no limit grabbed %d, want 4", len(g.got))
		}
	})
}

// Only mode On is limited: a dry run still records what it would grab,
// however full the queue is.
func TestQueueLimitIgnoredInDryRun(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabDryRun, TVMode: settings.GrabDryRun, MinSeeders: 1, QueueLimit: 1}
		g, q := &fakeGrabber{}, &fakeQueue{size: 40}
		f.setDownloads(g, q)
		ronin := f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if n := len(f.events(t, EventWouldGrab)); n != 1 {
			t.Errorf("would-grab events = %d, want 1", n)
		}
		if len(g.got) != 0 || len(f.events(t, EventGrabSkipped)) != 0 {
			t.Errorf("dry run grabbed %v or skipped %+v", g.got, f.events(t, EventGrabSkipped))
		}
		if _, marked := f.lib.searched[ronin]; !marked {
			t.Error("a dry-run search didn't count")
		}
		if full, err := f.svc.queueFull(ctx, f.mode.Mode); err != nil || full {
			t.Errorf("queueFull in dry run = %v, %v", full, err)
		}
	})
}

func TestLimitLifted(t *testing.T) {
	on := func(n int) settings.Grabbing { return settings.Grabbing{Mode: settings.GrabOn, QueueLimit: n} }
	dry := settings.Grabbing{Mode: settings.GrabDryRun, QueueLimit: 5}
	for _, c := range []struct {
		name          string
		before, after settings.Grabbing
		want          bool
	}{
		{"raised", on(5), on(20), true},
		{"removed", on(5), on(0), true},
		{"lowered", on(20), on(5), false},
		{"unchanged", on(5), on(5), false},
		{"already unlimited", on(0), on(0), false},
		{"limit added", on(0), on(5), false},
		{"turned on", dry, on(5), true},
		{"dry run raised", dry, settings.Grabbing{Mode: settings.GrabDryRun, QueueLimit: 50}, false},
		{"TV turned on", on(5), settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, QueueLimit: 5}, true},
		{"TV limit raised", settings.Grabbing{TVMode: settings.GrabOn, QueueLimit: 5}, settings.Grabbing{TVMode: settings.GrabOn, QueueLimit: 9}, true},
	} {
		if got := LimitLifted(c.before, c.after); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestQueueFull(t *testing.T) {
	on := settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOff, QueueLimit: 20}
	tvOn := settings.Grabbing{Mode: settings.GrabDryRun, TVMode: settings.GrabOn, QueueLimit: 20}
	off := settings.Grabbing{Mode: settings.GrabDryRun, TVMode: settings.GrabOff, QueueLimit: 20}
	for _, c := range []struct {
		name string
		g    settings.Grabbing
		size int
		want bool
	}{
		{"under the limit", on, 19, false},
		{"at the limit", on, 20, true},
		{"over the limit", on, 47, true},
		{"TV on", tvOn, 20, true},
		{"neither on", off, 47, false},
		{"no limit", settings.Grabbing{Mode: settings.GrabOn, QueueLimit: 0}, 47, false},
	} {
		if got := QueueFull(c.g, c.size); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUpgradeAndCutoff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		heat := f.lib.versions[0].Version.ID
		f.idx.releases = []indexer.Release{
			rel("Heat.1995.1080p.WEBRip.x264-GRP", 100),    // same rung as the WEB-DL file
			rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 10), // better
		}
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}
		would := f.events(t, EventWouldGrab)
		if len(would) != 1 || would[0].SourceTitle != "Heat.1995.1080p.BluRay.x264-AMIABLE" ||
			!strings.Contains(would[0].Message, "Would upgrade to") {
			t.Fatalf("would-grab = %+v", would)
		}

		// A Blu-ray file meets HD's "upgrade until": no longer wanted.
		f.lib.versions[0].Files[0].Source = "BluRay"
		ws, err := f.svc.load(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.v.Version.ID == heat {
				t.Error("a version at its cutoff is still wanted")
			}
		}
	})
}

func TestRSSSync(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
			rel("Ronin.1998.720p.HDTV.x264-GRP", 40),
			rel("Some.Other.Movie.2024.1080p.WEB-DL.H.264-GRP", 400),
		}
		msg, quiet, err := f.svc.RSSSync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "1 match") {
			t.Errorf("message = %q", msg)
		}
		if quiet {
			t.Error("a run that matched a wanted movie must not be quiet")
		}
		if q := f.idx.queries[0]; q.Keywords != "" {
			t.Errorf("RSS searched for %q", q.Keywords)
		}
		if !f.idx.opts[0].Feed {
			t.Error("RSS must ask for a feed read, so solver-backed indexers are read hourly")
		}
		rss := f.events(t, EventRSS)
		if len(rss) != 1 || !strings.Contains(rss[0].Message, "2 new releases for Ronin (1998), HD, 1 acceptable") {
			t.Fatalf("rss events = %+v", rss)
		}
		if len(f.events(t, EventWouldGrab)) != 1 {
			t.Error("no would-grab")
		}
		// The same feed 15 minutes later records nothing new.
		f.now = f.now.Add(15 * time.Minute)
		if _, quiet, err := f.svc.RSSSync(ctx); err != nil || !quiet {
			t.Fatalf("a repeat with nothing new should be quiet: quiet=%v err=%v", quiet, err)
		}
		if len(f.events(t, EventRSS)) != 1 || len(f.events(t, EventWouldGrab)) != 1 {
			t.Error("RSS recorded the same releases twice")
		}
	})
}

func TestOffMeansNoAutomaticSearching(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode.Mode = settings.GrabOff
		for _, run := range []func(context.Context) (string, error){
			func(ctx context.Context) (string, error) {
				msg, quiet, err := f.svc.RSSSync(ctx)
				if !quiet {
					t.Error("RSS sync while grabbing is off should be quiet")
				}
				return msg, err
			},
			func(ctx context.Context) (string, error) { return f.svc.ScheduledPass(ctx, 3, searchEvery) },
		} {
			msg, err := run(ctx)
			if err != nil || !strings.Contains(msg, "Grabbing is off") {
				t.Errorf("%q, %v", msg, err)
			}
		}
		if len(f.idx.queries) != 0 {
			t.Error("searched while off")
		}
	})
}

func TestScheduledPass(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		// Both movies want something (Heat could upgrade from WEB-DL).
		// Ronin has no file, so it's searched first although Heat is listed
		// first: missing movies come before upgrades.
		if _, err := f.svc.ScheduledPass(ctx, 1, searchEvery); err != nil {
			t.Fatal(err)
		}
		if len(f.idx.queries) != 1 || !strings.Contains(f.idx.queries[0].Keywords, "Ronin") {
			t.Fatalf("first pass searched %+v, want Ronin", f.idx.queries)
		}
		if _, err := f.svc.ScheduledPass(ctx, 1, searchEvery); err != nil {
			t.Fatal(err)
		}
		if len(f.idx.queries) != 2 || f.idx.queries[0].Keywords == f.idx.queries[1].Keywords {
			t.Fatalf("queries = %+v", f.idx.queries)
		}
		// Both were just searched: nothing is due.
		msg, _ := f.svc.ScheduledPass(ctx, 1, searchEvery)
		if len(f.idx.queries) != 2 || !strings.Contains(msg, "Nothing to search") {
			t.Errorf("third pass searched again: %q", msg)
		}
		// Unreleased movies wait.
		f.now = f.now.Add(searchEvery + time.Hour)
		f.lib.versions[1].Title.Status, f.lib.versions[1].Title.DigitalRelease = "Post Production", "2027-01-01"
		f.lib.versions[0].Version.Monitored = false
		f.lib.versions = f.lib.versions[1:]
		msg, _ = f.svc.ScheduledPass(ctx, 1, searchEvery)
		if len(f.idx.queries) != 2 {
			t.Errorf("searched an unreleased movie: %q", msg)
		}
	})
}

func TestPruneKeepsGrabs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		old := f.now.Add(-100 * 24 * time.Hour)
		for _, kind := range []string{EventSearched, EventRSS, EventGrabbed} {
			if err := f.hist.Add(ctx, &Event{Event: kind, CreatedAt: old}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		n, err := f.hist.Prune(ctx)
		if err != nil || n != 2 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if evs := f.events(t); len(evs) != 1 || evs[0].Event != EventGrabbed {
			t.Errorf("left %+v", evs)
		}
	})
}

func TestReleased(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		t    library.Title
		want bool
	}{
		{library.Title{Status: "Released"}, true},
		{library.Title{Status: "Released", DigitalRelease: "2026-12-01"}, false}, // in cinemas only
		{library.Title{Status: "Released", DigitalRelease: "2026-09-01"}, true},
		{library.Title{Status: "Post Production"}, false},
	} {
		if got := released(tt.t, now); got != tt.want {
			t.Errorf("%+v: %v", tt.t, got)
		}
	}
}

type fakeQueue struct {
	// size is how many downloads Activity → Queue lists.
	size int
	// moving: the download service is moving torrents to another client.
	moving    bool
	active    map[int64]bool
	activeEps map[int64]map[int64]bool
	blocked   map[string]bool
	// current are the downloads episode files came from (episode → download).
	current map[int64]decision.Current
}

func (q *fakeQueue) ActiveVersions(context.Context) (map[int64]bool, error) { return q.active, nil }

func (q *fakeQueue) QueueSize(context.Context) (int, error) { return q.size, nil }

// Moving says torrents are moving between download clients.
func (q *fakeQueue) Moving() bool { return q.moving }

func (q *fakeQueue) ActiveEpisodes(context.Context) (map[int64]map[int64]bool, error) {
	return q.activeEps, nil
}

func (q *fakeQueue) CurrentEpisodeDownloads(_ context.Context, _ int64, since time.Time) (map[int64]decision.Current, error) {
	out := map[int64]decision.Current{}
	for id, c := range q.current {
		if !c.ImportedAt.Before(since) {
			out[id] = c
		}
	}
	return out, nil
}

func (q *fakeQueue) Blocklisted(context.Context, int64) (func(name, hash string) bool, error) {
	return func(name, _ string) bool { return q.blocked[name] }, nil
}

func TestQueueAndBlocklist(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1}
		g := &fakeGrabber{}
		q := &fakeQueue{active: map[int64]bool{}, blocked: map[string]bool{"Ronin.1998.1080p.BluRay.x264-AMIABLE": true}}
		f.svc.SetDownloads(g, q)
		ronin := f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 400), // blocklisted
			rel("Ronin.1998.1080p.WEB-DL.H.264-GRP", 10),
		}
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 || g.got[0] != "Ronin.1998.1080p.WEB-DL.H.264-GRP" {
			t.Fatalf("grabbed %v (the blocklisted release must be skipped)", g.got)
		}
		var data EventData
		_ = json.Unmarshal([]byte(f.events(t, EventSearched)[0].Data), &data)
		if r := data.Releases[1]; r.Accepted || !strings.Contains(strings.Join(r.Rejections, " "), "blocklist") {
			t.Errorf("blocklisted verdict = %+v", r)
		}

		// While it downloads, automatic searches leave Ronin alone, and a
		// manual search doesn't start a second download.
		q.active[ronin] = true
		ws, _ := f.svc.load(ctx, true)
		for _, w := range ws {
			if w.v.Version.ID == ronin {
				t.Error("a version with an active download is still wanted")
			}
		}
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 {
			t.Error("grabbed again while a download is in progress")
		}
	})
}

func TestSearchWithNoIndexerAnswering(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		ronin := f.lib.versions[1].Version.ID
		f.idx.failures = []indexer.IndexerError{{Indexer: "1337x", Message: "resting after an error until 10:30", Skipped: true}}
		msg, err := f.svc.SearchVersion(ctx, ronin, TriggerScheduled)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "Couldn't search for Ronin") || !strings.Contains(msg, "1337x: resting") {
			t.Errorf("message = %q", msg)
		}
		if _, ok := f.lib.searched[ronin]; ok {
			t.Error("a search no indexer answered counted as searched")
		}
	})
}

func TestGrabsToday(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db) // now is 2026-10-09 12:00 UTC, and the day is UTC
		add := func(event string, at time.Time) {
			t.Helper()
			if err := f.hist.Add(ctx, &Event{Event: event, CreatedAt: at}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		midnight := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
		add(EventGrabbed, midnight.Add(-time.Second))    // yesterday
		add(EventGrabbed, midnight)                      // exactly midnight counts
		add(EventGrabbed, midnight.Add(5*time.Hour))     // today
		add(EventGrabSkipped, midnight.Add(5*time.Hour)) // not a grab
		n, err := f.svc.GrabsToday(ctx)
		if err != nil || n != 2 {
			t.Errorf("GrabsToday = %d, %v; want 2", n, err)
		}
		// After the next midnight the count starts again.
		f.now = midnight.Add(25 * time.Hour)
		if n, _ := f.svc.GrabsToday(ctx); n != 0 {
			t.Errorf("next day = %d, want 0", n)
		}
	})
}

// A full download disk skips the grab, says why in History, and emails
// once, not on every search.
func TestGrabSkippedForSpace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1}
		f.svc.grabber = &fakeGrabber{err: fmt.Errorf("%w: the download disk has 50.0 GB free", ErrNotEnoughSpace)}
		var sent []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { sent = append(sent, ev) })
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		for range 2 {
			if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
				t.Fatal(err)
			}
		}
		ev := f.events(t, EventGrabSkipped)
		if len(ev) != 2 || !strings.Contains(ev[0].Message, "didn't grab it: the download disk has 50.0 GB free.") {
			t.Errorf("skipped = %+v", ev)
		}
		if len(sent) != 1 || !strings.Contains(sent[0].Message, "skipping grabs") {
			t.Errorf("notifications = %+v", sent)
		}
	})
}

// A search Seerr asked for (TriggerRequest) goes past the queue limit and
// its grab is marked Requested; the same search started any other way is
// still held back.
func TestRequestSkipsTheQueueLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1, QueueLimit: 1}
		g := &fakeGrabber{}
		f.setDownloads(g, &fakeQueue{})
		heat, ronin := f.lib.versions[0].Version.ID, f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
			rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40),
		}
		// Ronin fills the queue's one place.
		if _, err := f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || g.grabs[0].Requested {
			t.Fatalf("first grab = %+v", g.grabs)
		}

		// A manual search for Heat is held back, and doesn't count as a search.
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 {
			t.Fatal("a manual search grabbed past the queue limit")
		}
		if _, marked := f.lib.searched[heat]; marked {
			t.Error("a search the limit blocked counted as a search")
		}

		// The same search from a request grabs, and is marked as requested.
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerRequest); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 2 || !g.grabs[1].Requested || g.grabs[1].Version.Version.ID != heat {
			t.Fatalf("grabs after a request = %+v", g.grabs)
		}
		if _, marked := f.lib.searched[heat]; !marked {
			t.Error("a request's search didn't count as a search")
		}
	})
}

// While torrents move between download clients nothing is grabbed: a search
// that picks a release says why in History (and nobody is emailed about a
// "refused" grab), and the scheduled passes don't search at all.
func TestGrabbingWaitsWhileTorrentsMove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1}
		g, q := &fakeGrabber{err: ErrMoving}, &fakeQueue{moving: true}
		f.setDownloads(g, q)
		var sent []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { sent = append(sent, ev) })
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		ev := f.events(t, EventGrabSkipped)
		if len(ev) != 1 || !strings.Contains(ev[0].Message, "torrents are moving to another download client") {
			t.Errorf("skipped = %+v", ev)
		}
		if len(sent) != 0 {
			t.Errorf("notifications = %+v", sent)
		}
		// The hourly pass and RSS sync don't even search.
		searches := len(f.idx.queries)
		msg, err := f.svc.ScheduledPass(ctx, 5, time.Hour)
		if err != nil || !strings.Contains(msg, "Paused: torrents are moving to another download client") {
			t.Errorf("ScheduledPass = %q, %v", msg, err)
		}
		if len(f.idx.queries) != searches {
			t.Error("the scheduled pass searched while torrents were moving")
		}
		// Not moving: the same search grabs.
		q.moving, g.err = false, nil
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.got) != 1 {
			t.Errorf("grabbed %v after the move", g.got)
		}
	})
}
