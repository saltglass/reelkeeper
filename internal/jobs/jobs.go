// Package jobs runs Reelkeeper's background tasks: library scans, metadata
// refreshes and so on. They appear under System → Tasks.
//
// A Task is registered once, in code, with a kind (a stable identifier such
// as "library.scan"), a plain-English name, and optionally a schedule. Each
// time it runs, a Job row records the run: queued, running, then succeeded,
// failed or cancelled, with progress and a message along the way.
//
// Jobs live in the database, so they survive restarts. A run cut short by a
// restart is queued again, up to maxAttempts times.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/store"
)

// Status is where a job is in its life.
type Status string

// Job statuses.
const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

// Finished reports whether the job has stopped for good.
func (s Status) Finished() bool { return s == Succeeded || s == Failed || s == Cancelled }

// Trigger says who started a job.
type Trigger string

// Job triggers.
const (
	Manual   Trigger = "manual"   // someone pressed "Run now"
	Schedule Trigger = "schedule" // its schedule came round
	System   Trigger = "system"   // Reelkeeper itself, e.g. after a setting changed
	// API: another app asked through the Radarr/Sonarr-compatible API,
	// such as Seerr's download check every minute.
	API Trigger = "api"
)

const (
	// maxAttempts bounds how often a job interrupted by restarts is retried.
	maxAttempts = 3
	// workers is how many jobs run at once. Small on purpose: the host is
	// short of RAM and the library is on slow network storage.
	workers = 2
	// keepFinished is how long finished jobs stay in the history.
	keepFinished = 30 * 24 * time.Hour
	// keepPerKind finished jobs of each kind are kept regardless of age, so
	// a weekly task still shows its last runs.
	keepPerKind = 10
	// progressEvery limits how often progress is written to the database.
	progressEvery = time.Second
)

// ErrUnknownTask means no task is registered under that kind.
var ErrUnknownTask = errors.New("no such task")

// ErrNotCancellable means the job has already finished.
var ErrNotCancellable = errors.New("the task has already finished")

// Job is one run of a task.
type Job struct {
	bun.BaseModel `bun:"table:jobs"`

	ID        int64   `bun:"id,pk,autoincrement" json:"id"`
	Kind      string  `bun:"kind" json:"kind"`
	Args      string  `bun:"args" json:"-"`
	Status    Status  `bun:"status" json:"status"`
	Trigger   Trigger `bun:"triggered_by" json:"trigger"`
	DedupeKey *string `bun:"dedupe_key" json:"-"`
	Progress  int     `bun:"progress" json:"progress"`
	Message   string  `bun:"message" json:"message"`
	Error     string  `bun:"error" json:"error"`
	Attempts  int     `bun:"attempts" json:"attempts"`
	// Quiet marks a routine run that found nothing to do. System → Tasks
	// hides these unless asked. See Run.Quiet.
	Quiet      bool       `bun:"quiet" json:"quiet"`
	CreatedAt  time.Time  `bun:"created_at" json:"createdAt"`
	StartedAt  *time.Time `bun:"started_at" json:"startedAt,omitempty"`
	FinishedAt *time.Time `bun:"finished_at" json:"finishedAt,omitempty"`
}

// Task describes a kind of background work.
type Task struct {
	// Kind is a stable identifier, such as "library.scan". It is stored in
	// the database, so never rename one that has shipped.
	Kind string
	// Name and Description are shown under System → Tasks.
	Name        string
	Description string
	// Every is how often it runs on its own; zero means only on demand.
	Every time.Duration
	// Run does the work. It should return promptly once ctx is cancelled
	// (the user pressed Cancel, or Reelkeeper is shutting down).
	Run func(ctx context.Context, r *Run) error
}

// Run is handed to a task while it runs.
type Run struct {
	job *Job
	m   *Manager

	mu       sync.Mutex
	lastSave time.Time
}

// Job returns a copy of the job being run.
func (r *Run) Job() Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.job
}

// Quiet marks this run as routine: it looked, found nothing to do, and
// isn't worth showing in System → Tasks' recent runs. Call it only when the
// run really did nothing. A run someone started by hand (Run now) is never
// quiet, because they want to see what happened. The mark is saved when the
// run finishes successfully.
func (r *Run) Quiet() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job.Trigger != Manual {
		r.job.Quiet = true
	}
}

// Args decodes the job's arguments into v.
func (r *Run) Args(v any) error {
	return json.Unmarshal([]byte(r.job.Args), v)
}

// Progress reports how far along the task is (0-100) and what it is doing,
// in plain words. Calls are cheap: the database is written at most once a
// second, but every call is shown to open browsers.
func (r *Run) Progress(percent int, message string) {
	percent = min(max(percent, 0), 100)
	r.mu.Lock()
	r.job.Progress, r.job.Message = percent, message
	save := time.Since(r.lastSave) >= progressEvery
	if save {
		r.lastSave = time.Now()
	}
	job := *r.job
	r.mu.Unlock()

	if save {
		_, err := r.m.db.DB.NewUpdate().Model(&job).Column("progress", "message").WherePK().Exec(context.Background())
		if err != nil {
			r.m.logger.Warn("can't save task progress", "job", job.ID, "error", err)
		}
	}
	r.m.notify(job)
}

// TaskInfo is a registered task and its recent state, for System → Tasks.
type TaskInfo struct {
	Task
	// Last is the most recent finished run, if any.
	Last *Job
	// Active is a queued or running job, if any.
	Active *Job
	// NextRun is when the schedule will start it next (zero if unscheduled
	// or already active).
	NextRun time.Time
}

// Manager owns the task registry, the workers and the scheduler.
type Manager struct {
	db     *store.Store
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	tasks   map[string]Task
	cancels map[int64]context.CancelFunc
	wake    chan struct{}

	listenersMu sync.Mutex
	listeners   []func(Job)
}

// PruneKind is the built-in task that tidies the task history.
const PruneKind = "jobs.prune"

// New returns a Manager. Register tasks, then call Start.
func New(db *store.Store, logger *slog.Logger) *Manager {
	m := &Manager{
		db:      db,
		logger:  logger,
		now:     time.Now,
		tasks:   map[string]Task{},
		cancels: map[int64]context.CancelFunc{},
		wake:    make(chan struct{}, 1),
	}
	m.Register(Task{
		Kind:        PruneKind,
		Name:        "Tidy task history",
		Description: "Removes finished task runs older than 30 days, keeping the last 10 of each task.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *Run) error {
			n, err := m.prune(ctx)
			if err == nil {
				r.Progress(100, fmt.Sprintf("Removed %d old runs", n))
			}
			return err
		},
	})
	return m
}

// Register adds a task. Registering the same kind twice panics: it's a
// programming mistake, caught by any test that starts the app.
func (m *Manager) Register(t Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.tasks[t.Kind]; dup {
		panic("jobs: task registered twice: " + t.Kind)
	}
	m.tasks[t.Kind] = t
}

// OnChange registers fn to be called whenever a job changes.
func (m *Manager) OnChange(fn func(Job)) {
	m.listenersMu.Lock()
	defer m.listenersMu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify(j Job) {
	m.listenersMu.Lock()
	ls := append([]func(Job){}, m.listeners...)
	m.listenersMu.Unlock()
	for _, fn := range ls {
		fn(j)
	}
}

// Enqueue queues a run of kind. If dedupeKey is set and a run with the same
// key is already queued or running, that run is returned instead and
// created is false.
func (m *Manager) Enqueue(ctx context.Context, kind string, args any, trigger Trigger, dedupeKey string) (job *Job, created bool, err error) {
	m.mu.Lock()
	_, known := m.tasks[kind]
	m.mu.Unlock()
	if !known {
		return nil, false, fmt.Errorf("%w: %s", ErrUnknownTask, kind)
	}
	if args == nil {
		args = struct{}{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return nil, false, err
	}

	job = &Job{Kind: kind, Args: string(b), Status: Queued, Trigger: trigger, CreatedAt: m.now().UTC()}
	if dedupeKey != "" {
		job.DedupeKey = &dedupeKey
		existing := new(Job)
		err := m.db.DB.NewSelect().Model(existing).Where("dedupe_key = ?", dedupeKey).Scan(ctx)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if _, err := m.db.DB.NewInsert().Model(job).Exec(ctx); err != nil {
		// Lost a race with another Enqueue of the same key.
		if dedupeKey != "" {
			existing := new(Job)
			if m.db.DB.NewSelect().Model(existing).Where("dedupe_key = ?", dedupeKey).Scan(ctx) == nil {
				return existing, false, nil
			}
		}
		return nil, false, err
	}
	m.notify(*job)
	m.poke()
	return job, true, nil
}

// RunNow queues a manual run of kind, unless one is already queued or
// running, in which case that one is returned.
func (m *Manager) RunNow(ctx context.Context, kind string) (*Job, bool, error) {
	return m.Enqueue(ctx, kind, nil, Manual, "task:"+kind)
}

// RunForAPI is RunNow for a run another app asked for, so Tasks says so
// instead of "started by hand".
func (m *Manager) RunForAPI(ctx context.Context, kind string) (*Job, bool, error) {
	return m.Enqueue(ctx, kind, nil, API, "task:"+kind)
}

// Cancel stops a queued or running job.
func (m *Manager) Cancel(ctx context.Context, id int64) error {
	job := new(Job)
	if err := m.db.DB.NewSelect().Model(job).Where("id = ?", id).Scan(ctx); err != nil {
		return err
	}
	if job.Status.Finished() {
		return ErrNotCancellable
	}
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel() // the worker records the cancellation when the task returns
		return nil
	}
	return m.finish(ctx, job, Cancelled, "Cancelled before it started", "")
}

// Tasks lists the registered tasks with their latest runs, sorted by name.
func (m *Manager) Tasks(ctx context.Context) ([]TaskInfo, error) {
	m.mu.Lock()
	tasks := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.Unlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })

	out := make([]TaskInfo, 0, len(tasks))
	for _, t := range tasks {
		info := TaskInfo{Task: t}
		last := new(Job)
		err := m.db.DB.NewSelect().Model(last).
			Where("kind = ?", t.Kind).Where("status IN (?)", bun.List([]Status{Succeeded, Failed, Cancelled})).
			Order("id DESC").Limit(1).Scan(ctx)
		if err == nil {
			info.Last = last
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		active := new(Job)
		err = m.db.DB.NewSelect().Model(active).
			Where("kind = ?", t.Kind).Where("status IN (?)", bun.List([]Status{Queued, Running})).
			Order("id").Limit(1).Scan(ctx)
		if err == nil {
			info.Active = active
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if t.Every > 0 && info.Active == nil {
			info.NextRun = m.nextRun(t, info.Last)
		}
		out = append(out, info)
	}
	return out, nil
}

// Recent returns the latest limit jobs, newest first.
//
// Quiet runs (routine ones that found nothing to do) are left out unless
// includeQuiet is true. When they're left out, hiddenQuiet says how many
// quiet runs fall inside the window the caller sees, that is, are newer than
// the oldest run returned, so the page can say "12 routine runs are hidden".
// With includeQuiet it is always 0.
func (m *Manager) Recent(ctx context.Context, limit int, includeQuiet bool) (jobs []Job, hiddenQuiet int, err error) {
	q := m.db.DB.NewSelect().Model(&jobs).Order("id DESC").Limit(int64(limit))
	if !includeQuiet {
		q = q.Where("quiet = ?", false)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, 0, err
	}
	if includeQuiet {
		return jobs, 0, nil
	}
	count := m.db.DB.NewSelect().Model((*Job)(nil)).Where("quiet = ?", true)
	if len(jobs) == limit {
		count = count.Where("id > ?", jobs[len(jobs)-1].ID)
	}
	n, err := count.Count(ctx)
	return jobs, int(n), err
}

// Get returns one job.
func (m *Manager) Get(ctx context.Context, id int64) (*Job, error) {
	job := new(Job)
	if err := m.db.DB.NewSelect().Model(job).Where("id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

// Name returns the plain-English name of a task kind.
func (m *Manager) Name(kind string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[kind]; ok {
		return t.Name
	}
	return kind
}

// nextRun is when a scheduled task is due: one interval after its last run
// started, or now if it has never run.
func (m *Manager) nextRun(t Task, last *Job) time.Time {
	if last == nil || last.StartedAt == nil {
		return m.now().UTC()
	}
	return last.StartedAt.Add(t.Every)
}

// Start recovers jobs interrupted by a restart, then runs the workers and
// the scheduler until ctx is cancelled. It returns once everything stopped.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.recover(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() { m.worker(ctx) })
	}
	wg.Go(func() { m.scheduler(ctx) })
	wg.Wait()
	return nil
}

// recover re-queues jobs that were running when Reelkeeper stopped, or fails
// them once they've been tried maxAttempts times.
func (m *Manager) recover(ctx context.Context) error {
	var stuck []Job
	if err := m.db.DB.NewSelect().Model(&stuck).Where("status = ?", Running).Scan(ctx); err != nil {
		return err
	}
	for i := range stuck {
		j := &stuck[i]
		if j.Attempts >= maxAttempts {
			if err := m.finish(ctx, j, Failed, "", "Interrupted by restarts too many times"); err != nil {
				return err
			}
			continue
		}
		j.Status = Queued
		j.Message = "Restarted after Reelkeeper was stopped"
		if _, err := m.db.DB.NewUpdate().Model(j).Column("status", "message").WherePK().Exec(ctx); err != nil {
			return err
		}
	}
	if len(stuck) > 0 {
		m.logger.Info("re-queued tasks interrupted by a restart", "count", len(stuck))
	}
	return nil
}

func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) worker(ctx context.Context) {
	for {
		job, err := m.claim(ctx)
		if err != nil && ctx.Err() == nil {
			m.logger.Error("can't pick up a task", "error", err)
		}
		if job != nil {
			m.execute(ctx, job)
			continue // look for more work straight away
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-time.After(30 * time.Second):
		}
	}
}

// claim marks the oldest queued job as running and returns it, or nil.
func (m *Manager) claim(ctx context.Context) (*Job, error) {
	m.mu.Lock() // one claimer at a time inside this process
	defer m.mu.Unlock()
	job := new(Job)
	err := m.db.DB.NewSelect().Model(job).Where("status = ?", Queued).Order("id").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	job.Status, job.StartedAt, job.Attempts = Running, &now, job.Attempts+1
	res, err := m.db.DB.NewUpdate().Model(job).Column("status", "started_at", "attempts").
		WherePK().Where("status = ?", Queued).Exec(ctx)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // cancelled in the meantime
	}
	return job, nil
}

func (m *Manager) execute(ctx context.Context, job *Job) {
	m.mu.Lock()
	task, ok := m.tasks[job.Kind]
	runCtx, cancel := context.WithCancel(ctx)
	m.cancels[job.ID] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, job.ID)
		m.mu.Unlock()
	}()

	m.notify(*job)
	if !ok {
		m.record(job, Failed, "", "This task no longer exists in this version of Reelkeeper")
		return
	}

	r := &Run{job: job, m: m}
	start := time.Now()
	err := safeRun(runCtx, task, r)
	r.mu.Lock()
	msg := r.job.Message
	r.mu.Unlock()

	switch {
	case err == nil:
		m.record(job, Succeeded, msg, "")
		m.logger.Info("task finished", "task", task.Name, "job", job.ID, "duration", time.Since(start).Round(time.Millisecond))
	case ctx.Err() != nil:
		// Reelkeeper is shutting down: leave it running in the database so the
		// next start re-queues it.
		m.logger.Info("task interrupted by shutdown; it will run again at the next start", "task", task.Name, "job", job.ID)
	case errors.Is(err, context.Canceled) || runCtx.Err() != nil:
		m.record(job, Cancelled, "Cancelled", "")
		m.logger.Info("task cancelled", "task", task.Name, "job", job.ID)
	default:
		m.record(job, Failed, msg, err.Error())
		m.logger.Error("task failed", "task", task.Name, "job", job.ID, "error", err)
	}
}

// safeRun turns a panic in a task into an error, so one bad task can't
// take Reelkeeper down.
func safeRun(ctx context.Context, t Task, r *Run) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("the task crashed: %v", p)
			slog.Error("task panic", "task", t.Kind, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	return t.Run(ctx, r)
}

// record finishes a job after it ran, using a fresh context so a shutdown
// mid-write doesn't lose the result.
func (m *Manager) record(job *Job, status Status, message, errText string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.finish(ctx, job, status, message, errText); err != nil {
		m.logger.Error("can't record task result", "job", job.ID, "error", err)
	}
}

func (m *Manager) finish(ctx context.Context, job *Job, status Status, message, errText string) error {
	now := m.now().UTC()
	job.Status, job.FinishedAt, job.DedupeKey = status, &now, nil
	if message != "" {
		job.Message = message
	}
	job.Error = errText
	if status == Succeeded {
		job.Progress = 100
	} else {
		job.Quiet = false // only a successful run can be routine
	}
	_, err := m.db.DB.NewUpdate().Model(job).
		Column("status", "finished_at", "dedupe_key", "message", "error", "progress", "quiet").WherePK().Exec(ctx)
	if err == nil {
		m.notify(*job)
	}
	return err
}

// scheduler queues scheduled tasks when they're due.
func (m *Manager) scheduler(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		m.queueDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (m *Manager) queueDue(ctx context.Context) {
	infos, err := m.Tasks(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Warn("can't check task schedules", "error", err)
		}
		return
	}
	now := m.now().UTC()
	for _, info := range infos {
		if info.Every == 0 || info.Active != nil || info.NextRun.After(now) {
			continue
		}
		if _, _, err := m.Enqueue(ctx, info.Kind, nil, Schedule, "task:"+info.Kind); err != nil && ctx.Err() == nil {
			m.logger.Warn("can't queue scheduled task", "task", info.Name, "error", err)
		}
	}
}

// prune deletes finished jobs older than keepFinished, but always keeps the
// newest keepPerKind of each kind. It returns how many it deleted.
func (m *Manager) prune(ctx context.Context) (int64, error) {
	cutoff := m.now().UTC().Add(-keepFinished)
	var kinds []string
	if err := m.db.DB.NewSelect().Model((*Job)(nil)).ColumnExpr("DISTINCT kind").Scan(ctx, &kinds); err != nil {
		return 0, err
	}
	var deleted int64
	for _, kind := range kinds {
		var keep []int64
		err := m.db.DB.NewSelect().Model((*Job)(nil)).Column("id").
			Where("kind = ?", kind).Order("id DESC").Limit(keepPerKind).Scan(ctx, &keep)
		if err != nil {
			return deleted, err
		}
		q := m.db.DB.NewDelete().Model((*Job)(nil)).
			Where("kind = ?", kind).Where("finished_at < ?", cutoff).
			Where("status IN (?)", bun.List([]Status{Succeeded, Failed, Cancelled}))
		if len(keep) > 0 {
			q = q.Where("id NOT IN (?)", bun.List(keep))
		}
		res, err := q.Exec(ctx)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// Describe returns a short plain-English summary of a job's state.
func Describe(j Job) string {
	switch j.Status {
	case Queued:
		return "Waiting to start"
	case Running:
		if j.Message != "" {
			return j.Message
		}
		return "Running"
	case Failed:
		return strings.TrimSpace("Failed: " + j.Error)
	default:
		return j.Message
	}
}
