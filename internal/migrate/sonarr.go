package migrate

// Import from Sonarr: the same two steps as Radarr's (a preview that
// changes nothing, then the import), for TV. What only Sonarr knows is
// which episodes it was monitoring, including old ones it never got.
// Reelkeeper's own scan found the shows and files, and each series wants
// only new episodes until this brings Sonarr's choices over.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
)

// SeriesLibrary is what the Sonarr import needs from the library, besides
// what Radarr's import uses too.
type SeriesLibrary interface {
	ProfileSeries(ctx context.Context, profileID int64, f library.ProfileSeriesFilter) ([]library.ProfileSeries, error)
	AddSeries(ctx context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*library.SeriesDetail, error)
	ApplyMonitorPreset(ctx context.Context, versionID int64, preset string) error
	ApplySeriesMonitoring(ctx context.Context, versionID int64, m library.SeriesMonitoring) error
	SetEpisodesMonitored(ctx context.Context, versionID int64, episodeIDs []int64, monitored bool) error
	EpisodeOverrides(ctx context.Context, versionID int64) (map[int64]bool, error)
	Today() string
}

// WantedSeason is one season of a series' episodes the import will make
// Reelkeeper look for.
type WantedSeason struct {
	Season   int `json:"season"`
	Episodes int `json:"episodes"`
}

// WantedSeries is a series with episodes the import will make Reelkeeper
// look for, by season.
type WantedSeries struct {
	Title   string         `json:"title"`
	Year    int            `json:"year"`
	Seasons []WantedSeason `json:"seasons"`
}

// SonarrReport is what a preview found (and, after an import, what it did).
// Series Sonarr has that Reelkeeper doesn't yet (ToAdd) have no episodes to
// compare until they're added, so the episode counts below leave them out
// in a preview; an import adds them in.
type SonarrReport struct {
	// Instance is Sonarr's instance name and Version its version.
	Instance string `json:"instance"`
	Version  string `json:"version"`

	Series          int `json:"series"`
	ToAdd           int `json:"toAdd"` // not in this Reelkeeper profile yet
	MonitoredSeries int `json:"monitoredSeries"`
	Episodes        int `json:"episodes"` // every episode Sonarr has

	// Matched are the episodes found in Reelkeeper by season and number,
	// MatchedByDate those found only by air date (the two apps number them
	// differently), Unmatched those found neither way.
	Matched       int `json:"matched"`
	MatchedByDate int `json:"matchedByDate"`
	Unmatched     int `json:"unmatched"`

	// NewlyWanted are episodes Sonarr monitors that have aired and
	// Reelkeeper has no file for and doesn't want yet; AlreadyWanted the
	// same ones Reelkeeper already wants. TurnedOff are matched episodes
	// Reelkeeper wants and Sonarr doesn't monitor.
	NewlyWanted   int `json:"newlyWanted"`
	AlreadyWanted int `json:"alreadyWanted"`
	TurnedOff     int `json:"turnedOff"`

	History   int            `json:"history"`
	Blocklist int            `json:"blocklist"`
	Wanted    []WantedSeries `json:"wanted"`
	Issues    []Issue        `json:"issues"`

	Imported          bool `json:"imported"`
	Added             int  `json:"added"`
	MonitorChanged    int  `json:"monitorChanged"`
	EpisodesTurnedOn  int  `json:"episodesTurnedOn"`
	EpisodesTurnedOff int  `json:"episodesTurnedOff"`
	HistoryAdded      int  `json:"historyAdded"`
	BlocklistAdded    int  `json:"blocklistAdded"`
}

// Sonarr's answers, only the fields used. Never the history's download
// URLs: they carry Prowlarr's API key.
type sSeries struct {
	ID              int64  `json:"id"`
	TvdbID          int    `json:"tvdbId"`
	TmdbID          int    `json:"tmdbId"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	Monitored       bool   `json:"monitored"`
	MonitorNewItems string `json:"monitorNewItems"`
	Path            string `json:"path"`
	RootFolderPath  string `json:"rootFolderPath"`
	Tags            []int  `json:"tags"`
}

type sEpisode struct {
	ID            int64  `json:"id"`
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	AirDate       string `json:"airDate"`
	Title         string `json:"title"`
	Monitored     bool   `json:"monitored"`
	HasFile       bool   `json:"hasFile"`
}

func (e sEpisode) label() string { return fmt.Sprintf("S%02dE%02d", e.SeasonNumber, e.EpisodeNumber) }

type sRecord struct {
	ID          int64  `json:"id"`
	SeriesID    int64  `json:"seriesId"`
	EpisodeID   int64  `json:"episodeId"`
	SourceTitle string `json:"sourceTitle"`
	EventType   string `json:"eventType"`
	Date        string `json:"date"`
	Message     string `json:"message"`
	Indexer     string `json:"indexer"`
	Quality     struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Data struct {
		Indexer         string `json:"indexer"`
		ImportedPath    string `json:"importedPath"`
		Reason          string `json:"reason"`
		Message         string `json:"message"`
		TorrentInfoHash string `json:"torrentInfoHash"`
	} `json:"data"`
	TorrentInfoHash string `json:"torrentInfoHash"`
}

type sonarrSnapshot struct {
	instance  string
	version   string
	series    []sSeries
	episodes  map[int64][]sEpisode // by Sonarr series ID
	tags      map[int]string
	history   []sRecord
	blocklist []sRecord
}

func (s *Service) readSonarr(ctx context.Context, o Options) (*sonarrSnapshot, error) {
	const app = "Sonarr"
	var status struct {
		AppName      string `json:"appName"`
		InstanceName string `json:"instanceName"`
		Version      string `json:"version"`
	}
	if err := s.get(ctx, app, o, "system/status", &status); err != nil {
		return nil, err
	}
	if status.AppName != app {
		return nil, arrFailure(app, "That address is %s, not Sonarr", firstNonEmpty(status.AppName, "something else"))
	}
	snap := &sonarrSnapshot{instance: firstNonEmpty(status.InstanceName, app), version: status.Version,
		tags: map[int]string{}, episodes: map[int64][]sEpisode{}}
	if err := s.get(ctx, app, o, "series", &snap.series); err != nil {
		return nil, err
	}
	for _, sr := range snap.series {
		var eps []sEpisode
		if err := s.get(ctx, app, o, fmt.Sprintf("episode?seriesId=%d", sr.ID), &eps); err != nil {
			return nil, err
		}
		snap.episodes[sr.ID] = eps
	}
	var tags []rTag
	if err := s.get(ctx, app, o, "tag", &tags); err != nil {
		return nil, err
	}
	for _, t := range tags {
		snap.tags[t.ID] = t.Label
	}
	var err error
	if snap.history, err = all[sRecord](ctx, s, app, o, "history"); err != nil {
		return nil, err
	}
	if snap.blocklist, err = all[sRecord](ctx, s, app, o, "blocklist"); err != nil {
		return nil, err
	}
	return snap, nil
}

// PreviewSonarr reads Sonarr and reports, changing nothing.
func (s *Service) PreviewSonarr(ctx context.Context, o Options) (*SonarrReport, error) {
	snap, err := s.readSonarr(ctx, o)
	if err != nil {
		return nil, err
	}
	rep, _, err := s.compareSonarr(ctx, o, snap)
	return rep, err
}

// seriesWork is what the comparison worked out for one Sonarr series.
type seriesWork struct {
	sr   sSeries
	ps   *library.ProfileSeries // nil: not in Reelkeeper's profile yet
	root *library.RootFolder    // where it would be added (series not in Reelkeeper only)
	plan *seriesPlan            // nil until there are Reelkeeper episodes to compare
}

// compareSonarr builds the report, and the per-series work an import does.
func (s *Service) compareSonarr(ctx context.Context, o Options, snap *sonarrSnapshot) (*SonarrReport, []*seriesWork, error) {
	rep := &SonarrReport{Instance: snap.instance, Version: snap.version, Series: len(snap.series),
		History: len(snap.history), Blocklist: len(snap.blocklist), Wanted: []WantedSeries{}, Issues: []Issue{}}
	pss, err := s.lib.ProfileSeries(ctx, o.ProfileID, library.ProfileSeriesFilter{})
	if err != nil {
		return nil, nil, err
	}
	byTMDB, byTVDB := map[int]int{}, map[int]int{}
	for i, ps := range pss {
		byTMDB[ps.Title.TMDBID] = i
		if ps.Title.TVDBID > 0 {
			byTVDB[ps.Title.TVDBID] = i
		}
	}
	roots, err := s.lib.RootFolders(ctx)
	if err != nil {
		return nil, nil, err
	}
	today := s.lib.Today()
	inSonarr := map[int64]bool{} // Reelkeeper version IDs Sonarr has
	var works []*seriesWork
	for _, sr := range snap.series {
		eps := snap.episodes[sr.ID]
		rep.Episodes += len(eps)
		if sr.Monitored {
			rep.MonitoredSeries++
		}
		issue := func(kind, detail string) {
			rep.Issues = append(rep.Issues, Issue{TMDBID: sr.TmdbID, Title: sr.Title, Year: sr.Year, Kind: kind, Detail: detail})
		}
		idx, ok := -1, false
		if sr.TmdbID > 0 {
			idx, ok = byTMDB[sr.TmdbID]
		}
		if !ok && sr.TvdbID > 0 {
			idx, ok = byTVDB[sr.TvdbID]
		}
		w := &seriesWork{sr: sr}
		works = append(works, w)
		if !ok {
			rep.ToAdd++
			w.root = seriesRootFor(roots, sr.RootFolderPath)
			withFiles := 0
			for _, e := range eps {
				if e.HasFile {
					withFiles++
				}
			}
			switch {
			case w.root == nil:
				issue("no-media-folder", fmt.Sprintf("Sonarr keeps it in %s, which isn't one of Reelkeeper's TV folders, so it can't be brought over.", sr.RootFolderPath))
			case sr.TmdbID <= 0:
				issue("missing-in-reelkeeper", "Sonarr has no TMDB ID for it, so Reelkeeper can't add it.")
				w.root = nil
			default:
				issue("missing-in-reelkeeper", fmt.Sprintf("Sonarr has it (%s with files); Reelkeeper's scan didn't find it. Importing adds it.",
					plural(withFiles, "episode")))
			}
			continue
		}
		w.ps = &pss[idx]
		inSonarr[w.ps.Version.ID] = true
		overrides, err := s.lib.EpisodeOverrides(ctx, w.ps.Version.ID)
		if err != nil {
			return nil, nil, err
		}
		w.plan = planSeries(sr, *w.ps, overrides, eps, today)
		rep.addPlan(sr, w.plan, issue)
	}
	for _, ps := range pss {
		if !inSonarr[ps.Version.ID] {
			rep.Issues = append(rep.Issues, Issue{TMDBID: ps.Title.TMDBID, Title: ps.Title.Title, Year: ps.Title.Year, Kind: "only-in-reelkeeper",
				Detail: "Reelkeeper has it in this profile; Sonarr doesn't."})
		}
	}
	rep.sortWanted()
	return rep, works, nil
}

// addPlan counts one series' plan into the report and raises its issues.
func (r *SonarrReport) addPlan(sr sSeries, p *seriesPlan, issue func(kind, detail string)) {
	r.Matched += p.byNumber
	r.MatchedByDate += p.byDate
	r.Unmatched += len(p.unmatched)
	r.NewlyWanted += len(p.newly)
	r.AlreadyWanted += p.already
	r.TurnedOff += p.turnedOff
	if len(p.newly) > 0 {
		r.Wanted = append(r.Wanted, wantedOf(sr, p.newly))
	}
	if p.byDate > 0 {
		issue("numbered-differently", fmt.Sprintf("%s matched by air date: Sonarr and Reelkeeper number them differently.",
			plural(p.byDate, "episode")))
	}
	if n := len(p.unmatched); n > 0 {
		labels := make([]string, n)
		for i, e := range p.unmatched {
			labels[i] = e.label()
		}
		issue("unmatched-episodes", fmt.Sprintf("%s couldn't be matched, so their monitoring wasn't brought over: %s.",
			plural(n, "Sonarr episode"), firstN(labels, 10)))
	}
	if n := len(p.filesDiffer); n > 0 {
		issue("files-differ", fmt.Sprintf("Sonarr has files for %s Reelkeeper has none for: %s.", plural(n, "episode"), firstN(p.filesDiffer, 10)))
	}
}

func (r *SonarrReport) sortWanted() {
	sort.SliceStable(r.Wanted, func(i, j int) bool {
		a, b := strings.ToLower(r.Wanted[i].Title), strings.ToLower(r.Wanted[j].Title)
		if a != b {
			return a < b
		}
		return r.Wanted[i].Year < r.Wanted[j].Year
	})
}

func wantedOf(sr sSeries, newly []library.Episode) WantedSeries {
	perSeason := map[int]int{}
	for _, e := range newly {
		perSeason[*e.Season]++
	}
	w := WantedSeries{Title: sr.Title, Year: sr.Year, Seasons: []WantedSeason{}}
	for n, c := range perSeason {
		w.Seasons = append(w.Seasons, WantedSeason{Season: n, Episodes: c})
	}
	sort.Slice(w.Seasons, func(i, j int) bool { return w.Seasons[i].Season < w.Seasons[j].Season })
	return w
}

// firstN lists up to n items, saying how many more there are.
func firstN(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func seriesRootFor(roots []library.RootFolder, p string) *library.RootFolder {
	p = strings.TrimRight(p, "/")
	for i := range roots {
		if roots[i].MediaType == library.Series && strings.TrimRight(roots[i].Path, "/") == p {
			return &roots[i]
		}
	}
	return nil
}

// seriesPlan is how one Sonarr series' episodes line up with Reelkeeper's,
// and what bringing its monitoring over changes.
type seriesPlan struct {
	// matches maps a Sonarr episode ID to the Reelkeeper episode ID.
	matches   map[int64]int64
	byNumber  int
	byDate    int
	unmatched []sEpisode // sorted by season and number

	newly       []library.Episode // monitored in Sonarr, aired, no file, not wanted yet
	already     int               // the same, but already wanted
	turnedOff   int               // wanted in Reelkeeper, not monitored in Sonarr
	turnedOn    int               // not wanted in Reelkeeper, monitored in Sonarr
	filesDiffer []string          // Sonarr has a file, Reelkeeper none

	// What to write: the episodes whose choice must change, given how the
	// version will look once its new-episode rule is settled.
	writeOn, writeOff []int64
}

// planSeries matches Sonarr's episodes of one series to Reelkeeper's and
// works out what to change. Sonarr numbers episodes in TVDB order and
// Reelkeeper in the series' chosen order, so numbers alone can't be
// trusted: an episode is matched by season and number when the air dates
// agree too, otherwise by air date if exactly one Reelkeeper episode has it,
// otherwise not at all. Nothing is guessed beyond that.
func planSeries(sr sSeries, ps library.ProfileSeries, overrides map[int64]bool, eps []sEpisode, today string) *seriesPlan {
	p := &seriesPlan{matches: map[int64]int64{}}
	type sn struct{ season, number int }
	byNumber := map[sn]library.Episode{}
	byDate := map[string][]library.Episode{}
	for _, e := range ps.Episodes {
		byNumber[sn{*e.Season, *e.Number}] = e
		if e.AirDate != "" {
			byDate[e.AirDate] = append(byDate[e.AirDate], e)
		}
	}
	rkOf := map[int64]library.Episode{}
	claimed := map[int64]bool{} // Reelkeeper episodes matched by number
	var rest []sEpisode
	for _, e := range eps {
		if rk, ok := byNumber[sn{e.SeasonNumber, e.EpisodeNumber}]; ok && datesAgree(e.AirDate, rk.AirDate) && !claimed[rk.ID] {
			p.matches[e.ID], rkOf[e.ID] = rk.ID, rk
			claimed[rk.ID] = true
			p.byNumber++
			continue
		}
		rest = append(rest, e)
	}
	// By air date, only when exactly one Reelkeeper episode has the date
	// and no other Sonarr episode wants the same one.
	wantedBy := map[int64]int{}
	pick := map[int64]library.Episode{}
	for _, e := range rest {
		if cands := byDate[e.AirDate]; e.AirDate != "" && len(cands) == 1 && !claimed[cands[0].ID] {
			pick[e.ID] = cands[0]
			wantedBy[cands[0].ID]++
		}
	}
	for _, e := range rest {
		rk, ok := pick[e.ID]
		if !ok || wantedBy[rk.ID] != 1 {
			p.unmatched = append(p.unmatched, e)
			continue
		}
		p.matches[e.ID], rkOf[e.ID] = rk.ID, rk
		p.byDate++
	}
	sort.Slice(p.unmatched, func(i, j int) bool {
		a, b := p.unmatched[i], p.unmatched[j]
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		return a.EpisodeNumber < b.EpisodeNumber
	})

	// Once imported, "new items: none" turns the version's date rule off.
	after := ps.Version
	if sr.MonitorNewItems == "none" {
		after.MonitorFrom = nil
	}
	sorted := append([]sEpisode(nil), eps...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		return a.EpisodeNumber < b.EpisodeNumber
	})
	for _, e := range sorted {
		if _, ok := p.matches[e.ID]; !ok {
			continue
		}
		rk := rkOf[e.ID]
		now := library.Monitored(ps.Version, rk, overrides)
		will := library.Monitored(after, rk, overrides)
		if e.HasFile && !ps.HasFile[rk.ID] {
			p.filesDiffer = append(p.filesDiffer, e.label())
		}
		if e.Monitored != will {
			if e.Monitored {
				p.writeOn = append(p.writeOn, rk.ID)
			} else {
				p.writeOff = append(p.writeOff, rk.ID)
			}
		}
		switch {
		case e.Monitored && !now:
			p.turnedOn++
		case !e.Monitored && now:
			p.turnedOff++
		}
		// Wanted means: monitored, aired, no file in Reelkeeper. Only a
		// monitored series is searched at all.
		if e.Monitored && sr.Monitored && rk.AirDate != "" && rk.AirDate <= today && !ps.HasFile[rk.ID] {
			if now {
				p.already++
			} else {
				p.newly = append(p.newly, rk)
			}
		}
	}
	return p
}

// datesAgree: equal, a day apart (time zones), or either unknown.
func datesAgree(a, b string) bool {
	if a == "" || b == "" || a == b {
		return true
	}
	ta, err1 := time.Parse("2006-01-02", a)
	tb, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return false
	}
	d := ta.Sub(tb)
	return d >= -24*time.Hour && d <= 24*time.Hour
}

// ImportSonarr brings Sonarr's series, monitoring, tags, history and
// blocklist over into the profile, and returns the report. It searches for
// nothing: the scheduled pass finds the newly wanted episodes by itself.
func (s *Service) ImportSonarr(ctx context.Context, o Options) (*SonarrReport, error) {
	snap, err := s.readSonarr(ctx, o)
	if err != nil {
		return nil, err
	}
	rep, works, err := s.compareSonarr(ctx, o, snap)
	if err != nil {
		return nil, err
	}
	source := snap.instance
	today := s.lib.Today()
	versionOf := map[int64]*library.Version{} // Sonarr series ID → Reelkeeper version
	matchesOf := map[int64]map[int64]int64{}  // Sonarr series ID → Sonarr episode ID → Reelkeeper episode ID
	for _, w := range works {
		sr := w.sr
		if w.ps == nil {
			if w.root == nil {
				continue
			}
			// Not in Reelkeeper: add it, wanting only what Sonarr's
			// episode choices say (below), then compare its episodes.
			d, err := s.lib.AddSeries(ctx, sr.TmdbID, o.ProfileID, w.root.ID, sr.Monitored)
			if err != nil {
				return nil, fmt.Errorf("%s (%d): %w", sr.Title, sr.Year, err)
			}
			rep.Added++
			vid := versionIDIn(d, o.ProfileID)
			preset := library.MonitorFuture
			if sr.MonitorNewItems == "none" {
				preset = library.MonitorNone
			}
			if err := s.lib.ApplyMonitorPreset(ctx, vid, preset); err != nil {
				return nil, err
			}
			list, err := s.lib.ProfileSeries(ctx, o.ProfileID, library.ProfileSeriesFilter{VersionID: vid})
			if err != nil {
				return nil, err
			}
			if len(list) != 1 {
				return nil, fmt.Errorf("%s (%d): added, but not found again", sr.Title, sr.Year)
			}
			w.ps = &list[0]
			overrides, err := s.lib.EpisodeOverrides(ctx, vid)
			if err != nil {
				return nil, err
			}
			w.plan = planSeries(sr, *w.ps, overrides, snap.episodes[sr.ID], today)
			rep.addPlan(sr, w.plan, func(kind, detail string) {
				rep.Issues = append(rep.Issues, Issue{TMDBID: sr.TmdbID, Title: sr.Title, Year: sr.Year, Kind: kind, Detail: detail})
			})
			rep.sortWanted()
		}
		v := w.ps.Version
		change := library.SeriesMonitoring{}
		if v.Monitored != sr.Monitored {
			m := sr.Monitored
			change.Monitored = &m
			rep.MonitorChanged++
		}
		if sr.MonitorNewItems == "none" && v.MonitorFrom != nil {
			off := false
			change.NewEpisodes = &off
		}
		if change.Monitored != nil || change.NewEpisodes != nil {
			if err := s.lib.ApplySeriesMonitoring(ctx, v.ID, change); err != nil {
				return nil, err
			}
		}
		if err := s.lib.SetEpisodesMonitored(ctx, v.ID, w.plan.writeOn, true); err != nil {
			return nil, err
		}
		if err := s.lib.SetEpisodesMonitored(ctx, v.ID, w.plan.writeOff, false); err != nil {
			return nil, err
		}
		rep.EpisodesTurnedOn += w.plan.turnedOn
		rep.EpisodesTurnedOff += w.plan.turnedOff
		versionOf[sr.ID], matchesOf[sr.ID] = &v, w.plan.matches
		if err := s.importTags(ctx, v.ID, sr.Tags, snap.tags); err != nil {
			return nil, err
		}
	}
	for _, r := range snap.history {
		v := versionOf[r.SeriesID]
		if v == nil {
			continue
		}
		fresh, err := s.claim(ctx, source+"/history", r.ID)
		if err != nil {
			return nil, err
		}
		if !fresh {
			continue
		}
		at, _ := time.Parse(time.RFC3339, r.Date)
		tid, vid := v.TitleID, v.ID
		ev := &grab.Event{CreatedAt: at.UTC(), TitleID: &tid, VersionID: &vid, Event: grab.EventFromSonarr,
			SourceTitle: r.SourceTitle, Indexer: r.Data.Indexer, Quality: r.Quality.Quality.Name,
			Message: sonarrHistoryMessage(source, r)}
		data := grab.EventData{Trigger: "sonarr"}
		if id, ok := matchesOf[r.SeriesID][r.EpisodeID]; ok {
			data.EpisodeIDs = []int64{id}
		}
		if err := s.history.Add(ctx, ev, data); err != nil {
			return nil, err
		}
		rep.HistoryAdded++
	}
	for _, r := range snap.blocklist {
		v := versionOf[r.SeriesID]
		if v == nil {
			continue
		}
		fresh, err := s.claim(ctx, source+"/blocklist", r.ID)
		if err != nil {
			return nil, err
		}
		if !fresh {
			continue
		}
		reason := "Blocklisted in " + source
		if r.Message != "" {
			reason += ": " + r.Message
		}
		if err := s.blocklist(ctx, download.BlockedRelease{TitleID: v.TitleID, ReleaseTitle: r.SourceTitle,
			Hash: strings.ToLower(firstNonEmpty(r.TorrentInfoHash, r.Data.TorrentInfoHash)), Indexer: r.Indexer,
			Quality: r.Quality.Quality.Name, Reason: reason, Rule: download.BlockSonarr}); err != nil {
			return nil, err
		}
		rep.BlocklistAdded++
	}
	rep.Imported = true
	return rep, nil
}

// versionIDIn finds the version a just-added series has in the profile.
func versionIDIn(d *library.SeriesDetail, profileID int64) int64 {
	for _, v := range d.Versions {
		if v.ProfileID == profileID {
			return v.ID
		}
	}
	return 0
}

// sonarrHistoryMessage words a Sonarr history record for Reelkeeper's History.
func sonarrHistoryMessage(source string, r sRecord) string {
	what := map[string]string{
		"grabbed":                "grabbed",
		"downloadFolderImported": "imported",
		"episodeFileImported":    "imported",
		"downloadFailed":         "saw a download fail:",
		"episodeFileDeleted":     "deleted the file",
		"episodeFileRenamed":     "renamed the file",
		"downloadIgnored":        "ignored the download",
		"seriesFolderImported":   "imported the folder",
	}[r.EventType]
	if what == "" {
		what = r.EventType
	}
	msg := fmt.Sprintf("%s %s %s", source, what, r.SourceTitle)
	if r.Data.Indexer != "" && r.EventType == "grabbed" {
		msg += " from " + r.Data.Indexer
	}
	if r.Data.ImportedPath != "" {
		msg += " as " + r.Data.ImportedPath
	}
	if reason := firstNonEmpty(r.Data.Reason, r.Data.Message); reason != "" {
		msg += " (" + reason + ")"
	}
	return msg + "."
}
