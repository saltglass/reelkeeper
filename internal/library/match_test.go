package library

import (
	"testing"

	"github.com/saltglass/reelkeeper/internal/metadata"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Spider-Man - No Way Home": "spider man no way home",
		"Spider-Man: No Way Home":  "spider man no way home",
		"Les Misérables":           "les miserables",
		"Lilo & Stitch":            "lilo and stitch",
		"Chip 'n Dale":             "chip n dale",
		"The World's End":          "the worlds end",
		"WALL·E":                   "wall e",
		"  Extra   Spaces ":        "extra spaces",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	for _, c := range []struct {
		a, b string
		min  float64
		max  float64
	}{
		{"Dune - Part Two", "Dune: Part Two", 1, 1},
		{"Lilo and Stitch", "Lilo & Stitch", 1, 1},
		{"Grinch", "The Grinch", 0.97, 0.97},
		{"Harry Potter And The Sorcerers Stone", "Harry Potter and the Philosopher's Stone", 0.6, 0.85},
		{"Dune", "Dune: Part Two", 0, 0.4},
		{"Alien", "Aliens", 0.8, 0.9},
		{"Gremlins 2", "Gremlins 2: The New Batch", 0.9, 0.9},
		{"Up", "Up in the Air", 0, 0.3},
		{"", "Anything", 0, 0},
	} {
		got := similarity(c.a, c.b)
		if got < c.min || got > c.max {
			t.Errorf("similarity(%q, %q) = %.2f, want %.2f..%.2f", c.a, c.b, got, c.min, c.max)
		}
	}
}

func res(id int, title, date string, pop float64) metadata.MovieResult {
	return metadata.MovieResult{ID: id, Title: title, OriginalTitle: title, ReleaseDate: date, Popularity: pop}
}

func voted(r metadata.MovieResult, votes int) metadata.MovieResult { r.VoteCount = votes; return r }

func TestDecide(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		year    int
		results []metadata.MovieResult
		wantID  int // 0 = needs review
	}{
		{"exact title and year", "28 Days Later", 2002,
			[]metadata.MovieResult{res(170, "28 Days Later", "2002-10-31", 30), res(1562, "28 Weeks Later", "2007-04-26", 20)}, 170},
		{"punctuation differs", "Dune - Part Two", 2024,
			[]metadata.MovieResult{res(693134, "Dune: Part Two", "2024-02-27", 300), res(438631, "Dune", "2021-09-15", 200)}, 693134},
		{"same title, year picks the film", "Dune", 2021,
			[]metadata.MovieResult{res(841, "Dune", "1984-12-14", 40), res(438631, "Dune", "2021-09-15", 200)}, 438631},
		{"year one off still fine", "Some Film", 2019,
			[]metadata.MovieResult{res(1, "Some Film", "2020-01-10", 5)}, 1},
		{"no year, two films share the title", "Dune", 0,
			[]metadata.MovieResult{res(841, "Dune", "1984-12-14", 40), res(438631, "Dune", "2021-09-15", 200)}, 0},
		{"wrong year only", "Some Film", 2010,
			[]metadata.MovieResult{res(1, "Some Film", "1995-01-10", 5)}, 0},
		{"nothing close", "Completely Different", 2020,
			[]metadata.MovieResult{res(1, "Another Movie", "2020-01-10", 5)}, 0},
		{"no results", "Obscure Home Video", 2003, nil, 0},
		{"exact year beats one year off", "Anaconda", 2025,
			[]metadata.MovieResult{res(1, "Anaconda", "2025-12-24", 50), res(2, "Anaconda", "2024-08-16", 10)}, 1},
		{"part two, not part one", "Harry Potter and the Deathly Hallows - Part 2", 2011,
			[]metadata.MovieResult{res(12445, "Harry Potter and the Deathly Hallows: Part 2", "2011-07-12", 80), res(12444, "Harry Potter and the Deathly Hallows: Part 1", "2010-11-17", 80)}, 12445},
		{"subtitle left out of the folder name", "Gremlins 2", 1990,
			[]metadata.MovieResult{res(928, "Gremlins 2: The New Batch", "1990-06-15", 20)}, 928},
		{"one-word title is not a prefix match", "Up", 2009,
			[]metadata.MovieResult{res(22947, "Up in the Air", "2009-12-04", 20)}, 0},
		{"famous film beats obscure namesakes", "It", 2017,
			[]metadata.MovieResult{voted(res(1, "It", "2017-01-01", 1), 4), voted(res(346364, "It", "2017-09-06", 90), 21000)}, 346364},
		{"namesakes equally obscure: ask", "Eden", 2025,
			[]metadata.MovieResult{voted(res(1, "Eden", "2025-01-01", 1), 30), voted(res(2, "Eden", "2025-03-01", 1), 12)}, 0},
		{"remake with the same title and year one apart", "The Little Mermaid", 2023,
			[]metadata.MovieResult{res(447277, "The Little Mermaid", "2023-05-18", 100), res(10144, "The Little Mermaid", "1989-11-17", 50)}, 447277},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, reason := decide(rank(c.title, c.year, c.results))
			switch {
			case c.wantID == 0 && w != nil:
				t.Errorf("accepted %d (%s %d, score %.2f); should need review", w.TMDBID, w.Title, w.Year, w.Score)
			case c.wantID != 0 && w == nil:
				t.Errorf("needs review (%s); want %d", reason, c.wantID)
			case c.wantID != 0 && w.TMDBID != c.wantID:
				t.Errorf("picked %d, want %d", w.TMDBID, c.wantID)
			}
			if w == nil && reason == "" {
				t.Error("a review must explain why")
			}
		})
	}
}

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Dark Knight": "dark knight", "A Quiet Place": "quiet place", "An American Tail": "american tail",
		"Them!": "them", "The": "the",
	} {
		if got := sortTitle(in); got != want {
			t.Errorf("sortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
