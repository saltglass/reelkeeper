package library

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// Seerr's TV request, as the Sonarr-compatible API passes it on: find the
// series by its TVDB ID, add it unmonitored, then monitor the requested
// seasons (and new episodes) in one go. The profile's list then shows it
// with exactly those episodes wanted; removing it keeps the files.
func TestSeriesForSonarrCompat(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		f.svc.now = func() time.Time { return time.Date(2020, 3, 31, 12, 0, 0, 0, time.UTC) }

		// TVDB → TMDB: from TMDB first, from the library once it's there.
		if id, err := f.svc.SeriesTMDBID(ctx, "tvdb", "362829"); err != nil || id != 94951 {
			t.Fatalf("tvdb lookup = %d, %v", id, err)
		}
		if _, err := f.svc.SeriesTMDBID(ctx, "tvdb", "999"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown tvdb id: %v", err)
		}

		// A preview of a series not in the library comes from TMDB.
		p, err := f.svc.SeriesPreview(ctx, 1437)
		if err != nil || p.Title.ID != 0 || p.Title.TVDBID != 78874 || len(p.Episodes) != 14 || *p.Episodes[0].Season != 0 {
			t.Fatalf("preview = %+v, %v", p, err)
		}

		d, err := f.svc.AddSeries(ctx, 94951, 1, f.tv.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		vid := d.Versions[0].ID
		calls := f.tmdb.callCount()
		if id, err := f.svc.SeriesTMDBID(ctx, "tvdb", "362829"); err != nil || id != 94951 || f.tmdb.callCount() != calls {
			t.Errorf("library tvdb lookup = %d, %v (TMDB calls %d → %d)", id, err, calls, f.tmdb.callCount())
		}
		on, off := true, false
		if err := f.svc.ApplySeriesMonitoring(ctx, vid, SeriesMonitoring{
			Monitored: &on, Seasons: map[int]bool{1: true, 2: false}, NewEpisodes: &on,
		}); err != nil {
			t.Fatal(err)
		}
		list, err := f.svc.ProfileSeries(ctx, 1, ProfileSeriesFilter{TVDBID: 362829})
		if err != nil || len(list) != 1 {
			t.Fatalf("profile series = %+v, %v", list, err)
		}
		ps := list[0]
		perSeason := map[int]int{}
		for _, e := range ps.Episodes {
			if ps.Monitored[e.ID] {
				perSeason[*e.Season]++
			}
		}
		// Season 1 requested, season 2 not, seasons 3 and 4 air later (new).
		if !ps.Version.Monitored || perSeason[1] != 9 || perSeason[2] != 0 || perSeason[3] != 10 || perSeason[4] != 10 {
			t.Errorf("monitored per season = %v (version monitored %v)", perSeason, ps.Version.Monitored)
		}
		if len(ps.Wanted) != 9 || ps.Root.ID != f.tv.ID {
			t.Errorf("wanted %d, root %+v", len(ps.Wanted), ps.Root)
		}
		// Other filters and profiles.
		if l, _ := f.svc.ProfileSeries(ctx, 1, ProfileSeriesFilter{VersionID: vid}); len(l) != 1 {
			t.Errorf("by version: %d", len(l))
		}
		if l, _ := f.svc.ProfileSeries(ctx, 2, ProfileSeriesFilter{}); len(l) != 0 {
			t.Errorf("another profile sees %d", len(l))
		}
		// New items off: only the season choices are left.
		if err := f.svc.ApplySeriesMonitoring(ctx, vid, SeriesMonitoring{NewEpisodes: &off}); err != nil {
			t.Fatal(err)
		}
		list, _ = f.svc.ProfileSeries(ctx, 1, ProfileSeriesFilter{})
		if n := len(list[0].Monitored); n != 9 {
			t.Errorf("without new episodes, %d monitored, want 9", n)
		}

		if _, err := f.svc.DeleteSeriesVersion(ctx, d.ID, vid); err != nil {
			t.Fatal(err)
		}
		if l, _ := f.svc.ProfileSeries(ctx, 1, ProfileSeriesFilter{}); len(l) != 0 {
			t.Errorf("after delete: %d", len(l))
		}
		if err := f.svc.ApplySeriesMonitoring(ctx, vid, SeriesMonitoring{Monitored: &on}); !errors.Is(err, ErrNotFound) {
			t.Errorf("monitoring a deleted version: %v", err)
		}
	})
}
