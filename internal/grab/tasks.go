package grab

import (
	"context"
	"fmt"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
)

// Task kinds (stored in the jobs table; never rename).
const (
	SearchKind = "grab.search"
	RSSKind    = "grab.rss"
	PruneKind  = "grab.prune-history"
	// BackfillKind is the backfill queue's task.
	BackfillKind = "grab.backfill"
)

// SearchArgs are a search job's arguments: one version, or 0 for the
// scheduled pass.
type SearchArgs struct {
	VersionID int64  `json:"versionId,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
}

// The scheduled pass searches perPass versions an hour, each at most once
// every searchEvery. With 3 an hour, 72 searches a day reach a few hundred
// waiting movies within a week, while staying far inside trackers' limits.
const (
	perPass     = 3
	searchEvery = 3 * 24 * time.Hour
)

// QueueSearch queues a search for one version (or returns the one already
// queued). trigger is TriggerManual, TriggerAdded or TriggerRequest; the
// job is recorded as started by the system (added), the API (a request from
// Seerr) or the user (anything else).
func QueueSearch(ctx context.Context, m *jobs.Manager, versionID int64, trigger string) (*jobs.Job, bool, error) {
	jt := jobs.Manual
	switch trigger {
	case TriggerAdded:
		jt = jobs.System
	case TriggerRequest:
		jt = jobs.API
	}
	return m.Enqueue(ctx, SearchKind, SearchArgs{VersionID: versionID, Trigger: trigger}, jt,
		fmt.Sprintf("%s:%d", SearchKind, versionID))
}

// RegisterTasks adds the search tasks.
func (s *Service) RegisterTasks(m *jobs.Manager) {
	m.Register(jobs.Task{
		Kind: SearchKind,
		Name: "Search for wanted movies and episodes",
		Description: fmt.Sprintf("Searches your indexers for %d movies that are missing or could be upgraded, "+
			"and %d missing episodes or season packs, starting with those waiting longest; each is searched "+
			"at most every %s. Runs only while Grabbing (for movies or TV) is Dry run or On.", perPass, perPass, days(searchEvery)),
		Every: time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			var args SearchArgs
			_ = r.Args(&args)
			var msg string
			var err error
			if args.VersionID != 0 {
				r.Progress(0, "Searching")
				msg, err = s.SearchVersion(ctx, args.VersionID, args.Trigger)
			} else {
				r.Progress(0, "Looking for movies and episodes to search")
				msg, err = s.ScheduledPass(ctx, perPass, searchEvery)
			}
			if err != nil {
				return err
			}
			r.Progress(100, msg)
			return nil
		},
	})
	m.Register(jobs.Task{
		Kind:        RSSKind,
		Name:        "RSS sync",
		Description: "Reads each indexer's newest releases and grabs any that a waiting movie or episode wants. Finds new releases without searching for each one. Runs only while Grabbing (for movies or TV) is Dry run or On.",
		Every:       15 * time.Minute,
		Run: func(ctx context.Context, r *jobs.Run) error {
			r.Progress(0, "Reading new releases")
			msg, quiet, err := s.RSSSync(ctx)
			if err != nil {
				return err
			}
			if quiet {
				r.Quiet()
			}
			r.Progress(100, msg)
			return nil
		},
	})
	m.Register(jobs.Task{
		Kind: BackfillKind,
		Name: "Backfill",
		Description: fmt.Sprintf("Works through the backfill queue one show at a time: turns on a show's older missing episodes, "+
			"searches for them, waits while they download, and moves to the next show when nothing more can be found. "+
			"At most %d grabs a day go to it. Runs only while TV grabbing is On.", BackfillGrabsPerDay),
		Every: 15 * time.Minute,
		Run: func(ctx context.Context, r *jobs.Run) error {
			msg, quiet, err := s.BackfillRun(ctx)
			if err != nil {
				return err
			}
			if quiet {
				r.Quiet()
			}
			r.Progress(100, msg)
			return nil
		},
	})
	m.Register(jobs.Task{
		Kind:        PruneKind,
		Name:        "Tidy up History",
		Description: "Removes search records older than 90 days. Grabs and imports are kept.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			n, err := s.history.Prune(ctx)
			if err != nil {
				return err
			}
			r.Progress(100, fmt.Sprintf("Removed %d old search records", n))
			return nil
		},
	})
}
