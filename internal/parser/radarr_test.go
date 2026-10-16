package parser

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// These tests run the parser against the release names in Radarr's own
// parser tests (testdata/parser/radarr; see its README for the source and
// license). Each table checks one thing the name says.

// radarrCase is one line of a testdata/parser/radarr/*.jsonl table. Only the
// fields present on a line are checked.
type radarrCase struct {
	Name      string   `json:"name"`
	Quality   *string  `json:"quality"`
	Proper    *bool    `json:"proper"`
	Revision  *int     `json:"revision"`
	Title     *string  `json:"title"`
	Year      *int     `json:"year"`
	Group     *string  `json:"group"`
	Edition   *string  `json:"edition"`
	TMDBID    *int     `json:"tmdbId"`
	IMDbID    *string  `json:"imdbId"`
	Languages []string `json:"languages"`
}

func loadRadarr(t *testing.T, table string) []radarrCase {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "parser", "radarr", table+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []radarrCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c radarrCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		cases = append(cases, c)
	}
	if len(cases) == 0 {
		t.Fatalf("%s: no cases", table)
	}
	return cases
}

// skipped lists names in a table that Reelkeeper deliberately does not match,
// with the reason for each. A table's skips are checked first; the test
// fails if a skip-listed name has disappeared from the table, so the list
// cannot go stale. Keep it short: every entry is a place where Reelkeeper
// disagrees with Radarr.
var radarrSkips = map[string]map[string]string{
	"quality": {
		"S07E23 .avi ":           "Radarr reads a quality from the file extension alone; Reelkeeper never guesses a quality from nothing",
		"Some.Movie.S02E15.avi":  "same: quality from the .avi extension only",
		"Some.Movie.S02E15.xvid": "same: quality from an .xvid extension only",
		"Some.Movie.S02E15.divx": "same: quality from a .divx extension only",
		"Movie Name.S03E01.The Electric Can Opener Fluctuation.m2ts": "same: quality from the .m2ts extension only",
		"[SubsPlease] Movie Title (540p) [AB649D32].mkv":             "anime: Radarr assumes WEB-DL from the .mkv extension",
		"[Erai-raws] Movie Title [540p][Multiple Subtitle].mkv":      "anime: Radarr assumes WEB-DL from the .mkv extension",
	},
	"language": {
		"Movie Title latino": "no year or quality marker, so Reelkeeper reads \"latino\" as part of the title; Radarr's test calls its language parser on the bare string",
		"Movie Title (Temporada 11 Completa) Audio Dual Ingles/Latino 1920x1080": "same: the language words are the tail of an undelimited title",
		"Movie title 7x4 audio latino":                                           "same: the language words are the tail of an undelimited title",
		"Movie.Title.2016.RU-KA.WEB-DL.h264":                                     "Radarr's full parser would take \"KA\" for the release group here and drop it too; only its isolated language test finds Georgian",
	},
}

// checkSkips fails if a skip-listed name is not in the table any more.
func checkSkips(t *testing.T, table string, names map[string]bool) {
	t.Helper()
	for name := range radarrSkips[table] {
		if !names[name] {
			t.Errorf("%s: skip-listed name %q is no longer in the table; remove it from radarrSkips", table, name)
		}
	}
}

func TestRadarrQuality(t *testing.T) {
	cases := loadRadarr(t, "quality")
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.Name] = true
		if _, skip := radarrSkips["quality"][c.Name]; skip {
			continue
		}
		r := Parse(c.Name)
		if c.Quality != nil {
			if got := r.Quality(); got != *c.Quality {
				t.Errorf("%q: quality %q, want %q", c.Name, got, *c.Quality)
			}
		}
		if c.Proper != nil && (r.Revision >= 2) != *c.Proper {
			t.Errorf("%q: revision %d, want proper=%v", c.Name, r.Revision, *c.Proper)
		}
		if c.Revision != nil && max(r.Revision, 1) != *c.Revision {
			t.Errorf("%q: revision %d, want %d", c.Name, r.Revision, *c.Revision)
		}
	}
	checkSkips(t, "quality", seen)
}

func TestRadarrTitle(t *testing.T) {
	for _, c := range loadRadarr(t, "title") {
		r := Parse(c.Name)
		if c.Title != nil && looseTitle(r.Title) != looseTitle(*c.Title) {
			t.Errorf("%q: title %q, want %q", c.Name, r.Title, *c.Title)
		}
		if c.Year != nil && r.Year != *c.Year {
			t.Errorf("%q: year %d, want %d", c.Name, r.Year, *c.Year)
		}
		if c.Group != nil && r.Group != *c.Group {
			t.Errorf("%q: group %q, want %q", c.Name, r.Group, *c.Group)
		}
		if c.Edition != nil && !sameEdition(r.Edition, *c.Edition) {
			t.Errorf("%q: edition %q, want %q", c.Name, r.Edition, *c.Edition)
		}
		if c.TMDBID != nil && r.TMDBID != *c.TMDBID {
			t.Errorf("%q: tmdb %d, want %d", c.Name, r.TMDBID, *c.TMDBID)
		}
		if c.IMDbID != nil && r.IMDbID != *c.IMDbID {
			t.Errorf("%q: imdb %q, want %q", c.Name, r.IMDbID, *c.IMDbID)
		}
	}
}

func TestRadarrGroup(t *testing.T) {
	for _, c := range loadRadarr(t, "group") {
		if got := Parse(c.Name).Group; got != *c.Group {
			t.Errorf("%q: group %q, want %q", c.Name, got, *c.Group)
		}
	}
}

func TestRadarrEdition(t *testing.T) {
	for _, c := range loadRadarr(t, "edition") {
		if got := Parse(c.Name).Edition; !sameEdition(got, *c.Edition) {
			t.Errorf("%q: edition %q, want %q", c.Name, got, *c.Edition)
		}
	}
}

func TestRadarrLanguage(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range loadRadarr(t, "language") {
		seen[c.Name] = true
		if _, skip := radarrSkips["language"][c.Name]; skip {
			continue
		}
		got := Parse(c.Name).Languages
		if len(c.Languages) == 0 && len(got) > 0 {
			t.Errorf("%q: languages %v, want none", c.Name, got)
		}
		for _, l := range c.Languages {
			if !slices.Contains(got, l) {
				t.Errorf("%q: languages %v, want %s", c.Name, got, l)
			}
		}
	}
	checkSkips(t, "language", seen)
}

// looseTitle ignores what Radarr and Reelkeeper legitimately write differently in
// a title: case, punctuation and spacing.
func looseTitle(s string) string {
	s = strings.NewReplacer("'", "", "\u2019", "").Replace(s) // "Director's" and "Directors" are the same
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// sameEdition compares editions loosely, the way looseTitle does.
func sameEdition(a, b string) bool { return looseTitle(a) == looseTitle(b) }
