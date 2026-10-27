package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// bulkTitle adds a title with one unmonitored version per profile, straight
// into the database, and returns the title's and versions' ids.
func bulkTitle(t *testing.T, db *store.Store, kind, name string, n, root int64, profiles ...int64) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	title := &library.Title{MediaType: kind, TMDBID: int(9000 + n), Title: name, Year: 2001, Status: "Released",
		CreatedAt: time.Now(), Genres: "[]", AltTitles: "[]"}
	if kind == library.Series {
		title.Status, title.SeriesType = "Ended", "standard"
	}
	if _, err := db.DB.NewInsert().Model(title).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var vids []int64
	for _, p := range profiles {
		v := &library.Version{TitleID: title.ID, ProfileID: p, RootFolderID: root, Folder: fmt.Sprintf("%s %d", name, p),
			Monitored: false, Upgrades: false, CreatedAt: time.Now()}
		if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		vids = append(vids, v.ID)
	}
	return title.ID, vids
}

// versionsOf reads every version's switches.
func versionsOf(t *testing.T, db *store.Store) map[int64]library.Version {
	t.Helper()
	var vs []library.Version
	if err := db.DB.NewSelect().Model(&vs).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := map[int64]library.Version{}
	for _, v := range vs {
		out[v.ID] = v
	}
	return out
}

// searchedVersions are the versions with a search on the task queue.
func searchedVersions(t *testing.T, a *app) []int64 {
	t.Helper()
	var out []int64
	for _, j := range searchJobs(t, a) {
		var args struct {
			VersionID int64 `json:"versionId"`
		}
		if err := json.Unmarshal([]byte(j), &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args.VersionID)
	}
	return out
}

func idList(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprint(id)
	}
	return "[" + strings.Join(s, ",") + "]"
}

func TestBulkEditMovies(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		ctx := context.Background()
		root := addMovieFixture(t, a)
		ps, err := a.library.Profiles(ctx)
		if err != nil || len(ps) < 2 {
			t.Fatalf("profiles: %v %v", ps, err)
		}
		hd, remux := ps[0].ID, ps[1].ID
		// Twelve movies with one version each, and one with two.
		var movies, versions []int64
		for i := range 12 {
			id, vs := bulkTitle(t, db, library.Movie, fmt.Sprintf("Movie %02d", i), int64(i), root, hd)
			movies, versions = append(movies, id), append(versions, vs...)
		}
		both, bothVersions := bulkTitle(t, db, library.Movie, "Two Copies", 50, root, hd, remux)
		show, _ := bulkTitle(t, db, library.Series, "A Show", 60, root, hd)
		all := append(slices.Clone(movies), both)
		before := versionsOf(t, db)
		unchanged := func(what string) {
			t.Helper()
			now := versionsOf(t, db)
			for id, v := range before {
				if now[id].Monitored != v.Monitored || now[id].Upgrades != v.Upgrades {
					t.Fatalf("%s: a refused request changed version %d", what, id)
				}
			}
		}

		// Mistakes change nothing.
		for _, tc := range []struct{ name, body string }{
			{"no titles", `{"titleIds":[],"monitored":true}`},
			{"no change", fmt.Sprintf(`{"titleIds":%s}`, idList(all))},
			{"a series", fmt.Sprintf(`{"titleIds":%s,"monitored":true}`, idList(append(slices.Clone(all), show)))},
			{"no version with the profile", fmt.Sprintf(`{"titleIds":%s,"profileId":%d,"monitored":true}`, idList(movies), remux)},
		} {
			if r := a.do("POST", "/api/v1/movies/bulk", tc.body); r.status != 422 {
				t.Errorf("%s: %d %s", tc.name, r.status, r.raw)
			}
			unchanged(tc.name)
		}
		if r := a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":[%d,999999],"monitored":true}`, both)); r.status != 404 {
			t.Errorf("unknown: %d %s", r.status, r.raw)
		}
		unchanged("unknown")
		if r := a.do("POST", "/api/v1/series/bulk", fmt.Sprintf(`{"titleIds":[%d],"upgrades":true}`, show)); r.status != 422 {
			t.Errorf("upgrades on a series: %d %s", r.status, r.raw)
		}

		// Upgrades on while unmonitored: nothing becomes wanted, no search.
		searched := len(searchedVersions(t, a))
		r := a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":%s,"upgrades":true}`, idList(all)))
		if r.status != 200 || r.body["titles"].(float64) != 13 || r.body["versions"].(float64) != 14 || r.body["searched"].(float64) != 0 {
			t.Fatalf("upgrades on: %d %s", r.status, r.raw)
		}
		for id, v := range versionsOf(t, db) {
			if !v.Upgrades && before[id].TitleID != show {
				t.Errorf("version %d still has upgrades off", id)
			}
		}
		if len(searchedVersions(t, a)) != searched {
			t.Error("searched for unmonitored movies")
		}

		// Monitoring on for twelve movies: the first ten (in the order sent)
		// are searched now, the other two by the hourly search.
		r = a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":%s,"monitored":true}`, idList(movies)))
		if r.status != 200 || r.body["versions"].(float64) != 12 || r.body["searched"].(float64) != 10 || r.body["later"].(float64) != 2 {
			t.Fatalf("monitored on: %d %s", r.status, r.raw)
		}
		got := searchedVersions(t, a)
		if len(got) != searched+10 {
			t.Fatalf("searches queued = %v", got)
		}
		for _, v := range versions[:10] {
			if !slices.Contains(got, v) {
				t.Errorf("version %d (in the first ten) wasn't searched", v)
			}
		}
		for _, v := range versions[10:] {
			if slices.Contains(got, v) {
				t.Errorf("version %d (after the first ten) was searched now", v)
			}
		}

		// Only the Remux copy of the movie with two.
		r = a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":[%d],"profileId":%d,"monitored":true}`, both, remux))
		if r.status != 200 || r.body["versions"].(float64) != 1 || r.body["searched"].(float64) != 1 {
			t.Fatalf("remux only: %d %s", r.status, r.raw)
		}
		now := versionsOf(t, db)
		if now[bothVersions[0]].Monitored || !now[bothVersions[1]].Monitored {
			t.Errorf("remux only changed HD %v, Remux %v", now[bothVersions[0]].Monitored, now[bothVersions[1]].Monitored)
		}

		// Turning things off never searches.
		searched = len(searchedVersions(t, a))
		r = a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":%s,"monitored":false,"upgrades":false}`, idList(all)))
		if r.status != 200 || r.body["versions"].(float64) != 14 || r.body["searched"].(float64) != 0 || r.body["later"].(float64) != 0 {
			t.Fatalf("off: %d %s", r.status, r.raw)
		}
		for id, v := range versionsOf(t, db) {
			if before[id].TitleID != show && (v.Monitored || v.Upgrades) {
				t.Errorf("version %d still on", id)
			}
		}
		if len(searchedVersions(t, a)) != searched {
			t.Error("turning off searched")
		}

		a.signOut()
		if r := a.do("POST", "/api/v1/movies/bulk", fmt.Sprintf(`{"titleIds":[%d],"monitored":true}`, both)); r.status != 401 {
			t.Errorf("signed out: %d", r.status)
		}
	})
}

func TestBulkEditSeries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		ctx := context.Background()
		_, seriesV := wantedFixture(t, a) // Firefly, every aired episode monitored
		var sv library.Version
		if err := db.DB.NewSelect().Model(&sv).Where("id = ?", seriesV).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		movie, _ := bulkTitle(t, db, library.Movie, "A Movie", 70, sv.RootFolderID, sv.ProfileID)
		if r := a.do("POST", "/api/v1/series/bulk", fmt.Sprintf(`{"titleIds":[%d],"monitored":true}`, movie)); r.status != 422 {
			t.Errorf("a movie: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/series/bulk", fmt.Sprintf(`{"titleIds":[%d],"monitor":"some"}`, sv.TitleID)); r.status != 400 && r.status != 422 {
			t.Errorf("bad preset: %d %s", r.status, r.raw)
		}

		// One season turned off by hand, then a preset for the whole show
		// replaces it.
		if r := a.do("POST", fmt.Sprintf("/api/v1/series/%d/versions/%d/monitor", sv.TitleID, seriesV), `{"seasons":[1],"monitored":false}`); r.status != 204 {
			t.Fatalf("season off: %d %s", r.status, r.raw)
		}
		var l wantedList
		a.get("/api/v1/wanted", &l)
		if l.Counts.MissingEpisodes != 0 {
			t.Fatalf("season off: %d still wanted", l.Counts.MissingEpisodes)
		}
		searched := len(searchedVersions(t, a))
		r := a.do("POST", "/api/v1/series/bulk", fmt.Sprintf(`{"titleIds":[%d],"monitor":"all"}`, sv.TitleID))
		if r.status != 200 || r.body["versions"].(float64) != 1 || r.body["searched"].(float64)+r.body["later"].(float64) != 1 {
			t.Fatalf("monitor all: %d %s", r.status, r.raw)
		}
		a.get("/api/v1/wanted", &l)
		if l.Counts.MissingEpisodes != 10 {
			t.Errorf("after the preset %d episodes wanted, want 10", l.Counts.MissingEpisodes)
		}
		if got := searchedVersions(t, a); !slices.Contains(got, seriesV) {
			t.Errorf("searches = %v (was %d); the show wasn't searched", got, searched)
		}

		// Monitoring off: no search, and nothing wanted.
		searched = len(searchedVersions(t, a))
		r = a.do("POST", "/api/v1/series/bulk", fmt.Sprintf(`{"titleIds":[%d],"monitored":false}`, sv.TitleID))
		if r.status != 200 || r.body["searched"].(float64) != 0 {
			t.Fatalf("off: %d %s", r.status, r.raw)
		}
		a.get("/api/v1/wanted", &l)
		if l.Counts.MissingEpisodes != 0 || len(searchedVersions(t, a)) != searched {
			t.Errorf("off: %d wanted, searches %d (was %d)", l.Counts.MissingEpisodes, len(searchedVersions(t, a)), searched)
		}
	})
}
