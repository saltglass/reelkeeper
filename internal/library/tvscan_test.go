package library

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/parser"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// addTV gives the fake TMDB a few series in the shapes real ones take.
func addTV(f *fakeTMDB) {
	f.series = map[int]*metadata.Series{}
	f.tvdb = map[int]int{}
	f.groups = map[string]*metadata.EpisodeGroup{}
	season := func(n int, names ...string) metadata.Season {
		s := metadata.Season{Number: n, Name: seasonName(n)}
		for i, name := range names {
			s.Episodes = append(s.Episodes, metadata.Episode{ID: 100000*n + 1000*len(f.series) + i + 1, Season: n, Number: i + 1, Name: name,
				AirDate: fmt.Sprintf("2020-%02d-%02d", min(n, 12)%12+1, i%28+1)})
		}
		return s
	}
	add := func(id, tvdb int, name, first string, seasons ...metadata.Season) *metadata.Series {
		s := &metadata.Series{ID: id, TVDBID: tvdb, Name: name, OriginalName: name, FirstAirDate: first, Seasons: seasons,
			Type: "Scripted", Networks: []string{"Net"}, PosterPath: "/p.jpg"}
		f.series[id] = s
		if tvdb != 0 {
			f.tvdb[tvdb] = id
		}
		return s
	}
	numbered := func(n, count int) metadata.Season {
		names := make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("Quest %d-%d", n, i+1)
		}
		return season(n, names...)
	}

	// Firefly: TMDB airs it in broadcast order; files usually follow the
	// intended order, which TMDB has as an episode group.
	ff := add(1437, 78874, "Firefly", "2002-09-20",
		season(0, "Trash", "The Message", "Heart of Gold"),
		season(1, "The Train Job", "Bushwhacked", "Our Mrs. Reynolds", "Jaynestown", "Out of Gas",
			"Shindig", "Safe", "Ariel", "War Stories", "Objects in Space", "Serenity"))
	ff.EpisodeGroups = []metadata.EpisodeGroupInfo{{ID: "intended", Name: "Intended Order", Type: 2}}
	ids := map[string]int{}
	for _, s := range ff.Seasons {
		for _, e := range s.Episodes {
			ids[e.Name] = e.ID
		}
	}
	g := &metadata.EpisodeGroup{ID: "intended", Name: "Intended Order", Type: 2}
	for i, name := range []string{"Serenity", "The Train Job", "Bushwhacked", "Shindig", "Safe", "Our Mrs. Reynolds",
		"Jaynestown", "Out of Gas", "Ariel", "War Stories", "Trash", "The Message", "Heart of Gold", "Objects in Space"} {
		g.Episodes = append(g.Episodes, metadata.GroupEpisode{TMDBID: ids[name], Season: 1, Number: i + 1})
	}
	f.groups["intended"] = g

	mq := numbered(1, 9)
	mq.Episodes[0].Name = "Pilot"
	add(94951, 362829, "Mythic Quest", "2020-02-07", mq, numbered(2, 9), numbered(3, 10), numbered(4, 10))
	add(1044, 0, "Planet Earth", "2006-03-05", season(1, "From Pole to Pole", "Mountains", "Fresh Water", "Caves",
		"Deserts", "Ice Worlds", "Great Plains", "Jungles", "Shallow Seas", "Seasonal Forests", "Ocean Deep"))
	add(57243, 78804, "Doctor Who", "2005-03-26", season(1, "Rose", "The End of the World"))
	add(121, 76107, "Doctor Who", "1963-11-23", season(1, "An Unearthly Child"))
	daily := add(500, 0, "The Daily Thing", "2010-01-04", season(2024, "Guest A", "Guest Name"))
	daily.Type = "Talk Show"
	daily.Seasons[0].Episodes[0].AirDate = "2024-03-14"
	daily.Seasons[0].Episodes[1].AirDate = "2024-03-15"
}

func (f *fakeTMDB) SearchTV(_ context.Context, q string, year int) ([]metadata.SeriesResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failKey {
		return nil, metadata.ErrBadKey
	}
	var out []metadata.SeriesResult
	for _, s := range f.series {
		if !strings.Contains(normalize(s.Name), normalize(q)) && !strings.Contains(normalize(q), normalize(s.Name)) {
			continue
		}
		if year != 0 && s.Year() != year {
			continue
		}
		out = append(out, metadata.SeriesResult{ID: s.ID, Name: s.Name, OriginalName: s.Name, FirstAirDate: s.FirstAirDate})
	}
	return out, nil
}

func (f *fakeTMDB) Series(_ context.Context, id int) (*metadata.Series, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if s, ok := f.series[id]; ok {
		return s, nil
	}
	return nil, metadata.ErrNotFound
}

func (f *fakeTMDB) FindSeriesByTVDB(_ context.Context, tvdb int) (*metadata.SeriesResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if id, ok := f.tvdb[tvdb]; ok {
		s := f.series[id]
		return &metadata.SeriesResult{ID: s.ID, Name: s.Name, FirstAirDate: s.FirstAirDate}, nil
	}
	return nil, metadata.ErrNotFound
}

func (f *fakeTMDB) FindSeriesByIMDb(context.Context, string) (*metadata.SeriesResult, error) {
	return nil, metadata.ErrNotFound
}

func (f *fakeTMDB) EpisodeGroup(_ context.Context, id string) (*metadata.EpisodeGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.groups[id]; ok {
		return g, nil
	}
	return nil, metadata.ErrNotFound
}

// buildShows creates a small TV library in the styles found in real ones.
func buildShows(t *testing.T, dir string) {
	t.Helper()
	for _, f := range []string{
		"Firefly (2002)/Season 1/Firefly (2002) - S01E01 - Serenity (1080p BluRay x265 grp).mkv",
		"Firefly (2002)/Season 1/Firefly (2002) - S01E02 - The Train Job (1080p BluRay x265 grp).mkv",
		"Firefly (2002)/Firefly (2002) - S01E14 - Objects in Space (1080p BluRay x265 grp).mkv",
		"Firefly (2002)/Featurettes/Deleted Scenes/S01E01 Serenity - Scene 1.mkv", // an extra, not an episode
		"Mythic Quest (2020) {tvdb-362829}/Mythic Quest (2020) - S01E01 - Pilot [WEBDL-1080p]-GRP.mkv",
		"Mythic Quest (2020) {tvdb-362829}/Mythic Quest (2020) - S02E01 - Quest 2-1 [WEBDL-1080p]-GRP.mkv",
		"Mythic Quest/Season 1/Mythic Quest (2020) - S01E01 - Pilot (1080p ATVP WEB-DL x265 grp).mkv",
		"Mythic Quest/Season 1/Mythic Quest (2020) - S01E01 - Pilot (1080p ATVP WEB-DL x265 grp)-thumb.jpg",
		"Mythic Quest/season01-poster.jpg",
		"Planet Earth (2006)/Planet Earth 07 Great Plains (1080p x265 grp).mkv",
		"Doctor Who (2005)/Doctor Who (2005) - S01E01 - Rose.mkv",
		"The Daily Thing/The.Daily.Thing.2024.03.15.Guest.Name.720p.WEB.h264-GRP.mkv",
		"Never Heard Of It/Never.Heard.Of.It.S01E01.720p.mkv",
	} {
		touch(t, dir, f, []byte("x"))
	}
}

type tvFixture struct {
	*fixture
	tv *RootFolder
}

func newTVFixture(t *testing.T, db *store.Store) *tvFixture {
	t.Helper()
	f := newFixture(t, db)
	dir := t.TempDir()
	buildShows(t, dir)
	root, err := f.svc.AddRootFolder(context.Background(), RootInput{Path: dir, MediaType: Series, DefaultProfileID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &tvFixture{fixture: f, tv: root}
}

func (f *tvFixture) seriesByTMDB(t *testing.T, id int) *Title {
	t.Helper()
	s := new(Title)
	if err := f.db.DB.NewSelect().Model(s).Where("media_type = ? AND tmdb_id = ?", Series, id).Scan(context.Background()); err != nil {
		t.Fatalf("series %d not in library: %v", id, err)
	}
	return s
}

// fileNotes maps each of a series' file names to its note and to the
// names of the episodes it's linked to.
func (f *tvFixture) fileNotes(t *testing.T, titleID int64) map[string]string {
	t.Helper()
	ctx := context.Background()
	var files []MediaFile
	if err := f.db.DB.NewSelect().Model(&files).
		Where("version_id IN (SELECT id FROM versions WHERE title_id = ?)", titleID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, mf := range files {
		var names []string
		err := f.db.DB.NewSelect().Model((*Episode)(nil)).Column("name").
			Where("id IN (SELECT episode_id FROM episode_files WHERE media_file_id = ?)", mf.ID).
			OrderExpr("number").Scan(ctx, &names)
		if err != nil {
			t.Fatal(err)
		}
		out[mf.Path] = strings.Join(names, "+") + " | " + mf.MatchNote
	}
	return out
}

func TestScanTVFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		res, err := f.svc.Scan(ctx, f.tv.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Seven series folders; "Never Heard Of It" goes to review.
		if res.Units != 7 || res.Added != 6 || res.Review != 1 || !strings.HasPrefix(res.Summary(), "7 series found") {
			t.Fatalf("scan = %+v (%s)", res, res.Summary())
		}

		// Mythic Quest: two folders, one series, two copies of the pilot.
		mq := f.seriesByTMDB(t, 94951)
		notes := f.fileNotes(t, mq.ID)
		if len(notes) != 3 {
			t.Errorf("Mythic Quest files = %v", notes)
		}
		for path, n := range notes {
			if strings.Contains(path, "S01E01") && n != "Pilot | " {
				t.Errorf("%s = %q", path, n)
			}
		}
		if n, _ := db.DB.NewSelect().Model((*Version)(nil)).Where("title_id = ?", mq.ID).Count(ctx); n != 1 {
			t.Errorf("Mythic Quest versions = %d, want 1", n)
		}

		// Planet Earth: "07 Great Plains" with no SxxEyy.
		pe := f.fileNotes(t, f.seriesByTMDB(t, 1044).ID)
		if pe["Planet Earth (2006)/Planet Earth 07 Great Plains (1080p x265 grp).mkv"] != "Great Plains | " {
			t.Errorf("Planet Earth = %v", pe)
		}
		// Doctor Who (2005), not (1963).
		f.seriesByTMDB(t, 57243)
		// The daily show, by air date.
		daily := f.seriesByTMDB(t, 500)
		if daily.SeriesType != "daily" {
			t.Errorf("series type = %q", daily.SeriesType)
		}
		for _, n := range f.fileNotes(t, daily.ID) {
			if n != "Guest Name | " {
				t.Errorf("daily = %q", n)
			}
		}

		// Firefly's files follow the intended order: in TMDB's aired order
		// they're flagged, and the deleted scene isn't an episode at all.
		ff := f.seriesByTMDB(t, 1437)
		notes = f.fileNotes(t, ff.ID)
		if len(notes) != 3 {
			t.Fatalf("Firefly files = %v", notes)
		}
		for path, n := range notes {
			switch {
			case strings.Contains(path, "S01E01"):
				if !strings.Contains(n, "which is S01E11 in TMDB's aired order") {
					t.Errorf("%s = %q", path, n)
				}
			case strings.Contains(path, "S01E14"):
				if !strings.Contains(n, "TMDB's aired order has no S01E14") {
					t.Errorf("%s = %q", path, n)
				}
			}
		}

		// Unchanged files aren't looked at again.
		before := f.tmdb.callCount()
		res, err = f.svc.Scan(ctx, f.tv.ID, nil)
		if err != nil || res.Unchanged != 6 || res.Review != 1 || f.tmdb.callCount() != before {
			t.Errorf("rescan = %+v, %v, %d new TMDB calls", res, err, f.tmdb.callCount()-before)
		}
	})
}

func TestEpisodeOrders(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		ff := f.seriesByTMDB(t, 1437)

		// Comparing orders changes nothing and shows which one fits.
		opts, err := f.svc.EpisodeOrders(ctx, ff.ID)
		if err != nil || len(opts) != 2 {
			t.Fatalf("orders = %+v, %v", opts, err)
		}
		if !opts[0].Current || opts[0].Fits != 0 || opts[0].Problems != 3 {
			t.Errorf("aired = %+v", opts[0])
		}
		if opts[1].Name != "Intended Order" || opts[1].Fits != 3 || opts[1].Problems != 0 || opts[1].Episodes != 14 {
			t.Errorf("intended = %+v", opts[1])
		}

		// Switching re-links the files; nothing on disk changes.
		if err := f.svc.SetEpisodeOrder(ctx, ff.ID, "intended"); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"Firefly (2002)/Season 1/Firefly (2002) - S01E01 - Serenity (1080p BluRay x265 grp).mkv":      "Serenity | ",
			"Firefly (2002)/Season 1/Firefly (2002) - S01E02 - The Train Job (1080p BluRay x265 grp).mkv": "The Train Job | ",
			"Firefly (2002)/Firefly (2002) - S01E14 - Objects in Space (1080p BluRay x265 grp).mkv":       "Objects in Space | ",
		}
		got := f.fileNotes(t, ff.ID)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}

		// A refresh keeps the chosen order.
		ff = f.seriesByTMDB(t, 1437)
		if err := f.svc.refreshSeries(ctx, ff); err != nil {
			t.Fatal(err)
		}
		if ff.EpisodeOrder != "intended" || f.fileNotes(t, ff.ID)[strings.Join(nil, "")+"Firefly (2002)/Firefly (2002) - S01E14 - Objects in Space (1080p BluRay x265 grp).mkv"] != "Objects in Space | " {
			t.Errorf("after refresh: %q %v", ff.EpisodeOrder, f.fileNotes(t, ff.ID))
		}

		// Back to aired order.
		if err := f.svc.SetEpisodeOrder(ctx, ff.ID, ""); err != nil {
			t.Fatal(err)
		}
		if n := f.fileNotes(t, ff.ID)["Firefly (2002)/Season 1/Firefly (2002) - S01E02 - The Train Job (1080p BluRay x265 grp).mkv"]; !strings.Contains(n, "Bushwhacked") {
			t.Errorf("aired again = %q", n)
		}
		if err := f.svc.SetEpisodeOrder(ctx, ff.ID, "no-such-order"); err == nil {
			t.Error("an unknown order should be refused")
		}
	})
}

func TestResolveSeriesReview(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		items, _ := f.svc.ReviewItems(ctx)
		if len(items) != 1 || items[0].Path != "Never Heard Of It" {
			t.Fatalf("review = %+v", items)
		}
		// Say it's Doctor Who (1963): the episode lands on its S01E01.
		title, err := f.svc.ResolveReview(ctx, items[0].ID, 121)
		if err != nil || title.MediaType != Series {
			t.Fatalf("resolve = %+v, %v", title, err)
		}
		for _, n := range f.fileNotes(t, title.ID) {
			if n != "An Unearthly Child | " {
				t.Errorf("placed = %q", n)
			}
		}
	})
}

func TestClaimFor(t *testing.T) {
	cases := []struct {
		rel, series string
		want        EpisodeClaim
	}{
		{"Show/Season 2/Show - S02E03 - Title.mkv", "Show", EpisodeClaim{Season: 2, Episodes: []int{3}, Title: "Title"}},
		{"Show/Season 2/Show E05.mkv", "Show", EpisodeClaim{Season: 2, Episodes: []int{5}}},
		{"Show/Specials/Show E01.mkv", "Show", EpisodeClaim{Season: 0, Episodes: []int{1}}},
		{"Planet Earth (2006)/Planet Earth 07 Great Plains (1080p x265 grp).mkv", "Planet Earth", EpisodeClaim{Season: -1, Episodes: []int{7}, Title: "Great Plains"}},
		{"Show/Season 3/Making Of.mkv", "Show", EpisodeClaim{Season: 3}},
		{"Show/Season 1/Show.S02E01.mkv", "Show", EpisodeClaim{Season: 2, Episodes: []int{1}}}, // the name wins
	}
	for _, c := range cases {
		got := claimFor(c.rel, parser.Parse(c.rel[strings.LastIndex(c.rel, "/")+1:]), c.series)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s = %+v, want %+v", c.rel, got, c.want)
		}
	}
}
