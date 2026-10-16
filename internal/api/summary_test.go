package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestSummaryAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")

		// Signed out, the API refuses.
		if r := a.do("GET", "/api/v1/summary", ""); r.status != 401 {
			t.Errorf("signed out: %d", r.status)
		}
		a.setup()

		// Empty library: zeros and empty lists (never null).
		r := a.do("GET", "/api/v1/summary", "")
		if r.status != 200 || r.raw == "" {
			t.Fatalf("empty: %d %s", r.status, r.raw)
		}
		if up, ok := r.body["upcoming"].([]any); !ok || len(up) != 0 {
			t.Errorf("upcoming = %v", r.body["upcoming"])
		}
		if f, ok := r.body["indexers"].(map[string]any)["failing"].([]any); !ok || len(f) != 0 {
			t.Errorf("failing = %v", r.body["indexers"])
		}

		// Queue: one downloading, one import-failed, one finished (not counted).
		root := addMovieFixture(t, a)
		now := time.Now().UTC()
		today := time.Now()
		day := func(n int) string { return today.AddDate(0, 0, n).Format("2006-01-02") }
		type movie struct {
			name                string
			digital, theatrical string
			monitored, withFile bool
		}
		var firstTitle, firstVersion int64
		for i, m := range []movie{
			{"Soon", day(2), "", true, false},
			{"Cinema", "", day(0), true, false},
			{"Later", day(8), "", true, false},
			{"Owned", day(1), "", true, true},
			{"Unwatched", day(1), "", false, false},
		} {
			title := &library.Title{MediaType: library.Movie, TMDBID: 7000 + i, Title: m.name, SortTitle: m.name, Year: 2026,
				DigitalRelease: m.digital, TheatricalRelease: m.theatrical, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
			v := &library.Version{ProfileID: 1, RootFolderID: root, Monitored: m.monitored, Upgrades: true, CreatedAt: now}
			if _, err := db.DB.NewInsert().Model(title).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			v.TitleID = title.ID
			if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if m.withFile {
				mf := &library.MediaFile{VersionID: v.ID, RootFolderID: root, Path: "Owned/Owned.mkv", AddedAt: now, ModifiedAt: now, ReleaseInfo: "{}", MediaInfo: "{}", EpisodeClaim: "{}"}
				if _, err := db.DB.NewInsert().Model(mf).Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if i == 0 {
				firstTitle, firstVersion = title.ID, v.ID
			}
		}
		for i, status := range []string{download.StatusDownloading, download.StatusImportFailed, download.StatusImported} {
			d := &download.Download{CreatedAt: now, TitleID: &firstTitle, VersionID: &firstVersion, Hash: fmt.Sprintf("%040d", i),
				Category: settings.MovieCategory, ReleaseTitle: "Soon.2026.1080p", Indexer: "ix", Status: status}
			if _, err := db.DB.NewInsert().Model(d).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// TV: a series airing an episode in three days (and one in ten),
		// and a download for it.
		tvRoot := &library.RootFolder{Path: t.TempDir(), MediaType: library.Series, DefaultProfileID: 1, CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(tvRoot).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		show := &library.Title{MediaType: library.Series, TMDBID: 8000, Title: "Show", SortTitle: "show", Year: 2020, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		all := library.AllEpisodes
		sv := &library.Version{TitleID: show.ID, ProfileID: 1, RootFolderID: tvRoot.ID, Monitored: true, Upgrades: true, MonitorFrom: &all, CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(sv).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		for n, air := range []string{day(3), day(10)} {
			e := &library.Episode{TitleID: show.ID, TMDBID: 8100 + n, Season: ptr(2), Number: ptr(n + 1), Name: fmt.Sprint("Part ", n+1), AirDate: air}
			if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
		tvd := &download.Download{CreatedAt: now, TitleID: &show.ID, VersionID: &sv.ID, Hash: strings.Repeat("e", 40),
			Category: settings.TVCategory, ReleaseTitle: "Show.S01.1080p", Indexer: "ix", Status: download.StatusDownloading}
		if _, err := db.DB.NewInsert().Model(tvd).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		// Indexers: one enabled and failing, one enabled and untried, one
		// disabled (a disabled one isn't counted at all).
		for _, ix := range []struct {
			name     string
			enabled  bool
			failures int
		}{{"Broken", true, 3}, {"Fine", true, 0}, {"Off", false, 5}} {
			ixm := &indexer.Indexer{Name: ix.name, Kind: "knaben", Enabled: ix.enabled, Settings: "{}", Categories: "[]",
				Failures: ix.failures, LastError: "boom", CreatedAt: now, UpdatedAt: now}
			if _, err := db.DB.NewInsert().Model(ixm).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// Grabs: two today, one yesterday-ish (before local midnight).
		midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
		for _, at := range []time.Time{midnight.Add(-time.Minute), midnight, midnight} {
			if err := a.history.Add(ctx, &grab.Event{Event: grab.EventGrabbed, CreatedAt: at.UTC()}, grab.EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.settings.SaveGrabbing(ctx, settings.Grabbing{Mode: settings.GrabDryRun, QueueLimit: 20, MinSeeders: 1}); err != nil {
			t.Fatal(err)
		}

		var got struct {
			Queue struct {
				Movies, TV, NeedsAttention, Limit int
				Full                              bool
			}
			UpcomingEpisodes []struct {
				TitleID           int64
				Title, Name, Date string
				Season, Episode   int
			}
			Upcoming []struct {
				TitleID    int64
				Title      string
				Year       int
				Kind, Date string
			}
			Indexers struct {
				Ok, Total int
				Failing   []string
			}
			Grabs struct {
				Today, Limit int
				Mode         string
			}
		}
		a.get("/api/v1/summary", &got)
		if got.Queue.Movies != 2 || got.Queue.TV != 1 || got.Queue.NeedsAttention != 1 {
			t.Errorf("queue = %+v", got.Queue)
		}
		if e := got.UpcomingEpisodes; len(e) != 1 || e[0].Title != "Show" || e[0].Season != 2 || e[0].Episode != 1 ||
			e[0].Name != "Part 1" || e[0].Date != day(3) || e[0].TitleID != show.ID {
			t.Errorf("upcoming episodes = %+v", e)
		}
		if len(got.Upcoming) != 2 || got.Upcoming[0].Title != "Cinema" || got.Upcoming[0].Kind != "in cinemas" ||
			got.Upcoming[0].Date != day(0) || got.Upcoming[1].Title != "Soon" || got.Upcoming[1].Kind != "digital" ||
			got.Upcoming[1].Date != day(2) || got.Upcoming[1].Year != 2026 || got.Upcoming[1].TitleID != firstTitle {
			t.Errorf("upcoming = %+v", got.Upcoming)
		}
		if got.Indexers.Total != 2 || got.Indexers.Ok != 1 || len(got.Indexers.Failing) != 1 || got.Indexers.Failing[0] != "Broken" {
			t.Errorf("indexers = %+v", got.Indexers)
		}
		// There is no daily limit any more: grabs.limit stays 0, and the
		// queue carries the limit. A dry run is never "full".
		if got.Grabs.Today != 2 || got.Grabs.Limit != 0 || got.Grabs.Mode != "dryrun" {
			t.Errorf("grabs = %+v", got.Grabs)
		}
		if got.Queue.Limit != 20 || got.Queue.Full {
			t.Errorf("queue limit = %+v", got.Queue)
		}
		// Grabbing On with the queue (3 downloads) at the limit: full; one
		// place more: not.
		for _, c := range []struct {
			limit int
			full  bool
		}{{3, true}, {2, true}, {4, false}, {0, false}} {
			if _, err := a.settings.SaveGrabbing(ctx, settings.Grabbing{Mode: settings.GrabOn, QueueLimit: c.limit, MinSeeders: 1}); err != nil {
				t.Fatal(err)
			}
			a.get("/api/v1/summary", &got)
			if got.Queue.Limit != c.limit || got.Queue.Full != c.full || got.Grabs.Limit != 0 {
				t.Errorf("limit %d: queue = %+v, grabs = %+v; want full = %v", c.limit, got.Queue, got.Grabs, c.full)
			}
		}
	})
}
