package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// upgradesOfMovie reads a movie's first version's "upgrades" flag.
func upgradesOfMovie(t *testing.T, a *app, movieID int64) bool {
	t.Helper()
	r := a.do("GET", fmt.Sprintf("/api/v1/movies/%d", movieID), "")
	if r.status != 200 {
		t.Fatalf("movie: %d %s", r.status, r.raw)
	}
	on, ok := r.body["versions"].([]any)[0].(map[string]any)["upgrades"].(bool)
	if !ok {
		t.Fatalf("versions carry no upgrades flag: %s", r.raw)
	}
	return on
}

func TestUpgradesAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		// A movie folder starts with upgrades off unless the request says so.
		add := func(mediaType, extra string) map[string]any {
			t.Helper()
			r := a.do("POST", "/api/v1/library/roots", fmt.Sprintf(`{"path":%q,"mediaType":%q,"defaultProfileId":1%s}`, t.TempDir(), mediaType, extra))
			if r.status != 200 {
				t.Fatalf("add %s folder: %d %s", mediaType, r.status, r.raw)
			}
			return r.body
		}
		if got := add("movie", "")["upgradeFound"]; got != false {
			t.Errorf("movie folder, no choice: upgradeFound = %v, want false", got)
		}
		if got := add("movie", `,"upgradeFound":true`)["upgradeFound"]; got != true {
			t.Errorf("movie folder, upgradeFound true: %v", got)
		}
		if got := add("series", "")["upgradeFound"]; got != true {
			t.Errorf("series folder: upgradeFound = %v, want true (ignored)", got)
		}

		// A movie added by hand always has upgrades on, whatever its folder says.
		off := add("movie", "")
		rootID := int64(off["id"].(float64))
		r := a.do("POST", "/api/v1/movies", fmt.Sprintf(`{"tmdbId":438631,"profileId":1,"rootFolderId":%d,"monitored":false}`, rootID))
		if r.status != 200 {
			t.Fatalf("add movie: %d %s", r.status, r.raw)
		}
		movieID := int64(r.body["id"].(float64))
		versionID := int64(r.body["versions"].([]any)[0].(map[string]any)["id"].(float64))
		if !upgradesOfMovie(t, a, movieID) {
			t.Error("a movie added by hand should have upgrades on")
		}

		// The version switch: neither field is a 422 with a field error.
		path := fmt.Sprintf("/api/v1/movies/%d/versions/%d", movieID, versionID)
		r = a.do("PATCH", path, `{}`)
		if r.status != 422 || !strings.Contains(r.raw, `"fields"`) {
			t.Errorf("empty patch: %d %s", r.status, r.raw)
		}
		if r := a.do("PATCH", path, `{"upgrades":false}`); r.status != 204 {
			t.Fatalf("upgrades off: %d %s", r.status, r.raw)
		}
		if upgradesOfMovie(t, a, movieID) {
			t.Error("upgrades should be saved as off")
		}
		if r := a.do("GET", fmt.Sprintf("/api/v1/movies/%d", movieID), ""); r.body["versions"].([]any)[0].(map[string]any)["monitored"] != false {
			t.Error("changing upgrades must leave monitored alone")
		}
		if r := a.do("PATCH", path, `{"monitored":true}`); r.status != 204 || upgradesOfMovie(t, a, movieID) {
			t.Errorf("monitored alone must leave upgrades alone: %d", r.status)
		}
		if r := a.do("PATCH", fmt.Sprintf("/api/v1/movies/%d/versions/999999", movieID), `{"upgrades":true}`); r.status != 404 {
			t.Errorf("unknown version: %d", r.status)
		}

		// Turning upgrades back on for a monitored version queues a search,
		// as monitoring does; turning them off, or on for an unmonitored
		// version, doesn't.
		queued := func() int {
			t.Helper()
			jobList, _, err := a.jobs.Recent(context.Background(), 50, true)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, j := range jobList {
				if j.Kind == grab.SearchKind && j.Status == jobs.Queued {
					n++
				}
			}
			return n
		}
		if queued() != 1 { // the one monitoring it queued, above
			t.Fatalf("queued searches = %d, want 1", queued())
		}
		jobList, _, _ := a.jobs.Recent(context.Background(), 50, true)
		for _, j := range jobList {
			if j.Kind == grab.SearchKind {
				_ = a.jobs.Cancel(context.Background(), j.ID)
			}
		}
		if queued() != 0 {
			t.Fatal("couldn't clear the queued search")
		}
		a.do("PATCH", path, `{"upgrades":false}`)
		if queued() != 0 {
			t.Error("turning upgrades off must not search")
		}
		if r := a.do("PATCH", path, `{"upgrades":true}`); r.status != 204 || !upgradesOfMovie(t, a, movieID) {
			t.Fatalf("upgrades on: %d %s", r.status, r.raw)
		}
		if queued() != 1 {
			t.Errorf("queued searches after turning upgrades on = %d, want 1", queued())
		}

		// The folder switch changes every movie version in it, but only
		// when the folder's own setting changes.
		rootPath := fmt.Sprintf("/api/v1/library/roots/%d", rootID)
		if r := a.do("PUT", rootPath, `{"defaultProfileId":1,"manageFiles":false,"upgradeFound":true}`); r.status != 200 || r.body["upgradeFound"] != true {
			t.Fatalf("folder on: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", rootPath, `{"defaultProfileId":1,"manageFiles":false,"upgradeFound":false}`); r.status != 200 || r.body["upgradeFound"] != false {
			t.Fatalf("folder off: %d %s", r.status, r.raw)
		}
		if upgradesOfMovie(t, a, movieID) {
			t.Error("the folder switch should turn the movie's upgrades off")
		}
		// Left out: unchanged.
		if r := a.do("PUT", rootPath, `{"defaultProfileId":1,"manageFiles":true}`); r.status != 200 || r.body["upgradeFound"] != false || upgradesOfMovie(t, a, movieID) {
			t.Errorf("folder without upgradeFound: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", rootPath, `{"defaultProfileId":1,"manageFiles":true,"upgradeFound":true}`); r.status != 200 || r.body["upgradeFound"] != true || !upgradesOfMovie(t, a, movieID) {
			t.Errorf("folder on again: %d %s", r.status, r.raw)
		}
	})
}
