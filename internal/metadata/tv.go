package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TV on TMDB, in short:
//
//   - A series has seasons (0 is "Specials") and each season has episodes,
//     numbered in TMDB's default order: the order they first aired.
//   - Some series also have episode groups: other orderings made by TMDB
//     users, such as "DVD Order" or "Intended Order". A group holds
//     "seasons" (sub-groups) that list TMDB's episodes in their new order.
//     Files named by Sonarr usually follow TVDB's order, which for most
//     shows is the aired order and for some matches one of these groups.

// SeriesResult is one series in search results.
type SeriesResult struct {
	ID            int      `json:"id"`
	Name          string   `json:"name"`
	OriginalName  string   `json:"original_name"`
	FirstAirDate  string   `json:"first_air_date"` // may be empty
	Overview      string   `json:"overview"`
	PosterPath    string   `json:"poster_path"`
	Popularity    float64  `json:"popularity"`
	VoteCount     int      `json:"vote_count"`
	OriginCountry []string `json:"origin_country"`
}

// Year returns the year the series started, or 0 if unknown.
func (s SeriesResult) Year() int { return yearOf(s.FirstAirDate) }

// Series is a series' full details, with every season's episodes.
type Series struct {
	ID               int
	IMDbID           string
	TVDBID           int
	Name             string
	OriginalName     string
	OriginalLanguage string
	Overview         string
	FirstAirDate     string
	LastAirDate      string
	Status           string // "Returning Series", "Ended", "Canceled"...
	Type             string // "Scripted", "Reality", "Talk Show", "News"...
	Networks         []string
	Genres           []string
	PosterPath       string
	BackdropPath     string
	Popularity       float64
	VoteAverage      float64
	VoteCount        int
	Runtime          int    // typical episode length in minutes, 0 if unknown
	Certification    string // US rating, e.g. "TV-14"
	// AlternativeTitles help match folder names.
	AlternativeTitles []string
	Seasons           []Season
	// EpisodeGroups lists the other orderings TMDB has for this series.
	// Their contents are fetched with EpisodeGroup.
	EpisodeGroups []EpisodeGroupInfo
}

// Year returns the year the series started, or 0 if unknown.
func (s Series) Year() int { return yearOf(s.FirstAirDate) }

// Season is one season and its episodes.
type Season struct {
	Number     int // 0 for specials
	Name       string
	Overview   string
	AirDate    string
	PosterPath string
	Episodes   []Episode
}

// Episode is one episode, numbered in TMDB's default (aired) order.
type Episode struct {
	ID        int // TMDB's episode ID; stays the same across orderings
	Season    int
	Number    int
	Name      string
	Overview  string
	AirDate   string // "2020-02-07", may be empty for unaired episodes
	Runtime   int
	StillPath string
	Type      string // "standard", "finale", "mid_season"
}

// EpisodeGroupInfo describes an alternative ordering.
type EpisodeGroupInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         int    `json:"type"`
	Description  string `json:"description"`
	EpisodeCount int    `json:"episode_count"`
	GroupCount   int    `json:"group_count"`
}

// TypeName describes an episode group's type in plain words.
func (g EpisodeGroupInfo) TypeName() string { return groupTypeName(g.Type) }

func groupTypeName(t int) string {
	return map[int]string{
		1: "Original air date", 2: "Absolute", 3: "DVD", 4: "Digital", 5: "Story arc", 6: "Production", 7: "TV",
	}[t]
}

// EpisodeGroup is an alternative ordering: every episode it places, with
// the season and number it gives that episode.
type EpisodeGroup struct {
	ID       string
	Name     string
	Type     int
	Episodes []GroupEpisode
}

// GroupEpisode places one TMDB episode in an ordering.
type GroupEpisode struct {
	TMDBID int // the episode's TMDB ID
	Season int // its season in this ordering (0 for specials)
	Number int // its number in this ordering
}

// SearchTV searches series by name, optionally narrowed to the year they
// started (0 for any). It returns the first page, best match first.
func (c *Client) SearchTV(ctx context.Context, query string, year int) ([]SeriesResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("first_air_date_year", strconv.Itoa(year))
	}
	var out struct {
		Results []SeriesResult `json:"results"`
	}
	if err := c.get(ctx, "/search/tv", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// FindSeriesByTVDB looks a series up by its TVDB ID, as written in folder
// names like "{tvdb-362829}" and as Seerr sends it. ErrNotFound if TMDB
// doesn't know it.
func (c *Client) FindSeriesByTVDB(ctx context.Context, tvdbID int) (*SeriesResult, error) {
	return c.findSeries(ctx, strconv.Itoa(tvdbID), "tvdb_id")
}

// FindSeriesByIMDb looks a series up by its IMDb ID ("tt0303461").
func (c *Client) FindSeriesByIMDb(ctx context.Context, imdbID string) (*SeriesResult, error) {
	return c.findSeries(ctx, imdbID, "imdb_id")
}

func (c *Client) findSeries(ctx context.Context, id, source string) (*SeriesResult, error) {
	var out struct {
		TV []SeriesResult `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &out); err != nil {
		return nil, err
	}
	if len(out.TV) == 0 {
		return nil, ErrNotFound
	}
	return &out.TV[0], nil
}

// TMDB lets one request carry at most 20 appended parts. The first request
// for a series uses four for IDs, titles, groups and ratings, which leaves
// room for this many seasons; later requests fetch 20 seasons each.
const (
	appendLimit        = 20
	firstRequestExtras = "external_ids,alternative_titles,episode_groups,content_ratings"
)

// rawSeason is a season as TMDB sends it, inside a series or on its own.
type rawSeason struct {
	SeasonNumber int    `json:"season_number"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
	Episodes     []struct {
		ID            int    `json:"id"`
		SeasonNumber  int    `json:"season_number"`
		EpisodeNumber int    `json:"episode_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
		AirDate       string `json:"air_date"`
		Runtime       int    `json:"runtime"`
		StillPath     string `json:"still_path"`
		EpisodeType   string `json:"episode_type"`
	} `json:"episodes"`
}

// Series fetches a series' details and all its seasons' episodes, in as
// few requests as TMDB allows (one for most series).
func (c *Client) Series(ctx context.Context, id int) (*Series, error) {
	var head struct {
		ID               int     `json:"id"`
		Name             string  `json:"name"`
		OriginalName     string  `json:"original_name"`
		OriginalLanguage string  `json:"original_language"`
		Overview         string  `json:"overview"`
		FirstAirDate     string  `json:"first_air_date"`
		LastAirDate      string  `json:"last_air_date"`
		Status           string  `json:"status"`
		Type             string  `json:"type"`
		PosterPath       string  `json:"poster_path"`
		BackdropPath     string  `json:"backdrop_path"`
		Popularity       float64 `json:"popularity"`
		VoteAverage      float64 `json:"vote_average"`
		VoteCount        int     `json:"vote_count"`
		EpisodeRunTime   []int   `json:"episode_run_time"`
		Networks         []struct {
			Name string `json:"name"`
		} `json:"networks"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Seasons     []rawSeason `json:"seasons"` // summaries, without episodes
		ExternalIDs struct {
			IMDbID string `json:"imdb_id"`
			TVDBID int    `json:"tvdb_id"`
		} `json:"external_ids"`
		AlternativeTitles struct {
			Results []struct {
				Title string `json:"title"`
			} `json:"results"`
		} `json:"alternative_titles"`
		EpisodeGroups struct {
			Results []EpisodeGroupInfo `json:"results"`
		} `json:"episode_groups"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
	}

	// The season list only arrives with the first answer, so the first
	// request guesses: seasons 0 to 15 cover nearly every series.
	firstBatch := seasonParts(0, appendLimit-4)
	body, raw, err := c.seriesRequest(ctx, id, firstRequestExtras+","+strings.Join(firstBatch, ","))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, fmt.Errorf("TMDB sent something unexpected: %w", err)
	}

	s := &Series{
		ID: head.ID, IMDbID: head.ExternalIDs.IMDbID, TVDBID: head.ExternalIDs.TVDBID, Name: head.Name,
		OriginalName: head.OriginalName, OriginalLanguage: head.OriginalLanguage, Overview: head.Overview,
		FirstAirDate: head.FirstAirDate, LastAirDate: head.LastAirDate, Status: head.Status, Type: head.Type,
		PosterPath: head.PosterPath, BackdropPath: head.BackdropPath, Popularity: head.Popularity,
		VoteAverage: head.VoteAverage, VoteCount: head.VoteCount, EpisodeGroups: head.EpisodeGroups.Results,
	}
	if len(head.EpisodeRunTime) > 0 {
		s.Runtime = head.EpisodeRunTime[0]
	}
	for _, n := range head.Networks {
		s.Networks = append(s.Networks, n.Name)
	}
	for _, g := range head.Genres {
		s.Genres = append(s.Genres, g.Name)
	}
	seen := map[string]bool{strings.ToLower(s.Name): true}
	for _, t := range head.AlternativeTitles.Results {
		if k := strings.ToLower(t.Title); !seen[k] {
			seen[k] = true
			s.AlternativeTitles = append(s.AlternativeTitles, t.Title)
		}
	}
	for _, r := range head.ContentRatings.Results {
		if r.Country == "US" {
			s.Certification = r.Rating
		}
	}

	// Seasons the first request didn't cover, 20 per request.
	var missing []string
	for _, sum := range head.Seasons {
		part := "season/" + strconv.Itoa(sum.SeasonNumber)
		if _, ok := raw[part]; !ok {
			missing = append(missing, part)
		}
	}
	for len(missing) > 0 {
		batch := missing[:min(appendLimit, len(missing))]
		missing = missing[len(batch):]
		_, more, err := c.seriesRequest(ctx, id, strings.Join(batch, ","))
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			raw[k] = v
		}
	}

	for _, sum := range head.Seasons {
		var rs rawSeason
		if b, ok := raw["season/"+strconv.Itoa(sum.SeasonNumber)]; ok {
			if err := json.Unmarshal(b, &rs); err != nil {
				return nil, fmt.Errorf("TMDB sent an unexpected season: %w", err)
			}
		} else {
			rs = sum // TMDB listed it but sent no details: keep the summary
		}
		season := Season{Number: sum.SeasonNumber, Name: rs.Name, Overview: rs.Overview, AirDate: rs.AirDate, PosterPath: rs.PosterPath}
		for _, e := range rs.Episodes {
			season.Episodes = append(season.Episodes, Episode{
				ID: e.ID, Season: e.SeasonNumber, Number: e.EpisodeNumber, Name: e.Name, Overview: e.Overview,
				AirDate: e.AirDate, Runtime: e.Runtime, StillPath: e.StillPath, Type: e.EpisodeType,
			})
		}
		s.Seasons = append(s.Seasons, season)
	}
	sort.Slice(s.Seasons, func(i, j int) bool { return s.Seasons[i].Number < s.Seasons[j].Number })
	return s, nil
}

func seasonParts(from, n int) []string {
	out := make([]string, 0, n)
	for i := from; i < from+n; i++ {
		out = append(out, "season/"+strconv.Itoa(i))
	}
	return out
}

// seriesRequest fetches /tv/{id} with appended parts. It returns the whole
// answer (for the series' own fields) and the seasons it carried, by part
// name ("season/3"). TMDB silently leaves out seasons that don't exist.
func (c *Client) seriesRequest(ctx context.Context, id int, parts string) (json.RawMessage, map[string]json.RawMessage, error) {
	var body json.RawMessage
	if err := c.get(ctx, "/tv/"+strconv.Itoa(id), url.Values{"append_to_response": {parts}}, &body); err != nil {
		return nil, nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, nil, fmt.Errorf("TMDB sent something unexpected: %w", err)
	}
	seasons := map[string]json.RawMessage{}
	for k, v := range all {
		if strings.HasPrefix(k, "season/") {
			seasons[k] = v
		}
	}
	return body, seasons, nil
}

// EpisodeGroup fetches an alternative ordering and works out the season
// and number it gives each episode.
func (c *Client) EpisodeGroup(ctx context.Context, id string) (*EpisodeGroup, error) {
	var raw struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   int    `json:"type"`
		Groups []struct {
			Name     string `json:"name"`
			Order    int    `json:"order"`
			Episodes []struct {
				ID    int `json:"id"`
				Order int `json:"order"`
			} `json:"episodes"`
		} `json:"groups"`
	}
	if err := c.get(ctx, "/tv/episode_group/"+url.PathEscape(id), nil, &raw); err != nil {
		return nil, err
	}
	g := &EpisodeGroup{ID: raw.ID, Name: raw.Name, Type: raw.Type}

	// Groups are "seasons" in this ordering. Their numbers come from their
	// names where possible ("Season 3", "Specials"), because the "order"
	// field isn't used consistently: Specials may be first or last.
	// Unnamed ones ("Book One: Water") are numbered in order from 1.
	sort.SliceStable(raw.Groups, func(i, j int) bool { return raw.Groups[i].Order < raw.Groups[j].Order })
	used := map[int]bool{}
	numbers := make([]int, len(raw.Groups))
	for i, grp := range raw.Groups {
		numbers[i] = -1
		if n, ok := groupSeason(grp.Name); ok && !used[n] {
			numbers[i] = n
			used[n] = true
		}
	}
	next := 1
	for i := range numbers {
		if numbers[i] >= 0 {
			continue
		}
		for used[next] {
			next++
		}
		numbers[i] = next
		used[next] = true
	}

	for i, grp := range raw.Groups {
		eps := grp.Episodes
		sort.SliceStable(eps, func(a, b int) bool { return eps[a].Order < eps[b].Order })
		// Episode "order" counts from 0 in most groups and from 1 in some.
		base := 0
		if len(eps) > 0 && eps[0].Order > 0 {
			base = eps[0].Order - 1
		}
		for _, e := range eps {
			g.Episodes = append(g.Episodes, GroupEpisode{TMDBID: e.ID, Season: numbers[i], Number: e.Order - base + 1})
		}
	}
	return g, nil
}

var groupSeasonRe = regexp.MustCompile(`(?i)^\s*(?:season|series|staffel|saison|temporada|volume|part)\s*(\d{1,3})\b`)

// groupSeason reads a season number from an episode group's name.
func groupSeason(name string) (int, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "specials" || n == "special" || strings.HasPrefix(n, "specials ") || n == "extras" {
		return 0, true
	}
	if m := groupSeasonRe.FindStringSubmatch(n); m != nil {
		v, _ := strconv.Atoi(m[1])
		return v, true
	}
	return 0, false
}
