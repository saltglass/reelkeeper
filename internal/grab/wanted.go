package grab

// The Wanted page: everything Reelkeeper is still looking for, and in
// plain words why it hasn't got it yet.
//
// What counts as wanted is decided by the same rules the searches use
// (load, released, SeriesVersion.Wanted, CutoffMet), so the page can't
// disagree with what the scheduled pass actually does. This file only adds
// the explanation: where each item's search stands, taken from the version's
// search mark, the Grabbing settings and the latest "searched" event in
// History.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// Wanted item kinds.
const (
	WantedMovie    = "movie"
	WantedEpisodes = "episodes"
)

// Where an item's search stands (the API's `state`).
const (
	StateDownloading = "downloading"
	StateOff         = "off"
	StateCapped      = "capped"
	StateNever       = "never"
	StateWaiting     = "waiting"
)

// How far back TV searches are read to find the latest one for a season,
// and how many per series version: a show searched every few days has a
// handful in a month, and a missing event only means no message.
const (
	wantedTVWindow     = 30 * 24 * time.Hour
	perVersionSearches = 20
)

// Status lines (see explain).
const (
	dryRunNote      = " Dry run: Reelkeeper notes what it would grab but downloads nothing."
	dueNowText      = "Due now: in line for the next scheduled search."
	neverText       = "Not searched yet. In line for the next scheduled search."
	downloadingText = "Downloading (Activity → Queue)."
	offText         = "Grabbing is off, so Reelkeeper doesn't search for it (Settings → Download Client)."
	cappedText      = "The queue is full (Settings → Download Client); searching resumes when downloads finish."
	airedTodayText  = "Aired today. Searching starts tomorrow (the RSS check may find it sooner)."
	// A movie searched without being marked searched: the queue limit held
	// its grab back, or no indexer answered. It stays first in line.
	heldBackText = "Searched, but the full queue or a resting indexer held it back, so it's first in line for the next scheduled search."
)

// WantedEpisode is one wanted episode.
type WantedEpisode struct {
	EpisodeID int64
	Season    int
	Number    int
	Name      string
	AirDate   string
}

// WantedItem is a movie version, or one season's wanted episodes of a
// series version, with where its search stands.
type WantedItem struct {
	Kind       string
	TitleID    int64
	VersionID  int64
	Title      string
	Year       int
	PosterPath string
	Profile    string
	// Date is "YYYY-MM-DD": when a movie became available, or the newest
	// air date among a group's episodes.
	Date   string
	State  string
	Status string
	// LastSearchedAt is the latest search (nil: never searched).
	LastSearchedAt *time.Time
	// LastSearchMessage is that search's History message ("" if none was
	// found, e.g. pruned).
	LastSearchMessage string
	// NextSearchAt is when the scheduled pass will next take it (waiting
	// items only; in the past means due now).
	NextSearchAt *time.Time
	// Upgrades: the file's quality and the profile's "upgrade until".
	Quality string
	Target  string
	// Upgrades whose quality already reaches "upgrade until": the file's
	// custom-format score, the score the profile upgrades until, and the
	// scored formats the file matches. Score is nil for the others, and
	// TargetScore when the profile has no score target (one made in the
	// simple editor stops at the quality; only a blocked format is left to
	// replace, and Formats names it).
	Score       *int
	TargetScore *int
	Formats     []decision.FormatMatch
	// Episode groups.
	Season   *int
	Episodes []WantedEpisode
}

// WantedList is the whole Wanted page.
type WantedList struct {
	// Missing holds movies and episode groups, newest first.
	Missing []WantedItem
	// Upgrades holds movies below their profile's target, newest first.
	Upgrades        []WantedItem
	MissingMovies   int
	MissingEpisodes int
}

// Wanted lists what Reelkeeper is still looking for. With full false, only
// the lists' sizes matter to the caller (the sidebar's count): the lists
// are still built, but History isn't read.
//
// A fixed handful of queries, whatever the library's size.
func (s *Service) Wanted(ctx context.Context, full bool) (*WantedList, error) {
	now := s.now()
	g := s.settings()
	movieCapped, err := s.queueFull(ctx, g.Mode)
	if err != nil {
		return nil, err
	}
	tvCapped, err := s.queueFull(ctx, g.TVMode)
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
	activeVersions := map[int64]bool{}
	activeEpisodes := map[int64]map[int64]bool{}
	if s.queue != nil {
		if activeVersions, err = s.queue.ActiveVersions(ctx); err != nil {
			return nil, err
		}
		if activeEpisodes, err = s.queue.ActiveEpisodes(ctx); err != nil {
			return nil, err
		}
	}

	out := &WantedList{Missing: []WantedItem{}, Upgrades: []WantedItem{}}

	// Movies: the same set the scheduled pass starts from, before it drops
	// versions that are downloading or already have what they need.
	mws, err := s.load(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, w := range mws {
		if !released(w.v.Title, now) {
			continue
		}
		it := WantedItem{
			Kind: WantedMovie, TitleID: w.v.Title.ID, VersionID: w.v.Version.ID, Title: w.v.Title.Title,
			Year: w.v.Title.Year, PosterPath: w.v.Title.PosterPath, Profile: w.pname, Date: availableDate(w.v.Title),
		}
		upgrade := false
		switch {
		case w.have == nil:
		case !w.v.Version.Upgrades:
			continue // has a file and upgrades are off: nothing is wanted
		case !w.profile.CutoffMet(w.have, w.movie):
			upgrade = true
			it.Quality, it.Target = w.have.Quality.Name, w.profile.UpgradeUntil
			if w.profile.QualityReached(w.have.Quality) {
				score, formats := w.profile.FileScore(w.have, w.movie)
				it.Score = &score
				if target := w.profile.UpgradeUntilScore; target > decision.BlockScore+1 {
					it.TargetScore = &target
				}
				for _, f := range formats {
					if f.Score != 0 {
						it.Formats = append(it.Formats, f)
					}
				}
			}
		default:
			continue // has what its profile aims for
		}
		in := explainIn{mode: g.Mode, capped: movieCapped, downloading: activeVersions[w.v.Version.ID], searchable: 1, now: now}
		if last := w.v.Version.LastSearchAt; last != nil {
			in.marks, in.dueMarks = []time.Time{*last}, []time.Time{*last}
		}
		s.explain(&it, in)
		if upgrade {
			out.Upgrades = append(out.Upgrades, it)
		} else {
			out.Missing = append(out.Missing, it)
			out.MissingMovies++
		}
	}

	// TV: wanted episodes of each monitored series version, a group per
	// season (a season pack is searched as one).
	svs, err := s.lib.SeriesVersions(ctx)
	if err != nil {
		return nil, err
	}
	today := s.localToday()
	for _, sv := range svs {
		bySeason := map[int][]library.Episode{}
		var seasons []int
		for _, e := range sv.Wanted {
			if _, ok := bySeason[*e.Season]; !ok {
				seasons = append(seasons, *e.Season)
			}
			bySeason[*e.Season] = append(bySeason[*e.Season], e)
		}
		for _, n := range seasons {
			eps := bySeason[n]
			season := n
			it := WantedItem{
				Kind: WantedEpisodes, TitleID: sv.Title.ID, VersionID: sv.Version.ID, Title: sv.Title.Title,
				Year: sv.Title.Year, PosterPath: sv.Title.PosterPath, Profile: names[sv.Version.ProfileID], Season: &season,
			}
			in := explainIn{mode: g.TVMode, capped: tvCapped, now: now}
			for _, e := range eps {
				it.Episodes = append(it.Episodes, WantedEpisode{EpisodeID: e.ID, Season: *e.Season, Number: *e.Number, Name: e.Name, AirDate: e.AirDate})
				if e.AirDate > it.Date {
					it.Date = e.AirDate
				}
				if activeEpisodes[sv.Version.ID][e.ID] {
					in.downloading = true
				}
				t, searched := sv.Searched[e.ID]
				if searched {
					in.marks = append(in.marks, t)
				}
				// Episodes are searched from the day after they air.
				if e.AirDate == "" || e.AirDate >= today {
					continue
				}
				in.searchable++
				if searched {
					in.dueMarks = append(in.dueMarks, t)
				}
			}
			s.explain(&it, in)
			out.Missing = append(out.Missing, it)
			out.MissingEpisodes += len(eps)
		}
	}

	if full {
		if err := s.addSearches(ctx, out); err != nil {
			return nil, err
		}
	}

	byDate := func(a, b WantedItem) int {
		if c := strings.Compare(b.Date, a.Date); c != 0 {
			return c
		}
		if c := strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.VersionID, b.VersionID); c != 0 {
			return c
		}
		return cmp.Compare(seasonOf(b), seasonOf(a))
	}
	slices.SortStableFunc(out.Missing, byDate)
	slices.SortStableFunc(out.Upgrades, byDate)
	return out, nil
}

// WantedTitleIDs are the titles that have something wanted right now: a
// monitored version missing a file or below its cutoff, or a series with a
// monitored missing episode. It is the Wanted page's own answer, so the two
// can't disagree. The blocklist uses it to tell which titles are dormant.
func (s *Service) WantedTitleIDs(ctx context.Context) (map[int64]bool, error) {
	l, err := s.Wanted(ctx, false)
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, list := range [][]WantedItem{l.Missing, l.Upgrades} {
		for _, it := range list {
			ids[it.TitleID] = true
		}
	}
	return ids, nil
}

func seasonOf(it WantedItem) int {
	if it.Season == nil {
		return 0
	}
	return *it.Season
}

// availableDate is when a movie became available: its earliest digital or
// physical release, else TMDB's release date ("YYYY-MM-DD"). Only released
// movies are listed, so the date is never in the future.
func availableDate(t library.Title) string {
	best := ""
	for _, d := range []string{t.DigitalRelease, t.PhysicalRelease} {
		if d != "" && (best == "" || d < best) {
			best = d
		}
	}
	if best == "" {
		best = t.ReleaseDate
	}
	if len(best) > 10 {
		best = best[:10]
	}
	return best
}

// explainIn is what an item's explanation is worked out from.
type explainIn struct {
	mode        string // the Grabbing mode for its media type
	capped      bool
	downloading bool
	// marks are when any of its parts was last searched.
	marks []time.Time
	// searchable counts the parts the scheduled pass can search now (a
	// movie: 1; episodes from the day after they air). dueMarks are when
	// those were last searched; the others never were.
	searchable int
	dueMarks   []time.Time
	now        time.Time
}

// explain sets an item's state, plain-English status, last-search time and
// next-search time. The order matters: a download in progress beats
// everything, then Grabbing off, then the queue limit.
func (s *Service) explain(it *WantedItem, in explainIn) {
	if len(in.marks) > 0 {
		latest := slices.MaxFunc(in.marks, time.Time.Compare)
		it.LastSearchedAt = &latest
	}
	switch {
	case in.downloading:
		it.State, it.Status = StateDownloading, downloadingText
		return
	case in.mode == settings.GrabOff:
		it.State, it.Status = StateOff, offText
		return
	case in.capped:
		it.State, it.Status = StateCapped, cappedText
		return
	}
	switch {
	case in.searchable == 0:
		it.State, it.Status = StateNever, airedTodayText
	case len(in.marks) == 0:
		it.State, it.Status = StateNever, neverText
	default:
		it.State = StateWaiting
		// Due when the least recently searched part is, and at once if a
		// part was never searched.
		due := in.now
		if len(in.dueMarks) == in.searchable {
			due = slices.MinFunc(in.dueMarks, time.Time.Compare).Add(searchEvery)
		}
		it.NextSearchAt = &due
		if !due.After(in.now) {
			it.Status = dueNowText
		} else {
			it.Status = "Next search in " + inAbout(due.Sub(in.now)) + "."
		}
	}
	if in.mode == settings.GrabDryRun {
		it.Status += dryRunNote
	}
}

// inAbout says how long to wait in rough, plain terms: "under an hour",
// "about 5 hours", "about a day", "about 2 days".
func inAbout(d time.Duration) string {
	switch h := int(d.Round(time.Hour).Hours()); {
	case d < 45*time.Minute:
		return "under an hour"
	case h <= 1:
		return "about an hour"
	case h < 22:
		return fmt.Sprintf("about %d hours", h)
	case h < 36:
		return "about a day"
	default:
		return fmt.Sprintf("about %d days", int((d+12*time.Hour)/(24*time.Hour)))
	}
}

// addSearches fills in each item's last search from History: one query for
// all movie versions and one for all series versions.
func (s *Service) addSearches(ctx context.Context, l *WantedList) error {
	var movieIDs, tvIDs []int64
	for _, list := range [][]WantedItem{l.Missing, l.Upgrades} {
		for _, it := range list {
			if it.Kind == WantedMovie {
				movieIDs = append(movieIDs, it.VersionID)
			} else if !slices.Contains(tvIDs, it.VersionID) {
				tvIDs = append(tvIDs, it.VersionID)
			}
		}
	}
	latest, err := s.history.latestSearches(ctx, movieIDs)
	if err != nil {
		return err
	}
	recent, err := s.history.recentSearches(ctx, tvIDs, s.now().Add(-wantedTVWindow), perVersionSearches)
	if err != nil {
		return err
	}
	// Each recent event's episodes, decoded once, newest event first.
	type searchOf struct {
		ev  Event
		ids []int64
	}
	byVersion := map[int64][]searchOf{}
	for _, ev := range recent {
		if ev.VersionID == nil {
			continue
		}
		var data EventData
		_ = json.Unmarshal([]byte(ev.Data), &data) // damaged data just matches nothing
		byVersion[*ev.VersionID] = append(byVersion[*ev.VersionID], searchOf{ev, data.EpisodeIDs})
	}

	for _, list := range [][]WantedItem{l.Missing, l.Upgrades} {
		for i := range list {
			it := &list[i]
			var ev *Event
			if it.Kind == WantedMovie {
				if e, ok := latest[it.VersionID]; ok {
					ev = &e
				}
			} else {
				for _, so := range byVersion[it.VersionID] {
					if slices.ContainsFunc(it.Episodes, func(e WantedEpisode) bool { return slices.Contains(so.ids, e.EpisodeID) }) {
						e := so.ev
						ev = &e
						break
					}
				}
			}
			if ev == nil {
				continue
			}
			it.LastSearchMessage = ev.Message
			if it.Kind == WantedMovie && it.State == StateNever {
				dry := strings.HasSuffix(it.Status, dryRunNote)
				it.Status = heldBackText
				if dry {
					it.Status += dryRunNote
				}
			}
			if it.LastSearchedAt == nil || ev.CreatedAt.After(*it.LastSearchedAt) {
				t := ev.CreatedAt
				it.LastSearchedAt = &t
			}
		}
	}
	return nil
}

// latestSearches returns each version's newest "searched" event, without
// the (large) release list. One query.
func (h *History) latestSearches(ctx context.Context, versionIDs []int64) (map[int64]Event, error) {
	out := map[int64]Event{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	var evs []Event
	err := h.db.DB.NewSelect().Model(&evs).Column("id", "version_id", "created_at", "message").
		Where("id IN (SELECT MAX(id) FROM history WHERE event = ? AND version_id IN (?) GROUP BY version_id)",
			EventSearched, bun.List(versionIDs)).Scan(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		if e.VersionID != nil {
			out[*e.VersionID] = e
		}
	}
	return out, nil
}

// recentSearches returns each version's newest "searched" events since a
// time (at most perVersion each), newest first, with their data (which
// episodes they were for). One query.
func (h *History) recentSearches(ctx context.Context, versionIDs []int64, since time.Time, perVersion int) ([]Event, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	var evs []Event
	err := h.db.DB.NewSelect().Model(&evs).Column("id", "version_id", "created_at", "message", "data").
		Where(`id IN (SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY version_id ORDER BY id DESC) AS rn
			FROM history WHERE event = ? AND created_at >= ? AND version_id IN (?)) AS ranked WHERE rn <= ?)`,
			EventSearched, since.UTC(), bun.List(versionIDs), perVersion).
		Order("id DESC").Scan(ctx)
	return evs, err
}
