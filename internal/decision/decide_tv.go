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
// the whole season as a pack, or (WantedSeasons) several whole seasons.
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

	// Seasons are all the series' seasons, by number: what a pack of
	// several holds, for its size limit, and which seasons exist, so a
	// range naming one the series doesn't have is turned down.
	Seasons map[int]SeasonInfo
	// WantedSeasons makes this a search for several whole seasons at once
	// (searching asks once for the whole show, 2026-11-28): a release
	// holding two or more of them is accepted, and so is a pack of one of
	// them; single episodes are left to the season searches. Season,
	// Episodes, Runtime and SeasonEpisodes are unused then.
	WantedSeasons []int
	// Ended: the series has finished, so a "Complete Series" release can
	// hold every season. A running show's "complete" pack predates its
	// latest season, so it isn't taken for one.
	Ended bool
}

// SeasonInfo is one season of a series, for packs of several seasons.
type SeasonInfo struct {
	Episodes int
	// Minutes is the season's running time, 0 when an episode's is
	// unknown.
	Minutes int
}

// Covers is what a TV release holds: a whole season, several whole
// seasons, these episodes, or a daily show's episode from this date.
type Covers struct {
	Season int  `json:"season"`
	Pack   bool `json:"pack,omitempty"`
	// LastSeason, for a pack of several seasons, is the last of them;
	// Season is the first.
	LastSeason int `json:"lastSeason,omitempty"`
	// AllSeasons is a "Complete Series" pack: every season but the
	// specials.
	AllSeasons bool   `json:"allSeasons,omitempty"`
	Episodes   []int  `json:"episodes,omitempty"`
	AirDate    string `json:"airDate,omitempty"`
}

// MultiSeason reports whether the release holds several whole seasons.
func (c Covers) MultiSeason() bool { return c.AllSeasons || c.LastSeason > c.Season }

// HoldsSeason reports whether the release holds season n whole.
func (c Covers) HoldsSeason(n int) bool {
	switch {
	case c.AllSeasons:
		return n > 0
	case c.MultiSeason():
		return n >= c.Season && n <= c.LastSeason
	}
	return c.Pack && n == c.Season
}

// SeasonsText names the seasons of a pack: "season 2", "seasons 1 to 5",
// "every season".
func (c Covers) SeasonsText() string {
	switch {
	case c.AllSeasons:
		return "every season"
	case c.MultiSeason():
		return fmt.Sprintf("seasons %d to %d", c.Season, c.LastSeason)
	}
	return fmt.Sprintf("season %d", c.Season)
}

// Breadth is how many wanted seasons a release holds whole (0 for single
// episodes). Searching takes the broadest acceptable release first: one
// grab instead of many.
func (t EpisodeTarget) Breadth(c Covers) int {
	if !c.Pack {
		return 0
	}
	if len(t.WantedSeasons) == 0 {
		if c.HoldsSeason(t.Season) {
			return 1
		}
		return 0
	}
	n := 0
	for _, s := range t.WantedSeasons {
		if c.HoldsSeason(s) {
			n++
		}
	}
	return n
}

// missingSeason is a season a pack names that the series doesn't have
// (0 when every one exists, or the series' seasons aren't known).
func (t EpisodeTarget) missingSeason(c Covers) int {
	if len(t.Seasons) == 0 || c.AllSeasons {
		return 0
	}
	for n := c.Season; n <= max(c.LastSeason, c.Season); n++ {
		if _, ok := t.Seasons[n]; !ok {
			return n
		}
	}
	return 0
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
	several := len(t.WantedSeasons) > 0
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
	case e.MultiSeason():
		cov = Covers{Season: max(e.Season, 0), LastSeason: e.LastSeason, AllSeasons: e.Complete, Pack: true}
		switch {
		case t.missingSeason(cov) > 0:
			reject("It's %s, but %s has no season %d.", cov.SeasonsText(), s.Title, t.missingSeason(cov))
			d.WrongContent = true
		case cov.AllSeasons && !t.Ended:
			reject("It says it's the complete series, but %s is still running, so it can't hold every season.", s.Title)
		case !several && cov.HoldsSeason(t.Season):
			reject("It's %s; only season %d is wanted as a whole.", cov.SeasonsText(), t.Season)
		case !several:
			reject("It's %s; this search is for season %d.", cov.SeasonsText(), t.Season)
			d.WrongContent = true
		case t.Breadth(cov) == 0:
			reject("It's %s, which holds none of the seasons wanted.", cov.SeasonsText())
		case t.Breadth(cov) == 1:
			reject("It's %s, which holds only one of the seasons wanted; that season is searched for on its own.", cov.SeasonsText())
		}
	case several && len(e.Episodes) == 0:
		cov = Covers{Season: e.Season, Pack: true}
		if !slices.Contains(t.WantedSeasons, e.Season) {
			reject("It's season %d, which isn't wanted as a whole.", e.Season)
		}
	case several:
		cov = Covers{Season: e.Season, Episodes: e.Episodes}
		reject("It's %s of season %d; this search is for whole seasons.", episodeList(e.Episodes), e.Season)
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
	if cov.Pack && (cov.MultiSeason() || len(t.WantedSeasons) > 0) {
		return seasonsRuntime(cov, t)
	}
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

// seasonsRuntime is coveredRuntime for a pack judged season by season:
// the minutes of every season it holds ("5 seasons, 56 episodes (3,100
// minutes)"). 0 when a season's runtime is unknown.
func seasonsRuntime(cov Covers, t EpisodeTarget) (int, string) {
	minutes, episodes, seasons := 0, 0, 0
	for n, info := range t.Seasons {
		if !cov.HoldsSeason(n) {
			continue
		}
		if info.Minutes <= 0 {
			return 0, ""
		}
		minutes += info.Minutes
		episodes += info.Episodes
		seasons++
	}
	if minutes == 0 {
		return 0, ""
	}
	return minutes, fmt.Sprintf("%s, %s (%d minutes)", plural(seasons, "season"), plural(episodes, "episode"), minutes)
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
