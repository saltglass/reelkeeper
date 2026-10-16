package grab

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// A queued search records who started it, so System → Tasks can say so, and
// carries the search's trigger along to the run.
func TestQueueSearchTrigger(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		m := jobs.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
		f.svc.RegisterTasks(m)
		// One version per case: a second search for the same one would be
		// folded into the first.
		for i, tc := range []struct {
			trigger string
			want    jobs.Trigger
		}{
			{TriggerManual, jobs.Manual},
			{TriggerAdded, jobs.System},
			{TriggerRequest, jobs.API},
		} {
			versionID := int64(100 + i)
			job, created, err := QueueSearch(ctx, m, versionID, tc.trigger)
			if err != nil || !created {
				t.Fatalf("%s: job %+v, created %v, err %v", tc.trigger, job, created, err)
			}
			if job.Trigger != tc.want {
				t.Errorf("%s: job trigger = %s, want %s", tc.trigger, job.Trigger, tc.want)
			}
		}
	})
}
