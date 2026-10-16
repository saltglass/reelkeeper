package library

import (
	"context"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// A series version wants monitored, aired episodes it has no file for. The
// date rule covers new episodes; per-episode choices override it both
// ways; specials need a choice; an episode with a file is never wanted (no
// TV upgrades).
func TestSeriesWantedEpisodes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.svc.now = func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
		now := f.svc.now()
		show := &Title{MediaType: Series, TMDBID: 1399, Title: "Show", Year: 2020, Genres: "[]", AltTitles: "[]", CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		ep := func(season, number int, air string) *Episode {
			e := &Episode{TitleID: show.ID, TMDBID: season*100 + number, AiredSeason: season, AiredNumber: number,
				Season: ptr(season), Number: ptr(number), AirDate: air}
			if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			return e
		}
		old := ep(1, 1, "2020-01-01")     // before the date rule
		recent := ep(2, 1, "2026-10-05")  // after it, aired
		recent2 := ep(2, 2, "2026-10-09") // after it, aired, but has a file
		future := ep(2, 3, "2026-12-01")  // not aired yet
		special := ep(0, 1, "2026-10-06") // a special: needs a choice
		from := "2026-10-01"
		v := &Version{TitleID: show.ID, ProfileID: 1, RootFolderID: f.hd.ID, Folder: "Show (2020)", Monitored: true, Upgrades: true,
			CreatedAt: now, MonitorFrom: &from}
		if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		file := &MediaFile{VersionID: v.ID, RootFolderID: f.hd.ID, Path: "Show (2020)/Season 02/x.mkv", Size: 1,
			AddedAt: now, MediaInfo: "{}", ReleaseInfo: "{}", EpisodeClaim: "{}"}
		if _, err := db.DB.NewInsert().Model(file).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.NewInsert().Model(&EpisodeFile{EpisodeID: recent2.ID, MediaFileID: file.ID}).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		wanted := func() []int64 {
			t.Helper()
			svs, err := f.svc.SeriesVersions(ctx)
			if err != nil || len(svs) != 1 {
				t.Fatalf("versions = %+v, %v", svs, err)
			}
			var ids []int64
			for _, e := range svs[0].Wanted {
				ids = append(ids, e.ID)
			}
			return ids
		}
		same := func(got []int64, want ...*Episode) bool {
			if len(got) != len(want) {
				return false
			}
			for i := range got {
				if got[i] != want[i].ID {
					return false
				}
			}
			return true
		}
		if got := wanted(); !same(got, recent) {
			t.Errorf("by date only: %v, want just S02E01", got)
		}
		// Season 1 turned on, the special too, the recent episode off.
		if err := f.svc.SetSeasonMonitored(ctx, v.ID, 1, true); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.SetEpisodesMonitored(ctx, v.ID, []int64{special.ID}, true); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.SetEpisodesMonitored(ctx, v.ID, []int64{recent.ID}, false); err != nil {
			t.Fatal(err)
		}
		if got := wanted(); !same(got, special, old) {
			t.Errorf("with choices: %v, want the special and S01E01", got)
		}
		// Turning the date rule off leaves only the explicit choices.
		if err := f.svc.SetMonitorFrom(ctx, v.ID, ""); err != nil {
			t.Fatal(err)
		}
		if got := wanted(); !same(got, special, old) {
			t.Errorf("without the date rule: %v", got)
		}
		_ = future
	})
}
