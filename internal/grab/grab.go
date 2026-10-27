// Package grab finds releases for wanted movies and grabs the best one.
//
// A movie version is wanted when it's monitored and either has no file or
// has one its profile would still upgrade. Three things look for releases:
//
//   - a search for one version: when a movie is added or monitored, or the
//     user presses "Search now";
//   - the scheduled pass: every hour, a few wanted versions that haven't
//     been searched for a while, so trackers aren't hammered;
//   - RSS sync: every 15 minutes, each indexer's newest releases, matched
//     against every wanted version. This finds new releases without
//     searching at all. Indexers behind the Cloudflare solver are read
//     hourly (indexer.SolverFeedEvery).
//
// Every release found goes through the decision engine (internal/decision),
// and the verdicts are written to History, so "why didn't it grab X?" has
// an answer. What happens to the best acceptable release depends on the
// Grabbing setting: Off (automatic searching stops), Dry run (History
// records "would grab", nothing is downloaded) or On.
package grab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/parser"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// Library is what grab needs from the library.
type Library interface {
	MonitoredMovieVersions(ctx context.Context, ids ...int64) ([]library.MovieVersion, error)
	CompiledProfile(ctx context.Context, id int64) (*decision.Profile, error)
	Profiles(ctx context.Context) ([]library.Profile, error)
	MarkSearched(ctx context.Context, versionID int64, at time.Time) error
	SeriesVersions(ctx context.Context, ids ...int64) ([]library.SeriesVersion, error)
	MarkEpisodesSearched(ctx context.Context, versionID int64, episodeIDs []int64, at time.Time) error
	SetEpisodesMonitored(ctx context.Context, versionID int64, episodeIDs []int64, monitored bool) error
	RootFolder(ctx context.Context, id int64) (*library.RootFolder, error)
}

// Indexers searches every suitable indexer.
type Indexers interface {
	Search(ctx context.Context, q core.Query, opt indexer.SearchOptions) ([]indexer.Release, []indexer.IndexerError, error)
}

// Grabber sends a release to the download client.
type Grabber interface {
	Grab(ctx context.Context, g Grabbed) error
}

// Grabbed is a release chosen for a version, with the decision behind it:
// a movie version (Version), or episodes of a series version (TV).
type Grabbed struct {
	Release indexer.Release
	Version library.MovieVersion
	TV      *TVGrab
	Quality string
	Score   int
	Upgrade bool
	// Requested: came from a request's search (Seerr); it skips the queue
	// limit and goes to the top of the download client's queue.
	Requested bool
}

// Queue is what grab needs to know about downloads: versions with a
// download in progress aren't searched again, and blocklisted releases
// are never grabbed.
type Queue interface {
	ActiveVersions(ctx context.Context) (map[int64]bool, error)
	// ActiveEpisodes are episodes with a download in progress, by series
	// version.
	ActiveEpisodes(ctx context.Context) (map[int64]map[int64]bool, error)
	// CurrentEpisodeDownloads are, by episode, the downloads that the
	// version's present episode files came from, when imported after
	// since. Only these files can be replaced by a REPACK or PROPER.
	CurrentEpisodeDownloads(ctx context.Context, versionID int64, since time.Time) (map[int64]decision.Current, error)
	Blocklisted(ctx context.Context, titleID int64) (func(name, hash string) bool, error)
	// QueueSize is how many downloads Activity → Queue lists, movies and
	// TV together; the queue limit is checked against it.
	QueueSize(ctx context.Context) (int, error)
}

// Service runs searches and RSS sync.
type Service struct {
	lib      Library
	idx      Indexers
	settings func() settings.Grabbing
	history  *History
	grabber  Grabber
	queue    Queue
	notify   func(ctx context.Context, ev notify.Event)
	logger   *slog.Logger
	now      func() time.Time
	// loc is the time zone whose midnight starts "today" for the grab counts
	// (the summary's grabs today, backfill's daily quota).
	loc *time.Location

	// rssSeen remembers which release each version has already been shown
	// by RSS sync, so a release that stays in the feed for hours is
	// recorded once, not every 15 minutes.
	rssMu   sync.Mutex
	rssSeen map[string]time.Time

	// spaceWarned is when the full-disk email last went out.
	spaceMu     sync.Mutex
	spaceWarned time.Time

	// backfillMu serializes changes to the backfill queue (adding,
	// removing, moving, starting the next show).
	backfillMu sync.Mutex
	// options are the releases offered under a show's "Show options" that
	// the user may still take, by token (see backfill.go).
	optMu   sync.Mutex
	options map[string]pickedOption
}

// New returns the service. grabber may be nil.
func New(lib Library, idx Indexers, grabbing func() settings.Grabbing, history *History, grabber Grabber, logger *slog.Logger) *Service {
	return &Service{
		lib: lib, idx: idx, settings: grabbing, history: history, grabber: grabber, logger: logger,
		now: time.Now, loc: time.Local, rssSeen: map[string]time.Time{}, options: map[string]pickedOption{},
	}
}

// SetNotifier connects notifications (a release the download client
// refused is a problem worth an email).
func (s *Service) SetNotifier(fn func(ctx context.Context, ev notify.Event)) { s.notify = fn }

// SetDownloads connects the download client (and its queue). Until it's
// called, grabbing "On" records why nothing was sent.
func (s *Service) SetDownloads(g Grabber, q Queue) { s.grabber, s.queue = g, q }

// Movie categories (Newznab standard): every indexer files movies under
// 2000 and its subcategories.
var movieCategories = []int{2000}

// ErrMoving is what the grabber answers while torrents are moving between
// download clients: nothing is grabbed until the move is done.
var ErrMoving = errors.New("torrents are moving to another download client")

// movingReason is the History text for a grab held back by a move.
const movingReason = "torrents are moving to another download client (System → Tasks); Reelkeeper grabs again when that's done."

// moving reports whether the download service says torrents are moving
// between clients (an optional extra of Queue, like diskChecker).
func (s *Service) moving() bool {
	m, ok := s.queue.(interface{ Moving() bool })
	return ok && m.Moving()
}

// ErrNotEnoughSpace is what the grabber returns when a release wouldn't
// fit on the download disk with the space kept free.
var ErrNotEnoughSpace = errors.New("not enough free space")

// spaceWarnEvery limits the email about a full download disk.
const spaceWarnEvery = 12 * time.Hour

// Triggers, recorded with each search.
const (
	TriggerManual    = "manual"
	TriggerAdded     = "added"
	TriggerScheduled = "scheduled"
	TriggerRSS       = "rss"
	// TriggerBackfill: the Backfill task's search. TriggerPicked: a release
	// the user chose under "Show options".
	TriggerBackfill = "backfill"
	TriggerPicked   = "picked"
	// TriggerRequest: a search another app (Seerr) asked for through the
	// Radarr/Sonarr-compatible API, when something is requested. Its grab
	// skips the queue limit and goes to the top of the download client's
	// queue.
	TriggerRequest = "request"
)

// wanted is a version that still wants something, ready for decisions.
type wanted struct {
	v       library.MovieVersion
	profile *decision.Profile
	pname   string
	movie   decision.Movie
	have    *decision.Existing // nil: no file
}

func (w wanted) label() string {
	return fmt.Sprintf("%s (%d), %s", w.v.Title.Title, w.v.Title.Year, w.pname)
}

// upgradesOffReason is why every release is turned down for a movie that
// has a file while its Upgrades switch is off.
const upgradesOffReason = "Upgrades are off for this movie, so Reelkeeper keeps the file it has."

// load prepares versions for deciding. With onlyWanted, versions whose
// file already meets the profile's cutoff are left out, and so are those
// with a file whose upgrades are off (a version with no file is still
// wanted, whatever the switch says).
func (s *Service) load(ctx context.Context, onlyWanted bool, ids ...int64) ([]wanted, error) {
	vs, err := s.lib.MonitoredMovieVersions(ctx, ids...)
	if err != nil {
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
	active := map[int64]bool{}
	if s.queue != nil && onlyWanted {
		if active, err = s.queue.ActiveVersions(ctx); err != nil {
			return nil, err
		}
	}
	profiles := map[int64]*decision.Profile{}
	var out []wanted
	for _, v := range vs {
		if active[v.Version.ID] {
			continue // already downloading something
		}
		p, ok := profiles[v.Version.ProfileID]
		if !ok {
			if p, err = s.lib.CompiledProfile(ctx, v.Version.ProfileID); err != nil {
				return nil, err
			}
			profiles[v.Version.ProfileID] = p
		}
		w := wanted{v: v, profile: p, pname: names[v.Version.ProfileID], movie: movieOf(v.Title)}
		w.have = bestFile(p, v.Files)
		if onlyWanted && w.have != nil && (!v.Version.Upgrades || p.CutoffMet(w.have, w.movie)) {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

func movieOf(t library.Title) decision.Movie {
	return decision.Movie{
		Title: t.Title, Year: t.Year, AltTitles: append(t.AltTitleList(), t.OriginalTitle),
		Runtime: t.Runtime, OriginalLanguage: decision.LanguageByCode(t.OriginalLanguage),
	}
}

// bestFile describes the version's best file, by the profile's ranking.
func bestFile(p *decision.Profile, files []library.MediaFile) *decision.Existing {
	var best *decision.Existing
	bestRank := -2
	for _, f := range files {
		name := path.Base(f.Path)
		r := parser.Parse(name)
		q := decision.QualityOf(parser.Release{Source: parser.Source(f.Source), Resolution: f.Resolution})
		e := &decision.Existing{Name: f.ScoringName(), FileName: name, Quality: q, Revision: r.Revision, Size: f.Size}
		if rank := p.Rank(q); rank > bestRank {
			best, bestRank = e, rank
		}
	}
	return best
}

// SearchVersion searches every indexer for one version and acts on the
// best release, as the Grabbing setting says. It runs even when grabbing
// is off (then it behaves like a dry run), because the user asked.
func (s *Service) SearchVersion(ctx context.Context, versionID int64, trigger string) (string, error) {
	ws, err := s.load(ctx, false, versionID)
	if err != nil {
		return "", err
	}
	if len(ws) == 0 {
		return s.SearchSeriesVersion(ctx, versionID, trigger) // not a movie version
	}
	return s.search(ctx, ws[0], trigger)
}

func (s *Service) search(ctx context.Context, w wanted, trigger string) (string, error) {
	t := w.v.Title
	q := core.Query{
		Type: "movie-search", Keywords: t.Title, Year: t.Year, IMDbID: t.IMDbID, TMDBID: t.TMDBID,
		Categories: movieCategories,
	}
	rels, failures, err := s.idx.Search(ctx, q, indexer.SearchOptions{})
	if err != nil {
		return "", err
	}
	// Nothing found because indexers failed or were resting doesn't count
	// as a search: the scheduled pass tries again within the hour instead
	// of in three days.
	unsearched := len(rels) == 0 && len(failures) > 0
	rels = dedupe(rels)
	ds, err := s.decide(ctx, w, rels)
	if err != nil {
		return "", err
	}

	accepted := 0
	for _, d := range ds {
		if d.d.Accepted {
			accepted++
		}
	}
	// Nor does a search whose grab the queue limit will block: the movie
	// stays first in line for when the queue has room or the limit is
	// raised. A request isn't held back by the limit, so it is searched
	// either way.
	capped := false
	if accepted > 0 && trigger != TriggerRequest {
		if capped, err = s.queueFull(ctx, s.settings().Mode); err != nil {
			return "", err
		}
	}
	msg := fmt.Sprintf("Searched for %s: %s, %d acceptable.", w.label(), countText(len(rels), "release"), accepted)
	if len(rels) > 0 && accepted == 0 {
		msg += " " + commonReason(ds)
	}
	var errs []string
	for _, f := range failures {
		errs = append(errs, f.Indexer+": "+f.Message)
	}
	switch {
	case unsearched:
		msg = fmt.Sprintf("Couldn't search for %s: no indexer answered (%s). It will be tried again soon.",
			w.label(), strings.Join(errs, "; "))
	case len(errs) > 0:
		msg += fmt.Sprintf(" %s failed or weren't asked.", countText(len(errs), "indexer"))
	}
	data := EventData{Trigger: trigger, IndexerErrors: errs}
	data.Releases, data.More = verdicts(ds)
	if err := s.history.Add(ctx, s.event(w, EventSearched, msg), data); err != nil {
		return "", err
	}
	noRoom := false
	if accepted > 0 {
		grabMsg, outcome, err := s.act(ctx, w, ds[0], trigger, true)
		if err != nil {
			return "", err
		}
		msg += " " + grabMsg
		// Nor does a search whose grab the download disk had no room for:
		// the movie stays first in line for when cleanup makes room, instead
		// of waiting three days.
		noRoom = outcome == outcomeNoSpace
	}
	if !unsearched && !capped && !noRoom {
		if err := s.lib.MarkSearched(ctx, w.v.Version.ID, s.now()); err != nil {
			return "", err
		}
	}
	return msg, nil
}

// candidate pairs a release with the verdict on it.
type candidate struct {
	rel indexer.Release
	d   decision.Decision
}

// decide judges releases for a version, best first.
func (s *Service) decide(ctx context.Context, w wanted, rels []indexer.Release) ([]candidate, error) {
	minSeeders := s.settings().MinSeeders
	blocked := func(string, string) bool { return false }
	if s.queue != nil {
		var err error
		if blocked, err = s.queue.Blocklisted(ctx, w.v.Title.ID); err != nil {
			return nil, err
		}
	}
	out := make([]candidate, len(rels))
	for i, r := range rels {
		out[i] = candidate{rel: r, d: decision.Decide(w.profile, w.movie, decision.Candidate{
			Name: r.Title, Size: r.Size, Seeders: r.Seeders, MinSeeders: minSeeders,
			IndexerFlags: indexerFlags(r.Release), Blocklisted: blocked(r.Title, r.InfoHash),
		}, w.have)}
		if w.have != nil && !w.v.Version.Upgrades {
			out[i].d.Accepted = false
			out[i].d.Rejections = append(out[i].d.Rejections, upgradesOffReason)
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int { return w.profile.Compare(a.d, b.d) })
	return out, nil
}

// act handles the chosen release according to the Grabbing setting.
// recordDuplicates is false for RSS, which mustn't record the same "would
// grab" every 15 minutes.
func (s *Service) act(ctx context.Context, w wanted, c candidate, trigger string, recordDuplicates bool) (string, grabOutcome, error) {
	g := s.settings()
	v := verdict(c)
	data := EventData{Trigger: trigger, Chosen: &v}
	ev := s.event(w, "", "")
	ev.SourceTitle, ev.Indexer, ev.Quality, ev.Score = c.rel.Title, c.rel.Indexer, c.d.Quality.Name, c.d.Score

	what := "grab"
	if c.d.Upgrade {
		what = "upgrade to"
	}
	if g.Mode != settings.GrabOn {
		if !recordDuplicates {
			seen, err := s.history.recently(ctx, w.v.Version.ID, EventWouldGrab, c.rel.Title, s.now().Add(-7*24*time.Hour))
			if err != nil || seen {
				return "", outcomeNone, err
			}
		}
		ev.Event = EventWouldGrab
		ev.Message = fmt.Sprintf("Would %s %s (%s, score %d, from %s). Dry run: nothing was downloaded.",
			what, c.rel.Title, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
		return ev.Message, outcomeNone, s.history.Add(ctx, ev, data)
	}

	skipAs := func(outcome grabOutcome, reason string) (string, grabOutcome, error) {
		ev.Event = EventGrabSkipped
		ev.Message = fmt.Sprintf("Chose %s, but didn't grab it: %s", c.rel.Title, reason)
		return ev.Message, outcome, s.history.Add(ctx, ev, data)
	}
	skip := func(reason string) (string, grabOutcome, error) { return skipAs(outcomeHeld, reason) }
	if s.grabber == nil {
		return skip("no download client is set up yet (Settings → Download Client).")
	}
	if reason, err := s.unmanaged(ctx, w.v.Version.RootFolderID); err != nil || reason != "" {
		if err != nil {
			return "", outcomeNone, err
		}
		return skip(reason)
	}
	if s.queue != nil {
		active, err := s.queue.ActiveVersions(ctx)
		if err != nil {
			return "", outcomeNone, err
		}
		if active[w.v.Version.ID] {
			return skip("a download for this movie is already in progress (Activity → Queue).")
		}
	}
	// A request (Seerr) goes past the queue limit: someone is waiting for it.
	if trigger != TriggerRequest {
		if full, err := s.queueFull(ctx, g.Mode); err != nil {
			return "", outcomeNone, err
		} else if full {
			return skip(queueFullReason(g.QueueLimit))
		}
	}
	if err := s.grabber.Grab(ctx, Grabbed{Release: c.rel, Version: w.v, Quality: c.d.Quality.Name, Score: c.d.Score, Upgrade: c.d.Upgrade,
		Requested: trigger == TriggerRequest}); err != nil {
		if errors.Is(err, ErrMoving) {
			return skip(movingReason)
		}
		if errors.Is(err, ErrNotEnoughSpace) {
			s.warnSpace(ctx, err)
			return skipAs(outcomeNoSpace, strings.TrimPrefix(err.Error(), ErrNotEnoughSpace.Error()+": ")+".")
		}
		if s.notify != nil {
			s.notify(ctx, notify.Event{Kind: notify.KindGrabFailed, Title: w.v.Title.Title, TitleID: w.v.Title.ID,
				Message: fmt.Sprintf("Reelkeeper chose %s for %s, but couldn't send it to the download client: %s", c.rel.Title, w.label(), err)})
		}
		return skip("the download client refused it: " + err.Error())
	}
	ev.Event = EventGrabbed
	ev.Message = fmt.Sprintf("Grabbed %s (%s, score %d, from %s).", c.rel.Title, c.d.Quality.Name, c.d.Score, c.rel.Indexer)
	if c.d.Upgrade {
		ev.Message = strings.TrimSuffix(ev.Message, ".") + " as an upgrade."
	}
	return ev.Message, outcomeGrabbed, s.history.Add(ctx, ev, data)
}

// unmanaged says why nothing should be grabbed for a version whose media
// folder has "Manage files" off: the download could never be imported
// there, so it would only use bandwidth and a place in the queue.
// It's "" when the folder is managed.
func (s *Service) unmanaged(ctx context.Context, rootFolderID int64) (string, error) {
	r, err := s.lib.RootFolder(ctx, rootFolderID)
	if err != nil {
		return "", err
	}
	if r.ManageFiles {
		return "", nil
	}
	return fmt.Sprintf("\"Manage files\" is off for %s, so Reelkeeper couldn't import the download. Turn it on under Settings → Media Folders.", r.Path), nil
}

// warnSpace emails about a full download disk, at most every few hours.
func (s *Service) warnSpace(ctx context.Context, err error) {
	s.spaceMu.Lock()
	due := s.spaceWarned.IsZero() || s.now().Sub(s.spaceWarned) >= spaceWarnEvery
	if due {
		s.spaceWarned = s.now()
	}
	s.spaceMu.Unlock()
	if due && s.notify != nil {
		s.notify(ctx, notify.Event{Kind: notify.KindGrabFailed, Title: "Download disk",
			Message: "Reelkeeper is skipping grabs: " + strings.TrimPrefix(err.Error(), ErrNotEnoughSpace.Error()+": ") + "."})
	}
}

func (s *Service) event(w wanted, kind, msg string) *Event {
	tid, vid := w.v.Title.ID, w.v.Version.ID
	return &Event{TitleID: &tid, VersionID: &vid, Event: kind, Message: msg}
}

// ScheduledPass searches a few wanted movie versions that are released and
// haven't been searched recently, longest-waiting first, and as many TV
// searches (see tvPass).
func (s *Service) ScheduledPass(ctx context.Context, perRun int, every time.Duration) (string, error) {
	movies, err := s.moviePass(ctx, perRun, every)
	if err != nil {
		return "", err
	}
	tv, err := s.tvPass(ctx, perRun, every)
	if err != nil {
		return "", err
	}
	if tv != "" && s.settings().Mode == settings.GrabOff {
		movies = "Movie grabbing is off."
	}
	return strings.TrimSpace(movies + " " + tv), nil
}

func (s *Service) moviePass(ctx context.Context, perRun int, every time.Duration) (string, error) {
	if s.settings().Mode == settings.GrabOff {
		return "Grabbing is off (Settings → Download Client).", nil
	}
	if msg, err := s.pausedByLimit(ctx, s.settings().Mode); msg != "" || err != nil {
		return msg, err
	}
	if msg := s.pausedForSpace(ctx, s.settings().Mode); msg != "" {
		return msg, nil
	}
	ws, err := s.load(ctx, true)
	if err != nil {
		return "", err
	}
	now := s.now()
	var due []wanted
	for _, w := range ws {
		if !released(w.v.Title, now) {
			continue
		}
		if last := w.v.Version.LastSearchAt; last != nil && now.Sub(*last) < every {
			continue
		}
		due = append(due, w)
	}
	// Missing movies come before upgrades (2026-10-14): with the queue
	// limit, upgrades could otherwise take every slot while missing movies
	// wait. Within each group, never searched first, then the longest ago.
	slices.SortStableFunc(due, func(a, b wanted) int {
		if ma, mb := a.have == nil, b.have == nil; ma != mb {
			if ma {
				return -1
			}
			return 1
		}
		la, lb := a.v.Version.LastSearchAt, b.v.Version.LastSearchAt
		switch {
		case la == nil && lb == nil:
			return 0
		case la == nil:
			return -1
		case lb == nil:
			return 1
		}
		return la.Compare(*lb)
	})
	if len(due) > perRun {
		due = due[:perRun]
	}
	if len(due) == 0 {
		return fmt.Sprintf("Nothing to search: %s waiting, all searched in the last %s or not yet released.",
			countText(len(ws), "movie version"), days(every)), nil
	}
	for _, w := range due {
		if _, err := s.search(ctx, w, TriggerScheduled); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("Searched %s of %s waiting.", countText(len(due), "movie version"), countText(len(ws), "")), nil
}

// released reports whether a movie can be downloaded yet: TMDB says it's
// released, or its digital or physical release date has passed. Cinema
// releases don't count (they only leak as camera recordings).
func released(t library.Title, now time.Time) bool {
	today := now.UTC().Format("2006-01-02")
	for _, d := range []string{t.DigitalRelease, t.PhysicalRelease} {
		if d != "" && d <= today {
			return true
		}
	}
	return t.Status == "Released" && t.DigitalRelease == "" && t.PhysicalRelease == ""
}

// RSSSync reads every indexer's newest releases and acts on any that a
// wanted version would take. quiet is true when it found nothing to act on
// and nothing went wrong, so the run isn't worth listing.
//
// Movies and TV share one read of each feed (an indexer behind the solver
// is only read hourly), split by category.
func (s *Service) RSSSync(ctx context.Context) (msg string, quiet bool, err error) {
	g := s.settings()
	var ws []wanted
	var tws []*tvWanted
	var notes []string // why a half isn't running
	if g.Mode != settings.GrabOff {
		if msg, err := s.pausedByLimit(ctx, g.Mode); err != nil {
			return "", false, err
		} else if msg != "" {
			notes = append(notes, msg)
		} else if ws, err = s.load(ctx, true); err != nil {
			return "", false, err
		}
	}
	tvSeries := false // any monitored series at all
	if tvActive(g) {
		all, err := s.loadTV(ctx)
		if err != nil {
			return "", false, err
		}
		if tvSeries = len(all) > 0; tvSeries {
			if msg, err := s.pausedByLimit(ctx, g.TVMode); err != nil {
				return "", false, err
			} else if msg != "" {
				notes = append(notes, "TV: "+msg)
			} else {
				for _, w := range all {
					// Also a series with nothing wanted, when a REPACK or
					// PROPER could fix a file it got recently (fixes.go).
					if err := s.loadRecent(ctx, w); err != nil {
						return "", false, err
					}
					if len(w.wanted) > 0 || len(w.recent) > 0 {
						tws = append(tws, w)
					}
				}
			}
		}
	}
	if len(ws) == 0 && len(tws) == 0 {
		switch {
		case len(notes) > 0:
			return strings.Join(notes, " "), true, nil
		case g.Mode == settings.GrabOff && !tvSeries:
			return "Grabbing is off (Settings → Download Client).", true, nil
		case g.Mode == settings.GrabOff:
			return "No episodes are waiting for a release (movie grabbing is off).", true, nil
		}
		return "No movies are waiting for a release.", true, nil
	}
	var cats []int
	if len(ws) > 0 {
		cats = append(cats, movieCategories...)
	}
	if len(tws) > 0 {
		cats = append(cats, tvCategories...)
	}
	feed, failures, err := s.idx.Search(ctx, core.Query{Type: "search", Categories: cats}, indexer.SearchOptions{Feed: true})
	if err != nil {
		return "", false, err
	}
	feed = dedupe(feed)
	var rels, tvRels []indexer.Release
	for _, r := range feed {
		if len(tws) > 0 && isTV(r) {
			tvRels = append(tvRels, r)
		} else {
			rels = append(rels, r)
		}
	}

	byKey := map[string][]int{}
	for i, w := range ws {
		for _, t := range append([]string{w.movie.Title}, w.movie.AltTitles...) {
			if k := decision.TitleKey(t); k != "" && !slices.Contains(byKey[k], i) {
				byKey[k] = append(byKey[k], i)
			}
		}
	}
	matched := map[int][]indexer.Release{}
	for _, r := range rels {
		p := parser.Parse(r.Title)
		for _, i := range byKey[decision.TitleKey(p.Title)] {
			if decision.SameMovie(p, ws[i].movie) {
				matched[i] = append(matched[i], r)
			}
		}
	}

	s.forgetOldRSS()
	found := 0
	for i, rs := range matched {
		w := ws[i]
		var fresh []indexer.Release
		for _, r := range rs {
			if s.firstRSSSight(w.v.Version.ID, r) {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		found++
		ds, err := s.decide(ctx, w, fresh)
		if err != nil {
			return "", false, err
		}
		accepted := 0
		for _, d := range ds {
			if d.d.Accepted {
				accepted++
			}
		}
		msg := fmt.Sprintf("RSS: %s for %s, %d acceptable.", countText(len(fresh), "new release"), w.label(), accepted)
		if accepted == 0 {
			msg += " " + commonReason(ds)
		}
		data := EventData{Trigger: TriggerRSS}
		data.Releases, data.More = verdicts(ds)
		if err := s.history.Add(ctx, s.event(w, EventRSS, msg), data); err != nil {
			return "", false, err
		}
		if accepted > 0 {
			if _, _, err := s.act(ctx, w, ds[0], TriggerRSS, false); err != nil {
				return "", false, err
			}
		}
	}
	tvFound := 0
	if len(tws) > 0 {
		if tvFound, err = s.rssTV(ctx, tws, tvRels); err != nil {
			return "", false, err
		}
	}
	msg = fmt.Sprintf("Read %s; %s for %s waiting.", countText(len(feed), "new release"),
		countText(found, "match"), countText(len(ws), "movie version"))
	if len(tws) > 0 {
		msg = fmt.Sprintf("Read %s; %s for %s and %s waiting.", countText(len(feed), "new release"),
			countText(found+tvFound, "match"), countText(len(ws), "movie version"), countText(len(tws), "series version"))
	}
	if len(notes) > 0 {
		msg += " " + strings.Join(notes, " ")
	}
	if len(failures) > 0 {
		msg += fmt.Sprintf(" %s failed.", countText(len(failures), "indexer"))
	}
	// Quiet: no release matched a wanted version and no indexer failed.
	return msg, found+tvFound == 0 && len(failures) == 0, nil
}

func rssKey(versionID int64, r indexer.Release) string {
	id := r.InfoHash
	if id == "" {
		id = r.GUID
	}
	if id == "" {
		id = r.Title
	}
	return fmt.Sprintf("%d|%d|%s", versionID, r.IndexerID, id)
}

func (s *Service) firstRSSSight(versionID int64, r indexer.Release) bool {
	s.rssMu.Lock()
	defer s.rssMu.Unlock()
	k := rssKey(versionID, r)
	if _, ok := s.rssSeen[k]; ok {
		return false
	}
	s.rssSeen[k] = s.now()
	return true
}

func (s *Service) forgetOldRSS() {
	s.rssMu.Lock()
	defer s.rssMu.Unlock()
	cutoff := s.now().Add(-48 * time.Hour)
	for k, t := range s.rssSeen {
		if t.Before(cutoff) {
			delete(s.rssSeen, k)
		}
	}
}

// dedupe drops the same torrent found on several indexers (or twice on
// one), keeping the copy with the most seeders.
func dedupe(rels []indexer.Release) []indexer.Release {
	seen := map[string]int{}
	var out []indexer.Release
	for _, r := range rels {
		k := r.InfoHash
		if k == "" {
			k = fmt.Sprintf("%s|%d", strings.ToLower(r.Title), r.Size)
		}
		if i, ok := seen[k]; ok {
			if r.Seeders > out[i].Seeders {
				out[i] = r
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, r)
	}
	return out
}

// Radarr's indexer flags, as custom formats test them.
const (
	flagFreeleech = 1
	flagHalfleech = 2
)

// indexerFlags reads freeleech from the download factor: 0 means the
// download doesn't count against your ratio (public trackers report 0
// too, which is true enough), 0.5 counts half. Indexers that don't say
// report 1.
func indexerFlags(r core.Release) int {
	switch r.DownloadFactor {
	case 0:
		return flagFreeleech
	case 0.5:
		return flagHalfleech
	}
	return 0
}

func verdict(c candidate) Verdict {
	v := Verdict{
		Name: c.rel.Title, Indexer: c.rel.Indexer, Size: c.rel.Size, Seeders: c.rel.Seeders,
		Quality: c.d.Quality.Name, Score: c.d.Score, Accepted: c.d.Accepted, Upgrade: c.d.Upgrade,
		Rejections: c.d.Rejections,
	}
	for _, f := range c.d.Formats {
		v.Formats = append(v.Formats, FormatScore{f.Name, f.Score})
	}
	return v
}

// verdicts keeps the best maxRecorded, and says how many were left out.
func verdicts(cs []candidate) ([]Verdict, int) {
	var out []Verdict
	for i, c := range cs {
		if i == maxRecorded {
			return out, len(cs) - maxRecorded
		}
		out = append(out, verdict(c))
	}
	return out, 0
}

// commonReason names the most frequent reason releases were turned down,
// so a search that found nothing acceptable says why at a glance.
func commonReason(cs []candidate) string {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, c := range cs {
		for _, r := range c.d.Rejections {
			counts[r]++
			if counts[r] > bestN {
				best, bestN = r, counts[r]
			}
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("Most common reason (%d of %d): %s", bestN, len(cs), best)
}

func countText(n int, word string) string {
	if word == "" {
		return fmt.Sprint(n)
	}
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "ch") {
		return fmt.Sprintf("%d %ses", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func days(d time.Duration) string {
	n := int(d.Hours() / 24)
	if n == 1 {
		return "day"
	}
	return fmt.Sprintf("%d days", n)
}

// queueFull reports whether grabbing in this mode (movies' or TV's) is On
// with a queue limit that Activity → Queue has already reached. Movies and
// TV share the queue and the limit. (Dry run and Off have no limit: they
// grab nothing.)
func (s *Service) queueFull(ctx context.Context, mode string) (bool, error) {
	g := s.settings()
	if mode != settings.GrabOn || g.QueueLimit <= 0 || s.queue == nil {
		return false, nil
	}
	n, err := s.queue.QueueSize(ctx)
	return n >= g.QueueLimit, err
}

// QueueFull reports whether automatic grabbing is held back by the queue
// limit: movie or TV grabbing is On and a queue of this size is at or over
// the limit. The Queue page, the sidebar and the Dashboard show it.
func QueueFull(g settings.Grabbing, size int) bool {
	return g.QueueLimit > 0 && size >= g.QueueLimit && (g.Mode == settings.GrabOn || g.TVMode == settings.GrabOn)
}

// queueFullReason is the History text for a grab held back by the limit.
func queueFullReason(limit int) string {
	return fmt.Sprintf("the queue is full (%s, the limit in Settings → Download Client); Reelkeeper grabs again when downloads finish.",
		countText(limit, "download"))
}

// GrabsToday counts the grabs made since local midnight (the Dashboard
// summary's "grabs today").
func (s *Service) GrabsToday(ctx context.Context) (int, error) {
	return s.history.countSince(ctx, EventGrabbed, s.dayStart())
}

// dayStart is the most recent local midnight.
func (s *Service) dayStart() time.Time {
	y, m, d := s.now().In(s.loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.loc)
}

// diskShort says why the download disk can't take another download right
// now (see download.Service.DiskShort), or "" when it can or can't be told.
func (s *Service) diskShort(ctx context.Context) string {
	dc, ok := s.queue.(diskChecker)
	if !ok {
		return ""
	}
	why, err := dc.DiskShort(ctx)
	if err != nil {
		return ""
	}
	return why
}

// spaceResumesText ends the messages about a full download disk.
const spaceResumesText = "Searching resumes when cleanup makes room."

// pausedForSpace explains why the scheduled pass does nothing while the
// download disk is short of space (only when grabbing is On; a dry run
// downloads nothing). Searching then would only spend the indexers' query
// limits on releases that couldn't be grabbed, and a release skipped for
// space doesn't count as searched, so it would be searched again every
// hour. RSS sync still reads its feeds.
func (s *Service) pausedForSpace(ctx context.Context, mode string) string {
	if mode != settings.GrabOn {
		return ""
	}
	if why := s.diskShort(ctx); why != "" {
		return "Paused: " + why + " " + spaceResumesText
	}
	return ""
}

// pausedByLimit explains why the scheduled pass and RSS sync do nothing
// while the queue is full: searching then would only spend the indexers'
// query limits on releases that can't be grabbed.
func (s *Service) pausedByLimit(ctx context.Context, mode string) (string, error) {
	if mode == settings.GrabOn && s.moving() {
		return "Paused: torrents are moving to another download client. Searching resumes when that's done.", nil
	}
	full, err := s.queueFull(ctx, mode)
	if err != nil || !full {
		return "", err
	}
	n, err := s.queue.QueueSize(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Paused: the queue is full (%d of %d downloads). Searching resumes when downloads finish, "+
		"or straight away if the limit is raised.", n, s.settings().QueueLimit), nil
}

// LimitLifted reports whether a change to the Grabbing settings may let
// through grabs that were held back, so a search pass should start now:
// movie or TV grabbing turned On, or the queue limit raised or removed.
// Lowering the limit needs nothing: every grab checks it.
func LimitLifted(before, after settings.Grabbing) bool {
	if after.Mode != settings.GrabOn && after.TVMode != settings.GrabOn {
		return false
	}
	if (after.Mode == settings.GrabOn && before.Mode != settings.GrabOn) ||
		(after.TVMode == settings.GrabOn && before.TVMode != settings.GrabOn) {
		return true
	}
	if before.QueueLimit == 0 {
		return false
	}
	return after.QueueLimit == 0 || after.QueueLimit > before.QueueLimit
}
