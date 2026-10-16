package library

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Placing files on episodes. Each file's claim (what its name or season
// folder says) is looked up in the series' episodes, numbered in the
// series' chosen order. The rules, in plain words:
//
//   - The numbers in the name decide. Emby and Sonarr go by them too.
//   - A claim the order can't satisfy ("S01E14" when season 1 has 11
//     episodes) leaves the file unplaced, with a note saying so.
//   - When the name also gives the episode's title and that title clearly
//     belongs to another episode, the file stays where its numbers put it
//     but gets a note: that's the sign of a file named in a different
//     order (DVD instead of aired, say).
//   - Nothing is ever guessed: a doubtful file gets a note, not a link.
//
// placeFiles in series.go stores the result.

// placement is where one file goes.
type placement struct {
	Episodes []int64 // episode IDs; empty when the file couldn't be placed
	Note     string  // plain-English problem, "" when all is well
}

// epKey is a season and episode number in some order.
type epKey struct{ season, number int }

// episodeIndex looks episodes up by their numbers in one order.
type episodeIndex struct {
	byNumber map[epKey]*Episode
	byDate   map[string][]*Episode
	ordered  []*Episode // episodes the order places, by season and number
	seasons  []int      // the order's regular seasons (not Specials)
	label    string     // "TMDB's aired order", "the DVD Order"...
}

// indexEpisodes indexes episodes by their numbers in the order the
// numbering function gives (nil numbers: not in this order).
func indexEpisodes(eps []Episode, numbers func(*Episode) (season, number *int), label string) *episodeIndex {
	idx := &episodeIndex{byNumber: map[epKey]*Episode{}, byDate: map[string][]*Episode{}, label: label}
	seasons := map[int]bool{}
	for i := range eps {
		e := &eps[i]
		s, n := numbers(e)
		if s == nil || n == nil {
			continue
		}
		k := epKey{*s, *n}
		if _, dup := idx.byNumber[k]; dup {
			continue // a broken order placing two episodes on one number: keep the first
		}
		idx.byNumber[k] = e
		idx.ordered = append(idx.ordered, e)
		if e.AirDate != "" {
			idx.byDate[e.AirDate] = append(idx.byDate[e.AirDate], e)
		}
		if *s > 0 {
			seasons[*s] = true
		}
	}
	for s := range seasons {
		idx.seasons = append(idx.seasons, s)
	}
	sort.Ints(idx.seasons)
	return idx
}

// chosenNumbers numbers episodes by the series' chosen order.
func chosenNumbers(e *Episode) (*int, *int) { return e.Season, e.Number }

// place works out where one file goes.
func (idx *episodeIndex) place(c EpisodeClaim) placement {
	switch {
	case c.AirDate != "":
		return idx.placeByDate(c)
	case len(c.Episodes) == 0 && c.Season >= 0:
		return placement{Note: fmt.Sprintf("The name gives a whole season (season %d) but not which episode.", c.Season)}
	case len(c.Episodes) == 0:
		return placement{Note: "Couldn't tell which episode this is from its name."}
	}

	season := c.Season
	if season < 0 {
		// "Episode 5" with no season anywhere: fine for a one-season show.
		if len(idx.seasons) != 1 {
			return placement{Note: fmt.Sprintf("The name gives episode %d but not the season, and it isn't in a Season folder.", c.Episodes[0])}
		}
		season = idx.seasons[0]
	}

	var p placement
	var found []*Episode
	var missing []string
	for _, n := range c.Episodes {
		if e := idx.byNumber[epKey{season, n}]; e != nil {
			p.Episodes = append(p.Episodes, e.ID)
			found = append(found, e)
		} else {
			missing = append(missing, code(season, n))
		}
	}
	if len(missing) > 0 {
		p.Note = fmt.Sprintf("%s has no %s. The file may be numbered in a different episode order.",
			capitalize(idx.label), joinWithAnd(missing))
		return p
	}
	p.Note = idx.titleCheck(c.Title, season, c.Episodes, found)
	return p
}

// titleCheck notes a file whose title clearly belongs to another episode.
// It stays quiet when the title fits, or when it fits nothing at all (a
// placeholder like "TBA", a translation, a typo).
func (idx *episodeIndex) titleCheck(title string, season int, numbers []int, found []*Episode) string {
	if title == "" || placeholderTitle.MatchString(title) {
		return ""
	}
	names := make([]string, 0, len(found))
	fit := 0.0
	for _, e := range found {
		names = append(names, e.Name)
		fit = max(fit, similarity(title, e.Name))
	}
	if len(names) > 1 {
		for _, sep := range []string{" and ", " & ", " + ", " / ", " "} {
			fit = max(fit, similarity(title, strings.Join(names, sep)))
		}
	}
	if fit >= 0.6 {
		return ""
	}
	var best *Episode
	bestFit := 0.0
	for _, e := range idx.ordered {
		if placeholderTitle.MatchString(e.Name) {
			continue
		}
		if f := similarity(title, e.Name); f > bestFit {
			best, bestFit = e, f
		}
	}
	if best == nil || bestFit < 0.9 {
		return ""
	}
	return fmt.Sprintf("Named %q, which is %s in %s, but numbered %s (%q). The file may follow a different episode order.",
		title, code(*best.Season, *best.Number), idx.label, code(season, numbers[0]), names[0])
}

func (idx *episodeIndex) placeByDate(c EpisodeClaim) placement {
	eps := idx.byDate[c.AirDate]
	switch {
	case len(eps) == 1:
		return placement{Episodes: []int64{eps[0].ID}}
	case len(eps) == 0:
		return placement{Note: fmt.Sprintf("TMDB lists no episode that aired on %s.", c.AirDate)}
	}
	// Several the same day: the title may say which.
	for _, e := range eps {
		if c.Title != "" && similarity(c.Title, e.Name) >= 0.9 {
			return placement{Episodes: []int64{e.ID}}
		}
	}
	return placement{Note: fmt.Sprintf("%d episodes aired on %s and the name doesn't say which one this is.", len(eps), c.AirDate)}
}

// Episode names TMDB uses before the real one is known.
var placeholderTitle = regexp.MustCompile(`(?i)^\s*(episode|chapter|part)\s*#?\d+\s*$|^\s*(tba|tbd|untitled)\s*$`)

// code writes an episode the usual way: S01E02.
func code(season, number int) string { return fmt.Sprintf("S%02dE%02d", season, number) }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
