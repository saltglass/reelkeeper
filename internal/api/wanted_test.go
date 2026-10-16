package api_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

type wantedList struct {
	Missing []struct {
		Kind      string `json:"kind"`
		TitleId   int64  `json:"titleId"`
		VersionId int64  `json:"versionId"`
		Title     string `json:"title"`
		State     string `json:"state"`
		Status    string `json:"status"`
		Season    *int   `json:"season"`
		Episodes  []struct {
			EpisodeId int64 `json:"episodeId"`
			Number    int   `json:"number"`
		} `json:"episodes"`
	} `json:"missing"`
	Upgrades []map[string]any `json:"upgrades"`
	Counts   struct {
		MissingMovies   int `json:"missingMovies"`
		MissingEpisodes int `json:"missingEpisodes"`
		Upgrades        int `json:"upgrades"`
	} `json:"counts"`
}

// wantedFixture makes one missing movie (Dune) and one show with its first
// episode on disk and every other aired episode wanted (Firefly).
func wantedFixture(t *testing.T, a *app) (movieVersion, seriesVersion int64) {
	t.Helper()
	root := addMovieFixture(t, a)
	r := a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d}`, root))
	if r.status != 200 {
		t.Fatalf("add movie: %d %s", r.status, r.raw)
	}
	movieVersion = int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))

	dir := t.TempDir()
	p := filepath.Join(dir, "Firefly (2002)/Season 01/Firefly (2002) - S01E01 - Serenity.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"series","defaultProfileId":1}`)
	if r.status != 200 {
		t.Fatalf("add TV root: %d %s", r.status, r.raw)
	}
	if _, err := a.library.Scan(context.Background(), int64(r.body["id"].(float64)), nil); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	a.get("/api/v1/series", &list)
	if len(list) != 1 {
		t.Fatalf("series: %v", list)
	}
	id := int64(list[0]["id"].(float64))
	seriesVersion = int64(list[0]["versions"].([]any)[0].(map[string]any)["id"].(float64))
	if r := a.do("PATCH", fmt.Sprintf("/api/v1/series/%d/versions/%d", id, seriesVersion), `{"monitor":"all"}`); r.status != 204 {
		t.Fatalf("monitor all: %d %s", r.status, r.raw)
	}
	return movieVersion, seriesVersion
}

func TestWantedAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		movieV, seriesV := wantedFixture(t, a)

		var l wantedList
		a.get("/api/v1/wanted", &l)
		if len(l.Missing) != 2 || l.Counts.MissingMovies != 1 || l.Counts.Upgrades != 0 || len(l.Upgrades) != 0 {
			t.Fatalf("wanted: %s", a.do("GET", "/api/v1/wanted", "").raw)
		}
		var dune, firefly int
		for i, it := range l.Missing {
			switch it.Title {
			case "Dune":
				dune = i
			case "Firefly":
				firefly = i
			}
		}
		d, f := l.Missing[dune], l.Missing[firefly]
		if d.Kind != "movie" || d.VersionId != movieV || d.State != "never" || !strings.Contains(d.Status, "Not searched yet") {
			t.Errorf("Dune = %+v", d)
		}
		// Firefly's 11 aired episodes in one season, less the one on disk.
		if f.Kind != "episodes" || f.VersionId != seriesV || f.Season == nil || *f.Season != 1 || len(f.Episodes) != 10 || l.Counts.MissingEpisodes != 10 {
			t.Errorf("Firefly = %+v (episodes counted: %d)", f, l.Counts.MissingEpisodes)
		}

		// The sidebar's cheap form: counts and no lists.
		var counts wantedList
		a.get("/api/v1/wanted?countsOnly=true", &counts)
		if len(counts.Missing) != 0 || counts.Counts.MissingMovies != 1 || counts.Counts.MissingEpisodes != 10 {
			t.Errorf("countsOnly: %+v", counts)
		}
		if !strings.Contains(a.do("GET", "/api/v1/wanted?countsOnly=true", "").raw, `"missing":[]`) {
			t.Error("countsOnly should return empty arrays, not null")
		}

		a.signOut()
		if r := a.do("GET", "/api/v1/wanted", ""); r.status != 401 {
			t.Errorf("signed out: %d", r.status)
		}
	})
}

func TestWantedSearchAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		movieV, seriesV := wantedFixture(t, a)
		before := len(searchJobs(t, a)) // the add and the monitoring already queued their own

		body := fmt.Sprintf(`{"versionIds":[%d,%d,%d]}`, movieV, seriesV, movieV)
		r := a.do("POST", "/api/v1/wanted/search", body)
		if r.status != 200 {
			t.Fatalf("search: %d %s", r.status, r.raw)
		}
		queued, already := int(r.body["queued"].(float64)), int(r.body["alreadyQueued"].(float64))
		if queued+already != 2 { // the repeated id counts once
			t.Errorf("queued %d, already %d", queued, already)
		}
		if got := len(searchJobs(t, a)); got != before+queued {
			t.Errorf("jobs = %d, want %d", got, before+queued)
		}
		// Again: everything is already waiting.
		r = a.do("POST", "/api/v1/wanted/search", body)
		if r.status != 200 || r.body["queued"].(float64) != 0 || r.body["alreadyQueued"].(float64) != 2 {
			t.Errorf("again: %d %s", r.status, r.raw)
		}

		if r := a.do("POST", "/api/v1/wanted/search", `{"versionIds":[]}`); r.status != 422 {
			t.Errorf("none: %d %s", r.status, r.raw)
		}
		ids := make([]string, 51)
		for i := range ids {
			ids[i] = fmt.Sprint(i + 1)
		}
		r = a.do("POST", "/api/v1/wanted/search", `{"versionIds":[`+strings.Join(ids, ",")+`]}`)
		if r.status != 422 || !strings.Contains(r.raw, "up to 50") {
			t.Errorf("too many: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/wanted/search", fmt.Sprintf(`{"versionIds":[%d,999999]}`, movieV)); r.status != 404 {
			t.Errorf("unknown: %d %s", r.status, r.raw)
		}
	})
}

func TestWantedUnmonitorAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		movieV, seriesV := wantedFixture(t, a)
		var l wantedList
		a.get("/api/v1/wanted", &l)
		var episodes []int64
		for _, it := range l.Missing {
			if it.Kind == "episodes" {
				for _, e := range it.Episodes[:3] {
					episodes = append(episodes, e.EpisodeId)
				}
			}
		}
		unchanged := func() {
			t.Helper()
			var now wantedList
			a.get("/api/v1/wanted", &now)
			if now.Counts != l.Counts {
				t.Fatalf("a refused request changed things: %+v, was %+v", now.Counts, l.Counts)
			}
		}

		// Mistakes change nothing.
		if r := a.do("POST", "/api/v1/wanted/unmonitor", fmt.Sprintf(`{"movies":[%d]}`, seriesV)); r.status != 422 {
			t.Errorf("series as movie: %d %s", r.status, r.raw)
		}
		bad := fmt.Sprintf(`{"movies":[%d],"episodes":[{"versionId":%d,"episodeIds":[%d]}]}`, movieV, movieV, episodes[0])
		if r := a.do("POST", "/api/v1/wanted/unmonitor", bad); r.status != 422 {
			t.Errorf("movie as series: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/wanted/unmonitor", fmt.Sprintf(`{"movies":[%d,999999]}`, movieV)); r.status != 404 {
			t.Errorf("unknown: %d %s", r.status, r.raw)
		}
		foreign := fmt.Sprintf(`{"movies":[%d],"episodes":[{"versionId":%d,"episodeIds":[%d,999999]}]}`, movieV, seriesV, episodes[0])
		if r := a.do("POST", "/api/v1/wanted/unmonitor", foreign); r.status != 422 {
			t.Errorf("foreign episode: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/wanted/unmonitor", `{}`); r.status != 422 {
			t.Errorf("nothing: %d %s", r.status, r.raw)
		}
		unchanged()

		ok := fmt.Sprintf(`{"movies":[%d],"episodes":[{"versionId":%d,"episodeIds":[%d,%d,%d,%d]}]}`,
			movieV, seriesV, episodes[0], episodes[1], episodes[2], episodes[0])
		r := a.do("POST", "/api/v1/wanted/unmonitor", ok)
		if r.status != 200 || r.body["movies"].(float64) != 1 || r.body["episodes"].(float64) != 3 {
			t.Fatalf("unmonitor: %d %s", r.status, r.raw)
		}
		var after wantedList
		a.get("/api/v1/wanted", &after)
		if after.Counts.MissingMovies != 0 || after.Counts.MissingEpisodes != 7 || len(after.Missing) != 1 {
			t.Errorf("after: %+v", after.Counts)
		}
	})
}
