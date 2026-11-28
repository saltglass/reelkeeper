package parser

import (
	"regexp"
	"strconv"
	"strings"
)

// Episode is what a TV file or folder name says about which episode(s) it
// holds. Names say it in many ways:
//
//	Show - S01E02 - Title [WEBDL-1080p]     season 1, episode 2, titled
//	Show.S05E01-E02.1080p, Show S01E01E02   one file, two episodes
//	Show.1x02.720p                          the older "1x02" style
//	Show.S01.Ep01, Show S01 E01             season and episode apart
//	Show.2024.03.15.Guest.720p              a daily show, by air date
//	Show.S02.1080p                          a whole season (a season pack)
//	Show.S01-S05.1080p, Show Season 1-5     several whole seasons
//	Show.Complete.Series.1080p              every season, unnumbered
//	Show E05, Show Ep05                     an episode without its season
//
// Which series it is, and the season when the name leaves it out, come
// from the folders around the file; the library works that out.
type Episode struct {
	// Season is the season number (0 for specials), or -1 when the name
	// gives an episode but no season ("Ep05").
	Season int `json:"season"`
	// Episodes are the episode numbers in the file, usually one. Empty for
	// a whole season or a daily episode.
	Episodes []int `json:"episodes,omitempty"`
	// AirDate is a daily show's air date, "2024-03-15".
	AirDate string `json:"airDate,omitempty"`
	// LastSeason, for a release of several whole seasons ("S01-S05",
	// "S01-05", "Season 1-5", "S01 S02 S03"), is the last of them; Season
	// is the first. 0 otherwise.
	LastSeason int `json:"lastSeason,omitempty"`
	// Complete marks a "Complete Series" release that doesn't number its
	// seasons: it claims every one. Season is -1 then.
	Complete bool `json:"complete,omitempty"`
	// Title is the episode's title when the name gives one: "Pilot" in
	// "Show - S01E01 - Pilot [WEBDL-1080p]". Used to spot files whose
	// numbers follow a different episode order.
	Title string `json:"title,omitempty"`
}

// MultiSeason reports whether the name is for several whole seasons (a
// range, or a complete series).
func (e *Episode) MultiSeason() bool {
	return e != nil && (e.LastSeason > max(e.Season, 0) || e.Complete)
}

var (
	// Several whole seasons in one token: S01-S05, S01-05.
	seasonRange = regexp.MustCompile(`^s(\d{1,3})-s?(\d{1,3})$`)
	// A range of numbers in one token, after "Season(s)": 1-5.
	numberRange = regexp.MustCompile(`^(\d{1,3})-(\d{1,3})$`)
	// S01E02, S01E02E03, S01E02-E03, S01E02-03.
	sxxeyy = regexp.MustCompile(`^s(\d{1,3})e(\d{1,4})((?:-?e\d{1,4})*)(?:-(\d{1,4}))?$`)
	// 1x02, 1x02-03, 1x02-1x03.
	nxnn = regexp.MustCompile(`^(\d{1,2})x(\d{2,3})(?:-(?:\d{1,2}x)?(\d{2,3}))?$`)
	// A season on its own: S01.
	sxx = regexp.MustCompile(`^s(\d{1,3})$`)
	// An episode on its own: E05, Ep05, E05-E06.
	exx = regexp.MustCompile(`^ep?(\d{1,4})((?:-?e?p?\d{1,4})*)$`)
	// More episode numbers after the first: "e03", "-e03", "-03".
	moreEps = regexp.MustCompile(`-?e?p?(\d{1,4})`)
	// A daily show's date written as one token: 2024-03-15.
	dashDate = regexp.MustCompile(`^((?:19|20)\d{2})-(\d{2})-(\d{2})$`)
	twoDigit = regexp.MustCompile(`^\d{2}$`)
	number   = regexp.MustCompile(`^\d{1,3}$`)
)

// episodeAt recognises an episode marker starting at token i. It returns
// what it found and how many tokens the marker spans (0 if none).
func episodeAt(tokens []token, i int) (*Episode, int) {
	t := tokens[i].lower
	next := ""
	if i+1 < len(tokens) {
		next = tokens[i+1].lower
	}

	if m := sxxeyy.FindStringSubmatch(t); m != nil {
		e := &Episode{Season: atoi(m[1]), Episodes: []int{atoi(m[2])}}
		e.Episodes = appendMore(e.Episodes, m[3], m[4])
		return e, 1
	}
	if m := nxnn.FindStringSubmatch(t); m != nil {
		e := &Episode{Season: atoi(m[1]), Episodes: []int{atoi(m[2])}}
		if m[3] != "" {
			e.Episodes = expand(e.Episodes[0], atoi(m[3]))
		}
		return e, 1
	}
	// "S01-S05", "S01-05": several seasons.
	if m := seasonRange.FindStringSubmatch(t); m != nil {
		return seasons(atoi(m[1]), atoi(m[2])), 1
	}
	// "S01 E01", "S01.Ep01", "S01 E01-E02", or "S01" alone (a season pack).
	if m := sxx.FindStringSubmatch(t); m != nil {
		season := atoi(m[1])
		if em := exx.FindStringSubmatch(next); em != nil {
			eps := appendMore([]int{atoi(em[1])}, em[2], "")
			return &Episode{Season: season, Episodes: eps}, 2
		}
		// "S01 S02 S03", or "S01 - S05" (a lone dash isn't a token): several
		// seasons, from the first to the last.
		n, last := 1, season
		for i+n < len(tokens) {
			mm := sxx.FindStringSubmatch(tokens[i+n].lower)
			if mm == nil {
				break
			}
			last = max(last, atoi(mm[1]))
			n++
		}
		if n > 1 {
			return seasons(season, last), n
		}
		return &Episode{Season: season}, 1
	}
	// "Season 1-5", "Seasons 1-5", "Seasons 1 to 5": several seasons.
	if t == "season" || t == "seasons" {
		if m := numberRange.FindStringSubmatch(next); m != nil {
			return seasons(atoi(m[1]), atoi(m[2])), 2
		}
		if i+3 < len(tokens) && number.MatchString(next) && tokens[i+2].lower == "to" && number.MatchString(tokens[i+3].lower) {
			return seasons(atoi(next), atoi(tokens[i+3].lower)), 4
		}
	}
	// "Season 2", "Season 2 Episode 5".
	if t == "season" && number.MatchString(next) {
		season := atoi(next)
		if i+3 < len(tokens) && tokens[i+2].lower == "episode" && number.MatchString(tokens[i+3].lower) {
			return &Episode{Season: season, Episodes: []int{atoi(tokens[i+3].lower)}}, 4
		}
		return &Episode{Season: season}, 2
	}
	// "Complete Series", "The Complete Series": every season. Only after
	// the title, so a show named "Complete Series" keeps its name.
	if i > 0 && t == "complete" && next == "series" {
		return &Episode{Season: -1, Complete: true}, 2
	}
	if i > 0 && t == "the" && next == "complete" && i+2 < len(tokens) && tokens[i+2].lower == "series" {
		return &Episode{Season: -1, Complete: true}, 3
	}
	// "E05", "Ep05" without a season. Only after other words, so a title
	// such as "E" or "Ep" can't be mistaken for one.
	if i > 0 {
		if em := exx.FindStringSubmatch(t); em != nil {
			return &Episode{Season: -1, Episodes: appendMore([]int{atoi(em[1])}, em[2], "")}, 1
		}
		if t == "episode" && number.MatchString(next) {
			return &Episode{Season: -1, Episodes: []int{atoi(next)}}, 2
		}
	}
	// Daily shows: "2024.03.15" (three tokens) or "2024-03-15" (one).
	// Never the first token, which would be the show's title.
	if i > 0 {
		if m := dashDate.FindStringSubmatch(t); m != nil {
			if d, ok := date(m[1], m[2], m[3]); ok {
				return &Episode{Season: -1, AirDate: d}, 1
			}
		}
		if yearRe.MatchString(t) && i+2 < len(tokens) && twoDigit.MatchString(next) && twoDigit.MatchString(tokens[i+2].lower) {
			if d, ok := date(t, next, tokens[i+2].lower); ok {
				return &Episode{Season: -1, AirDate: d}, 3
			}
		}
	}
	return nil, 0
}

// seasons is a release of seasons first to last. A backwards or
// implausible range ("S05-S01", "S01-S99") is read as its first season
// alone, so it is never taken for more than it says.
func seasons(first, last int) *Episode {
	if last <= first || last-first > 50 {
		return &Episode{Season: first}
	}
	return &Episode{Season: first, LastSeason: last}
}

// appendMore adds the episodes in a multi-episode suffix: "E03E04"
// lists them, "-E05" or "-05" ends a range.
func appendMore(eps []int, more, rangeEnd string) []int {
	for _, part := range moreEps.FindAllStringSubmatch(more, -1) {
		n := atoi(part[1])
		last := eps[len(eps)-1]
		if strings.HasPrefix(part[0], "-") {
			eps = append(eps[:len(eps)-1], expand(last, n)...)
		} else if n > last {
			eps = append(eps, n)
		}
	}
	if rangeEnd != "" {
		eps = append(eps[:len(eps)-1], expand(eps[len(eps)-1], atoi(rangeEnd))...)
	}
	return eps
}

// expand turns a range into its episodes: 1-3 is 1, 2, 3. Implausible
// ranges (backwards, or longer than a season) keep just the first.
func expand(from, to int) []int {
	if to <= from || to-from > 30 {
		return []int{from}
	}
	out := make([]int, 0, to-from+1)
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

func date(y, m, d string) (string, bool) {
	mm, dd := atoi(m), atoi(d)
	if mm < 1 || mm > 12 || dd < 1 || dd > 31 {
		return "", false
	}
	return y + "-" + m + "-" + d, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// findEpisode looks for the first episode marker before the quality
// markers start. It returns the episode and the token range it spans.
func findEpisode(tokens []token, firstMarker int) (e *Episode, start, end int) {
	for i := 0; i < len(tokens) && i < firstMarker; i++ {
		if e, n := episodeAt(tokens, i); e != nil {
			return e, i, i + n
		}
	}
	return nil, -1, -1
}

var seasonFolder = regexp.MustCompile(`(?i)^\s*(?:season|series|saison|staffel|temporada)[\s._-]*(\d{1,3})\b|^\s*s(\d{1,3})\s*$`)

// SeasonFolder reads a season folder's name: "Season 1", "Season 01",
// "S01", "Series 2" (British usage), "Specials" (season 0). It reports
// false for anything else.
func SeasonFolder(name string) (int, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "specials" || n == "special" || n == "season 0" || n == "extras (specials)" {
		return 0, true
	}
	if m := seasonFolder.FindStringSubmatch(n); m != nil {
		if m[1] != "" {
			return atoi(m[1]), true
		}
		return atoi(m[2]), true
	}
	return 0, false
}
