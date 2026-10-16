package grab

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// addSeries stores a series with episodes ("S01E02" → air date) and gives
// the fake library a monitored version of it wanting the listed episodes.
// The fixture's today is 2026-10-09.
func addSeries(t *testing.T, db *store.Store, f *fixture, title, seriesType string, eps map[string]string, wanted ...string) *library.SeriesVersion {
	t.Helper()
	ctx := context.Background()
	show := &library.Title{MediaType: library.Series, TMDBID: 9000 + len(f.lib.series), TVDBID: 7000 + len(f.lib.series),
		Title: title, Year: 2015, SeriesType: seriesType, OriginalLanguage: "en", CreatedAt: f.now, Genres: "[]", AltTitles: "[]"}
	if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var root library.RootFolder
	if err := db.DB.NewSelect().Model(&root).Limit(1).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	v := &library.Version{TitleID: show.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: true, CreatedAt: f.now}
	if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	sv := library.SeriesVersion{Version: *v, Title: *show, HasFile: map[int64]bool{}}
	labels := make([]string, 0, len(eps))
	for l := range eps {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	byLabel := map[string]library.Episode{}
	for _, l := range labels {
		var s, n int
		if _, err := fmt.Sscanf(l, "S%dE%d", &s, &n); err != nil {
			t.Fatal(err)
		}
		e := &library.Episode{TitleID: show.ID, TMDBID: s*1000 + n, AiredSeason: s, AiredNumber: n, Season: &s, Number: &n,
			AirDate: eps[l], Runtime: 50}
		if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		sv.Episodes = append(sv.Episodes, *e)
		byLabel[l] = *e
	}
	for _, l := range wanted {
		sv.Wanted = append(sv.Wanted, byLabel[l])
	}
	f.lib.series = append(f.lib.series, sv)
	return &f.lib.series[len(f.lib.series)-1]
}

func epID(sv *library.SeriesVersion, label string) int64 {
	for _, e := range sv.Episodes {
		if fmt.Sprintf("S%02dE%02d", *e.Season, *e.Number) == label {
			return e.ID
		}
	}
	panic("no episode " + label)
}

func tvRel(name string, size int64, seeders int) indexer.Release {
	r := rel(name, seeders)
	r.Size = size
	r.Categories = []int{5040}
	return r
}

// A finished season with most of it wanted is one pack search, and an
// acceptable pack beats single episodes.
func TestTVSeasonPackSearch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S01E01": "2015-12-14", "S01E02": "2015-12-15", "S01E03": "2015-12-22",
		}, "S01E01", "S01E02", "S01E03")
		f.idx.releases = []indexer.Release{
			tvRel("The.Expanse.S01E01.1080p.WEB-DL.DDP5.1.H.264-NTb", 2<<30, 90),
			tvRel("The.Expanse.S01.1080p.BluRay.x264-ROVERS", 9<<30, 20),
		}
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		q := f.idx.queries[0]
		if len(f.idx.queries) != 1 || q.Type != "tv-search" || q.Season != 1 || q.Episode != "" || q.Keywords != "The Expanse" || q.TVDBID != sv.Title.TVDBID {
			t.Errorf("queries = %+v", f.idx.queries)
		}
		if !strings.Contains(msg, "season 1: 2 releases, 2 acceptable") || !strings.Contains(msg, "Would grab The.Expanse.S01.1080p.BluRay.x264-ROVERS for season 1 (3 wanted episodes)") {
			t.Errorf("message = %q", msg)
		}
		if strings.Contains(msg, "S01E01.1080p") && strings.Count(msg, "Would grab") > 1 {
			t.Errorf("grabbed a single episode the pack already covers: %q", msg)
		}
		if len(f.lib.epSearched[sv.Version.ID]) != 3 {
			t.Errorf("search times = %v", f.lib.epSearched)
		}
		ev := f.events(t, EventWouldGrab)
		if len(ev) != 1 || *ev[0].VersionID != sv.Version.ID {
			t.Fatalf("would-grab events = %+v", ev)
		}
		var data EventData
		_ = json.Unmarshal([]byte(ev[0].Data), &data)
		if len(data.EpisodeIDs) != 3 {
			t.Errorf("event episodes = %v", data.EpisodeIDs)
		}
	})
}

// One wanted episode of a season is searched for on its own; a pack
// isn't wanted for it.
func TestTVSingleEpisodeSearch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S04E01": "2019-12-13", "S04E02": "2019-12-13", "S04E03": "2019-12-13", "S04E04": "2019-12-13",
		}, "S04E03")
		f.idx.releases = []indexer.Release{
			tvRel("The.Expanse.S04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 9<<30, 90),
			tvRel("The.Expanse.S04E03.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2<<30, 30),
		}
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if q := f.idx.queries[0]; q.Season != 4 || q.Episode != "3" {
			t.Errorf("query = %+v", q)
		}
		if !strings.Contains(msg, "S04E03: 2 releases, 1 acceptable") || !strings.Contains(msg, "Would grab The.Expanse.S04E03.1080p") {
			t.Errorf("message = %q", msg)
		}
	})
}

// A season search can bring back several wanted episodes as singles; each
// is grabbed once, and the queue limit (shared with movies) still applies.
func TestTVGrabsSeveralEpisodesWithinTheLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g := &fakeGrabber{}
		f.setDownloads(g, &fakeQueue{})
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 2}
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S02E01": "2017-02-01", "S02E02": "2017-02-08", "S02E03": "2017-02-15",
		}, "S02E01", "S02E02", "S02E03")
		f.idx.releases = []indexer.Release{
			tvRel("The.Expanse.S02E01.1080p.WEB-DL.H.264-NTb", 2<<30, 50),
			tvRel("The.Expanse.S02E01.720p.HDTV.x264-KILLERS", 1<<30, 80), // same episode, worse
			tvRel("The.Expanse.S02E02.1080p.WEB-DL.H.264-NTb", 2<<30, 40),
			tvRel("The.Expanse.S02E03.1080p.WEB-DL.H.264-NTb", 2<<30, 30),
		}
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 2 {
			t.Fatalf("grabbed %v (%s)", g.got, msg)
		}
		for i, want := range []string{"S02E01", "S02E02"} {
			tg := g.grabs[i].TV
			if tg == nil || len(tg.EpisodeIDs) != 1 || tg.EpisodeIDs[0] != epID(sv, want) || tg.Label != want {
				t.Errorf("grab %d = %+v", i, tg)
			}
		}
		if !strings.Contains(msg, "the queue is full (2 downloads") {
			t.Errorf("the third should be held back by the limit: %q", msg)
		}
		// The one the limit held back stays first in line; the grabbed ones
		// count as searched.
		got := f.lib.epSearched[sv.Version.ID]
		if _, held := got[epID(sv, "S02E03")]; held || len(got) != 2 {
			t.Errorf("search times = %v", got)
		}
	})
}

// With the queue full, the scheduled pass and RSS sync don't search for TV
// at all, and say why.
func TestTVPausedWhileTheQueueIsFull(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := &fakeGrabber{}, &fakeQueue{size: 3}
		f.setDownloads(g, q)
		f.mode = settings.Grabbing{Mode: settings.GrabOff, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 3}
		addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S02E01": "2017-02-01"}, "S02E01")
		f.idx.releases = []indexer.Release{tvRel("The.Expanse.S02E01.1080p.WEB-DL.H.264-NTb", 2<<30, 50)}

		msg, err := f.svc.ScheduledPass(ctx, 3, searchEvery)
		if err != nil || !strings.Contains(msg, "Paused: the queue is full (3 of 3 downloads)") {
			t.Errorf("pass = %q, %v", msg, err)
		}
		if msg, _, err := f.svc.RSSSync(ctx); err != nil || !strings.Contains(msg, "Paused: the queue is full") {
			t.Errorf("rss = %q, %v", msg, err)
		}
		if len(f.idx.queries) != 0 || len(g.grabs) != 0 {
			t.Errorf("searched %d times and grabbed %v with the queue full", len(f.idx.queries), g.got)
		}
		// Room again: the pass grabs.
		q.size = 2
		if _, err := f.svc.ScheduledPass(ctx, 3, searchEvery); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 {
			t.Errorf("grabbed %v with room in the queue", g.got)
		}
	})
}

// TV checks the media folder too: nothing is grabbed for a series whose
// folder has "Manage files" off.
func TestTVNoGrabIntoUnmanagedFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g := &fakeGrabber{}
		f.svc.SetDownloads(g, &fakeQueue{})
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 5}
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S02E01": "2017-02-01"}, "S02E01")
		f.lib.unmanaged = map[int64]bool{sv.Version.RootFolderID: true}
		f.idx.releases = []indexer.Release{tvRel("The.Expanse.S02E01.1080p.WEB-DL.H.264-NTb", 2<<30, 50)}

		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 0 {
			t.Fatalf("grabbed %v into an unmanaged folder", g.got)
		}
		if !strings.Contains(msg, `"Manage files" is off`) {
			t.Errorf("message = %q", msg)
		}
		if ev := f.events(t, EventGrabSkipped); len(ev) != 1 {
			t.Errorf("skipped = %+v", ev)
		}
	})
}

// Episodes already downloading, not aired yet, or airing today aren't
// searched for by the scheduled pass.
func TestTVScheduledPassSkipsWhatIsntDue(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S06E01": "2026-10-01", "S06E02": "2026-10-08", "S06E03": "2026-10-09", "S06E04": "2026-10-16",
		}, "S06E01", "S06E02", "S06E03", "S06E04")
		f.svc.SetDownloads(&fakeGrabber{}, &fakeQueue{activeEps: map[int64]map[int64]bool{sv.Version.ID: {epID(sv, "S06E01"): true}}})
		f.idx.releases = nil
		msg, err := f.svc.ScheduledPass(ctx, 3, searchEvery)
		if err != nil {
			t.Fatal(err)
		}
		var tvQueries []core.Query
		for _, q := range f.idx.queries {
			if q.Type == "tv-search" {
				tvQueries = append(tvQueries, q)
			}
		}
		if len(tvQueries) != 1 || tvQueries[0].Episode != "2" {
			t.Errorf("TV queries = %+v (%s)", tvQueries, msg)
		}
		if !strings.Contains(msg, "TV: searched for The Expanse S06E02 (1 waiting)") {
			t.Errorf("message = %q", msg)
		}
		// Searched just now: not due again for three days.
		f.idx.queries = nil
		msg, err = f.svc.ScheduledPass(ctx, 3, searchEvery)
		if err != nil || !strings.Contains(msg, "TV: nothing to search") {
			t.Errorf("again: %q, %v", msg, err)
		}
	})
}

// RSS reads one feed for movies and TV and splits it by category.
func TestTVRSS(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S06E05": "2026-10-09", "S06E06": "2026-10-16",
		}, "S06E05")
		f.idx.releases = []indexer.Release{
			tvRel("The.Expanse.S06E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2<<30, 10), // aired today: RSS takes it
			tvRel("The.Expanse.S06E06.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2<<30, 10), // not wanted
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
		}
		msg, quiet, err := f.svc.RSSSync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.idx.queries) != 1 || len(f.idx.queries[0].Categories) != 2 {
			t.Errorf("feed queries = %+v", f.idx.queries)
		}
		if quiet || !strings.Contains(msg, "2 matches for 2 movie versions and 1 series version") {
			t.Errorf("message = %q, quiet %v", msg, quiet)
		}
		var names []string
		for _, e := range f.events(t, EventWouldGrab) {
			names = append(names, e.SourceTitle)
		}
		if len(names) != 2 || !strings.Contains(strings.Join(names, " "), "S06E05") || strings.Contains(strings.Join(names, " "), "S06E06") {
			t.Errorf("would grab %v", names)
		}
		// The same feed again is old news.
		if _, quiet, _ := f.svc.RSSSync(ctx); !quiet {
			t.Error("a repeat should be quiet")
		}
	})
}

// Daily shows are searched by air date, the Torznab way.
func TestTVDailyQuery(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "The Daily Show", "daily", map[string]string{"S31E120": "2026-10-08"}, "S31E120")
		f.idx.releases = []indexer.Release{tvRel("The.Daily.Show.2026.10.08.1080p.WEB.h264-GRP", 1<<30, 20)}
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if q := f.idx.queries[0]; q.Season != 2026 || q.Episode != "10/08" {
			t.Errorf("query = %+v", q)
		}
		if !strings.Contains(msg, "2026-10-08: 1 release, 1 acceptable") || !strings.Contains(msg, "Would grab The.Daily.Show.2026.10.08") {
			t.Errorf("message = %q", msg)
		}
	})
}

// TV mode Off: TV isn't searched automatically; movies carry on.
func TestTVModeOff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode.TVMode = settings.GrabOff
		addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S06E02": "2026-10-08"}, "S06E02")
		msg, err := f.svc.ScheduledPass(ctx, 3, searchEvery)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range f.idx.queries {
			if q.Type == "tv-search" {
				t.Errorf("TV searched while off: %+v", q)
			}
		}
		if strings.Contains(msg, "TV") {
			t.Errorf("message = %q", msg)
		}
		f.idx.releases = []indexer.Release{tvRel("The.Expanse.S06E02.1080p.WEB-DL.H.264-NTb", 2<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if q := f.idx.queries[len(f.idx.queries)-1]; len(q.Categories) != 1 || q.Categories[0] != 2000 {
			t.Errorf("feed asked for %v", q.Categories)
		}
		if n := len(f.events(t, EventWouldGrab)); n != 0 {
			t.Errorf("%d would-grabs while TV is off", n)
		}
	})
}

// A request (Seerr) for a series goes past the queue limit and backfill's
// quota, and its grab is marked Requested. The same search started any
// other way is held back by whichever limit is reached.
func TestTVRequestSkipsTheLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		held  string // what History says a manual search is held back by
	}{
		{"backfill quota", 50, "continues tomorrow"},
		{"queue limit", BackfillGrabsPerDay, "the queue is full (5 downloads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				f := newFixture(t, db)
				g, q := bfOn(f)
				f.mode.QueueLimit = tc.limit
				q.size = BackfillGrabsPerDay
				a := bfShow(t, db, f, "The IT Crowd")
				if _, err := f.svc.BackfillAdd(ctx, a); err != nil {
					t.Fatal(err)
				}
				for range BackfillGrabsPerDay {
					if err := f.hist.Add(ctx, &Event{VersionID: &a, Event: EventGrabbed, CreatedAt: f.now}, EventData{}); err != nil {
						t.Fatal(err)
					}
				}
				f.idx.releases = []indexer.Release{tvRel("The.IT.Crowd.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30)}

				msg, err := f.svc.SearchVersion(ctx, a, TriggerManual)
				if err != nil || !strings.Contains(msg, tc.held) || len(g.grabs) != 0 {
					t.Fatalf("manual search: %q, %v (grabbed %v)", msg, err, g.got)
				}

				if _, err := f.svc.SearchVersion(ctx, a, TriggerRequest); err != nil {
					t.Fatal(err)
				}
				if len(g.grabs) != 1 || !g.grabs[0].Requested || g.grabs[0].TV == nil {
					t.Fatalf("grabs after a request = %+v", g.grabs)
				}
			})
		})
	}
}
