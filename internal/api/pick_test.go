package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// "Choose a release" over HTTP: the list answers in its documented shape,
// the endpoints are for movies only and for a version of that movie,
// forged tokens grab nothing, and signing out locks both.
func TestMovieChoicesAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		movieV, seriesV := wantedFixture(t, a)
		var movies []map[string]any
		a.get("/api/v1/movies", &movies)
		if len(movies) != 1 {
			t.Fatalf("movies: %v", movies)
		}
		movie := int64(movies[0]["id"].(float64))
		var series []map[string]any
		a.get("/api/v1/series", &series)
		show := int64(series[0]["id"].(float64))

		// No indexers to ask: nothing to choose from, but a proper answer.
		path := fmt.Sprintf("/api/v1/movies/%d/versions/%d/releases", movie, movieV)
		r := a.do("GET", path, "")
		if r.status != 200 || !strings.Contains(r.raw, `"releases":[]`) || r.body["hidden"] != float64(0) || r.body["more"] != float64(0) {
			t.Errorf("list: %d %s", r.status, r.raw)
		}

		// Only a version of this movie, and only movies.
		for _, p := range []string{
			fmt.Sprintf("/api/v1/movies/%d/versions/99999/releases", movie),
			fmt.Sprintf("/api/v1/movies/99999/versions/%d/releases", movieV),
			fmt.Sprintf("/api/v1/movies/%d/versions/%d/releases", show, seriesV),
		} {
			if r := a.do("GET", p, ""); r.status != 404 {
				t.Errorf("GET %s: %d %s", p, r.status, r.raw)
			}
		}

		// A token the server never gave out can't grab anything.
		for _, token := range []string{"nope", "magnet:?xt=urn:btih:0123456789abcdef"} {
			r = a.do("POST", path+"/grab", fmt.Sprintf(`{"token":%q}`, token))
			if r.status != 404 || r.body["code"] != "option_expired" {
				t.Errorf("forged token %q: %d %s", token, r.status, r.raw)
			}
		}
		if r := a.do("POST", path+"/grab", `{}`); r.status != 404 && r.status != 400 && r.status != 422 {
			t.Errorf("no token: %d %s", r.status, r.raw)
		}

		a.signOut()
		if r := a.do("GET", path, ""); r.status != 401 {
			t.Errorf("signed out, list: %d", r.status)
		}
		if r := a.do("POST", path+"/grab", `{"token":"nope"}`); r.status != 401 {
			t.Errorf("signed out, grab: %d", r.status)
		}
	})
}
