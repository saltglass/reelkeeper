package library

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestUpcomingMovies(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
		n := 0
		add := func(name, theatrical, digital, physical string, monitored, withFile bool) int64 {
			n++
			title := &Title{MediaType: Movie, TMDBID: 9000 + n, Title: name, SortTitle: name, Year: 2026,
				TheatricalRelease: theatrical, DigitalRelease: digital, PhysicalRelease: physical,
				Status: "Released", CreatedAt: now, Genres: "[]", AltTitles: "[]"}
			if _, err := db.DB.NewInsert().Model(title).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			v := &Version{TitleID: title.ID, ProfileID: 1, RootFolderID: f.hd.ID, Monitored: monitored, CreatedAt: now}
			if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if withFile {
				mf := &MediaFile{VersionID: v.ID, RootFolderID: f.hd.ID, Path: fmt.Sprintf("%s/%s.mkv", name, name), AddedAt: now, ModifiedAt: now, ReleaseInfo: "{}", MediaInfo: "{}", EpisodeClaim: "{}"}
				if _, err := db.DB.NewInsert().Model(mf).Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			return title.ID
		}
		// Window: 2026-10-10 to 2026-10-17, inclusive.
		bTitle := add("Bravo", "", "2026-10-12", "", true, false)
		_ = add("Alpha", "2026-10-12", "", "", true, false)                // same date as Bravo: Alpha first
		early := add("Early", "2026-10-01", "2026-10-17", "", true, false) // theatrical passed; digital on the last day
		both := add("Both", "2026-10-15", "2026-10-11", "2026-10-16", true, false)
		add("Today", "", "", "2026-10-10", true, false)
		add("TooLate", "", "2026-10-18", "", true, false)
		add("Past", "", "2026-10-09", "", true, false)
		add("HasFile", "", "2026-10-12", "", true, true)
		add("Unmonitored", "", "2026-10-12", "", false, false)

		got, err := f.svc.UpcomingMovies(ctx, "2026-10-10", "2026-10-17", 10)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, u := range got {
			names = append(names, u.Title+" "+u.Kind+" "+u.Date)
		}
		want := []string{
			"Today physical 2026-10-10",
			"Both digital 2026-10-11",
			"Alpha in cinemas 2026-10-12",
			"Bravo digital 2026-10-12",
			"Early digital 2026-10-17",
		}
		if fmt.Sprint(names) != fmt.Sprint(want) {
			t.Errorf("got %q\nwant %q", names, want)
		}
		if got[1].TitleID != both || got[3].TitleID != bTitle || got[4].TitleID != early || got[3].Year != 2026 {
			t.Errorf("ids/years wrong: %+v", got)
		}

		// The cap cuts the list.
		if got, _ := f.svc.UpcomingMovies(ctx, "2026-10-10", "2026-10-17", 2); len(got) != 2 || got[1].Title != "Both" {
			t.Errorf("limit 2: %+v", got)
		}
	})
}

func TestUpcomingEpisodes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		f.svc.now = func() time.Time { return time.Date(2020, 2, 1, 12, 0, 0, 0, time.UTC) }
		// Both series' first seasons air one episode a day from 2020-02-01.
		mq, err := f.svc.AddSeries(ctx, 94951, 1, f.tv.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.AddSeries(ctx, 1437, 1, f.tv.ID, true); err != nil {
			t.Fatal(err)
		}
		// Mythic Quest's episode 4 isn't wanted.
		svs, _ := f.svc.SeriesVersions(ctx, mq.Versions[0].ID)
		for _, e := range svs[0].Episodes {
			if *e.Season == 1 && *e.Number == 4 {
				if err := f.svc.SetEpisodesMonitored(ctx, mq.Versions[0].ID, []int64{e.ID}, false); err != nil {
					t.Fatal(err)
				}
			}
		}
		got, err := f.svc.UpcomingEpisodes(ctx, "2020-02-03", "2020-02-04", 10)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, u := range got {
			names = append(names, fmt.Sprintf("%s %s S%02dE%02d", u.Date, u.Title, u.Season, u.Number))
		}
		want := []string{"2020-02-03 Firefly S01E03", "2020-02-03 Mythic Quest S01E03", "2020-02-04 Firefly S01E04"}
		if fmt.Sprint(names) != fmt.Sprint(want) {
			t.Errorf("got %q\nwant %q", names, want)
		}
		if got, _ := f.svc.UpcomingEpisodes(ctx, "2020-02-03", "2020-02-04", 1); len(got) != 1 {
			t.Errorf("limit 1: %+v", got)
		}
	})
}
