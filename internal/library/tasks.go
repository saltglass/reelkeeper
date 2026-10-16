package library

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
)

// Background task kinds (stored in the jobs table; never rename).
const (
	ScanKind    = "library.scan"
	RefreshKind = "library.refresh-metadata"
)

// ScanArgs are the scan task's arguments; RootID 0 scans every folder.
type ScanArgs struct {
	RootID int64 `json:"rootId,omitempty"`
}

// QueueScan queues a scan of one root folder (0 for all), or returns the
// one already queued or running.
func QueueScan(ctx context.Context, m *jobs.Manager, rootID int64, trigger jobs.Trigger) (*jobs.Job, bool, error) {
	return m.Enqueue(ctx, ScanKind, ScanArgs{RootID: rootID}, trigger, fmt.Sprintf("%s:%d", ScanKind, rootID))
}

// RegisterTasks adds the library's background tasks.
func (s *Service) RegisterTasks(m *jobs.Manager) {
	m.Register(jobs.Task{
		Kind:        ScanKind,
		Name:        "Scan media folders",
		Description: "Looks through your media folders for new, changed and missing files, and identifies new movies and series. Only reads; never changes files.",
		Every:       6 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			var args ScanArgs
			_ = r.Args(&args)
			roots, err := s.RootFolders(ctx)
			if err != nil {
				return err
			}
			var todo []RootFolder
			for _, root := range roots {
				if args.RootID == 0 || root.ID == args.RootID {
					todo = append(todo, root)
				}
			}
			if len(todo) == 0 {
				r.Progress(100, "No media folders to scan. Add one in Settings → Media Folders.")
				return nil
			}
			var summaries []string
			for i, root := range todo {
				res, err := s.Scan(ctx, root.ID, func(pct int, msg string) {
					r.Progress((i*100+pct)/len(todo), msg)
				})
				if err != nil {
					return fmt.Errorf("%s: %w", root.Path, err)
				}
				summaries = append(summaries, root.Path+": "+res.Summary())
			}
			r.Progress(100, strings.Join(summaries, "; "))
			return nil
		},
	})
	m.Register(jobs.Task{
		Kind:        RefreshKind,
		Name:        "Refresh movie and series details",
		Description: "Fetches the latest details from TMDB (release dates, artwork, new episodes) for movies and series not refreshed in two weeks, up to 200 at a time.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			n, err := s.RefreshStale(ctx, 14*24*time.Hour, 200, r.Progress)
			if err != nil {
				return err
			}
			r.Progress(100, fmt.Sprintf("Refreshed %d movies and series", n))
			return nil
		},
	})
}
