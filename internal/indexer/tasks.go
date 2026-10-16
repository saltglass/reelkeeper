package indexer

import (
	"context"
	"fmt"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
)

// UpdateDefinitionsKind is the task that refreshes tracker definitions
// (stored in the jobs table; never rename).
const UpdateDefinitionsKind = "indexer.update-definitions"

// RegisterTasks adds the indexers' background tasks.
func (s *Service) RegisterTasks(m *jobs.Manager) {
	m.Register(jobs.Task{
		Kind:        UpdateDefinitionsKind,
		Name:        "Update tracker definitions",
		Description: "Downloads the latest community tracker definitions (the ones Prowlarr uses), so trackers keep working when their sites change. If the download fails, the current ones stay.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			r.Progress(0, "Downloading tracker definitions")
			n, err := s.catalog.Update(ctx)
			if err != nil {
				return err
			}
			// Indexers pick up their new definitions on their next use.
			s.mu.Lock()
			s.clients = map[int64]*cached{}
			s.mu.Unlock()
			r.Progress(100, fmt.Sprintf("%d tracker definitions are available", n))
			return nil
		},
	})
}
