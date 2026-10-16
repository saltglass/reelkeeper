package library

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/saltglass/reelkeeper/internal/metadata"
)

// Identification compares what a folder says ("Dune - Part Two", 2024)
// with TMDB's search results and scores each candidate from 0 to 1. The
// rules, in plain words:
//
//   - Titles are compared after normalising: lower case, accents removed,
//     "&" read as "and", punctuation ignored. "Spider-Man - No Way Home" and
//     "Spider-Man: No Way Home" are the same.
//   - The year must match exactly or be one off (release dates differ by
//     country). A different year is a strong "no".
//   - A clear winner is accepted. Anything doubtful goes to the review
//     screen with the candidates, never accepted silently.

// Thresholds for accepting a match without asking.
const (
	acceptScore  = 0.90 // the best candidate must score at least this
	acceptMargin = 0.08 // and beat the runner-up by this much
)

// normalize prepares a title for comparison.
func normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	s, _, _ = transform.String(t, s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// sortTitle is how titles are sorted: without a leading "The", "A" or "An".
func sortTitle(s string) string {
	n := normalize(s)
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(n, a) && len(n) > len(a) {
			return n[len(a):]
		}
	}
	return n
}

// similarity scores two titles from 0 (nothing alike) to 1 (the same).
func similarity(a, b string) float64 {
	na, nb := normalize(a), normalize(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	// Leading articles are often dropped in folder names.
	if sortTitle(a) == sortTitle(b) {
		return 0.97
	}
	// Folder names often drop a subtitle: "Gremlins 2" for "Gremlins 2:
	// The New Batch". Only for titles of two or more words, so "Up" doesn't
	// match "Up in the Air".
	short, long := na, nb
	if len(short) > len(long) {
		short, long = long, short
	}
	if strings.Count(short, " ") >= 1 && strings.HasPrefix(long, short+" ") {
		return 0.9
	}
	d := levenshtein([]rune(na), []rune(nb))
	longest := max(len([]rune(na)), len([]rune(nb)))
	return 1 - float64(d)/float64(longest)
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// yearScore scores how well a candidate's year fits the folder's.
func yearScore(want, got int) float64 {
	switch {
	case want == 0:
		return 0.6 // the folder doesn't say; neither helps nor hurts much
	case got == 0:
		return 0.5
	case want == got:
		return 1
	case want-got == 1 || got-want == 1:
		// Release years differ between countries, so one year off can be
		// the same film, but an exact year is clearly better.
		return 0.6
	}
	return 0
}

// score combines title and year into one number.
func score(title string, year int, cand metadata.MovieResult, altTitles ...string) float64 {
	ts := max(similarity(title, cand.Title), similarity(title, cand.OriginalTitle))
	for _, alt := range altTitles {
		ts = max(ts, similarity(title, alt))
	}
	ys := yearScore(year, cand.Year())
	if ys == 0 {
		return ts * 0.4 // right title, wrong year: almost certainly another film
	}
	return 0.75*ts + 0.25*ys
}

// rank scores search results for a title/year and returns them best first.
func rank(title string, year int, results []metadata.MovieResult) []Candidate {
	out := make([]Candidate, 0, len(results))
	for _, r := range results {
		out = append(out, Candidate{
			TMDBID: r.ID, Title: r.Title, Year: r.Year(), Overview: snippet(r.Overview),
			PosterPath: r.PosterPath, Score: score(title, year, r), Votes: r.VoteCount,
		})
	}
	// Equal scores: the better-known film first.
	pop := map[int]float64{}
	for _, r := range results {
		pop[r.ID] = r.Popularity
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return pop[out[i].TMDBID] > pop[out[j].TMDBID]
	})
	return out
}

// Popularity tiebreak: when several films fit equally well, one with at
// least this many TMDB votes and this many times more than any other is
// taken to be the one meant. TMDB is full of obscure films sharing a famous
// film's title and year.
const (
	tiebreakMinVotes = 50
	tiebreakRatio    = 10
)

// decide picks the winner from ranked candidates, or explains why not.
func decide(c []Candidate) (winner *Candidate, reason string) {
	switch {
	case len(c) == 0:
		return nil, "TMDB has nothing by this name."
	case c[0].Score < acceptScore:
		return nil, "No candidate is a close enough match."
	case len(c) == 1 || c[0].Score-c[1].Score >= acceptMargin:
		return &c[0], ""
	}
	// Several fit about equally well: is one far better known?
	var tied []*Candidate
	for i := range c {
		if c[0].Score-c[i].Score < acceptMargin && c[i].Score >= acceptScore {
			tied = append(tied, &c[i])
		}
	}
	best, second := tied[0], 0
	for _, t := range tied {
		if t.Votes > best.Votes {
			best = t
		}
	}
	for _, t := range tied {
		if t != best && t.Votes > second {
			second = t.Votes
		}
	}
	if best.Votes >= tiebreakMinVotes && best.Votes >= tiebreakRatio*max(second, 1) {
		return best, ""
	}
	return nil, "More than one film fits; please choose."
}

func snippet(s string) string {
	const n = 220
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
