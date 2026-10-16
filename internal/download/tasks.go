package download

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
)

// Task kinds (stored; never rename).
const (
	CheckKind   = "download.check"
	CleanupKind = "download.cleanup"
	EmptyKind   = "download.empty-replaced"
	ExpireKind  = "download.expire-blocklist"
)

// RegisterTasks adds the downloads task.
func (s *Service) RegisterTasks(m *jobs.Manager) {
	m.Register(jobs.Task{
		Kind:        CheckKind,
		Name:        "Check downloads",
		Description: "Asks the download client how Reelkeeper's downloads are doing, and imports finished ones into your library. Torrents keep seeding after import.",
		Every:       time.Minute,
		Run: func(ctx context.Context, r *jobs.Run) error {
			msg, quiet, err := s.Check(ctx)
			if errors.Is(err, ErrNoClient) {
				r.Progress(100, "No download client is set up.")
				r.Quiet()
				return nil
			}
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
		Kind: CleanupKind,
		Name: "Clean up downloads",
		Description: "Removes Reelkeeper's torrents once they've seeded enough (never before the tracker's minimum), " +
			"and replaces downloads that are stuck. Only touches Reelkeeper's own category. In dry run it only records what it would do.",
		Every: 5 * time.Minute,
		Run: func(ctx context.Context, r *jobs.Run) error {
			msg, quiet, err := s.Cleanup(ctx)
			if errors.Is(err, ErrNoClient) {
				r.Progress(100, "No download client is set up.")
				r.Quiet()
				return nil
			}
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
		Kind:        EmptyKind,
		Name:        "Empty replaced files",
		Description: "Deletes files that upgrades replaced (kept in .reelkeeper-replaced in each media folder) once they're older than the cleanup setting.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			msg, err := s.EmptyReplacedFiles(ctx)
			if err != nil {
				return err
			}
			r.Progress(100, msg)
			return nil
		},
	})
	m.Register(s.expireTask())
	s.jobs = m
	m.Register(s.moveTask())
}

// expireTask is the daily task that deletes expired blocklist entries.
func (s *Service) expireTask() jobs.Task {
	return jobs.Task{
		Kind: ExpireKind,
		Name: "Expire blocklist entries",
		Description: "Deletes temporary blocklist entries that have run out (releases blocked because a download got stuck are tried again after 30 days). " +
			"They already stopped blocking; this keeps the list small. Entries you made yourself never expire.",
		Every: 24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			n, err := s.ExpireBlocklist(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
				r.Quiet()
				r.Progress(100, "No blocklist entries had expired.")
				return nil
			}
			if n == 1 {
				r.Progress(100, "Removed 1 expired blocklist entry.")
			} else {
				r.Progress(100, fmt.Sprintf("Removed %d expired blocklist entries.", n))
			}
			return nil
		},
	}
}
