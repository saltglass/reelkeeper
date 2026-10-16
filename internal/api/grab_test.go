package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestGrabbingSettingsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	if r := a.do("GET", "/api/v1/settings/grabbing", ""); r.status != 200 || r.body["mode"] != "dryrun" || r.body["minSeeders"].(float64) != 1 {
		t.Errorf("defaults: %d %s", r.status, r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"off","queueLimit":5,"minSeeders":3}`); r.status != 200 || r.body["mode"] != "off" || r.body["queueLimit"].(float64) != 5 {
		t.Errorf("save: %d %s", r.status, r.raw)
	}
	if r := a.do("GET", "/api/v1/settings/grabbing", ""); r.body["minSeeders"].(float64) != 3 {
		t.Errorf("reload: %s", r.raw)
	}
	r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"dryrun","queueLimit":-1,"minSeeders":500}`)
	if r.status != 422 || !strings.Contains(r.raw, "queueLimit") || !strings.Contains(r.raw, "minSeeders") {
		t.Errorf("validation: %d %s", r.status, r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"sometimes","queueLimit":0,"minSeeders":1}`); r.status != 400 && r.status != 422 {
		t.Errorf("bad mode: %d %s", r.status, r.raw)
	}
}

// addMovieFixture makes a movie media folder and returns its id.
func addMovieFixture(t *testing.T, a *app) int64 {
	t.Helper()
	r := a.do("POST", "/api/v1/library/roots", `{"path":"`+t.TempDir()+`","mediaType":"movie","defaultProfileId":1}`)
	if r.status != 200 {
		t.Fatalf("add root: %d %s", r.status, r.raw)
	}
	return int64(r.body["id"].(float64))
}

func searchJobs(t *testing.T, a *app) []string {
	t.Helper()
	jobs, _, err := a.jobs.Recent(context.Background(), 50, true)
	if err != nil {
		t.Fatal(err)
	}
	// Decoded and re-encoded: Postgres stores JSON with its keys reordered.
	var out []string
	for _, j := range jobs {
		if j.Kind == grab.SearchKind {
			var args grab.SearchArgs
			if err := json.Unmarshal([]byte(j.Args), &args); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(args)
			out = append(out, string(b))
		}
	}
	return out
}

func TestSearchOnAddAndManualSearch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		root := addMovieFixture(t, a)
		add := fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d}`, root)

		// Dry run (the default): adding a monitored movie queues a search.
		r := a.do("POST", "/api/v1/movies", add)
		if r.status != 200 {
			t.Fatalf("add: %d %s", r.status, r.raw)
		}
		movieID := int64(r.body["id"].(float64))
		versionID := int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))
		got := searchJobs(t, a)
		want := fmt.Sprintf(`{"versionId":%d,"trigger":"added"}`, versionID)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("search jobs = %v, want [%s]", got, want)
		}

		// Un-monitoring and monitoring again searches again (the first job
		// is still queued, so it is reused: still one job).
		path := fmt.Sprintf("/api/v1/movies/%d/versions/%d", movieID, versionID)
		if r := a.do("PATCH", path, `{"monitored":false}`); r.status != 204 {
			t.Fatalf("unmonitor: %d %s", r.status, r.raw)
		}
		if r := a.do("PATCH", path, `{"monitored":true}`); r.status != 204 {
			t.Fatalf("monitor: %d %s", r.status, r.raw)
		}
		if n := len(searchJobs(t, a)); n != 1 {
			t.Errorf("jobs after re-monitor = %d, want 1 (deduped)", n)
		}

		// Manual search: returns the job; wrong version is 404.
		r = a.do("POST", path+"/search", "")
		if r.status != 200 || r.body["kind"] != grab.SearchKind {
			t.Fatalf("search: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", fmt.Sprintf("/api/v1/movies/%d/versions/999999/search", movieID), ""); r.status != 404 {
			t.Errorf("wrong version: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/movies/999999/versions/1/search", ""); r.status != 404 {
			t.Errorf("wrong movie: %d %s", r.status, r.raw)
		}
	})
}

func TestNoSearchOnAddWhenOff(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	root := addMovieFixture(t, a)
	if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"off","queueLimit":0,"minSeeders":1}`); r.status != 200 {
		t.Fatal(r.raw)
	}
	r := a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d}`, root))
	if r.status != 200 {
		t.Fatalf("add: %d %s", r.status, r.raw)
	}
	if got := searchJobs(t, a); len(got) != 0 {
		t.Errorf("Off should not queue a search, got %v", got)
	}
	// A manual search still works while Off.
	movieID := int64(r.body["id"].(float64))
	versionID := int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))
	if r := a.do("POST", fmt.Sprintf("/api/v1/movies/%d/versions/%d/search", movieID, versionID), ""); r.status != 200 {
		t.Errorf("manual search while off: %d %s", r.status, r.raw)
	}
	// An unmonitored add never searches, even in dry run.
	a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"dryrun","queueLimit":0,"minSeeders":1}`)
	if r := a.do("DELETE", fmt.Sprintf("/api/v1/movies/%d/versions/%d", movieID, versionID), ""); r.status != 204 {
		t.Fatalf("remove: %d %s", r.status, r.raw)
	}
	before := len(searchJobs(t, a))
	a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d,"monitored":false}`, root))
	if n := len(searchJobs(t, a)); n != before {
		t.Errorf("unmonitored add queued a search")
	}
}

func TestHistoryAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	root := addMovieFixture(t, a)
	r := a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d,"monitored":false}`, root))
	if r.status != 200 {
		t.Fatalf("add: %d %s", r.status, r.raw)
	}
	titleID := int64(r.body["id"].(float64))
	versionID := int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))
	ctx := context.Background()
	kinds := []string{grab.EventSearched, grab.EventWouldGrab, grab.EventSearched, grab.EventRSS, grab.EventGrabbed}
	for i, k := range kinds {
		e := &grab.Event{TitleID: &titleID, VersionID: &versionID, Event: k, Message: fmt.Sprintf("event %d", i), Quality: "WEBDL-1080p"}
		data := grab.EventData{Trigger: "manual"}
		if k == grab.EventSearched {
			data.Releases = []grab.Verdict{{Name: "Dune.2021.1080p", Indexer: "ix", Size: 5 << 30, Seeders: 9, Accepted: false, Rejections: []string{"Too small"}}}
			data.More = 3
		}
		if err := a.history.Add(ctx, e, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.history.Add(ctx, &grab.Event{Event: grab.EventRSS, Message: "orphan"}, grab.EventData{}); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Events []struct {
			ID        int64   `json:"id"`
			Title     *string `json:"title"`
			Year      *int    `json:"year"`
			MediaType *string `json:"mediaType"`
			Profile   *string `json:"profile"`
			Event     string  `json:"event"`
			Message   string  `json:"message"`
			Data      struct {
				Releases []struct {
					Name       string   `json:"name"`
					Rejections []string `json:"rejections"`
				} `json:"releases"`
				More int `json:"more"`
			} `json:"data"`
		} `json:"events"`
		NextBefore *int64 `json:"nextBefore"`
	}
	var p page
	a.get("/api/v1/history", &p)
	if len(p.Events) != 6 || p.NextBefore != nil {
		t.Fatalf("all: %d events, next %v", len(p.Events), p.NextBefore)
	}
	if p.Events[0].Message != "orphan" || p.Events[0].Title != nil || p.Events[0].Profile != nil {
		t.Errorf("newest should be the orphan with null title: %+v", p.Events[0])
	}
	e := p.Events[5] // oldest: the first search
	if e.Title == nil || *e.Title != "Dune" || e.Year == nil || *e.Year != 2021 || e.Profile == nil || *e.Profile == "" ||
		e.MediaType == nil || *e.MediaType != "movie" || p.Events[0].MediaType != nil ||
		len(e.Data.Releases) != 1 || e.Data.Releases[0].Rejections[0] != "Too small" || e.Data.More != 3 {
		t.Errorf("decoded event: %+v", e)
	}

	// Filters.
	p = page{}
	a.get(fmt.Sprintf("/api/v1/history?titleId=%d&event=searched&event=rss", titleID), &p)
	if len(p.Events) != 3 {
		t.Errorf("titleId+events: %d events", len(p.Events))
	}
	// Paging: two at a time, newest first, no overlap.
	var seen []int64
	p = page{}
	a.get("/api/v1/history?limit=4", &p)
	if len(p.Events) != 4 || p.NextBefore == nil || *p.NextBefore != p.Events[3].ID {
		t.Fatalf("page 1: %d events, next %v", len(p.Events), p.NextBefore)
	}
	for _, ev := range p.Events {
		seen = append(seen, ev.ID)
	}
	next := *p.NextBefore
	p = page{}
	a.get(fmt.Sprintf("/api/v1/history?limit=4&before=%d", next), &p)
	if len(p.Events) != 2 || p.NextBefore != nil {
		t.Fatalf("page 2: %d events, next %v", len(p.Events), p.NextBefore)
	}
	for _, ev := range p.Events {
		seen = append(seen, ev.ID)
	}
	b, _ := json.Marshal(seen)
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Errorf("not newest-first without overlap: %s", b)
		}
	}
	if r := a.do("GET", "/api/v1/history?event=bogus", ""); r.status != 400 {
		t.Errorf("bad event: %d %s", r.status, r.raw)
	}
}
