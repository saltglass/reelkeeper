package grab

// "Choose a release": a movie version is searched on request, and every
// release found is shown with the profile's verdict on it, so the user can
// pick one themselves, including one the profile turned down (a German
// film for an English profile, an old film that only exists as a DVD rip).
//
// The picker shares its short-lived tokens, refusals and dry-run handling
// with backfill's "Show options" (backfill.go), which does the same for a
// show's missing episodes.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// maxChoices bounds how many releases "Choose a release" lists: the best
// ones, then the closest misses. More would only be noise on a phone.
const maxChoices = 50

// Refusal is why a release the user picked can't be grabbed: the same
// reasons that stop an automatic grab when the grab couldn't work (no
// download client, a media folder Reelkeeper doesn't manage, no room on the
// download disk, a blocklisted release).
type Refusal string

func (r Refusal) Error() string { return string(r) }

// MovieChoice is one release offered under "Choose a release".
type MovieChoice struct {
	// Token identifies the release when it is grabbed; see GrabMovieChoice.
	Token   string
	Title   string
	Size    int64
	Quality string
	Indexer string
	Seeders int
	// Usenet marks a Usenet release (it has no seeders).
	Usenet bool
	Score  int
	// Accepted means the profile would take it; Reasons say why not.
	Accepted bool
	Reasons  []string
}

// MovieChoiceList is what "Choose a release" offers for a movie version.
type MovieChoiceList struct {
	// Releases come best first: the ones the profile accepts, then the ones
	// it turned down, fewest broken rules first.
	Releases []MovieChoice
	// Hidden counts releases never offered: for another movie, on the
	// blocklist, or torrents nobody seeds.
	Hidden int
	// More counts releases past maxChoices, left out.
	More int
	// IndexerErrors name the indexers that failed or weren't asked.
	IndexerErrors []string
}

// movieQuery is the indexer search for a movie, for both automatic
// searches and "Choose a release".
func movieQuery(t library.Title) core.Query {
	return core.Query{
		Type: "movie-search", Keywords: t.Title, Year: t.Year, IMDbID: t.IMDbID, TMDBID: t.TMDBID,
		Categories: movieCategories,
	}
}

// loadMovieVersion loads one movie version for picking, checking that it
// belongs to the movie the request named.
func (s *Service) loadMovieVersion(ctx context.Context, titleID, versionID int64) (wanted, error) {
	ws, err := s.load(ctx, false, versionID)
	if err != nil {
		return wanted{}, err
	}
	if len(ws) == 0 || ws[0].v.Title.ID != titleID {
		return wanted{}, library.ErrNotFound
	}
	return ws[0], nil
}

// MovieChoices searches every indexer for a movie version and lists the
// releases with the profile's verdict on each, the ones it accepts first
// and then the closest misses (fewest broken rules, then the profile's own
// ranking). The version's Upgrades switch is ignored, since the user is
// asking to choose. Nothing is stored except the short-lived tokens that
// let the user take one, and nothing is grabbed.
func (s *Service) MovieChoices(ctx context.Context, titleID, versionID int64) (*MovieChoiceList, error) {
	w, err := s.loadMovieVersion(ctx, titleID, versionID)
	if err != nil {
		return nil, err
	}
	rels, failures, err := s.idx.Search(ctx, movieQuery(w.v.Title), indexer.SearchOptions{})
	if err != nil {
		return nil, err
	}
	blocked := func(string, string) bool { return false }
	if s.queue != nil {
		if blocked, err = s.queue.Blocklisted(ctx, w.v.Title.ID); err != nil {
			return nil, err
		}
	}
	judge := w
	judge.v.Version.Upgrades = true
	cs, err := s.decide(ctx, judge, dedupe(rels))
	if err != nil {
		return nil, err
	}

	out := &MovieChoiceList{Releases: []MovieChoice{}}
	for _, f := range failures {
		out.IndexerErrors = append(out.IndexerErrors, f.Indexer+": "+f.Message)
	}
	var offered []candidate
	for _, c := range cs {
		if c.d.WrongContent || blocked(c.rel.Title, c.rel.InfoHash) || (c.rel.Seeders <= 0 && !c.rel.Usenet()) {
			out.Hidden++
			continue
		}
		offered = append(offered, c)
	}
	slices.SortStableFunc(offered, func(a, b candidate) int {
		if !a.d.Accepted && !b.d.Accepted {
			if c := cmp.Compare(len(a.d.Rejections), len(b.d.Rejections)); c != 0 {
				return c
			}
		}
		return w.profile.Compare(a.d, b.d)
	})
	if len(offered) > maxChoices {
		out.More = len(offered) - maxChoices
		offered = offered[:maxChoices]
	}

	s.optMu.Lock()
	s.forgetOldOptionsLocked()
	s.optMu.Unlock()
	for _, c := range offered {
		token, err := s.rememberOption(pickedOption{movie: true, versionID: versionID, label: w.label(), rel: c.rel, d: c.d})
		if err != nil {
			return nil, err
		}
		out.Releases = append(out.Releases, MovieChoice{
			Token: token, Title: c.rel.Title, Size: c.rel.Size, Quality: c.d.Quality.Name, Indexer: c.rel.Indexer,
			Seeders: c.rel.Seeders, Usenet: c.rel.Usenet(), Score: c.d.Score, Accepted: c.d.Accepted,
			Reasons: c.d.Rejections,
		})
	}
	return out, nil
}

// PickResult is what grabbing a picked release did.
type PickResult struct {
	// Message is what History says about it.
	Message string
	// DryRun: Grabbing is off, so nothing was downloaded; History records
	// what would have been grabbed.
	DryRun bool
	// UpgradesOff: the release was outside the profile, so the version's
	// Upgrades switch was turned off to keep the file it brings.
	UpgradesOff bool
}

// GrabMovieChoice grabs the release behind a token from MovieChoices.
//
// A release outside the profile turns the version's Upgrades switch off,
// or the next scheduled search would replace the file with one the profile
// likes (an English copy of a German film). The queue limit doesn't apply
// and the download goes to the top of the client's queue, since someone
// is waiting for it. Refused as for an automatic grab: no download client,
// a folder with "Manage files" off, too little space, a blocklisted
// release, a download already in progress for the version. With Grabbing
// off it only records "would grab".
func (s *Service) GrabMovieChoice(ctx context.Context, titleID, versionID int64, token string) (PickResult, error) {
	o, err := s.option(token, func(o pickedOption) bool { return o.movie && o.versionID == versionID })
	if err != nil {
		return PickResult{}, err
	}
	w, err := s.loadMovieVersion(ctx, titleID, versionID)
	if err != nil {
		return PickResult{}, err
	}
	outside := !o.d.Accepted
	c := candidate{rel: o.rel, d: o.d}
	c.d.Upgrade = w.have != nil
	v := verdict(c)
	ev := s.event(w, "", "")
	ev.SourceTitle, ev.Indexer, ev.Quality, ev.Score = o.rel.Title, o.rel.Indexer, o.d.Quality.Name, o.d.Score
	data := EventData{Trigger: TriggerPicked, Chosen: &v}
	picked := "Picked by you."
	if outside {
		picked = "Picked by you outside the profile: " + strings.Join(o.d.Rejections, " ")
	}

	if err := s.pickRefusal(ctx, w.v.Title.ID, w.v.Version.RootFolderID, o); err != nil {
		return PickResult{}, err
	}
	if s.queue != nil {
		active, err := s.queue.ActiveVersions(ctx)
		if err != nil {
			return PickResult{}, err
		}
		if active[versionID] {
			return PickResult{}, Refusal("A download for this movie is already in progress (Activity → Queue).")
		}
	}

	if s.settings().Mode != settings.GrabOn {
		s.forgetOption(token)
		ev.Event = EventWouldGrab
		ev.Message = fmt.Sprintf("Would grab %s (%s, from %s). %s Dry run: nothing was downloaded.",
			o.rel.Title, o.d.Quality.Name, o.rel.Indexer, picked)
		return PickResult{Message: ev.Message, DryRun: true}, s.history.Add(ctx, ev, data)
	}
	if s.grabber == nil {
		return PickResult{}, Refusal(noClientText)
	}
	if err := s.grabber.Grab(ctx, Grabbed{Release: o.rel, Version: w.v, Quality: o.d.Quality.Name, Score: o.d.Score,
		Upgrade: c.d.Upgrade, Requested: true}); err != nil {
		return PickResult{}, grabRefusal(err)
	}
	s.forgetOption(token)
	res := PickResult{}
	if outside && w.v.Version.Upgrades {
		if err := s.lib.SetUpgrades(ctx, w.v.Title.ID, versionID, false); err != nil {
			return PickResult{}, err
		}
		res.UpgradesOff = true
		picked += " Upgrades are now off for this version, so Reelkeeper keeps this file."
	}
	ev.Event = EventGrabbed
	ev.Message = fmt.Sprintf("Grabbed %s (%s, from %s). %s", o.rel.Title, o.d.Quality.Name, o.rel.Indexer, picked)
	res.Message = ev.Message
	return res, s.history.Add(ctx, ev, data)
}

// option finds a token's release, if it hasn't expired and belongs to what
// the request is about (mine).
func (s *Service) option(token string, mine func(pickedOption) bool) (pickedOption, error) {
	s.optMu.Lock()
	defer s.optMu.Unlock()
	s.forgetOldOptionsLocked()
	o, ok := s.options[token]
	if !ok || !mine(o) {
		return pickedOption{}, ErrOptionGone
	}
	return o, nil
}

// forgetOption makes a token unusable once its release is taken.
func (s *Service) forgetOption(token string) {
	s.optMu.Lock()
	defer s.optMu.Unlock()
	delete(s.options, token)
}

// pickRefusal refuses a release the user picked that is on the blocklist,
// or whose title's version lives in a media folder Reelkeeper doesn't
// manage (the download could never be imported). Both apply in a dry run
// too; a missing download client doesn't, as for automatic grabs.
func (s *Service) pickRefusal(ctx context.Context, titleID, rootFolderID int64, o pickedOption) error {
	if s.queue != nil {
		blocked, err := s.queue.Blocklisted(ctx, titleID)
		if err != nil {
			return err
		}
		if blocked(o.rel.Title, o.rel.InfoHash) {
			return Refusal("That release is on the blocklist.")
		}
	}
	reason, err := s.unmanaged(ctx, rootFolderID)
	if err != nil {
		return err
	}
	if reason != "" {
		return Refusal(reason)
	}
	return nil
}

// noClientText is the refusal when no download client is set up.
const noClientText = "No download client is set up yet (Settings → Download Client)."

// grabRefusal turns the download client's refusal into words for the user.
func grabRefusal(err error) error {
	switch {
	case errors.Is(err, ErrMoving):
		return Refusal("Torrents are moving to another download client; try again when that's done (System → Tasks).")
	case errors.Is(err, ErrNotEnoughSpace):
		return Refusal(strings.TrimPrefix(err.Error(), ErrNotEnoughSpace.Error()+": ") + ".")
	}
	return Refusal("The download client refused it: " + err.Error())
}
