package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// tvRecorded serves the recorded Firefly responses.
func tvRecorded(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var name string
		switch {
		case r.URL.Path == "/search/tv" && q.Get("query") == "Firefly":
			name = "search-tv-firefly.json"
		case r.URL.Path == "/find/362829" && q.Get("external_source") == "tvdb_id":
			name = "find-tvdb-362829.json"
		case r.URL.Path == "/tv/1437":
			name = "tv-1437.json"
		case strings.HasPrefix(r.URL.Path, "/tv/episode_group/"):
			name = "episode-group-" + strings.TrimPrefix(r.URL.Path, "/tv/episode_group/") + ".json"
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, name))
	}
}

func TestSearchAndFindTV(t *testing.T) {
	c := fakeTMDB(t, tvRecorded(t))
	ctx := context.Background()
	res, err := c.SearchTV(ctx, "Firefly", 0)
	if err != nil || len(res) == 0 || res[0].ID != 1437 || res[0].Year() != 2002 {
		t.Fatalf("search = %+v, %v", res, err)
	}
	s, err := c.FindSeriesByTVDB(ctx, 362829)
	if err != nil || s.ID != 94951 || s.Name != "Mythic Quest" {
		t.Errorf("find = %+v, %v", s, err)
	}
	if _, err := c.FindSeriesByIMDb(ctx, "tt0000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown ID: %v", err)
	}
}

func TestSeries(t *testing.T) {
	c := fakeTMDB(t, tvRecorded(t))
	s, err := c.Series(context.Background(), 1437)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Firefly" || s.Year() != 2002 || s.TVDBID != 78874 || s.IMDbID != "tt0303461" || s.Status != "Canceled" {
		t.Errorf("series = %+v", s)
	}
	if len(s.Seasons) != 2 || s.Seasons[0].Number != 0 || s.Seasons[1].Number != 1 {
		t.Fatalf("seasons = %+v", s.Seasons)
	}
	s1 := s.Seasons[1]
	if len(s1.Episodes) != 11 || s1.Episodes[0].Name != "The Train Job" || s1.Episodes[10].Name != "Serenity" || s1.Episodes[10].Type != "finale" {
		t.Errorf("season 1 = %+v", s1.Episodes)
	}
	if len(s.EpisodeGroups) != 2 || s.EpisodeGroups[1].Name != "DVD Order" || s.EpisodeGroups[1].TypeName() != "DVD" {
		t.Errorf("groups = %+v", s.EpisodeGroups)
	}
	if len(s.Networks) == 0 || s.Networks[0] != "FOX" {
		t.Errorf("networks = %v", s.Networks)
	}
}

func TestEpisodeGroupNumbering(t *testing.T) {
	c := fakeTMDB(t, tvRecorded(t))
	ctx := context.Background()
	// Intended Order: Serenity first, Objects in Space 14th, three
	// unaired episodes (TMDB specials) placed in season 1.
	g, err := c.EpisodeGroup(ctx, "5acfb484c3a368738a004068")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c.Series(ctx, 1437)
	byID := map[int]Episode{}
	for _, season := range s.Seasons {
		for _, e := range season.Episodes {
			byID[e.ID] = e
		}
	}
	got := map[string]string{}
	for _, ge := range g.Episodes {
		got[fmt.Sprintf("S%02dE%02d", ge.Season, ge.Number)] = byID[ge.TMDBID].Name
	}
	for k, want := range map[string]string{"S01E01": "Serenity", "S01E02": "The Train Job", "S01E11": "Trash", "S01E14": "Objects in Space"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// DVD Order lists Specials with order 0: still season 0.
	d, err := c.EpisodeGroup(ctx, "6463b4890f3655011907aac2")
	if err != nil {
		t.Fatal(err)
	}
	specials := 0
	for _, ge := range d.Episodes {
		if ge.Season == 0 {
			specials++
		}
	}
	if specials != 9 {
		t.Errorf("DVD order specials = %d, want 9", specials)
	}
}

func TestGroupSeasonNames(t *testing.T) {
	for name, want := range map[string]int{"Season 1": 1, "Season 12": 12, "Specials": 0, "Series 3": 3, "Part 2": 2} {
		if got, ok := groupSeason(name); !ok || got != want {
			t.Errorf("%q = %d, %v", name, got, ok)
		}
	}
	if _, ok := groupSeason("Book One: Water"); ok {
		t.Error("unnumbered names fall back to order")
	}
}

// A long-running series needs more than one request: TMDB appends at most
// 20 parts per request.
func TestSeriesWithManySeasons(t *testing.T) {
	var requests atomic.Int32
	c := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		parts := strings.Split(r.URL.Query().Get("append_to_response"), ",")
		if len(parts) > 20 {
			t.Errorf("%d parts in one request", len(parts))
		}
		body := map[string]any{"id": 9, "name": "Long Show", "first_air_date": "1990-01-01"}
		var seasons []map[string]any
		for n := 0; n <= 25; n++ {
			seasons = append(seasons, map[string]any{"season_number": n, "name": fmt.Sprintf("Season %d", n)})
		}
		body["seasons"] = seasons
		for _, p := range parts {
			var n int
			if _, err := fmt.Sscanf(p, "season/%d", &n); err == nil && n <= 25 {
				body[p] = map[string]any{"season_number": n, "episodes": []map[string]any{
					{"id": 1000 + n, "season_number": n, "episode_number": 1, "name": fmt.Sprintf("S%d first", n)},
				}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	s, err := c.Series(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Seasons) != 26 || s.Seasons[25].Episodes[0].Name != "S25 first" {
		t.Errorf("got %d seasons: %+v", len(s.Seasons), s.Seasons[len(s.Seasons)-1])
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}
