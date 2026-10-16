package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func backfillJobs(t *testing.T, a *app) int {
	t.Helper()
	jobs, _, err := a.jobs.Recent(context.Background(), 50, true)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, j := range jobs {
		if j.Kind == grab.BackfillKind {
			n++
		}
	}
	return n
}

// The backfill endpoints, end to end: add (which starts the first show
// and runs the task), list, refuse a repeat, options, a forged token,
// the moves that don't apply to a show being worked on, and remove.
func TestBackfillAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		movieV, seriesV := wantedFixture(t, a) // Firefly: 11 aired episodes, the first on disk

		var l struct {
			Now         map[string]any   `json:"now"`
			Next        []map[string]any `json:"next"`
			Decision    []map[string]any `json:"decision"`
			GrabsToday  int              `json:"grabsToday"`
			GrabsPerDay int              `json:"grabsPerDay"`
		}
		a.get("/api/v1/backfill", &l)
		if l.Now != nil || l.GrabsPerDay != 5 || !strings.Contains(a.do("GET", "/api/v1/backfill", "").raw, `"next":[]`) {
			t.Fatalf("empty queue: %s", a.do("GET", "/api/v1/backfill", "").raw)
		}

		// Unknown versions, and movies, aren't shows.
		for _, id := range []int64{99999, movieV} {
			if r := a.do("POST", "/api/v1/backfill", fmt.Sprintf(`{"versionId":%d}`, id)); r.status != 404 {
				t.Errorf("version %d: %d %s", id, r.status, r.raw)
			}
		}
		r := a.do("POST", "/api/v1/backfill", fmt.Sprintf(`{"versionId":%d}`, seriesV))
		if r.status != 201 || r.body["state"] != "active" || r.body["title"] != "Firefly" || int(r.body["missing"].(float64)) != 10 {
			t.Fatalf("add: %d %s", r.status, r.raw)
		}
		id := int64(r.body["id"].(float64))
		if backfillJobs(t, a) != 1 {
			t.Error("adding a show that starts at once should run the Backfill task")
		}
		if r := a.do("POST", "/api/v1/backfill", fmt.Sprintf(`{"versionId":%d}`, seriesV)); r.status != 409 || r.body["code"] != "already_queued" {
			t.Errorf("add again: %d %s", r.status, r.raw)
		}

		a.get("/api/v1/backfill", &l)
		if l.Now == nil || int(l.Now["found"].(float64)) != 1 || int(l.Now["missing"].(float64)) != 10 || len(l.Next) != 0 {
			t.Errorf("list = %+v", l)
		}
		var hist struct {
			Events []struct {
				Event   string `json:"event"`
				Message string `json:"message"`
			} `json:"events"`
		}
		a.get("/api/v1/history?event=backfill", &hist)
		if len(hist.Events) != 1 || !strings.HasPrefix(hist.Events[0].Message, "Backfill started Firefly") {
			t.Errorf("History = %+v", hist)
		}

		// Options with no indexers to ask: nothing to offer, but a proper answer.
		r = a.do("GET", fmt.Sprintf("/api/v1/backfill/%d/options", id), "")
		if r.status != 200 || !strings.Contains(r.raw, `"units":[`) || !strings.Contains(r.raw, `"options":[]`) {
			t.Errorf("options: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/backfill/99999/options", ""); r.status != 404 {
			t.Errorf("options of an unknown row: %d", r.status)
		}
		// A token the server never gave out can't grab anything.
		for _, token := range []string{"nope", "magnet:?xt=urn:btih:0123456789abcdef"} {
			r = a.do("POST", fmt.Sprintf("/api/v1/backfill/%d/grab", id), fmt.Sprintf(`{"token":%q}`, token))
			if r.status != 404 || r.body["code"] != "option_expired" {
				t.Errorf("forged token %q: %d %s", token, r.status, r.raw)
			}
		}

		// The show being worked on can't be moved or hidden.
		for _, path := range []string{"move", "hide"} {
			body := ""
			if path == "move" {
				body = `{"direction":"up"}`
			}
			if r := a.do("POST", fmt.Sprintf("/api/v1/backfill/%d/%s", id, path), body); r.status != 422 {
				t.Errorf("%s the active show: %d %s", path, r.status, r.raw)
			}
		}
		if r := a.do("POST", fmt.Sprintf("/api/v1/backfill/%d/move", id), `{"direction":"sideways"}`); r.status != 400 && r.status != 422 {
			t.Errorf("bad direction: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/backfill/99999/hide", ""); r.status != 404 {
			t.Errorf("hide unknown: %d", r.status)
		}

		if r := a.do("DELETE", fmt.Sprintf("/api/v1/backfill/%d", id), ""); r.status != 204 {
			t.Errorf("remove: %d %s", r.status, r.raw)
		}
		if r := a.do("DELETE", fmt.Sprintf("/api/v1/backfill/%d", id), ""); r.status != 404 {
			t.Errorf("remove again: %d", r.status)
		}
		l.Now = nil // a missing "now" leaves the old value in place
		a.get("/api/v1/backfill", &l)
		if l.Now != nil {
			t.Errorf("after removing: %+v", l)
		}
		// Removing it leaves the episodes as they were: still wanted.
		var w wantedList
		a.get("/api/v1/wanted", &w)
		if w.Counts.MissingEpisodes != 10 {
			t.Errorf("wanted episodes = %d, want 10 still wanted", w.Counts.MissingEpisodes)
		}
		// And it can be added again.
		if r := a.do("POST", "/api/v1/backfill", fmt.Sprintf(`{"versionId":%d}`, seriesV)); r.status != 201 {
			t.Errorf("add after remove: %d %s", r.status, r.raw)
		}
	})
}
