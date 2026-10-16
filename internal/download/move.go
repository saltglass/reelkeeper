package download

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// Moving torrents between download clients (1.3.0).
//
// When the download client is changed (qBittorrent, Transmission or Deluge),
// Reelkeeper's active torrents move with it: ones still downloading, ones
// finished and waiting to be imported, and, unless the user chose to leave
// them, ones seeding. The new client checks the files already on disk against
// the torrent's pieces, so nothing is downloaded again. Reelkeeper never asks
// either client to delete data during a move.
//
// The move is a task (System → Tasks). Each download records which step it
// has reached (Download.MoveState), so the task carries on after a restart;
// movestep.go has the steps and what happens when one fails. While a move is
// running (MoveStore says so), grabbing, imports and cleanup wait.

// MoveKind is the move task (stored; never rename). It also runs on a
// schedule, quietly, to pick a move up again after a restart or a failure.
const MoveKind = "download.move"

// MoveStore keeps the move in progress, with the old client's connection
// (settings.Service).
type MoveStore interface {
	DownloadMove() settings.DownloadMove
	SaveDownloadMove(ctx context.Context, m settings.DownloadMove) error
	ClearDownloadMove(ctx context.Context) error
}

// SetMoves connects the store of the move in progress.
func (s *Service) SetMoves(m MoveStore) { s.moves = m }

// Moving reports whether torrents are moving between download clients.
// Grabbing, imports and cleanup check this one flag and wait.
func (s *Service) Moving() bool { return s.moves != nil && s.moves.DownloadMove().Active }

// ErrMoveRunning means a move to another client hasn't finished, so the
// client can't be changed again yet.
var ErrMoveRunning = errors.New("torrents are still moving to the other download client (System → Tasks)")

// moveTiming says how long a move waits for the clients.
type moveTiming struct {
	poll     time.Duration // between looks at a client
	begin    time.Duration // for a check to show that it has started
	stop     time.Duration // for a stopped torrent to report stopped
	check    time.Duration // for a check to finish
	metadata time.Duration // for a magnet link's details to arrive
	files    time.Duration // for a client to finish moving files to their folder
	giveUp   time.Duration // after which a torrent that still hasn't moved is given up on
}

// moveGiveUp is how long a torrent gets to finish moving, counted from when it
// was marked to move. Past it, a step that keeps failing (the old client gone
// for good, say) ends the torrent as a failure instead of being tried again
// every minute, so grabbing, imports and cleanup can't wait forever.
const moveGiveUp = time.Hour

var defaultMoveTiming = moveTiming{
	poll: 500 * time.Millisecond, begin: 5 * time.Second, stop: 2 * time.Minute,
	check: 12 * time.Hour, metadata: 2 * time.Minute, files: 10 * time.Minute, giveUp: moveGiveUp,
}

// heldBy is the type of client holding a download: what is recorded, or the
// client that is set up for downloads from before Reelkeeper recorded it.
func (d *Download) heldBy(current string) string {
	if d.Client != "" {
		return d.Client
	}
	return current
}

// MovingTo is the name of the client a moving download is on its way to, or "".
func (d *Download) MovingTo() string {
	if d.MoveState == "" {
		return ""
	}
	return settings.ClientDisplayName(d.MoveTo)
}

// activeStatuses are the statuses of downloads whose torrent is in the client.
var activeStatuses = []string{StatusDownloading, StatusImportFailed, StatusImported}

// MoveProblem is a torrent that can't move, and why.
type MoveProblem struct {
	Release string
	Reason  string
	Seeding bool
}

// MovePreview says what changing the download client would move.
type MovePreview struct {
	// Total are all the torrents that could move; Downloading the ones in
	// Activity → Queue (they always move) and Seeding the imported ones still
	// seeding (the user may leave them).
	Total, Downloading, Seeding int
	// CannotMove are torrents that would stay where they are, with the
	// reason (included in the counts above).
	CannotMove []MoveProblem
	// ClientError is set when the current client couldn't be asked, so
	// CannotMove may be incomplete.
	ClientError string
}

// candidates are the downloads a move would take: those in the client that is
// set up, not already on their way somewhere.
func (s *Service) candidates(ctx context.Context) ([]Download, error) {
	var ds []Download
	err := s.db.DB.NewSelect().Model(&ds).Where("status IN (?)", bun.List(activeStatuses)).
		Where("move_state = ''").Order("id").Scan(ctx)
	if err != nil {
		return nil, err
	}
	cur := s.settings().ClientType()
	out := ds[:0]
	for _, d := range ds {
		if d.heldBy(cur) == cur {
			out = append(out, d)
		}
	}
	return out, nil
}

// MovePreview counts what a change of download client would move, and finds
// the torrents that couldn't. It reads the current client (the one the
// torrents are in now); the new one doesn't matter yet.
func (s *Service) MovePreview(ctx context.Context) (MovePreview, error) {
	var p MovePreview
	if !s.settings().Configured() {
		return p, nil
	}
	ds, err := s.candidates(ctx)
	if err != nil {
		return p, err
	}
	c, err := s.client()
	var live map[string]Torrent
	if err == nil {
		var ts []Torrent
		if ts, err = ourTorrents(ctx, c); err == nil {
			live = map[string]Torrent{}
			for _, t := range ts {
				live[t.Hash] = t
			}
		}
	}
	if err != nil {
		p.ClientError = capitalizeFirst(err.Error()) + "."
	}
	for i := range ds {
		d := &ds[i]
		seeding := d.Status == StatusImported
		if live != nil {
			if _, ok := live[d.Hash]; !ok {
				continue // not in the client: Reelkeeper will notice, there's nothing to move
			}
		}
		p.Total++
		if seeding {
			p.Seeding++
		} else {
			p.Downloading++
		}
		if live == nil {
			continue
		}
		if _, ok, err := s.storedSource(ctx, d.ID); err == nil && ok {
			continue
		}
		if _, err := c.Source(ctx, d.Category, d.Hash); err != nil {
			var ns *NoSourceError
			if errors.As(err, &ns) {
				p.CannotMove = append(p.CannotMove, MoveProblem{Release: d.ReleaseTitle, Reason: ns.Reason, Seeding: seeding})
			}
		}
	}
	return p, nil
}

func capitalizeFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// PlanMove checks that changing the download client from old to next can go
// ahead: no move is running, and when there is something to move, both
// clients can be reached with the login they were given. It changes nothing.
func (s *Service) PlanMove(ctx context.Context, old, next settings.DownloadClient, leaveSeeding bool) error {
	if s.Moving() {
		return ErrMoveRunning
	}
	if !old.Configured() {
		// The old client was removed, so Reelkeeper no longer knows how to
		// reach it: downloads in the Queue can't be taken along.
		n, err := s.QueueSize(ctx)
		if err != nil || n == 0 {
			return err
		}
		return fmt.Errorf("the download client was removed, so Reelkeeper can't reach the %s in the Queue to move %s: set the old client up again, or finish or remove them first",
			plural(n, "download"), pluralThem(n))
	}
	n, err := s.countToMove(ctx, leaveSeeding)
	if err != nil || n == 0 {
		return err
	}
	if _, err := s.Test(ctx, old); err != nil {
		return fmt.Errorf("Reelkeeper can't move the torrents: %s needs to be reachable first (%w)", old.ClientName(), err) //nolint:staticcheck // a proper name
	}
	if _, err := s.Test(ctx, next); err != nil {
		return fmt.Errorf("Reelkeeper can't move the torrents: %s can't be used yet (%w)", next.ClientName(), err) //nolint:staticcheck // a proper name
	}
	return nil
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func pluralThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func (s *Service) countToMove(ctx context.Context, leaveSeeding bool) (int, error) {
	ds, err := s.candidates(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		if d.Status != StatusImported || !leaveSeeding {
			n++
		}
	}
	return n, nil
}

// BeginMove marks the downloads that will move and records the old client's
// connection, before the new settings are saved. It returns how many will
// move. Call FinishSwitch once the settings are saved, or CancelMove if
// saving them failed.
func (s *Service) BeginMove(ctx context.Context, old settings.DownloadClient, next settings.DownloadClient, leaveSeeding bool) (int, error) {
	s.syncMu.Lock() // wait for a check or an import in progress
	defer s.syncMu.Unlock()
	if s.Moving() {
		return 0, ErrMoveRunning
	}
	if s.moves == nil || !old.Configured() {
		return 0, nil
	}
	ds, err := s.candidates(ctx)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for _, d := range ds {
		if d.Status != StatusImported || !leaveSeeding {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.moves.SaveDownloadMove(ctx, settings.DownloadMove{Active: true, Old: old}); err != nil {
		return 0, err
	}
	_, err = s.db.DB.NewUpdate().Model((*Download)(nil)).
		Set("client = ?", old.ClientType()).Set("move_state = ?", moveQueued).Set("move_to = ?", next.ClientType()).
		Where("id IN (?)", bun.List(ids)).Exec(ctx)
	if err != nil {
		_ = s.moves.ClearDownloadMove(ctx)
		return 0, err
	}
	return len(ids), nil
}

// CancelMove undoes BeginMove when the new settings couldn't be saved.
func (s *Service) CancelMove(ctx context.Context) {
	if s.moves == nil {
		return
	}
	if _, err := s.db.DB.NewUpdate().Model((*Download)(nil)).
		Set("move_state = ''").Set("move_to = ''").Set("move_started_at = NULL").
		Where("move_state = ?", moveQueued).Exec(ctx); err != nil {
		s.logger.Error("couldn't cancel a move to another download client", "error", err)
	}
	if err := s.moves.ClearDownloadMove(ctx); err != nil {
		s.logger.Error("couldn't cancel a move to another download client", "error", err)
	}
}

// ClientChanged is called after the download client's type was changed
// (to another of qBittorrent, Transmission and Deluge), once BeginMove has marked what
// moves. Downloads imported and still seeding in the old client that were
// not marked are the ones the user chose to leave there: Reelkeeper can't
// apply its seeding goal in a client it no longer talks to, so they are
// marked "seeded" (the status a download gets when it is done and gone from
// the client), which no cleanup rule acts on. Nothing is deleted; History
// records each, and the number is returned. The move, if there is one, is
// started.
func (s *Service) ClientChanged(ctx context.Context, oldName, newName string) (int, error) {
	s.syncMu.Lock()
	s.clientFailures = 0
	var ds []Download
	err := s.db.DB.NewSelect().Model(&ds).Where("status = ?", StatusImported).Where("move_state = ''").Order("id").Scan(ctx)
	if err != nil {
		s.syncMu.Unlock()
		return 0, err
	}
	cur := s.settings().ClientType()
	var left []Download
	for _, d := range ds {
		// Downloads that already moved to the client now set up stay.
		if d.Client == "" || d.Client != cur {
			left = append(left, d)
		}
	}
	msg := "Left seeding in " + oldName + " when the download client changed to " + newName + "; Reelkeeper no longer tracks it."
	for i := range left {
		s.finish(ctx, &left[i], StatusSeeded, msg, grab.EventRemoved)
	}
	s.syncMu.Unlock()
	if s.Moving() && s.jobs != nil {
		if _, _, err := s.jobs.Enqueue(ctx, MoveKind, nil, jobs.System, "task:"+MoveKind); err != nil {
			s.logger.Error("couldn't start the move to the new download client", "error", err)
		}
	}
	return len(left), nil
}

// moveTask is the task that does the moving.
func (s *Service) moveTask() jobs.Task {
	return jobs.Task{
		Kind: MoveKind,
		Name: "Move torrents",
		Description: "Moves Reelkeeper's torrents to a new download client after the client was changed (Settings → Download Client): the new client checks the files already on disk, so nothing is downloaded again, and nothing is deleted. " +
			"Grabbing, imports and cleanup wait until it's done. It also picks a move up again after a restart.",
		Every: time.Minute,
		Run:   func(ctx context.Context, r *jobs.Run) error { return s.RunMove(ctx, r) },
	}
}

// reporter is the part of a running task (*jobs.Run) that RunMove uses.
type reporter interface {
	Progress(percent int, message string)
	Quiet()
}

// RunMove does the move task: it takes each download that is moving through
// the steps it hasn't completed yet.
func (s *Service) RunMove(ctx context.Context, r reporter) error {
	if !s.moveMu.TryLock() {
		r.Quiet()
		r.Progress(100, "A move is already running.")
		return nil
	}
	defer s.moveMu.Unlock()
	if s.moves == nil || !s.moves.DownloadMove().Active {
		r.Quiet()
		r.Progress(100, "No torrents are moving.")
		return nil
	}
	mv := s.moves.DownloadMove()
	cur := s.settings()
	if !cur.Configured() || cur.ClientType() == mv.Old.ClientType() {
		// The new settings were never saved (a crash between BeginMove and
		// saving them): nothing moves after all.
		s.CancelMove(ctx)
		r.Progress(100, "The download client wasn't changed, so nothing was moved.")
		return nil
	}
	var ds []Download
	if err := s.db.DB.NewSelect().Model(&ds).Where("move_state <> ''").Order("id").Scan(ctx); err != nil {
		return err
	}
	e := &moveEnv{
		old: s.NewClient(mv.Old), oldName: mv.Old.ClientName(), oldType: mv.Old.ClientType(),
		new: s.NewClient(cur), newName: cur.ClientName(), newType: cur.ClientType(),
	}
	var firstErr error
	for i := range ds {
		d := &ds[i]
		r.Progress(i*100/max(len(ds), 1), fmt.Sprintf("Moving %d of %d to %s: %s", i+1, len(ds), e.newName, d.ReleaseTitle))
		if err := s.moveOne(ctx, e, d); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.logger.Warn("a torrent couldn't be moved yet; it will be tried again", "release", d.ReleaseTitle, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", d.ReleaseTitle, err)
			}
		}
	}
	left, err := s.db.DB.NewSelect().Model((*Download)(nil)).Where("move_state <> ''").Count(ctx)
	if err != nil {
		return err
	}
	if left > 0 {
		if firstErr == nil {
			firstErr = errors.New("some torrents haven't finished moving")
		}
		return fmt.Errorf("%d torrents haven't finished moving (they are tried again every minute): %w", left, firstErr)
	}
	if err := s.moves.ClearDownloadMove(ctx); err != nil {
		return err
	}
	r.Progress(100, e.summary())
	s.logger.Info("move to another download client finished", "from", e.oldName, "to", e.newName, "summary", e.summary())
	return nil
}

// moveEnv is the two clients of a move and what has happened so far.
type moveEnv struct {
	old, new Client
	// oldName and newName are for messages; oldType and newType are the
	// settings' names for the clients.
	oldName, newName string
	oldType, newType string
	moved, failed    int
}

func (e *moveEnv) summary() string {
	parts := []string{fmt.Sprintf("Moved %d to %s", e.moved, e.newName)}
	if e.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d couldn't move and stayed in %s (see History)", e.failed, e.oldName))
	}
	return strings.Join(parts, "; ") + "."
}
