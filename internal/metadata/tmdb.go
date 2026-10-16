// Package metadata talks to TMDB (themoviedb.org), Reelkeeper's source of movie
// and TV information: titles, years, runtimes, IDs, posters.
//
// TMDB's terms require attribution: "This product uses the TMDB API but is
// not endorsed or certified by TMDB." It's shown on System → Status.
//
// The client keeps well under TMDB's rate limit, retries politely when told
// to slow down, and never puts the API key into errors or logs.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors callers can act on.
var (
	ErrNoKey     = errors.New("no TMDB API key is set; add one in Settings → General")
	ErrBadKey    = errors.New("TMDB rejected the API key; check it in Settings → General")
	ErrNotFound  = errors.New("not found on TMDB")
	ErrRateLimit = errors.New("TMDB is asking us to slow down; try again shortly")
)

const (
	defaultBase = "https://api.themoviedb.org/3"
	// ImageBase is where posters and backdrops are served from.
	ImageBase = "https://image.tmdb.org/t/p"
	// minInterval spaces requests: 10 per second, well under TMDB's limit.
	minInterval = 100 * time.Millisecond
	maxRetries  = 3
)

// Client is a TMDB API client.
type Client struct {
	key  func() string // read on every call, so a changed setting applies at once
	lang func() string
	http *http.Client
	base string

	mu   sync.Mutex
	next time.Time // earliest time the next request may start
}

// New returns a client that reads its key and language through the given
// functions (normally from settings).
func New(key, lang func() string) *Client {
	return &Client{key: key, lang: lang, http: &http.Client{Timeout: 20 * time.Second}, base: defaultBase}
}

// SetBaseURL points the client at a different server (tests use a fake).
func (c *Client) SetBaseURL(u string) { c.base = strings.TrimRight(u, "/") }

// MovieResult is one movie in search results.
type MovieResult struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	ReleaseDate   string  `json:"release_date"` // "2002-10-31", may be empty
	Overview      string  `json:"overview"`
	PosterPath    string  `json:"poster_path"`
	Popularity    float64 `json:"popularity"`
	VoteCount     int     `json:"vote_count"`
	Adult         bool    `json:"adult"`
}

// Year returns the release year, or 0 if unknown.
func (m MovieResult) Year() int { return yearOf(m.ReleaseDate) }

// Movie is a movie's full details.
type Movie struct {
	ID               int
	IMDbID           string
	TVDBID           int
	Title            string
	OriginalTitle    string
	OriginalLanguage string
	Overview         string
	Tagline          string
	ReleaseDate      string
	Runtime          int // minutes
	Status           string
	Genres           []string
	PosterPath       string
	BackdropPath     string
	Popularity       float64
	VoteAverage      float64
	VoteCount        int
	Collection       string
	// AlternativeTitles are other names the movie is known by, which helps
	// match folder names ("Dune Part Two" vs "Dune: Part Two").
	AlternativeTitles []string
	// Release dates in the US, when known: "2024-03-01".
	Theatrical, Digital, Physical string
	Certification                 string // US rating, e.g. "PG-13"
}

// Year returns the release year, or 0 if unknown.
func (m Movie) Year() int { return yearOf(m.ReleaseDate) }

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}

// SearchMovies searches by title, optionally narrowed to a year (0 for
// any). It returns the first page of results, best match first.
func (c *Client) SearchMovies(ctx context.Context, query string, year int) ([]MovieResult, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var out struct {
		Results []MovieResult `json:"results"`
	}
	if err := c.get(ctx, "/search/movie", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Movie fetches a movie's details, IDs, release dates and alternative
// titles in one request.
func (c *Client) Movie(ctx context.Context, id int) (*Movie, error) {
	var raw struct {
		ID               int     `json:"id"`
		IMDbID           string  `json:"imdb_id"`
		Title            string  `json:"title"`
		OriginalTitle    string  `json:"original_title"`
		OriginalLanguage string  `json:"original_language"`
		Overview         string  `json:"overview"`
		Tagline          string  `json:"tagline"`
		ReleaseDate      string  `json:"release_date"`
		Runtime          int     `json:"runtime"`
		Status           string  `json:"status"`
		PosterPath       string  `json:"poster_path"`
		BackdropPath     string  `json:"backdrop_path"`
		Popularity       float64 `json:"popularity"`
		VoteAverage      float64 `json:"vote_average"`
		VoteCount        int     `json:"vote_count"`
		Genres           []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Collection *struct {
			Name string `json:"name"`
		} `json:"belongs_to_collection"`
		ExternalIDs struct {
			IMDbID string `json:"imdb_id"`
			TVDBID int    `json:"tvdb_id"`
		} `json:"external_ids"`
		AlternativeTitles struct {
			Titles []struct {
				Title string `json:"title"`
			} `json:"titles"`
		} `json:"alternative_titles"`
		ReleaseDates struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Certification string `json:"certification"`
					Date          string `json:"release_date"`
					Type          int    `json:"type"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	q := url.Values{"append_to_response": {"external_ids,release_dates,alternative_titles"}}
	if err := c.get(ctx, "/movie/"+strconv.Itoa(id), q, &raw); err != nil {
		return nil, err
	}
	m := &Movie{
		ID: raw.ID, IMDbID: raw.IMDbID, TVDBID: raw.ExternalIDs.TVDBID, Title: raw.Title,
		OriginalTitle: raw.OriginalTitle, OriginalLanguage: raw.OriginalLanguage, Overview: raw.Overview,
		Tagline: raw.Tagline, ReleaseDate: raw.ReleaseDate, Runtime: raw.Runtime, Status: raw.Status,
		PosterPath: raw.PosterPath, BackdropPath: raw.BackdropPath, Popularity: raw.Popularity,
		VoteAverage: raw.VoteAverage, VoteCount: raw.VoteCount,
	}
	if m.IMDbID == "" {
		m.IMDbID = raw.ExternalIDs.IMDbID
	}
	for _, g := range raw.Genres {
		m.Genres = append(m.Genres, g.Name)
	}
	if raw.Collection != nil {
		m.Collection = raw.Collection.Name
	}
	seen := map[string]bool{strings.ToLower(m.Title): true}
	for _, t := range raw.AlternativeTitles.Titles {
		if k := strings.ToLower(t.Title); !seen[k] {
			seen[k] = true
			m.AlternativeTitles = append(m.AlternativeTitles, t.Title)
		}
	}
	// US release dates by type: 3 theatrical, 4 digital, 5 physical.
	for _, r := range raw.ReleaseDates.Results {
		if r.Country != "US" {
			continue
		}
		for _, d := range r.Dates {
			date := d.Date
			if len(date) >= 10 {
				date = date[:10]
			}
			switch d.Type {
			case 3:
				if m.Theatrical == "" {
					m.Theatrical = date
				}
			case 4:
				if m.Digital == "" {
					m.Digital = date
				}
			case 5:
				if m.Physical == "" {
					m.Physical = date
				}
			}
			if m.Certification == "" && d.Certification != "" {
				m.Certification = d.Certification
			}
		}
	}
	return m, nil
}

// FindByIMDb looks a movie up by its IMDb ID ("tt0289043"). It returns
// ErrNotFound if TMDB doesn't know it.
func (c *Client) FindByIMDb(ctx context.Context, imdbID string) (*MovieResult, error) {
	var out struct {
		Movies []MovieResult `json:"movie_results"`
	}
	q := url.Values{"external_source": {"imdb_id"}}
	if err := c.get(ctx, "/find/"+url.PathEscape(imdbID), q, &out); err != nil {
		return nil, err
	}
	if len(out.Movies) == 0 {
		return nil, ErrNotFound
	}
	return &out.Movies[0], nil
}

// Check makes one cheap request to see whether the key works.
func (c *Client) Check(ctx context.Context) error {
	var out map[string]any
	return c.get(ctx, "/configuration", nil, &out)
}

// get performs a GET, waiting its turn, retrying on rate limits and
// server errors, and decodes JSON into out.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	key := strings.TrimSpace(c.key())
	if key == "" {
		return ErrNoKey
	}
	if q == nil {
		q = url.Values{}
	}
	if lang := c.lang(); lang != "" {
		q.Set("language", lang)
	}
	// A v4 "read access token" goes in a header; a v3 key in the query.
	bearer := strings.HasPrefix(key, "eyJ")
	if !bearer {
		q.Set("api_key", key)
	}
	u := c.base + path + "?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if bearer {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("can't reach TMDB: %w", redact(err))
			c.backoff(time.Duration(attempt+1) * time.Second)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				return fmt.Errorf("reading TMDB's answer: %w", readErr)
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("TMDB sent something unexpected: %w", err)
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized:
			return ErrBadKey
		case resp.StatusCode == http.StatusNotFound:
			return ErrNotFound
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = ErrRateLimit
			wait := 2 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 60 {
				wait = time.Duration(s) * time.Second
			}
			c.backoff(wait)
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("TMDB is having trouble (HTTP %d)", resp.StatusCode)
			c.backoff(time.Duration(attempt+1) * time.Second)
		default:
			return fmt.Errorf("TMDB refused the request (HTTP %d)", resp.StatusCode)
		}
	}
	return lastErr
}

// wait blocks until this request's turn, spacing requests minInterval apart.
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	start := c.next
	if start.Before(now) {
		start = now
	}
	c.next = start.Add(minInterval)
	c.mu.Unlock()
	if d := time.Until(start); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// backoff pushes back the next allowed request time.
func (c *Client) backoff(d time.Duration) {
	c.mu.Lock()
	if t := time.Now().Add(d); t.After(c.next) {
		c.next = t
	}
	c.mu.Unlock()
}

// redact strips the request URL (which holds the API key) from errors.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
