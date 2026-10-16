package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeRadarr serves a tiny Radarr with one wanted movie (Dune) filed under
// movieRoot, one movie in a folder Reelkeeper doesn't have, a history record
// and a blocklist entry.
func fakeRadarr(t *testing.T, movieRoot string) *httptest.Server {
	t.Helper()
	movies := fmt.Sprintf(`[
	 {"id":1,"tmdbId":438631,"title":"Dune","year":2021,"monitored":true,"hasFile":false,"path":%q,"rootFolderPath":%q,"tags":[1]},
	 {"id":2,"tmdbId":606,"title":"Elsewhere","year":2010,"monitored":true,"hasFile":false,"path":"/data/Elsewhere (2010)","rootFolderPath":"/data","tags":[]}]`,
		movieRoot+"/Dune (2021)", movieRoot)
	history := `{"totalRecords":1,"records":[
	 {"id":10,"movieId":1,"sourceTitle":"Dune.2021.1080p.BluRay-GRP","eventType":"grabbed","date":"2026-01-01T10:00:00Z",
	  "quality":{"quality":{"name":"Bluray-1080p"}},"data":{"indexer":"1337x","downloadUrl":"https://prowlarr/1/download?apikey=SECRET"}}]}`
	blocklist := `{"totalRecords":1,"records":[{"id":5,"movieId":1,"sourceTitle":"Dune.2021.CAM-BAD","indexer":"Knaben","message":"Fake",
	  "torrentInfoHash":"ABCDEF","quality":{"quality":{"name":"CAM"}},"date":"2026-01-02T00:00:00Z"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write([]byte(`{"appName":"Radarr","instanceName":"Radarr","version":"6.4.4"}`))
		case "/api/v3/movie":
			_, _ = w.Write([]byte(movies))
		case "/api/v3/tag":
			_, _ = w.Write([]byte(`[{"id":1,"label":"1 - dan"}]`))
		case "/api/v3/history":
			_, _ = w.Write([]byte(history))
		case "/api/v3/blocklist":
			_, _ = w.Write([]byte(blocklist))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRadarrImportAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	dir := t.TempDir()
	if r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`); r.status != 200 {
		t.Fatalf("add root: %d %s", r.status, r.raw)
	}
	radarr := fakeRadarr(t, dir)
	body := func(key string) string {
		return fmt.Sprintf(`{"url":%q,"apiKey":%q,"profileId":1}`, radarr.URL, key)
	}

	r := a.do("POST", "/api/v1/migrate/radarr/preview", body("key"))
	if r.status != 200 || r.body["movies"].(float64) != 2 || r.body["wanted"].(float64) != 2 ||
		r.body["history"].(float64) != 1 || r.body["blocklist"].(float64) != 1 || r.body["imported"] != false {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	issues := r.body["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["kind"] != "no-media-folder" {
		t.Errorf("issues: %s", r.raw)
	}
	if r := a.do("GET", "/api/v1/history?event=from-radarr", ""); r.status != 200 || len(r.body["events"].([]any)) != 0 {
		t.Errorf("the preview changed History: %s", r.raw)
	}

	r = a.do("POST", "/api/v1/migrate/radarr/import", body("key"))
	if r.status != 200 || r.body["imported"] != true || r.body["added"].(float64) != 1 ||
		r.body["historyAdded"].(float64) != 1 || r.body["blocklistAdded"].(float64) != 1 {
		t.Fatalf("import: %d %s", r.status, r.raw)
	}
	h := a.do("GET", "/api/v1/history?event=from-radarr", "")
	if h.status != 200 || len(h.body["events"].([]any)) != 1 || strings.Contains(h.raw, "SECRET") {
		t.Errorf("history: %d %s", h.status, h.raw)
	}
	if r := a.do("GET", "/api/v1/history?event=nonsense", ""); r.status != 400 && r.status != 422 {
		t.Errorf("bad event filter: %d", r.status)
	}

	// Again: nothing is duplicated.
	r = a.do("POST", "/api/v1/migrate/radarr/import", body("key"))
	if r.status != 200 || r.body["added"].(float64) != 0 || r.body["historyAdded"].(float64) != 0 {
		t.Errorf("second import: %d %s", r.status, r.raw)
	}

	// A wrong key is a plain 422, and the key isn't echoed.
	r = a.do("POST", "/api/v1/migrate/radarr/preview", body("wrong-key-123"))
	if r.status != 422 || !strings.Contains(r.raw, "Radarr refused the API key") || strings.Contains(r.raw, "wrong-key-123") {
		t.Errorf("wrong key: %d %s", r.status, r.raw)
	}
	// Missing fields and an unknown profile.
	r = a.do("POST", "/api/v1/migrate/radarr/import", `{"url":"","apiKey":"","profileId":999}`)
	if r.status != 422 || !strings.Contains(r.raw, "url") || !strings.Contains(r.raw, "apiKey") || !strings.Contains(r.raw, "profileId") {
		t.Errorf("missing fields: %d %s", r.status, r.raw)
	}
}

// fakeSonarr serves a tiny Sonarr: Firefly (which the fake TMDB knows) with
// two monitored episodes, one series in a folder Reelkeeper doesn't have, a
// history record and a blocklist entry.
func fakeSonarr(t *testing.T, tvRoot string) *httptest.Server {
	t.Helper()
	series := fmt.Sprintf(`[
	 {"id":1,"tvdbId":78874,"tmdbId":1437,"title":"Firefly","year":2002,"monitored":true,"monitorNewItems":"all","path":%q,"rootFolderPath":%q,"tags":[1]},
	 {"id":2,"tvdbId":2,"tmdbId":606,"title":"Elsewhere","year":2010,"monitored":true,"monitorNewItems":"all","path":"/data/tv/Elsewhere","rootFolderPath":"/data/tv","tags":[]}]`,
		tvRoot+"/Firefly", tvRoot)
	episodes := `[
	 {"id":11,"seasonNumber":1,"episodeNumber":1,"title":"Serenity","monitored":true,"hasFile":false},
	 {"id":12,"seasonNumber":1,"episodeNumber":2,"title":"The Train Job","monitored":true,"hasFile":false},
	 {"id":13,"seasonNumber":1,"episodeNumber":3,"title":"Bushwhacked","monitored":false,"hasFile":false}]`
	history := `{"totalRecords":1,"records":[
	 {"id":10,"seriesId":1,"episodeId":11,"sourceTitle":"Firefly.S01E01.1080p.BluRay-GRP","eventType":"grabbed","date":"2026-01-01T10:00:00Z",
	  "quality":{"quality":{"name":"Bluray-1080p"}},"data":{"indexer":"1337x","downloadUrl":"https://prowlarr/1/download?apikey=SECRET"}}]}`
	blocklist := `{"totalRecords":1,"records":[{"id":5,"seriesId":1,"episodeIds":[11],"sourceTitle":"Firefly.CAM-BAD","indexer":"Knaben","message":"Fake",
	  "torrentInfoHash":"ABCDEF","quality":{"quality":{"name":"CAM"}},"date":"2026-01-02T00:00:00Z"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write([]byte(`{"appName":"Sonarr","instanceName":"Sonarr","version":"4.0.20"}`))
		case "/api/v3/series":
			_, _ = w.Write([]byte(series))
		case "/api/v3/episode":
			if r.URL.Query().Get("seriesId") == "1" {
				_, _ = w.Write([]byte(episodes))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/api/v3/tag":
			_, _ = w.Write([]byte(`[{"id":1,"label":"1 - dan"}]`))
		case "/api/v3/history":
			_, _ = w.Write([]byte(history))
		case "/api/v3/blocklist":
			_, _ = w.Write([]byte(blocklist))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSonarrImportAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	dir := t.TempDir()
	rr := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"series","defaultProfileId":1}`)
	if rr.status != 200 {
		t.Fatalf("add root: %d %s", rr.status, rr.raw)
	}
	// Reelkeeper already has Firefly, wanting nothing yet.
	if r := a.do("POST", "/api/v1/series", fmt.Sprintf(`{"tmdbId":1437,"profileId":1,"rootFolderId":%d,"monitored":false}`, int64(rr.body["id"].(float64)))); r.status != 200 && r.status != 201 {
		t.Fatalf("add series: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/profiles", `{"name":"4K","appliesTo":"movie"}`); r.status != 200 {
		t.Fatalf("add profile: %d %s", r.status, r.raw)
	}
	sonarr := fakeSonarr(t, dir)
	body := func(key string) string {
		return fmt.Sprintf(`{"url":%q,"apiKey":%q,"profileId":1}`, sonarr.URL, key)
	}

	r := a.do("POST", "/api/v1/migrate/sonarr/preview", body("key"))
	if r.status != 200 || r.body["series"].(float64) != 2 || r.body["episodes"].(float64) != 3 || r.body["matched"].(float64) != 3 ||
		r.body["newlyWanted"].(float64) != 2 || r.body["history"].(float64) != 1 || r.body["blocklist"].(float64) != 1 || r.body["imported"] != false {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	wanted := r.body["wanted"].([]any)
	if len(wanted) != 1 || wanted[0].(map[string]any)["title"] != "Firefly" {
		t.Errorf("wanted: %s", r.raw)
	}
	issues := r.body["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["kind"] != "no-media-folder" {
		t.Errorf("issues: %s", r.raw)
	}
	if r := a.do("GET", "/api/v1/history?event=from-sonarr", ""); r.status != 200 || len(r.body["events"].([]any)) != 0 {
		t.Errorf("the preview changed History: %s", r.raw)
	}

	r = a.do("POST", "/api/v1/migrate/sonarr/import", body("key"))
	if r.status != 200 || r.body["imported"] != true || r.body["episodesTurnedOn"].(float64) != 2 || r.body["monitorChanged"].(float64) != 1 ||
		r.body["historyAdded"].(float64) != 1 || r.body["blocklistAdded"].(float64) != 1 {
		t.Fatalf("import: %d %s", r.status, r.raw)
	}
	h := a.do("GET", "/api/v1/history?event=from-sonarr", "")
	if h.status != 200 || len(h.body["events"].([]any)) != 1 || strings.Contains(h.raw, "SECRET") {
		t.Errorf("history: %d %s", h.status, h.raw)
	}

	// Again: nothing is duplicated, and nothing new is wanted.
	r = a.do("POST", "/api/v1/migrate/sonarr/import", body("key"))
	if r.status != 200 || r.body["newlyWanted"].(float64) != 0 || r.body["episodesTurnedOn"].(float64) != 0 || r.body["historyAdded"].(float64) != 0 {
		t.Errorf("second import: %d %s", r.status, r.raw)
	}

	// A wrong key is a plain 422 naming Sonarr, and the key isn't echoed.
	r = a.do("POST", "/api/v1/migrate/sonarr/preview", body("wrong-key-123"))
	if r.status != 422 || !strings.Contains(r.raw, "Sonarr refused the API key") || strings.Contains(r.raw, "wrong-key-123") ||
		r.body["code"] != "sonarr" {
		t.Errorf("wrong key: %d %s", r.status, r.raw)
	}
	// A Radarr where a Sonarr should be.
	radarr := fakeRadarr(t, dir)
	r = a.do("POST", "/api/v1/migrate/sonarr/preview", fmt.Sprintf(`{"url":%q,"apiKey":"key","profileId":1}`, radarr.URL))
	if r.status != 422 || !strings.Contains(r.raw, "That address is Radarr, not Sonarr") {
		t.Errorf("wrong app: %d %s", r.status, r.raw)
	}
	// Missing fields, an unknown profile, and a movie-only profile.
	r = a.do("POST", "/api/v1/migrate/sonarr/import", `{"url":"","apiKey":"","profileId":999}`)
	if r.status != 422 || !strings.Contains(r.raw, "url") || !strings.Contains(r.raw, "apiKey") || !strings.Contains(r.raw, "profileId") {
		t.Errorf("missing fields: %d %s", r.status, r.raw)
	}
	var profiles []map[string]any
	a.get("/api/v1/profiles", &profiles)
	for _, p := range profiles {
		if p["appliesTo"] == "movie" {
			r = a.do("POST", "/api/v1/migrate/sonarr/preview", fmt.Sprintf(`{"url":%q,"apiKey":"key","profileId":%d}`, sonarr.URL, int64(p["id"].(float64))))
			if r.status != 422 || !strings.Contains(r.raw, "isn't for TV") {
				t.Errorf("movie profile: %d %s", r.status, r.raw)
			}
		}
	}
}
