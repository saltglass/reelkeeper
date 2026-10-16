package grab

// TV fixes (2026-10-12): TV stops at the first acceptable file, with
// one narrow exception. When a release group fixes a broken episode with a
// REPACK, or another group with a PROPER, RSS sync may replace the file
// Reelkeeper downloaded in the last two weeks. decision.Profile.CheckFix is
// the rule; this is the plumbing: which episodes are eligible, running the
// usual TV decision on the release, and recording why in History. Only RSS
// looks for fixes (no extra searches, so no extra load on the indexers).

import (
	"context"
	"fmt"
	"strings"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// loadRecent fills in which of a version's episodes can be fixed: those
// with a file from a download imported within decision.FixWindow. It does
// nothing unless the version's profile prefers propers.
func (s *Service) loadRecent(ctx context.Context, w *tvWanted) error {
	if !w.profile.PreferPropers || s.queue == nil || len(w.sv.HasFile) == 0 {
		return nil
	}
	cur, err := s.queue.CurrentEpisodeDownloads(ctx, w.sv.Version.ID, s.now().Add(-decision.FixWindow))
	if err != nil {
		return err
	}
	w.recent = map[int64]decision.Current{}
	for id, c := range cur {
		if w.sv.HasFile[id] {
			w.recent[id] = c
		}
	}
	return nil
}

// rssFixes looks at new releases that are a PROPER or REPACK (kSeason is
// the season they're for; a daily show's, < 0, any season) and grabs those
// that fix files the version has, as decision.Profile.CheckFix says.
// Releases that aren't about a recent download's episodes are left alone
// without a word: they're none of its business.
func (s *Service) rssFixes(ctx context.Context, w *tvWanted, kSeason int, fresh []indexer.Release) error {
	var rels []indexer.Release
	for _, r := range fresh {
		if parser.Parse(r.Title).Revision >= 2 {
			rels = append(rels, r)
		}
	}
	// Everything the season has a file for: a season pack replaces all of
	// it or nothing.
	var have []library.Episode
	for _, e := range w.sv.Episodes {
		if w.sv.HasFile[e.ID] && e.Season != nil && e.Number != nil && (kSeason < 0 || *e.Season == kSeason) {
			have = append(have, e)
		}
	}
	if len(rels) == 0 || len(have) == 0 {
		return nil
	}
	season := kSeason
	if season < 0 {
		season = *have[0].Season
	}
	u := tvUnit{w: w, season: season, episodes: have, pack: true}
	cs, err := s.decideTV(ctx, w, u.target(), rels)
	if err != nil {
		return err
	}

	now := s.now()
	fixes := map[string]decision.Fix{}
	var kept []tvCandidate
	for _, c := range cs {
		if c.d.WrongContent {
			continue // another show, season or episode
		}
		var haves []decision.Current
		var missing []string
		for _, e := range have {
			if !covers(c.cov, e) {
				continue
			}
			if cur, ok := w.recent[e.ID]; ok {
				haves = append(haves, cur)
			} else {
				missing = append(missing, episodeName(w, e))
			}
		}
		if len(haves) == 0 {
			continue // none of its episodes has a recent download
		}
		reject := func(why string) {
			c.d.Rejections = append(c.d.Rejections, why)
			c.d.Accepted = false
		}
		if len(missing) > 0 {
			if len(missing) > 3 {
				missing = append(missing[:3], "…")
			}
			reject(fmt.Sprintf("Some of the files it would replace (%s) weren't downloaded by Reelkeeper in the last %d days.",
				strings.Join(missing, ", "), int(decision.FixWindow.Hours()/24)))
		} else if fix, why := w.profile.CheckFix(haves, c.rel.Title, now); why != "" {
			reject(why)
		} else {
			fixes[c.rel.Title] = fix
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return nil
	}

	accepted := 0
	for _, c := range kept {
		if c.d.Accepted {
			accepted++
		}
	}
	msg := fmt.Sprintf("RSS: %s to fix files of %s, %d acceptable.", countText(len(kept), "PROPER or REPACK release"), w.label(), accepted)
	if accepted == 0 {
		msg += " " + commonReason(plain(kept))
	}
	data := EventData{Trigger: TriggerRSS, EpisodeIDs: u.episodeIDs()}
	data.Releases, data.More = verdicts(plain(kept))
	if err := s.history.Add(ctx, s.tvEvent(w, EventRSS, msg), data); err != nil {
		return err
	}
	for _, p := range pickTV(kept, have) {
		fix := fixes[p.c.rel.Title]
		p.fix = &fix
		if _, _, err := s.actTV(ctx, w, p, TriggerRSS, false); err != nil {
			return err
		}
	}
	return nil
}
