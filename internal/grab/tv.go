package grab

// TV works like movies (a search, the scheduled pass and RSS sync; every
// verdict in History; the Grabbing setting's own TV mode), in smaller
// pieces. A series version wants episodes (library.SeriesVersions), and
// they're searched for in units:
//
//   - a season pack, when a season has finished airing and more than half
//     of it is wanted: one search instead of many. Indexers answer a
//     season search with the season's single episodes too, so those are
//     still found when no pack is acceptable;
//   - otherwise each wanted episode on its own (a daily show's by air
//     date).
//
// An episode is searched for from the day after it airs (TMDB gives dates,
// not times); RSS sync catches same-day releases. Each episode remembers
// when it was last searched for, so the scheduled pass spreads TV searches
// out as it does movies. TV stops at the first acceptable file: an episode with a file is never searched for.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/parser"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// TV categories (Newznab standard): every indexer files TV under 5000 and
// its subcategories.
var tvCategories = []int{5000}

// TVGrab is what a TV grab is for.
type TVGrab struct {
	Title   library.Title
	Version library.Version
	// EpisodeIDs are the wanted episodes the release holds; importing
	// brings in only these.
	EpisodeIDs []int64
	// Label names them for people: "S02E05", "season 2".
	Label string
}

// tvActive reports whether TV searching runs (Dry run or On).
func tvActive(g settings.Grabbing) bool {
	return g.TVMode == settings.GrabDryRun || g.TVMode == settings.GrabOn
}

// tvWanted is a series version ready for decisions.
type tvWanted struct {
	sv      library.SeriesVersion
	profile *decision.Profile
	pname   string
	show    decision.Show
	// wanted are the version's wanted episodes, less those already
	// downloading.
	wanted []library.Episode
	// byID finds any of the series' episodes.
	byID map[int64]library.Episode
	// recent are the episodes whose present file came from a download
	// imported within decision.FixWindow, by episode, and so may be
	// replaced by a REPACK or PROPER. RSS sync fills it, only for profiles
	// that prefer propers (see fixes.go).
	recent map[int64]decision.Current
}

func (w *tvWanted) label() string {
	return fmt.Sprintf("%s (%d), %s", w.sv.Title.Title, w.sv.Title.Year, w.pname)
}

func (w *tvWanted) daily() bool { return w.sv.Title.SeriesType == "daily" }

// loadTV prepares series versions for deciding: every monitored one, or
// just the given ones.
func (s *Service) loadTV(ctx context.Context, ids ...int64) ([]*tvWanted, error) {
	svs, err := s.lib.SeriesVersions(ctx, ids...)
	if err != nil || len(svs) == 0 {
		return nil, err
	}
	ps, err := s.lib.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, p := range ps {
		names[p.ID] = p.Name
	}
	active := map[int64]map[int64]bool{}
	if s.queue != nil {
		if active, err = s.queue.ActiveEpisodes(ctx); err != nil {
			return nil, err
		}
	}
	profiles := map[int64]*decision.Profile{}
	out := make([]*tvWanted, 0, len(svs))
	for _, sv := range svs {
		p, ok := profiles[sv.Version.ProfileID]
		if !ok {
			if p, err = s.lib.CompiledProfile(ctx, sv.Version.ProfileID); err != nil {
				return nil, err
			}
			profiles[sv.Version.ProfileID] = p
		}
		w := &tvWanted{sv: sv, profile: p, pname: names[sv.Version.ProfileID], show: showOf(sv.Title), byID: map[int64]library.Episode{}}
		for _, e := range sv.Episodes {
			w.byID[e.ID] = e
		}
		for _, e := range sv.Wanted {
			if !active[sv.Version.ID][e.ID] {
				w.wanted = append(w.wanted, e)
			}
		}
		out = append(out, w)
	}
	return out, nil
}

func showOf(t library.Title) decision.Show {
	alt := t.AltTitleList()
	if t.OriginalTitle != "" {
		alt = append(alt, t.OriginalTitle)
	}
	return decision.Show{Title: t.Title, Year: t.Year, AltTitles: alt, OriginalLanguage: decision.LanguageByCode(t.OriginalLanguage)}
}

// tvUnit is one TV search: a season pack, or one episode.
type tvUnit struct {
	w        *tvWanted
	season   int
	episodes []library.Episode
	pack     bool
	// fallback marks a season search made because a single episode's search
	// found nothing acceptable (see packFallback). It leaves the search times
	// to the episode searches, so it never delays one.
	fallback bool
}

func (u tvUnit) label() string {
	if u.pack {
		return fmt.Sprintf("season %d", u.season)
	}
	return episodeName(u.w, u.episodes[0])
}

func (u tvUnit) episodeIDs() []int64 {
	ids := make([]int64, len(u.episodes))
	for i, e := range u.episodes {
		ids[i] = e.ID
	}
	return ids
}

// lastSearched is when the unit's least recently searched episode was
// searched for (zero: one never was).
func (u tvUnit) lastSearched() time.Time {
	var oldest time.Time
	for i, e := range u.episodes {
		t := u.w.sv.Searched[e.ID]
		if i == 0 || t.Before(oldest) {
			oldest = t
		}
	}
	return oldest
}

// episodeName is how History names one episode: "S02E05", or a daily
// show's air date.
func episodeName(w *tvWanted, e library.Episode) string {
	if w.daily() && e.AirDate != "" {
		return e.AirDate
	}
	return fmt.Sprintf("S%02dE%02d", *e.Season, *e.Number)
}

// units splits a version's wanted episodes into searches.
func (s *Service) units(w *tvWanted) []tvUnit {
	today := s.localToday()
	bySeason := map[int][]library.Episode{}
	var seasons []int
	for _, e := range w.wanted {
		if e.AirDate == "" || e.AirDate >= today {
			continue // from the day after it airs
		}
		n := *e.Season
		if _, ok := bySeason[n]; !ok {
			seasons = append(seasons, n)
		}
		bySeason[n] = append(bySeason[n], e)
	}
	var out []tvUnit
	for _, n := range seasons {
		eps := bySeason[n]
		if !w.daily() && n != 0 && packWorthIt(w.sv.Episodes, n, len(eps), today) {
			out = append(out, tvUnit{w: w, season: n, episodes: eps, pack: true})
			continue
		}
		for _, e := range eps {
			out = append(out, tvUnit{w: w, season: n, episodes: []library.Episode{e}})
		}
	}
	return out
}

// packWorthIt reports whether a season pack is the better search: the
// season has finished airing and more than half of it is wanted.
func packWorthIt(all []library.Episode, season, wanted int, today string) bool {
	total := 0
	for _, e := range all {
		if *e.Season == season {
			total++
		}
	}
	return seasonFinished(all, season, today) && wanted*2 > total
}

// packFallback is the season search to try when a single episode's search
// found nothing acceptable, or ok false when there is none to try. Indexers
// asked for one episode (S01E06) answer with releases named for it, so a
// complete-season torrent never shows up; when the season has finished
// airing, a pack may be the only copy there is (2026-10-20: a show missing
// one episode, with a complete season one pack on the tracker). The pack is
// for every wanted episode of the season not already chosen in this run, so
// a season with several missing episodes is grabbed once; importing takes
// only those episodes and leaves the files the version has alone.
func (s *Service) packFallback(u tvUnit, run *searchRun) (tvUnit, bool) {
	if u.pack || u.w.daily() || u.season == 0 || !seasonFinished(u.w.sv.Episodes, u.season, s.localToday()) {
		return tvUnit{}, false
	}
	var eps []library.Episode
	for _, e := range u.w.wanted {
		if *e.Season == u.season && e.AirDate != "" && !run.claimed[e.ID] {
			eps = append(eps, e)
		}
	}
	return tvUnit{w: u.w, season: u.season, episodes: eps, pack: true, fallback: true}, true
}

// seasonFinished reports whether every episode of a season has aired.
func seasonFinished(all []library.Episode, season int, today string) bool {
	total := 0
	for _, e := range all {
		if *e.Season != season {
			continue
		}
		total++
		if e.AirDate == "" || e.AirDate >= today {
			return false
		}
	}
	return total > 1
}

// searchRun is what the searches of one run share: the seasons already
// searched as a fallback pack (by version and season), so each is searched
// once, and the episodes chosen releases are on their way for, so their own
// searches are skipped.
type searchRun struct {
	packTried map[[2]int64]bool
	claimed   map[int64]bool
}

func newSearchRun() *searchRun {
	return &searchRun{packTried: map[[2]int64]bool{}, claimed: map[int64]bool{}}
}

// skip reports whether every episode of a unit is already on its way.
func (r *searchRun) skip(u tvUnit) bool {
	return !slices.ContainsFunc(u.episodes, func(e library.Episode) bool { return !r.claimed[e.ID] })
}

// target is what the decision engine checks releases against.
func (u tvUnit) target() decision.EpisodeTarget {
	t := decision.EpisodeTarget{Season: u.season, Runtime: map[int]int{}, AllowPack: u.pack}
	for _, e := range u.w.sv.Episodes {
		if *e.Season == u.season {
			t.SeasonEpisodes++
			t.Runtime[*e.Number] = e.Runtime
		}
	}
	for _, e := range u.episodes {
		t.Episodes = append(t.Episodes, *e.Number)
		if e.AirDate != "" {
			t.AirDates = append(t.AirDates, e.AirDate)
		}
	}
	return t
}

// query is the indexer search for a unit.
func (u tvUnit) query() core.Query {
	t := u.w.sv.Title
	q := core.Query{
		Type: "tv-search", Keywords: t.Title, TVDBID: t.TVDBID, TMDBID: t.TMDBID, IMDbID: t.IMDbID,
		Season: u.season, Categories: tvCategories,
	}
	if u.pack {
		return q
	}
	e := u.episodes[0]
	if u.w.daily() && len(e.AirDate) == 10 {
		// Daily shows, by the Torznab convention: the year as the season,
		// "MM/DD" as the episode.
		q.Season, _ = strconv.Atoi(e.AirDate[:4])
		q.Episode = e.AirDate[5:7] + "/" + e.AirDate[8:10]
		return q
	}
	q.Episode = strconv.Itoa(*e.Number)
	return q
}

// tvCandidate is a release with the verdict on it and what it holds.
type tvCandidate struct {
	candidate
	cov decision.Covers
}

// decideTV judges releases for a unit, best first. A pack search prefers
// an acceptable pack to single episodes: one grab instead of many.
func (s *Service) decideTV(ctx context.Context, w *tvWanted, t decision.EpisodeTarget, rels []indexer.Release) ([]tvCandidate, error) {
	minSeeders := s.settings().MinSeeders
	blocked := func(string, string) bool { return false }
	if s.queue != nil {
		var err error
		if blocked, err = s.queue.Blocklisted(ctx, w.sv.Title.ID); err != nil {
			return nil, err
		}
	}
	out := make([]tvCandidate, len(rels))
	for i, r := range rels {
		d, cov := decision.DecideEpisode(w.profile, w.show, t, decision.Candidate{
			Name: r.Title, Size: r.Size, Seeders: r.Seeders, MinSeeders: minSeeders,
			IndexerFlags: indexerFlags(r.Release), Blocklisted: blocked(r.Title, r.InfoHash), Usenet: r.Usenet(),
		})
		out[i] = tvCandidate{candidate: candidate{rel: r, d: d}, cov: cov}
	}
	slices.SortStableFunc(out, func(a, b tvCandidate) int {
		if a.d.Accepted && b.d.Accepted && a.cov.Pack != b.cov.Pack {
			if a.cov.Pack {
				return -1
			}
			return 1
		}
		return w.profile.Compare(a.d, b.d)
	})
	return out, nil
}

func plain(cs []tvCandidate) []candidate {
	out := make([]candidate, len(cs))
	for i, c := range cs {
		out[i] = c.candidate
	}
	return out
}

// tvPick is a release chosen for some of a unit's episodes.
type tvPick struct {
	c   tvCandidate
	ids []int64
	// fix is set when the release replaces files the version has (see
	// fixes.go).
	fix *decision.Fix
}

// pickTV chooses releases for a unit's episodes, best first, each covering
// episodes no earlier pick does (a search for a season can grab several
// single episodes; two releases of one episode are never both grabbed).
func pickTV(cs []tvCandidate, episodes []library.Episode) []tvPick {
	remaining := slices.Clone(episodes)
	var out []tvPick
	for _, c := range cs {
		if !c.d.Accepted || len(remaining) == 0 {
			continue
		}
		var ids []int64
		remaining = slices.DeleteFunc(remaining, func(e library.Episode) bool {
			if covers(c.cov, e) {
				ids = append(ids, e.ID)
				return true
			}
			return false
		})
		if len(ids) > 0 {
			out = append(out, tvPick{c: c, ids: ids})
		}
	}
	return out
}

// covers reports whether a release holds an episode.
func covers(cov decision.Covers, e library.Episode) bool {
	switch {
	case cov.AirDate != "":
		return e.AirDate == cov.AirDate
	case e.Season == nil || e.Number == nil || *e.Season != cov.Season:
		return false
	case cov.Pack:
		return true
	}
	return slices.Contains(cov.Episodes, *e.Number)
}

// pickLabel names what a pick is for: "season 2" when it's a pack of the
// whole wanted season, else the episodes.
func pickLabel(w *tvWanted, p tvPick) string {
	if p.c.cov.Pack {
		return fmt.Sprintf("season %d (%s)", p.c.cov.Season, countText(len(p.ids), "wanted episode"))
	}
	names := make([]string, len(p.ids))
	for i, id := range p.ids {
		names[i] = episodeName(w, w.byID[id])
	}
	return strings.Join(names, ", ")
}

// SearchSeriesVersion searches for every episode a series version wants
// now, as the TV Grabbing mode says. Like SearchVersion, it runs even when
// TV grabbing is off (then as a dry run), because the user asked.
func (s *Service) SearchSeriesVersion(ctx context.Context, versionID int64, trigger string) (string, error) {
	res, err := s.searchSeries(ctx, versionID, trigger, time.Time{})
	return res.Message, err
}

// seriesSearch is what searching a series version came to. Backfill reads
// the counts to tell "nothing acceptable exists" (the show is finished)
// from "something was held back" and "not everything was searched" (it
// waits and tries again).
type seriesSearch struct {
	Message string
	// Units is how many searches there were to make (season packs and
	// single episodes); Incomplete of them were not made, or no indexer
	// answered.
	Units, Incomplete int
	// Acceptable counts the releases chosen; Grabbed of them were sent to
	// the download client, Held were not (queue limit, backfill's quota,
	// disk space, an unmanaged folder, a refusing client).
	Acceptable, Grabbed, Held int
}

// searchSeries searches for a version's wanted units. When there are more
// than one search may make, the rest count as Incomplete unless they were
// searched since `since` (backfill passes when the show's turn began, so a
// show with dozens of single episodes can be finished once every one has
// been tried).
func (s *Service) searchSeries(ctx context.Context, versionID int64, trigger string, since time.Time) (seriesSearch, error) {
	ws, err := s.loadTV(ctx, versionID)
	if err != nil {
		return seriesSearch{}, err
	}
	if len(ws) == 0 {
		return seriesSearch{}, fmt.Errorf("version %d not found", versionID)
	}
	us := s.units(ws[0])
	if len(us) == 0 {
		return seriesSearch{Message: fmt.Sprintf("%s: no aired episodes are waiting.", ws[0].label())}, nil
	}
	res := seriesSearch{Units: len(us)}
	if len(us) > maxUnitsPerSearch {
		// The rest are left to the scheduled pass, oldest search first.
		slices.SortStableFunc(us, func(a, b tvUnit) int { return a.lastSearched().Compare(b.lastSearched()) })
		for _, u := range us[maxUnitsPerSearch:] {
			if since.IsZero() || u.lastSearched().Before(since) {
				res.Incomplete++
			}
		}
		us = us[:maxUnitsPerSearch]
	}
	var msgs []string
	run := newSearchRun()
	for _, u := range us {
		if run.skip(u) {
			continue // a season pack grabbed in this run holds it
		}
		ur, err := s.searchUnit(ctx, u, trigger, run)
		if err != nil {
			return res, err
		}
		msgs = append(msgs, ur.message)
		res.Acceptable += ur.acceptable
		res.Grabbed += ur.grabbed
		res.Held += ur.held
		if ur.unanswered {
			res.Incomplete++
		}
	}
	res.Message = strings.Join(msgs, " ")
	return res, nil
}

// maxUnitsPerSearch bounds one "Search now": a show with dozens of wanted
// episodes would otherwise send dozens of queries to every indexer at
// once. The scheduled pass gets to the rest.
const maxUnitsPerSearch = 10

// unitSearch is what one TV search came to.
type unitSearch struct {
	message string
	// acceptable releases were chosen; grabbed of them were sent to the
	// download client and held were not.
	acceptable, grabbed, held int
	// unanswered: no indexer answered, so nothing was really searched.
	unanswered bool
	// heldIDs are the episodes whose chosen release was held back.
	heldIDs map[int64]bool
}

// searchUnit searches for a unit and acts on what it finds. When a single
// episode's search finds nothing acceptable, the season is searched as a
// pack too (packFallback), once per run.
func (s *Service) searchUnit(ctx context.Context, u tvUnit, trigger string, run *searchRun) (unitSearch, error) {
	rels, failures, err := s.idx.Search(ctx, u.query(), indexer.SearchOptions{})
	if err != nil {
		return unitSearch{}, err
	}
	unsearched := len(rels) == 0 && len(failures) > 0
	rels = dedupe(rels)
	cs, err := s.decideTV(ctx, u.w, u.target(), rels)
	if err != nil {
		return unitSearch{}, err
	}
	accepted := 0
	for _, c := range cs {
		if c.d.Accepted {
			accepted++
		}
	}
	picks := pickTV(cs, u.episodes)
	what := u.w.label() + " " + u.label()
	msg := fmt.Sprintf("Searched for %s: %s, %d acceptable.", what, countText(len(rels), "release"), accepted)
	if u.fallback {
		msg = fmt.Sprintf("Nothing for the episode on its own, so searched for %s as a whole: %s, %d acceptable.",
			what, countText(len(rels), "release"), accepted)
	}
	if len(rels) > 0 && accepted == 0 {
		msg += " " + commonReason(plain(cs))
	}
	msg += usenetTie(u.w.profile, plain(cs))
	var errs []string
	for _, f := range failures {
		errs = append(errs, f.Indexer+": "+f.Message)
	}
	switch {
	case unsearched:
		msg = fmt.Sprintf("Couldn't search for %s: no indexer answered (%s). It will be tried again soon.", what, strings.Join(errs, "; "))
	case len(errs) > 0:
		msg += fmt.Sprintf(" %s failed or weren't asked.", countText(len(errs), "indexer"))
	}
	data := EventData{Trigger: trigger, IndexerErrors: errs, EpisodeIDs: u.episodeIDs()}
	data.Releases, data.More = verdicts(plain(cs))
	if err := s.history.Add(ctx, s.tvEvent(u.w, EventSearched, msg), data); err != nil {
		return unitSearch{}, err
	}
	res := unitSearch{acceptable: len(picks), unanswered: unsearched, heldIDs: map[int64]bool{}}
	// Episodes whose grab the queue limit (or backfill's quota) holds back,
	// or the download disk has no room for, don't count as searched: they
	// stay first in line for when the queue or the disk has room, or the
	// limit is raised.
	held := res.heldIDs
	for _, p := range picks {
		limited := false
		if trigger != TriggerRequest { // a request isn't held back by either limit
			var err error
			if limited, err = s.grabLimited(ctx, s.settings().TVMode, u.w.sv.Version.ID); err != nil {
				return unitSearch{}, err
			}
		}
		if limited {
			for _, id := range p.ids {
				held[id] = true
			}
		}
		gm, outcome, err := s.actTV(ctx, u.w, p, trigger, true)
		if err != nil {
			return unitSearch{}, err
		}
		switch outcome {
		case outcomeGrabbed:
			res.grabbed++
		case outcomeHeld:
			res.held++
		case outcomeNoSpace:
			res.held++
			for _, id := range p.ids {
				held[id] = true
			}
		}
		if outcome != outcomeHeld && outcome != outcomeNoSpace {
			for _, id := range p.ids {
				run.claimed[id] = true
			}
		}
		if gm != "" {
			msg += " " + gm
		}
	}
	if pu, ok := s.packFallback(u, run); ok && !unsearched && len(picks) == 0 && !run.packTried[[2]int64{u.w.sv.Version.ID, int64(u.season)}] {
		run.packTried[[2]int64{u.w.sv.Version.ID, int64(u.season)}] = true
		pr, err := s.searchUnit(ctx, pu, trigger, run)
		if err != nil {
			return unitSearch{}, err
		}
		msg += " " + pr.message
		res.acceptable += pr.acceptable
		res.grabbed += pr.grabbed
		res.held += pr.held
		for id := range pr.heldIDs {
			held[id] = true
		}
	}
	if !unsearched && !u.fallback {
		var done []int64
		for _, id := range u.episodeIDs() {
			if !held[id] {
				done = append(done, id)
			}
		}
		if err := s.lib.MarkEpisodesSearched(ctx, u.w.sv.Version.ID, done, s.now()); err != nil {
			return unitSearch{}, err
		}
	}
	res.message = msg
	return res, nil
}

func (s *Service) tvEvent(w *tvWanted, kind, msg string) *Event {
	tid, vid := w.sv.Title.ID, w.sv.Version.ID
	return &Event{TitleID: &tid, VersionID: &vid, Event: kind, Message: msg}
}

// actTV handles a chosen TV release according to the TV Grabbing mode.
// recordDuplicates is false for RSS, as for movies. The outcome says
// whether it was grabbed, or held back (a limit, the disk, the folder, the
// download client).
func (s *Service) actTV(ctx context.Context, w *tvWanted, p tvPick, trigger string, recordDuplicates bool) (string, grabOutcome, error) {
	g := s.settings()
	c := p.c
	v := verdict(c.candidate)
	data := EventData{Trigger: trigger, Chosen: &v, EpisodeIDs: p.ids}
	ev := s.tvEvent(w, "", "")
	ev.SourceTitle, ev.Indexer, ev.Quality, ev.Score = c.rel.Title, c.rel.Indexer, c.d.Quality.Name, c.d.Score
	label := pickLabel(w, p)
	if p.fix != nil && c.cov.Pack {
		label = fmt.Sprintf("season %d", c.cov.Season) // a fix replaces what the season has, not "wanted" episodes
	}

	if g.TVMode != settings.GrabOn {
		if !recordDuplicates {
			seen, err := s.history.recently(ctx, w.sv.Version.ID, EventWouldGrab, c.rel.Title, s.now().Add(-7*24*time.Hour))
			if err != nil || seen {
				return "", outcomeNone, err
			}
		}
		ev.Event = EventWouldGrab
		ev.Message = fmt.Sprintf("Would grab %s for %s (%s, score %d, from %s). Dry run: nothing was downloaded.",
			c.rel.Title, label, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
		if p.fix != nil {
			ev.Message = fmt.Sprintf("Would grab %s. Release: %s (%s, score %d, from %s). Dry run: nothing was downloaded.",
				p.fix.Describe(label), c.rel.Title, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
		}
		return ev.Message, outcomeNone, s.history.Add(ctx, ev, data)
	}

	skipAs := func(outcome grabOutcome, reason string) (string, grabOutcome, error) {
		ev.Event = EventGrabSkipped
		ev.Message = fmt.Sprintf("Chose %s for %s, but didn't grab it: %s", c.rel.Title, label, reason)
		return ev.Message, outcome, s.history.Add(ctx, ev, data)
	}
	skip := func(reason string) (string, grabOutcome, error) { return skipAs(outcomeHeld, reason) }
	if s.grabber == nil {
		return skip("no download client is set up yet (Settings → Download Client).")
	}
	if reason, err := s.unmanaged(ctx, w.sv.Version.RootFolderID); err != nil || reason != "" {
		if err != nil {
			return "", outcomeNone, err
		}
		return skip(reason)
	}
	if s.queue != nil {
		active, err := s.queue.ActiveEpisodes(ctx)
		if err != nil {
			return "", outcomeNone, err
		}
		if slices.ContainsFunc(p.ids, func(id int64) bool { return active[w.sv.Version.ID][id] }) {
			return skip("a download for these episodes is already in progress (Activity → Queue).")
		}
	}
	// A request (Seerr) goes past both the queue limit and backfill's quota
	// (which is backfill's own allowance, not a request's): someone is
	// waiting for it.
	if trigger != TriggerRequest {
		if full, err := s.queueFull(ctx, g.TVMode); err != nil {
			return "", outcomeNone, err
		} else if full {
			return skip(queueFullReason(g.QueueLimit))
		}
		if full, err := s.backfillQuotaReached(ctx, w.sv.Version.ID); err != nil {
			return "", outcomeNone, err
		} else if full {
			return skip(backfillQuotaText)
		}
	}
	tg := &TVGrab{Title: w.sv.Title, Version: w.sv.Version, EpisodeIDs: p.ids, Label: label}
	if err := s.grabber.Grab(ctx, Grabbed{Release: c.rel, Quality: c.d.Quality.Name, Score: c.d.Score, TV: tg, Upgrade: p.fix != nil, Requested: trigger == TriggerRequest}); err != nil {
		if errors.Is(err, ErrMoving) {
			return skip(movingReason)
		}
		if errors.Is(err, ErrNotEnoughSpace) {
			s.warnSpace(ctx, err)
			return skipAs(outcomeNoSpace, strings.TrimPrefix(err.Error(), ErrNotEnoughSpace.Error()+": ")+".")
		}
		if s.notify != nil {
			s.notify(ctx, notify.Event{Kind: notify.KindGrabFailed, Title: w.sv.Title.Title, TitleID: w.sv.Title.ID,
				Message: fmt.Sprintf("Reelkeeper chose %s for %s %s, but couldn't send it to the download client: %s", c.rel.Title, w.label(), label, err)})
		}
		return skip("the download client refused it: " + err.Error())
	}
	ev.Event = EventGrabbed
	ev.Message = fmt.Sprintf("Grabbed %s for %s (%s, score %d, from %s).", c.rel.Title, label, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
	if p.fix != nil {
		ev.Message = fmt.Sprintf("Grabbed %s. Release: %s (%s, score %d, from %s).",
			p.fix.Describe(label), c.rel.Title, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
	}
	return ev.Message, outcomeGrabbed, s.history.Add(ctx, ev, data)
}

// grabOutcome says what a chosen release came to.
type grabOutcome int

const (
	outcomeNone    grabOutcome = iota // nothing was sent (a dry run, or already recorded)
	outcomeGrabbed                    // sent to the download client
	outcomeHeld                       // acceptable, but not sent: a limit, the folder or the client
	// outcomeNoSpace: acceptable, but the download disk had no room. It
	// doesn't count as a search, so the next scheduled pass with room
	// tries again instead of waiting three days.
	outcomeNoSpace
)

// tvPass is the scheduled pass's TV half: a few units that haven't been
// searched for recently, never-searched and longest-waiting first. It
// says nothing ("") when no series is monitored.
func (s *Service) tvPass(ctx context.Context, perRun int, every time.Duration) (string, error) {
	g := s.settings()
	if !tvActive(g) {
		return "", nil
	}
	ws, err := s.loadTV(ctx)
	if err != nil || len(ws) == 0 {
		return "", err
	}
	if msg, err := s.pausedByLimit(ctx, g.TVMode); msg != "" || err != nil {
		return "TV: " + msg, err
	}
	if msg := s.pausedForSpace(ctx, g.TVMode); msg != "" {
		return "TV: " + msg, nil
	}
	now := s.now()
	var all, due []tvUnit
	for _, w := range ws {
		for _, u := range s.units(w) {
			all = append(all, u)
			if last := u.lastSearched(); last.IsZero() || now.Sub(last) >= every {
				due = append(due, u)
			}
		}
	}
	slices.SortStableFunc(due, func(a, b tvUnit) int { return a.lastSearched().Compare(b.lastSearched()) })
	if len(due) > perRun {
		due = due[:perRun]
	}
	if len(due) == 0 {
		if len(all) == 0 {
			return "TV: no aired episodes are waiting.", nil
		}
		return fmt.Sprintf("TV: nothing to search: %s waiting, all searched in the last %s.", countText(len(all), "search"), days(every)), nil
	}
	var names []string
	run := newSearchRun()
	for _, u := range due {
		if run.skip(u) {
			continue
		}
		if _, err := s.searchUnit(ctx, u, TriggerScheduled, run); err != nil {
			return "", err
		}
		names = append(names, u.w.sv.Title.Title+" "+u.label())
	}
	return fmt.Sprintf("TV: searched for %s (%d waiting).", strings.Join(names, ", "), len(all)), nil
}

// rssTV matches RSS releases against wanted episodes and acts on them. It
// returns how many series versions had new releases.
func (s *Service) rssTV(ctx context.Context, ws []*tvWanted, rels []indexer.Release) (int, error) {
	byKey := map[string][]int{}
	for i, w := range ws {
		for _, t := range append([]string{w.show.Title}, w.show.AltTitles...) {
			if k := decision.TitleKey(t); k != "" && !slices.Contains(byKey[k], i) {
				byKey[k] = append(byKey[k], i)
			}
		}
	}
	// Releases per version and season.
	type key struct{ w, season int }
	matched := map[key][]indexer.Release{}
	var order []key
	for _, r := range rels {
		p := parser.Parse(r.Title)
		if p.Episode == nil {
			continue
		}
		cands := byKey[decision.TitleKey(p.Title)]
		if len(cands) == 0 && p.Year != 0 {
			// "Show 2019 S01E01": the year may be part of the title key.
			cands = byKey[decision.TitleKey(fmt.Sprintf("%s %d", p.Title, p.Year))]
		}
		for _, i := range cands {
			w := ws[i]
			if !decision.SameShow(p, w.show) {
				continue
			}
			season := p.Episode.Season
			if p.Episode.AirDate != "" {
				season = -1 // daily: matched by date below, any season
			}
			k := key{i, season}
			if _, ok := matched[k]; !ok {
				order = append(order, k)
			}
			matched[k] = append(matched[k], r)
		}
	}

	today := s.localToday()
	found := map[int]bool{}
	for _, k := range order {
		w := ws[k.w]
		// The wanted episodes this season's releases could be for (RSS
		// takes same-day releases too).
		var eps []library.Episode
		for _, e := range w.wanted {
			if e.AirDate != "" && e.AirDate <= today && (k.season < 0 || *e.Season == k.season) {
				eps = append(eps, e)
			}
		}
		fixes := len(w.recent) > 0 && slices.ContainsFunc(matched[k], func(r indexer.Release) bool { return parser.Parse(r.Title).Revision >= 2 })
		if len(eps) == 0 && !fixes {
			continue
		}
		var fresh []indexer.Release
		for _, r := range matched[k] {
			if s.firstRSSSight(w.sv.Version.ID, r) {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		found[k.w] = true
		if len(eps) > 0 {
			if err := s.rssWanted(ctx, w, k.season, eps, fresh, today); err != nil {
				return 0, err
			}
		}
		if fixes {
			if err := s.rssFixes(ctx, w, k.season, fresh); err != nil {
				return 0, err
			}
		}
	}
	return len(found), nil
}

// rssWanted decides new releases for the wanted episodes of one season (a
// daily show's: kSeason < 0, any season) and acts on the picks.
func (s *Service) rssWanted(ctx context.Context, w *tvWanted, kSeason int, eps []library.Episode, fresh []indexer.Release, today string) error {
	season := kSeason
	if season < 0 {
		season = *eps[0].Season
	}
	u := tvUnit{w: w, season: season, episodes: eps}
	u.pack = !w.daily() && season > 0 && packWorthIt(w.sv.Episodes, season, len(eps), today)
	t := u.target()
	if kSeason < 0 {
		t.AirDates = nil
		for _, e := range eps {
			t.AirDates = append(t.AirDates, e.AirDate)
		}
	}
	cs, err := s.decideTV(ctx, w, t, fresh)
	if err != nil {
		return err
	}
	accepted := 0
	for _, c := range cs {
		if c.d.Accepted {
			accepted++
		}
	}
	msg := fmt.Sprintf("RSS: %s for %s, %d acceptable.", countText(len(fresh), "new release"), w.label(), accepted)
	if accepted == 0 {
		msg += " " + commonReason(plain(cs))
	}
	msg += usenetTie(w.profile, plain(cs))
	data := EventData{Trigger: TriggerRSS, EpisodeIDs: u.episodeIDs()}
	data.Releases, data.More = verdicts(plain(cs))
	if err := s.history.Add(ctx, s.tvEvent(w, EventRSS, msg), data); err != nil {
		return err
	}
	for _, p := range pickTV(cs, eps) {
		if _, _, err := s.actTV(ctx, w, p, TriggerRSS, false); err != nil {
			return err
		}
	}
	return nil
}

// localToday is today's date where Reelkeeper runs, as TMDB air dates are
// written ("2026-10-11").
func (s *Service) localToday() string { return s.now().In(s.loc).Format("2006-01-02") }

// isTV reports whether a feed release is TV: by its categories when the
// indexer gives them, else by its name.
func isTV(r indexer.Release) bool {
	for _, c := range r.Categories {
		switch {
		case c >= 5000 && c < 6000:
			return true
		case c >= 2000 && c < 3000:
			return false
		}
	}
	return parser.Parse(r.Title).Episode != nil
}
