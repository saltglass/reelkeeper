package decision

import (
	"fmt"
	"slices"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// Show is what the engine needs to know about the series being searched
// for.
type Show struct {
	Title     string
	Year      int
	AltTitles []string
	// OriginalLanguage as a Radarr language ID (see LanguageByCode).
	OriginalLanguage int
}

// EpisodeTarget is what one TV search wants: some episodes of one season,
// or the whole season as a pack.
type EpisodeTarget struct {
	Season int
	// Episodes are the wanted episode numbers in the season.
	Episodes []int
	// Runtime is each episode's length in minutes, by episode number, for
	// size limits (missing or 0 skips them).
	Runtime map[int]int
	// SeasonEpisodes is how many episodes the season has, which a season
	// pack holds.
	SeasonEpisodes int
	// AllowPack accepts a whole-season release. Searching decides when a
	// pack is worth it (most of a finished season wanted).
	AllowPack bool
	// AirDates, for a daily show, are the wanted episodes' air dates
	// ("2026-10-09"), since its releases are named by date.
	AirDates []string
}

// Covers is what a TV release holds: a whole season, these episodes, or a
// daily show's episode from this date.
type Covers struct {
	Season   int    `json:"season"`
	Pack     bool   `json:"pack,omitempty"`
	Episodes []int  `json:"episodes,omitempty"`
	AirDate  string `json:"airDate,omitempty"`
}

// DecideEpisode checks one release against a profile for a TV search.
// There's no "have": TV stops at the first acceptable file, so only episodes without a file are ever searched for. The
// one exception, a REPACK or PROPER that fixes a recent download, is
// Profile.CheckFix, applied by RSS sync on top of this.
//
// Like Decide, every check runs, so the reasons list everything wrong.
func DecideEpisode(p *Profile, s Show, t EpisodeTarget, c Candidate) (Decision, Covers) {
	r := parser.Parse(c.Name)
	m := Movie{Title: s.Title, Year: s.Year, AltTitles: s.AltTitles, OriginalLanguage: s.OriginalLanguage}
	d := Decision{
		Name: c.Name, Release: r, Quality: QualityOf(r), Revision: max(r.Revision, 1),
		Languages: languagesOf(r, m), seeders: c.Seeders, size: c.Size, Usenet: c.Usenet,
	}
	d.rank = p.rank(d.Quality)
	d.Score, d.Formats = p.Score(Input{
		Name: c.Name, Release: r, Quality: d.Quality, Languages: d.Languages,
		OriginalLanguage: s.OriginalLanguage, Size: c.Size, IndexerFlags: c.IndexerFlags,
	})
	reject := func(format string, args ...any) { d.Rejections = append(d.Rejections, fmt.Sprintf(format, args...)) }

	var cov Covers
	if why := wrongShow(r, s); why != "" {
		reject("%s", why)
		d.WrongContent = true
	}
	switch e := r.Episode; {
	case e != nil && e.AirDate != "":
		cov = Covers{Season: t.Season, AirDate: e.AirDate}
		if !slices.Contains(t.AirDates, e.AirDate) {
			reject("It's the episode from %s, which isn't wanted.", e.AirDate)
			d.WrongContent = true
		}
	case e == nil:
		reject("The name doesn't say which season and episode it is.")
		d.WrongContent = true
	case e.Season != t.Season:
		reject("It's season %d; this search is for season %d.", e.Season, t.Season)
		d.WrongContent = true
	case len(e.Episodes) == 0:
		cov = Covers{Season: e.Season, Pack: true}
		if !t.AllowPack {
			reject("It's the whole season; only %s wanted.", episodeList(t.Episodes))
		}
	default:
		cov = Covers{Season: e.Season, Episodes: e.Episodes}
		if !slices.ContainsFunc(e.Episodes, func(n int) bool { return slices.Contains(t.Episodes, n) }) {
			reject("It's %s, which isn't wanted.", episodeList(e.Episodes))
			d.WrongContent = true
		}
	}
	switch {
	case d.Quality.ID == 0:
		reject("Reelkeeper can't tell the quality from the name.")
		d.WrongContent = true // not a name it can make sense of
	case !p.Allowed(d.Quality):
		reject("%s isn't a quality this profile accepts.", d.Quality.Name)
	}
	if minutes, what := coveredRuntime(cov, t); minutes > 0 {
		if why := p.sizeProblem(d.Quality, c.Size, minutes, what); why != "" {
			reject("%s", why)
		}
	}
	if blocked := blockedFormats(d.Formats); len(blocked) > 0 {
		reject("It matches %s, which this profile blocks.", list(blocked))
	} else if d.Score < p.MinScore {
		reject("Its custom-format score is %d; this profile needs at least %d.", d.Score, p.MinScore)
	}
	for _, req := range p.Required {
		if !slices.ContainsFunc(d.Formats, func(f FormatMatch) bool { return slices.Contains(req.AnyOf, f.Name) }) {
			reject("It doesn't have %s, which this profile requires.", req.Label)
		}
	}
	if c.Blocklisted {
		reject("It's on the blocklist.")
	}
	if c.MinSeeders > 0 && c.Seeders < c.MinSeeders && !c.Usenet {
		reject("It has %s; at least %d needed.", plural(c.Seeders, "seeder"), c.MinSeeders)
	}
	d.Accepted = len(d.Rejections) == 0
	return d, cov
}

// SameShow reports whether a release name is for this series (title or an
// alternative title, and a year within one of the series' when it states
// one). RSS sync uses it to find which wanted series a release is for.
func SameShow(r parser.Release, s Show) bool { return wrongShow(r, s) == "" }

// wrongShow explains why the name isn't this series, or returns "".
func wrongShow(r parser.Release, s Show) string {
	if r.Title == "" {
		return "Reelkeeper can't tell which series this is from the name."
	}
	got := matchKey(r.Title)
	ok := got == matchKey(s.Title)
	for _, t := range s.AltTitles {
		ok = ok || got == matchKey(t)
	}
	// "Show 2019 S01E01": a year in the name tells remakes apart.
	if !ok && s.Year != 0 {
		ok = got == matchKey(fmt.Sprintf("%s %d", s.Title, s.Year))
	}
	switch {
	case !ok:
		return fmt.Sprintf("The name is for %q, not %s.", r.Title, s.Title)
	case r.Year != 0 && s.Year != 0 && abs(r.Year-s.Year) > 1:
		return fmt.Sprintf("The name says %d, but %s started in %d.", r.Year, s.Title, s.Year)
	}
	return ""
}

// coveredRuntime is the minutes a release holds, for size limits, and how
// to say it ("3 episodes (135 minutes)"). 0 when unknown.
func coveredRuntime(cov Covers, t EpisodeTarget) (int, string) {
	var eps []int
	switch {
	case cov.AirDate != "":
		eps = t.Episodes // a daily search is for one date's episode
	case cov.Pack:
		for n := 1; n <= t.SeasonEpisodes; n++ {
			eps = append(eps, n)
		}
	default:
		eps = cov.Episodes
	}
	total := 0
	for _, n := range eps {
		rt := t.Runtime[n]
		if rt <= 0 {
			return 0, "" // one unknown runtime makes the whole limit a guess
		}
		total += rt
	}
	if total == 0 {
		return 0, ""
	}
	if len(eps) == 1 {
		return total, fmt.Sprintf("a %d-minute episode", total)
	}
	return total, fmt.Sprintf("%d episodes (%d minutes)", len(eps), total)
}

// episodeList names episode numbers: "episode 3", "episodes 3 and 4",
// "episodes 3, 4 and 5".
func episodeList(eps []int) string {
	switch len(eps) {
	case 0:
		return "no episodes"
	case 1:
		return fmt.Sprintf("episode %d", eps[0])
	}
	out := ""
	for i, n := range eps {
		switch {
		case i == 0:
		case i == len(eps)-1:
			out += " and "
		default:
			out += ", "
		}
		out += fmt.Sprint(n)
	}
	return "episodes " + out
}
