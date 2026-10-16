package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "0123456789abcdef0123456789abcdef"

// fakeTMDB serves recorded responses from testdata/tmdb by path.
func fakeTMDB(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(func() string { return testKey }, func() string { return "en-US" })
	c.SetBaseURL(srv.URL)
	return c
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/tmdb/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func recorded(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != testKey {
			http.Error(w, `{"status_code":7}`, http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		var name string
		switch {
		case r.URL.Path == "/search/movie" && q.Get("query") == "28 Days Later" && q.Get("year") == "2002":
			name = "search-28-days-later-2002.json"
		case r.URL.Path == "/search/movie" && q.Get("query") == "Dune":
			name = "search-dune.json"
		case r.URL.Path == "/movie/170":
			name = "movie-170.json"
		case r.URL.Path == "/movie/438631":
			name = "movie-438631.json"
		case r.URL.Path == "/find/tt0289043":
			name = "find-tt0289043.json"
		default:
			http.Error(w, `{"status_code":34}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, name))
	}
}

func TestSearchAndDetails(t *testing.T) {
	c := fakeTMDB(t, recorded(t))
	ctx := context.Background()

	res, err := c.SearchMovies(ctx, "28 Days Later", 2002)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ID != 170 || res[0].Year() != 2002 {
		t.Fatalf("search = %+v", res)
	}

	m, err := c.Movie(ctx, 170)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "28 Days Later" || m.Year() != 2002 || m.IMDbID != "tt0289043" || m.Runtime < 100 ||
		len(m.Genres) == 0 || m.PosterPath == "" || m.Theatrical == "" {
		t.Errorf("movie = %+v", m)
	}

	dune, err := c.Movie(ctx, 438631)
	if err != nil {
		t.Fatal(err)
	}
	if dune.Year() != 2021 || len(dune.AlternativeTitles) == 0 || dune.Collection == "" {
		t.Errorf("dune = %+v", dune)
	}

	found, err := c.FindByIMDb(ctx, "tt0289043")
	if err != nil || found.ID != 170 {
		t.Errorf("find = %+v, %v", found, err)
	}
	if _, err := c.Movie(ctx, 999999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown movie: %v", err)
	}
}

func TestKeyErrorsAndBearerToken(t *testing.T) {
	ctx := context.Background()
	noKey := New(func() string { return "" }, func() string { return "" })
	if _, err := noKey.SearchMovies(ctx, "x", 0); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}

	c := fakeTMDB(t, recorded(t))
	c.key = func() string { return "wrong-key-wrong-key-wrong-key-xx" }
	if _, err := c.SearchMovies(ctx, "Dune", 0); !errors.Is(err, ErrBadKey) {
		t.Errorf("bad key: %v", err)
	}

	// A v4 read access token goes in the Authorization header, not the URL.
	var sawHeader, sawQuery atomic.Bool
	c = fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		sawHeader.Store(r.Header.Get("Authorization") == "Bearer eyJtoken")
		sawQuery.Store(r.URL.Query().Has("api_key"))
		_, _ = w.Write([]byte(`{"results":[]}`))
	})
	c.key = func() string { return "eyJtoken" }
	if _, err := c.SearchMovies(ctx, "x", 0); err != nil {
		t.Fatal(err)
	}
	if !sawHeader.Load() || sawQuery.Load() {
		t.Errorf("header=%v query=%v", sawHeader.Load(), sawQuery.Load())
	}
}

func TestRetriesRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	c := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"results":[{"id":1,"title":"X"}]}`))
		}
	})
	start := time.Now()
	res, err := c.SearchMovies(context.Background(), "x", 0)
	if err != nil || len(res) != 1 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	if time.Since(start) < time.Second {
		t.Error("Retry-After should be respected")
	}
}

func TestRequestsAreSpacedOut(t *testing.T) {
	c := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"results":[]}`)) })
	start := time.Now()
	for range 5 {
		if _, err := c.SearchMovies(context.Background(), "x", 0); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 4*minInterval {
		t.Errorf("5 requests took %s; they should be spaced %s apart", d, minInterval)
	}
}

func TestErrorsNeverContainTheKey(t *testing.T) {
	// Nothing listens here, so the request fails with a URL error.
	c := New(func() string { return testKey }, func() string { return "en-US" })
	c.SetBaseURL("http://127.0.0.1:1")
	c.http.Timeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := c.SearchMovies(ctx, "x", 0)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks the key: %v", err)
	}
}
