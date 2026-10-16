package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// started runs a Manager's workers until the test ends.
func started(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = m.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func status(t *testing.T, m *Manager, id int64) Status {
	t.Helper()
	j, err := m.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status
}

func newManager(db *store.Store) *Manager {
	return New(db, slog.New(slog.DiscardHandler))
}

func TestRunToSuccess(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		var got struct{ Folder string }
		m.Register(Task{Kind: "test.ok", Name: "OK task", Run: func(ctx context.Context, r *Run) error {
			if err := r.Args(&got); err != nil {
				return err
			}
			r.Progress(50, "Halfway")
			r.Progress(90, "Nearly done")
			return nil
		}})
		var mu sync.Mutex
		var seen []Status
		m.OnChange(func(j Job) {
			if j.Kind != "test.ok" { // the built-in history task also runs at start
				return
			}
			mu.Lock()
			seen = append(seen, j.Status)
			mu.Unlock()
		})
		started(t, m)

		job, created, err := m.Enqueue(ctx, "test.ok", map[string]string{"Folder": "/media/movies"}, Manual, "")
		if err != nil || !created {
			t.Fatal(err)
		}
		waitFor(t, "success", func() bool { return status(t, m, job.ID) == Succeeded })

		j, _ := m.Get(ctx, job.ID)
		if j.Progress != 100 || j.Message != "Nearly done" || j.Attempts != 1 || j.StartedAt == nil || j.FinishedAt == nil {
			t.Errorf("finished job = %+v", j)
		}
		if got.Folder != "/media/movies" {
			t.Errorf("args = %+v", got)
		}
		// The listener hears about the finish just after it's stored, so
		// wait for that too (checking at once was flaky on CI).
		waitFor(t, "listener sees success", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(seen) > 0 && seen[len(seen)-1] == Succeeded
		})
		mu.Lock()
		defer mu.Unlock()
		if len(seen) < 3 || seen[0] != Queued {
			t.Errorf("listener saw %v", seen)
		}
	})
}

func TestDedupe(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		release := make(chan struct{})
		m.Register(Task{Kind: "test.block", Name: "Blocks", Run: func(ctx context.Context, r *Run) error {
			<-release
			return nil
		}})
		started(t, m)
		defer close(release)

		first, created, err := m.RunNow(ctx, "test.block")
		if err != nil || !created {
			t.Fatal(err)
		}
		second, created, err := m.RunNow(ctx, "test.block")
		if err != nil || created || second.ID != first.ID {
			t.Errorf("second RunNow should return the first job: %+v created=%v err=%v", second, created, err)
		}
		// A run another app asks for shares the same queue slot, and says
		// who asked when it's the one that starts.
		third, created, err := m.RunForAPI(ctx, "test.block")
		if err != nil || created || third.ID != first.ID {
			t.Errorf("RunForAPI should return the queued job: %+v created=%v err=%v", third, created, err)
		}
		if first.Trigger != Manual {
			t.Errorf("trigger = %q", first.Trigger)
		}
	})
}

func TestRunForAPITrigger(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		release := make(chan struct{})
		m.Register(Task{Kind: "test.block", Name: "Blocks", Run: func(ctx context.Context, r *Run) error {
			<-release
			return nil
		}})
		started(t, m)
		defer close(release)
		job, created, err := m.RunForAPI(ctx, "test.block")
		if err != nil || !created || job.Trigger != API {
			t.Fatalf("job = %+v created=%v err=%v", job, created, err)
		}
	})
}

func TestFailureAndPanic(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		m.Register(Task{Kind: "test.fail", Name: "Fails", Run: func(context.Context, *Run) error {
			return errors.New("the folder /media/movies isn't reachable")
		}})
		m.Register(Task{Kind: "test.panic", Name: "Panics", Run: func(context.Context, *Run) error {
			panic("simulated bug in a task")
		}})
		started(t, m)

		fail, _, _ := m.Enqueue(ctx, "test.fail", nil, Manual, "")
		boom, _, _ := m.Enqueue(ctx, "test.panic", nil, Manual, "")
		waitFor(t, "both finished", func() bool {
			return status(t, m, fail.ID) == Failed && status(t, m, boom.ID) == Failed
		})
		j, _ := m.Get(ctx, fail.ID)
		if !strings.Contains(j.Error, "isn't reachable") {
			t.Errorf("error = %q", j.Error)
		}
		j, _ = m.Get(ctx, boom.ID)
		if !strings.Contains(j.Error, "crashed") {
			t.Errorf("panic should be recorded as a crash, got %q", j.Error)
		}

		// A crash didn't stop the workers.
		m.Register(Task{Kind: "test.after", Name: "After", Run: func(context.Context, *Run) error { return nil }})
		after, _, _ := m.Enqueue(ctx, "test.after", nil, Manual, "")
		waitFor(t, "a later task to run", func() bool { return status(t, m, after.ID) == Succeeded })
	})
}

func TestCancel(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		running := make(chan struct{})
		m.Register(Task{Kind: "test.long", Name: "Long", Run: func(ctx context.Context, r *Run) error {
			close(running)
			<-ctx.Done()
			return ctx.Err()
		}})
		m.Register(Task{Kind: "test.never", Name: "Never", Run: func(context.Context, *Run) error { return nil }})

		// Cancel before the workers start: it never runs.
		queued, _, _ := m.Enqueue(ctx, "test.never", nil, Manual, "")
		if err := m.Cancel(ctx, queued.ID); err != nil {
			t.Fatal(err)
		}
		if status(t, m, queued.ID) != Cancelled {
			t.Error("queued job should be cancelled")
		}

		started(t, m)
		long, _, _ := m.Enqueue(ctx, "test.long", nil, Manual, "")
		<-running
		if err := m.Cancel(ctx, long.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "cancelled", func() bool { return status(t, m, long.ID) == Cancelled })
		if err := m.Cancel(ctx, long.ID); !errors.Is(err, ErrNotCancellable) {
			t.Errorf("cancelling a finished job: %v", err)
		}
	})
}

func TestRestartRecovery(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		// A run that was in progress when Reelkeeper stopped.
		now := time.Now().UTC()
		interrupted := &Job{Kind: "test.ok", Args: "{}", Status: Running, Trigger: Schedule, Attempts: 1, CreatedAt: now, StartedAt: &now}
		tooMany := &Job{Kind: "test.ok", Args: "{}", Status: Running, Trigger: Schedule, Attempts: maxAttempts, CreatedAt: now, StartedAt: &now}
		for _, j := range []*Job{interrupted, tooMany} {
			if _, err := db.DB.NewInsert().Model(j).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}

		m := newManager(db)
		ran := make(chan struct{}, 2)
		m.Register(Task{Kind: "test.ok", Name: "OK", Run: func(context.Context, *Run) error { ran <- struct{}{}; return nil }})
		started(t, m)

		waitFor(t, "the interrupted job to run again", func() bool { return status(t, m, interrupted.ID) == Succeeded })
		j, _ := m.Get(ctx, tooMany.ID)
		if j.Status != Failed || !strings.Contains(j.Error, "too many") {
			t.Errorf("job interrupted %d times: %+v", maxAttempts, j)
		}
		if len(ran) != 1 {
			t.Errorf("task ran %d times, want 1", len(ran))
		}
	})
}

func TestShutdownLeavesJobForNextStart(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	ctx := context.Background()
	m := newManager(db)
	running := make(chan struct{})
	m.Register(Task{Kind: "test.long", Name: "Long", Run: func(ctx context.Context, r *Run) error {
		close(running)
		<-ctx.Done()
		return ctx.Err()
	}})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = m.Start(runCtx); close(done) }()
	job, _, _ := m.Enqueue(ctx, "test.long", nil, Manual, "")
	<-running
	stop()
	<-done
	if s := status(t, m, job.ID); s != Running {
		t.Errorf("after shutdown the job should still be marked running (to be re-queued), got %s", s)
	}
}

func TestScheduling(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		var runs sync.WaitGroup
		runs.Add(1)
		m.Register(Task{Kind: "test.daily", Name: "Daily", Every: 24 * time.Hour, Run: func(context.Context, *Run) error {
			runs.Done()
			return nil
		}})
		m.Register(Task{Kind: "test.manual", Name: "Manual only", Run: func(context.Context, *Run) error { return nil }})

		byKind := func() map[string]TaskInfo {
			infos, err := m.Tasks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]TaskInfo{}
			for _, i := range infos {
				out[i.Kind] = i
			}
			return out
		}
		infos := byKind()
		if infos["test.daily"].NextRun.IsZero() || !infos["test.manual"].NextRun.IsZero() {
			t.Errorf("tasks = %+v", infos)
		}
		if _, ok := infos[PruneKind]; !ok {
			t.Error("the built-in history task should be registered")
		}

		started(t, m) // the scheduler checks straight away
		runs.Wait()
		waitFor(t, "the daily run to be recorded", func() bool {
			d := byKind()["test.daily"]
			return d.Last != nil && d.Last.Status == Succeeded
		})
		daily := byKind()["test.daily"]
		next := daily.NextRun
		if d := time.Until(next); d < 23*time.Hour || d > 25*time.Hour {
			t.Errorf("next daily run in %s, want about 24h", d)
		}
		if daily.Last.Trigger != Schedule {
			t.Errorf("trigger = %s", daily.Last.Trigger)
		}

		// Due again only once its interval has passed.
		m.queueDue(ctx)
		if byKind()["test.daily"].Active != nil {
			t.Error("a task that just ran must not be queued again")
		}
	})
}

func TestPruneKeepsRecentHistory(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		old := time.Now().UTC().Add(-60 * 24 * time.Hour)
		insert := func(kind string, n int, at time.Time) {
			for range n {
				j := &Job{Kind: kind, Args: "{}", Status: Succeeded, Trigger: Schedule, CreatedAt: at, StartedAt: &at, FinishedAt: &at}
				if _, err := db.DB.NewInsert().Model(j).Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		insert("test.frequent", 25, old) // old: only the newest keepPerKind survive
		insert("test.frequent", 3, time.Now().UTC())
		insert("test.rare", 2, old) // old, but the last few of a kind are kept

		if n, err := m.prune(ctx); err != nil || n != 18 {
			t.Fatalf("pruned %d, %v; want 18", n, err)
		}
		count := func(kind string) int {
			n, _ := db.DB.NewSelect().Model((*Job)(nil)).Where("kind = ?", kind).Count(ctx)
			return int(n)
		}
		if n := count("test.frequent"); n != keepPerKind {
			t.Errorf("frequent: %d kept, want %d", n, keepPerKind)
		}
		if n := count("test.rare"); n != 2 {
			t.Errorf("rare: %d kept, want 2", n)
		}
	})
}

func TestUnknownTask(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	m := newManager(db)
	if _, _, err := m.Enqueue(context.Background(), "nope", nil, Manual, ""); !errors.Is(err, ErrUnknownTask) {
		t.Errorf("err = %v", err)
	}
}

// A routine run is marked quiet and left out of Recent unless asked for.
// A run started by hand is never quiet, and a failed run isn't either.
func TestQuietRuns(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newManager(db)
		m.Register(Task{Kind: "test.routine", Name: "Routine", Run: func(ctx context.Context, r *Run) error {
			r.Quiet()
			r.Progress(100, "Nothing to do")
			return nil
		}})
		m.Register(Task{Kind: "test.quietfail", Name: "Quiet then fails", Run: func(ctx context.Context, r *Run) error {
			r.Quiet()
			return errors.New("boom")
		}})
		started(t, m)
		// Let the built-in prune task, queued at start, finish first so it
		// can't land in the middle of the runs below.
		waitFor(t, "the prune task", func() bool {
			js, _, _ := m.Recent(ctx, 5, true)
			return len(js) == 1 && js[0].Status.Finished()
		})

		finishedJob := func(j *Job) Job {
			waitFor(t, "job to finish", func() bool { return status(t, m, j.ID).Finished() })
			got, err := m.Get(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			return *got
		}

		byHand, _, _ := m.RunNow(ctx, "test.routine")
		if j := finishedJob(byHand); j.Quiet {
			t.Error("a run started by hand must never be quiet")
		}
		failed, _, _ := m.Enqueue(ctx, "test.quietfail", nil, Schedule, "")
		if j := finishedJob(failed); j.Quiet || j.Status != Failed {
			t.Errorf("a failed run must not be quiet: %+v", j)
		}
		scheduled, _, _ := m.Enqueue(ctx, "test.routine", nil, Schedule, "")
		if j := finishedJob(scheduled); !j.Quiet {
			t.Error("a scheduled run that called Quiet should be quiet")
		}
		viaAPI, _, _ := m.RunForAPI(ctx, "test.routine")
		if j := finishedJob(viaAPI); !j.Quiet {
			t.Error("a run for another app that called Quiet should be quiet")
		}

		// Default: quiet runs are left out and counted.
		// (The built-in prune task's run is in the list too; ignore it.)
		ours := func(js []Job) (out []Job) {
			for _, j := range js {
				if strings.HasPrefix(j.Kind, "test.") {
					out = append(out, j)
				}
			}
			return out
		}
		jobs, hidden, err := m.Recent(ctx, 50, false)
		if jobs = ours(jobs); err != nil || len(jobs) != 2 || hidden != 2 {
			t.Fatalf("hidden view: %d jobs, %d hidden, %v", len(jobs), hidden, err)
		}
		for _, j := range jobs {
			if j.Quiet {
				t.Errorf("quiet run listed: %+v", j)
			}
		}
		// Asked for: everything, nothing counted as hidden.
		jobs, hidden, err = m.Recent(ctx, 50, true)
		if jobs = ours(jobs); err != nil || len(jobs) != 4 || hidden != 0 {
			t.Fatalf("full view: %d jobs, %d hidden, %v", len(jobs), hidden, err)
		}
		// The window: only quiet runs newer than the oldest one shown count.
		jobs, hidden, err = m.Recent(ctx, 1, false)
		if err != nil || len(jobs) != 1 || jobs[0].ID != failed.ID || hidden != 2 {
			t.Fatalf("window: %+v hidden %d %v", jobs, hidden, err)
		}
	})
}
