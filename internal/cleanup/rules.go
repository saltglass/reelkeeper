// Package cleanup decides what Reelkeeper removes from the download client, and
// why. It only decides: internal/download carries the decisions out (or,
// in dry run, records them), and only ever for torrents in Reelkeeper's own
// category.
//
// Two kinds of removal (Usenet downloads have their own, simpler rules: see
// EvaluateUsenet):
//
//   - Seeded: an imported torrent has met the tracker's own minimum AND
//     the user's seeding goal. Never before the tracker's minimum: that's
//     how private trackers tell a hit-and-run.
//   - Stuck: a download that isn't getting anywhere (no progress, too
//     slow, stuck fetching details, deleted by the tracker, files lost, or
//     a failed import left alone). It's removed, its release blocklisted,
//     and another searched for.
//
// Stuck rules never act on a single look: they need the problem to last
// (a time window) or to be seen several checks in a row (strikes).
//
// Everything here is a pure function of its inputs.
package cleanup

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Track is what cleanup remembers about a download between checks.
type Track struct {
	// Left is the bytes still to download at the last check, and
	// ProgressAt when that last went down.
	Left       int64
	ProgressAt *time.Time
	// SlowSince is when the download dropped below the slow threshold.
	SlowSince *time.Time
	// MetaSince is when it started waiting for the torrent's details.
	MetaSince *time.Time
	// Problem and Strikes count a problem seen check after check.
	Problem string
	Strikes int
}

// Download is Reelkeeper's record of a download, as cleanup needs it.
type Download struct {
	Imported    bool
	ImportFail  bool
	CompletedAt *time.Time
	// The tracker's own minimums, recorded when it was grabbed. Nil means
	// the tracker sets none.
	SeedRatio       *float64
	SeedTimeMinutes *int
}

// Torrent is what the download client says now.
type Torrent struct {
	State torrent.State
	// ClientName is the download client's name, for the reasons given to
	// the user ("" reads as "The download client").
	ClientName  string
	Done        bool
	Left        int64
	Speed       int64 // bytes per second
	Ratio       float64
	SeedingTime time.Duration
	// Unregistered is set when a tracker says the torrent no longer
	// exists (only asked about when it looks stuck).
	Unregistered bool
	// Message is what the client says is wrong (a failed Usenet job's
	// reason).
	Message string
}

// Action is what to do.
type Action string

// Actions.
const (
	Keep         Action = ""
	RemoveSeeded Action = "seeded" // done seeding: remove, nothing else
	RemoveStuck  Action = "stuck"  // remove, blocklist, search again
)

// Verdict is a decision with its reason in plain words.
type Verdict struct {
	Action Action
	// Rule names the rule ("seeded", "stalled", "slow", "metadata",
	// "unregistered", "missing", "import-failed", and for Usenet
	// "usenet-imported" and "usenet-failed").
	Rule   string
	Reason string
}

// strikesNeeded is how many checks in a row a problem must be seen.
const strikesNeeded = 3

// Evaluate decides about one download, and returns what to remember for
// next time.
func Evaluate(d Download, t Torrent, tr Track, cfg settings.Cleanup, now time.Time) (Verdict, Track) {
	switch {
	case d.Imported:
		return seeded(d, t, cfg, now), tr
	case d.ImportFail:
		if cfg.ImportFailedDays > 0 && d.CompletedAt != nil && now.Sub(*d.CompletedAt) >= days(cfg.ImportFailedDays) {
			return Verdict{RemoveStuck, "import-failed", fmt.Sprintf("Its import failed %s ago and hasn't been retried.", ago(now.Sub(*d.CompletedAt)))}, tr
		}
		return Verdict{}, tr
	case t.Done:
		return Verdict{}, tr // finished, waiting to import
	}
	return stuck(t, tr, cfg, now)
}

// EvaluateUsenet decides about one Usenet download. There is no seeding, so
// an imported one's downloaded copy goes straight away; a job the Usenet
// client gave up on (missing articles, a failed repair or unpack) is stuck
// for good, so it is removed, blocklisted and searched again at once, with
// the client's reason. A failed import waits as long as a torrent's does.
// A job refused as a duplicate isn't the release's fault: it stays (the
// Queue says to turn duplicate detection off), never blocklisted.
func EvaluateUsenet(d Download, t Torrent, cfg settings.Cleanup, now time.Time) Verdict {
	switch {
	case d.Imported:
		return Verdict{RemoveSeeded, "usenet-imported", "Imported, and a Usenet download has nothing to seed."}
	case d.ImportFail:
		v, _ := Evaluate(d, t, Track{}, cfg, now)
		return v
	case t.State == torrent.Failed && strings.Contains(strings.ToLower(t.Message), "duplicate"):
		return Verdict{}
	case t.State == torrent.Failed:
		return Verdict{RemoveStuck, "usenet-failed", fmt.Sprintf("%s couldn't download it: %s.", t.clientName(), strings.TrimSuffix(t.Message, "."))}
	}
	return Verdict{}
}

// seeded checks the tracker's minimum (real seeding time: that's what a
// private tracker counts) and then the user's goal. The goal's time counts
// from when the download finished, not from qBittorrent's seeding time:
// qBittorrent's queue can keep a finished torrent waiting to seed, its
// clock stopped, so a goal measured in seeding time might never be met
// while the disk fills up (2026-10-11).
func seeded(d Download, t Torrent, cfg settings.Cleanup, now time.Time) Verdict {
	if t.Unregistered && cfg.Unregistered {
		return Verdict{RemoveSeeded, "unregistered", "The tracker no longer has this torrent, so there's nothing left to seed."}
	}
	trackerMet, trackerSays := trackerMinimum(d, t)
	if !trackerMet {
		return Verdict{}
	}
	since := finishedFor(d, t, now)
	goalMet := (cfg.SeedRatio == 0 && cfg.SeedHours == 0) ||
		(cfg.SeedRatio > 0 && t.Ratio >= cfg.SeedRatio) ||
		(cfg.SeedHours > 0 && since >= time.Duration(cfg.SeedHours)*time.Hour)
	if !goalMet {
		return Verdict{}
	}
	return Verdict{RemoveSeeded, "seeded", fmt.Sprintf("Seeded to ratio %.2f; finished %s ago (%s of it seeding). %s Your goal: %s.",
		t.Ratio, ago(since), ago(t.SeedingTime), trackerSays, goal(cfg))}
}

// finishedFor is how long ago the download finished (at least its seeding
// time, for downloads from before completion times were recorded).
func finishedFor(d Download, t Torrent, now time.Time) time.Duration {
	since := t.SeedingTime
	if d.CompletedAt != nil && now.Sub(*d.CompletedAt) > since {
		since = now.Sub(*d.CompletedAt)
	}
	return since
}

// Seeding is an imported torrent that MakeRoom may remove.
type Seeding struct {
	D    Download
	T    Torrent
	Size int64
}

// MakeRoom picks which imported torrents to remove when the download disk
// has less than reserve bytes free: those whose tracker minimum is met,
// longest finished first, until enough would be freed. It returns their
// indexes in items, each with its reason. Nothing is picked when there is
// room, and a torrent still owed to its tracker is never picked.
func MakeRoom(items []Seeding, free, reserve int64, now time.Time) (picked []int, reasons []Verdict) {
	if reserve <= 0 || free >= reserve {
		return nil, nil
	}
	var order []int
	for i, it := range items {
		if met, _ := trackerMinimum(it.D, it.T); met && it.T.Done {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return finishedFor(items[order[a]].D, items[order[a]].T, now) > finishedFor(items[order[b]].D, items[order[b]].T, now)
	})
	for _, i := range order {
		if free >= reserve {
			break
		}
		it := items[i]
		_, says := trackerMinimum(it.D, it.T)
		picked = append(picked, i)
		reasons = append(reasons, Verdict{RemoveSeeded, "space", fmt.Sprintf(
			"Removed to free space: the download disk had %s free, below the %s you keep free. Finished %s ago, ratio %.2f. %s",
			Size(free), Size(reserve), ago(finishedFor(it.D, it.T, now)), it.T.Ratio, says)})
		free += it.Size
	}
	return picked, reasons
}

// Size writes a byte count in GB (or MB when small).
func Size(n int64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// trackerMinimum reports whether the tracker's rule is met (ratio or seed
// time, whichever it sets; either is enough), and describes the rule.
func trackerMinimum(d Download, t Torrent) (bool, string) {
	var parts []string
	met := d.SeedRatio == nil && d.SeedTimeMinutes == nil
	if d.SeedRatio != nil {
		parts = append(parts, fmt.Sprintf("ratio %.2g", *d.SeedRatio))
		met = met || t.Ratio >= *d.SeedRatio
	}
	if d.SeedTimeMinutes != nil {
		need := time.Duration(*d.SeedTimeMinutes) * time.Minute
		parts = append(parts, ago(need))
		met = met || t.SeedingTime >= need
	}
	if len(parts) == 0 {
		return met, "The tracker sets no minimum."
	}
	return met, "The tracker asks for " + strings.Join(parts, " or ") + "."
}

func goal(cfg settings.Cleanup) string {
	var parts []string
	if cfg.SeedRatio > 0 {
		parts = append(parts, fmt.Sprintf("ratio %.2g", cfg.SeedRatio))
	}
	if cfg.SeedHours > 0 {
		parts = append(parts, ago(time.Duration(cfg.SeedHours)*time.Hour))
	}
	if len(parts) == 0 {
		return "nothing beyond the tracker's minimum"
	}
	return strings.Join(parts, " or ")
}

// waiting are states where a torrent isn't trying to download: queued
// behind others or paused by someone. Not being stuck, they reset the
// clocks.
var waiting = map[torrent.State]bool{
	torrent.Queued: true, torrent.Paused: true, torrent.Checking: true, torrent.Moving: true,
}

func stuck(t Torrent, tr Track, cfg settings.Cleanup, now time.Time) (Verdict, Track) {
	at := now
	if waiting[t.State] {
		return Verdict{}, Track{Left: t.Left, ProgressAt: &at}
	}

	// Problems that need several checks in a row.
	problem := ""
	switch {
	case t.Unregistered && cfg.Unregistered:
		problem = "unregistered"
	case (t.State == torrent.MissingFiles || t.State == torrent.Error) && cfg.MissingFiles:
		problem = "missing"
	}
	if problem != tr.Problem {
		tr.Problem, tr.Strikes = problem, 0
	}
	if problem != "" {
		tr.Strikes++
		if tr.Strikes >= strikesNeeded {
			reason := "The tracker says this torrent no longer exists (deleted or replaced)."
			if problem == "missing" {
				reason = t.clientName() + " reports an error or missing files for it."
			}
			return Verdict{RemoveStuck, problem, fmt.Sprintf("%s Seen %d checks in a row.", reason, tr.Strikes)}, tr
		}
	}

	// Waiting for the torrent's details (a magnet nobody shares).
	if t.State == torrent.Metadata {
		if tr.MetaSince == nil {
			tr.MetaSince = &at
		}
		if cfg.MetadataHours > 0 && now.Sub(*tr.MetaSince) >= time.Duration(cfg.MetadataHours)*time.Hour {
			return Verdict{RemoveStuck, "metadata", fmt.Sprintf("It has been trying to get the torrent's details for %s; nobody seems to be sharing it.", ago(now.Sub(*tr.MetaSince)))}, tr
		}
		return Verdict{}, tr
	}
	tr.MetaSince = nil

	// Progress: any drop in what's left counts.
	if tr.ProgressAt == nil || t.Left < tr.Left || tr.Left == 0 {
		tr.Left, tr.ProgressAt = t.Left, &at
	}
	if cfg.StalledHours > 0 && now.Sub(*tr.ProgressAt) >= time.Duration(cfg.StalledHours)*time.Hour {
		return Verdict{RemoveStuck, "stalled", fmt.Sprintf("No progress for %s.", ago(now.Sub(*tr.ProgressAt)))}, tr
	}

	// Speed.
	if cfg.SlowKBps > 0 && t.Speed < int64(cfg.SlowKBps)*1024 {
		if tr.SlowSince == nil {
			tr.SlowSince = &at
		}
		if now.Sub(*tr.SlowSince) >= time.Duration(cfg.SlowHours)*time.Hour {
			return Verdict{RemoveStuck, "slow", fmt.Sprintf("Slower than %d KB/s for %s (now %d KB/s).", cfg.SlowKBps, ago(now.Sub(*tr.SlowSince)), t.Speed/1024)}, tr
		}
	} else {
		tr.SlowSince = nil
	}
	return Verdict{}, tr
}

// unregisteredRe matches what trackers say about a torrent they've
// deleted (the wording Radarr's and Sonarr's cleanup tools look for). It is
// the same wording from any download client; Transmission shows a tracker's
// own words, sometimes after "Tracker gave an error:".
var unregisteredRe = regexp.MustCompile(`(?i)unregistered|not registered|torrent not found|torrent does not exist|info[ _]?hash not found|has been deleted|trumped|nuked|torrent not exist`)

// Unregistered reports whether tracker messages say the torrent is gone.
func Unregistered(messages []string) bool {
	for _, m := range messages {
		if unregisteredRe.MatchString(m) {
			return true
		}
	}
	return false
}

// LooksStuck says whether asking the trackers is worth it: only for
// downloads that aren't moving or that the download client reports a problem
// with.
func LooksStuck(t Torrent) bool {
	switch t.State {
	case torrent.Stalled, torrent.Metadata, torrent.Error, torrent.MissingFiles, torrent.SeedingStalled:
		return true
	}
	return false
}

// clientName is the download client's name for a sentence's start.
func (t Torrent) clientName() string {
	if t.ClientName == "" {
		return "The download client"
	}
	// A name that isn't a proper name ("the built-in client") needs its capital.
	if strings.HasPrefix(t.ClientName, "the ") {
		return "T" + t.ClientName[1:]
	}
	return t.ClientName
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// ago words a duration roughly: "40 minutes", "6 hours", "3 days".
func ago(d time.Duration) string {
	switch {
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// Waiting says what an imported torrent is still seeding for, in plain
// words, or "" when cleanup would remove it now (or is off).
func Waiting(d Download, t Torrent, cfg settings.Cleanup, now time.Time) string {
	if cfg.Mode == settings.CleanupOff {
		return ""
	}
	if met, says := trackerMinimum(d, t); !met {
		return "The tracker's minimum: " + strings.TrimSuffix(strings.TrimPrefix(says, "The tracker asks for "), ".") + "."
	}
	if v := seeded(d, t, cfg, now); v.Action == Keep {
		return "Your seeding goal: " + goal(cfg) + "."
	}
	return ""
}
