package sonarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// oneShowTMDB is a TMDB that knows one series: two seasons (and a
// special) of three episodes, the last one not aired yet.
type oneShowTMDB struct{ library.TMDB }

func (oneShowTMDB) FindSeriesByTVDB(_ context.Context, tvdb int) (*metadata.SeriesResult, error) {
	if tvdb == 917002 {
		return &metadata.SeriesResult{ID: 917001, Name: "The Dravnik Reach"}, nil
	}
	return nil, metadata.ErrNotFound
}

func (oneShowTMDB) Series(_ context.Context, id int) (*metadata.Series, error) {
	if id != 917001 {
		return nil, metadata.ErrNotFound
	}
	s := &metadata.Series{ID: 917001, TVDBID: 917002, Name: "The Dravnik Reach", FirstAirDate: "2015-12-14", Status: "Returning Series",
		Type: "Scripted", Networks: []string{"Prime Video"}, Runtime: 45}
	for n := 0; n <= 2; n++ {
		season := metadata.Season{Number: n}
		for e := 1; e <= 3; e++ {
			air := fmt.Sprintf("201%d-01-0%d", 5+n, e)
			if n == 2 && e == 3 {
				air = "2099-01-01"
			}
			season.Episodes = append(season.Episodes, metadata.Episode{ID: n*10 + e, Season: n, Number: e, Name: fmt.Sprintf("E%d", e), AirDate: air})
		}
		s.Seasons = append(s.Seasons, season)
	}
	return s, nil
}

// Seerr's request flow against the real library, on both databases: what
// the fakes promise, the library does.
func TestSeerrFlowWithTheLibrary(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		lib := library.New(db, oneShowTMDB{}, slog.New(slog.DiscardHandler))
		root, err := lib.AddRootFolder(ctx, library.RootInput{Path: t.TempDir(), MediaType: library.Series, DefaultProfileID: 1})
		if err != nil {
			t.Fatal(err)
		}
		var searched []int64
		h := Router(Deps{Library: lib, Downloads: &fakeDownloads{}, Logger: slog.New(slog.DiscardHandler),
			Search: func(_ context.Context, v int64) error { searched = append(searched, v); return nil }})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{Via: auth.ViaAPIKey}))
			http.StripPrefix("/compat", h).ServeHTTP(w, r)
		}))
		defer srv.Close()
		do := func(method, path, body string) (int, []byte) {
			t.Helper()
			req, _ := http.NewRequest(method, srv.URL+"/compat/sonarr/hd/api/v3/"+path, strings.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, b
		}

		_, b := do("GET", "series/lookup?term=tvdb:917002", "")
		if l := decode[[]map[string]any](t, b); len(l) != 1 || l[0]["id"] != nil || len(l[0]["seasons"].([]any)) != 3 {
			t.Fatalf("lookup = %s", b)
		}
		code, b := do("POST", "series", fmt.Sprintf(`{"tvdbId":917002,"monitored":true,"monitorNewItems":"all","rootFolderPath":%q,
			"seasons":[{"seasonNumber":0,"monitored":false},{"seasonNumber":1,"monitored":true},{"seasonNumber":2,"monitored":false}],
			"addOptions":{"searchForMissingEpisodes":true}}`, root.Path))
		if code != 201 {
			t.Fatalf("add: %d %s", code, b)
		}
		s := decode[map[string]any](t, b)
		id := int64(s["id"].(float64))
		on := func(s map[string]any) string {
			out := ""
			for _, x := range s["seasons"].([]any) {
				out += fmt.Sprint(x.(map[string]any)["monitored"].(bool), " ")
			}
			return out
		}
		// Season 2 wasn't requested: off, its unaired episode too (as in
		// Sonarr; "new episodes" covers seasons TMDB doesn't list yet).
		if on(s) != "false true false " || s["tvdbId"].(float64) != 917002 || len(searched) != 1 {
			t.Errorf("added: seasons %s, %s, searched %v", on(s), b, searched)
		}
		st := s["statistics"].(map[string]any)
		if st["episodeCount"].(float64) != 3 || st["totalEpisodeCount"].(float64) != 9 {
			t.Errorf("statistics = %v", st)
		}
		// Season 2 requested later: all its episodes are wanted.
		s["seasons"].([]any)[2].(map[string]any)["monitored"] = true
		body, _ := json.Marshal(s)
		do("PUT", "series", string(body))
		_, b = do("GET", fmt.Sprintf("episode?seriesId=%d&seasonNumber=2", id), "")
		eps := decode[[]map[string]any](t, b)
		if len(eps) != 3 || eps[0]["monitored"] != true || eps[2]["monitored"] != true {
			t.Errorf("season 2 ticked: %s", b)
		}
		// One special turned on: counted in its season, not the series.
		_, b = do("GET", fmt.Sprintf("episode?seriesId=%d&seasonNumber=0", id), "")
		special := decode[[]map[string]any](t, b)[0]["id"]
		do("PUT", "episode/monitor", fmt.Sprintf(`{"episodeIds":[%v],"monitored":true}`, special))
		_, b = do("GET", fmt.Sprintf("series/%d", id), "")
		s = decode[map[string]any](t, b)
		s0 := s["seasons"].([]any)[0].(map[string]any)
		if st := s["statistics"].(map[string]any); st["episodeCount"].(float64) != 5 ||
			s0["monitored"] != true || s0["statistics"].(map[string]any)["episodeCount"].(float64) != 1 {
			t.Errorf("after the special: %s", b)
		}
		if code, _ := do("DELETE", fmt.Sprintf("series/%d?deleteFiles=true", id), ""); code != 200 {
			t.Errorf("delete: %d", code)
		}
		if _, b = do("GET", "series", ""); string(b) != "[]\n" {
			t.Errorf("after delete: %s", b)
		}
	})
}
