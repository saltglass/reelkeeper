package library

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

func TestSeriesListAndDetail(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		list, err := f.svc.SeriesList(ctx)
		if err != nil || len(list) != 5 {
			t.Fatalf("list = %d, %v", len(list), err)
		}
		by := map[string]SeriesView{}
		for _, sv := range list {
			by[sv.Title.Title+sv.ReleaseDate[:4]] = sv
		}
		mq := by["Mythic Quest2020"]
		if mq.Seasons != 4 || mq.Aired != 38 || len(mq.Versions) != 1 || mq.Versions[0].EpisodesWithFiles != 2 || mq.Problems != 0 {
			t.Errorf("Mythic Quest = seasons %d aired %d versions %d with files %d problems %d",
				mq.Seasons, mq.Aired, len(mq.Versions), mq.Versions[0].EpisodesWithFiles, mq.Problems)
		}
		if ff := by["Firefly2002"]; ff.Problems != 3 {
			t.Errorf("Firefly problems = %d", ff.Problems)
		}
		// Movies stay on their own page.
		if movies, _ := f.svc.Movies(ctx); len(movies) != 0 {
			t.Errorf("series leaked into movies: %d", len(movies))
		}

		// The page: regular seasons first, Specials last.
		ff := f.seriesByTMDB(t, 1437)
		d, err := f.svc.SeriesDetailByID(ctx, ff.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.SeasonList) != 2 || d.SeasonList[0].Number != 1 || len(d.SeasonList[0].Episodes) != 11 || d.SeasonList[1].Name != "Specials" {
			t.Fatalf("seasons = %+v", d.SeasonList)
		}
		if e := d.SeasonList[0].Episodes[0]; e.Name != "The Train Job" || len(e.FileIDs) != 1 {
			t.Errorf("S01E01 = %+v", e)
		}
		// In the intended order the three unaired episodes join season 1.
		if err := f.svc.SetEpisodeOrder(ctx, ff.ID, "intended"); err != nil {
			t.Fatal(err)
		}
		d, _ = f.svc.SeriesDetailByID(ctx, ff.ID)
		if len(d.SeasonList) != 1 || len(d.SeasonList[0].Episodes) != 14 || len(d.Unordered) != 0 || d.Problems != 0 {
			t.Errorf("intended: %d seasons, %d unordered, %d problems", len(d.SeasonList), len(d.Unordered), d.Problems)
		}
		if _, err := f.svc.SeriesDetailByID(ctx, 99999); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown series: %v", err)
		}
	})
}

func TestAddAndLookupSeries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		res, err := f.svc.LookupSeries(ctx, "Doctor Who 2005")
		if err != nil || len(res) != 1 || res[0].SeriesID == 0 {
			t.Errorf("lookup = %+v, %v", res, err)
		}

		d, err := f.svc.AddSeries(ctx, 121, 1, f.tv.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if d.Title.Title != "Doctor Who" || len(d.Versions) != 1 || d.Versions[0].Folder != "Doctor Who (1963) {tvdb-76107}" {
			t.Errorf("added = %+v", d.Versions)
		}
		if _, err := f.svc.AddSeries(ctx, 121, 1, f.tv.ID, true); !errors.Is(err, ErrInUse) {
			t.Errorf("twice: %v", err)
		}
		var errs validation.Errors
		if _, err := f.svc.AddSeries(ctx, 1044, 2, f.hd.ID, true); !errors.As(err, &errs) ||
			errs["profileId"] == "" || errs["rootFolderId"] != "Choose a TV media folder." {
			t.Errorf("movie folder and movie-only profile: %v", err)
		}
	})
}

func TestSeriesRenamePlan(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		plan, err := f.svc.RenamePlan(ctx, f.seriesByTMDB(t, 94951).ID)
		if err != nil || len(plan) != 1 {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		// Only the copy whose name differs from the template is listed.
		if len(plan[0].Files) != 1 || plan[0].Files[0].To != "Mythic Quest/Season 1/Mythic Quest (2020) - S01E01 - Pilot [WEBDL-1080p][h265]-grp.mkv" {
			t.Errorf("files = %+v", plan[0].Files)
		}
		// Firefly's files are in doubt (aired order): never renamed.
		plan, _ = f.svc.RenamePlan(ctx, f.seriesByTMDB(t, 1437).ID)
		for _, fr := range plan[0].Files {
			if !strings.Contains(fr.Problem, "isn't sure which episode") || fr.To != fr.From {
				t.Errorf("doubtful file = %+v", fr)
			}
		}
	})
}

func TestRefreshStaleIncludesAiringSeries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		// A day later: only the series still on the air are due.
		mq := f.seriesByTMDB(t, 94951)
		if _, err := db.DB.NewUpdate().Model((*Title)(nil)).Set("status = ?", "Returning Series").Where("id = ?", mq.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		f.svc.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
		n, err := f.svc.RefreshStale(ctx, 14*24*time.Hour, 100, nil)
		if err != nil || n != 1 {
			t.Errorf("refreshed %d, %v; want just the airing series", n, err)
		}
	})
}
