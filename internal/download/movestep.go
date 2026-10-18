package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Moving one torrent. The order never changes:
//
//  1. Note whether it was paused, stop it in the old client and WAIT until it
//     reports stopped (a client keeps writing pieces until then). Only then
//     read what it has verified. Two clients never have the same files open
//     at once.
//  2. Find the files on disk and record each one's exact name and folder, so
//     a failed move can put them back exactly. Unfinished files may carry a
//     name the new client doesn't know (Transmission's ".part", qBittorrent's
//     ".!qB"); Reelkeeper renames them to the plain name itself. (Deluge keeps
//     unfinished files under their plain names, so it needs this on the way
//     in and never causes it on the way out.)
//  3. Add the torrent to the new client STOPPED, pointed at the folder the
//     files are in now. Never "skip check".
//  4. Start the check explicitly and trust no number from before it. Wait
//     until it has begun and then finished.
//  5. Success is the new client having verified at least what the old one
//     had (a finished torrent: all of it). Then the seeding limits go off,
//     it is paused again if it was paused, back to the top of the queue if it
//     was a Seerr request, the record says which client has it, and it is
//     removed from the old client WITHOUT deleting any file.
//
// If the check falls short, or a step is refused, the torrent is removed from
// the new client without deleting files, every file gets back the name and
// folder it had, and the old client checks it and (if it was seeding) starts
// it again. Nothing in this file ever asks a client to delete data.
//
// Every step is saved (Download.MoveState) before the next begins, and each
// can be done again safely, so the task carries on after a restart.

// The steps (stored in downloads.move_state; never rename one that has shipped).
const (
	// moveQueued: chosen to move; the old client hasn't been touched.
	moveQueued = "queued"
	// moveStopped: stopped in the old client; its counters are recorded.
	moveStopped = "stopped"
	// moveRenaming: the files' names are recorded; renaming them.
	moveRenaming = "renaming"
	// moveRenamed: the files have plain names.
	moveRenamed = "renamed"
	// moveAdded: in the new client, stopped.
	moveAdded = "added"
	// moveChecking: the new client is checking the files.
	moveChecking = "checking"
	// moveFinishing: checked; handing over (limits, pause, old client).
	moveFinishing = "finishing"
	// moveRollbackEarly and moveRollback: undoing a failed move. Early means
	// the old client had not been stopped, so nothing was touched.
	moveRollbackEarly = "rollback-early"
	moveRollback      = "rollback"
)

// moveFailure is a step that can't succeed, and why. Anything else a step
// returns (a client that can't be reached, say) is tried again later.
type moveFailure struct{ reason string }

func (f *moveFailure) Error() string { return f.reason }

func failf(format string, args ...any) error { return &moveFailure{fmt.Sprintf(format, args...)} }

// movedFile is one of a torrent's files as it was before the move.
type movedFile struct {
	// Name is the file's path inside the torrent.
	Name string `json:"name"`
	// Was is the name it had in Dir, with the ending an unfinished file may
	// have ("Name.part"); empty if it didn't exist yet.
	Was string `json:"was,omitempty"`
}

// moveLayout is what Reelkeeper records about a torrent's files before it
// touches anything (stored in downloads.move_layout).
type moveLayout struct {
	// Dir is the folder the files were in, and Final the folder the torrent
	// belongs in once finished (the same, unless it was unfinished in a
	// temporary folder). Look are the other places to look for them when
	// putting them back.
	Dir   string      `json:"dir"`
	Final string      `json:"final"`
	Look  []string    `json:"look,omitempty"`
	Files []movedFile `json:"files"`
	// Done: the old client had finished the torrent. Size is its size.
	Done bool  `json:"done,omitempty"`
	Size int64 `json:"size,omitempty"`
	// NoDetails: the torrent has no details yet (a magnet link still
	// fetching them), so there is nothing to check.
	NoDetails bool `json:"noDetails,omitempty"`
}

func (d *Download) layout() (moveLayout, error) {
	var l moveLayout
	if d.MoveLayout == "" {
		return l, nil
	}
	err := json.Unmarshal([]byte(d.MoveLayout), &l)
	return l, err
}

// saveMove writes the named columns of a download's move record.
func (s *Service) saveMove(ctx context.Context, d *Download, cols ...string) error {
	_, err := s.db.DB.NewUpdate().Model(d).Column(cols...).WherePK().Exec(ctx)
	return err
}

// moveOne takes one download through the steps it hasn't completed. A nil
// return means it is done (moved, or failed and put back); an error means it
// should be tried again later, from the step it saved.
func (s *Service) moveOne(ctx context.Context, e *moveEnv, d *Download) error {
	if d.MoveStartedAt == nil && d.MoveState != "" {
		now := s.now().UTC()
		d.MoveStartedAt = &now
		if err := s.saveMove(ctx, d, "move_started_at"); err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.moveHook != nil && d.MoveState != "" {
			if err := s.moveHook(d.MoveState); err != nil {
				return err
			}
		}
		var err error
		switch d.MoveState {
		case "":
			return nil
		case moveQueued:
			err = s.mvStop(ctx, e, d)
		case moveStopped:
			err = s.mvPlan(ctx, e, d)
		case moveRenaming:
			err = s.mvRename(ctx, e, d)
		case moveRenamed:
			err = s.mvAdd(ctx, e, d)
		case moveAdded, moveChecking:
			err = s.mvCheck(ctx, e, d)
		case moveFinishing:
			err = s.mvFinish(ctx, e, d)
		case moveRollbackEarly, moveRollback:
			err = s.mvRollback(ctx, e, d)
		default:
			err = failf("it was left at a step this version doesn't know (%q)", d.MoveState)
		}
		var f *moveFailure
		inRollback := d.MoveState == moveRollback || d.MoveState == moveRollbackEarly
		switch {
		case err == nil:
		case errors.As(err, &f) && !inRollback:
			if err := s.startRollback(ctx, d, f.reason); err != nil {
				return err
			}
		case ctx.Err() != nil || !s.overdue(ctx, d):
			// A failure while undoing, or a client that can't be reached:
			// try again later.
			return err
		case inRollback:
			return s.mvGiveUp(ctx, e, d, err)
		default:
			// A step that has kept failing for an hour is a failure like any
			// other: undo the move (the loop runs the rollback right away).
			reason := fmt.Sprintf("it couldn't be moved within %s: %v", hoursText(s.moveTiming.giveUp), err)
			if err := s.startRollback(ctx, d, reason); err != nil {
				return err
			}
		}
	}
}

// startRollback records that a move can't go ahead and must be undone. Early
// means the old client hasn't been touched yet.
func (s *Service) startRollback(ctx context.Context, d *Download, reason string) error {
	state := moveRollback
	if d.MoveState == moveQueued {
		state = moveRollbackEarly
	}
	d.MoveState, d.Message = state, reason
	return s.saveMove(ctx, d, "move_state", "message")
}

// overdue reports whether a download's move has gone on for longer than it is
// allowed. The hour starts when the torrent's turn comes (moveOne), not when
// the client was switched, so one late in a long list gets its full hour.
func (s *Service) overdue(ctx context.Context, d *Download) bool {
	now := s.now().UTC()
	if d.MoveStartedAt == nil {
		d.MoveStartedAt = &now
		if err := s.saveMove(ctx, d, "move_started_at"); err != nil {
			s.logger.Warn("couldn't record when a move began", "release", d.ReleaseTitle, "error", err)
		}
		return false
	}
	return now.Sub(*d.MoveStartedAt) >= s.moveTiming.giveUp
}

// hoursText says how long a move gets, in words (tests shorten it).
func hoursText(d time.Duration) string {
	if d == time.Hour {
		return "an hour"
	}
	return d.String()
}

// ---- looking at a client

// torrentIn finds one of Reelkeeper's torrents in a client.
func torrentIn(ctx context.Context, c Client, category, hash string) (Torrent, bool, error) {
	ts, err := c.Torrents(ctx, category)
	if err != nil {
		return Torrent{}, false, err
	}
	for _, t := range ts {
		if t.Hash == hash {
			return t, true, nil
		}
	}
	return Torrent{}, false, nil
}

// sleep waits, unless the context ends first.
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitFor polls a client until the torrent satisfies ok or the time is up.
// The last look is returned either way; a torrent that vanished is an error.
func (s *Service) waitFor(ctx context.Context, c Client, d *Download, within time.Duration, ok func(Torrent) bool) (Torrent, bool, error) {
	deadline := time.Now().Add(within)
	for {
		t, found, err := torrentIn(ctx, c, d.Category, d.Hash)
		if err != nil {
			return t, false, err
		}
		if !found {
			return t, false, fmt.Errorf("%s doesn't list the torrent", sentenceName(c.Name()))
		}
		if ok(t) {
			return t, true, nil
		}
		if !time.Now().Before(deadline) {
			return t, false, nil
		}
		if err := sleep(ctx, s.moveTiming.poll); err != nil {
			return t, false, err
		}
	}
}

// waitStopped waits until the client says the torrent is stopped.
func (s *Service) waitStopped(ctx context.Context, c Client, d *Download) (Torrent, bool, error) {
	return s.waitFor(ctx, c, d, s.moveTiming.stop, Torrent.Stopped)
}

// check makes a client check a stopped torrent's files and waits for the
// answer. It trusts nothing from before: it waits for the check to begin
// (qBittorrent takes a moment to show it; a check of a tiny torrent may be
// over before it is seen), then to finish.
func (s *Service) check(ctx context.Context, c Client, d *Download) (Torrent, error) {
	if err := c.Recheck(ctx, d.Category, d.Hash); err != nil {
		return Torrent{}, err
	}
	began := func(t Torrent) bool { return t.State == torrent.Checking }
	if _, _, err := s.waitFor(ctx, c, d, s.moveTiming.begin, began); err != nil {
		return Torrent{}, err
	}
	t, ok, err := s.waitFor(ctx, c, d, s.moveTiming.check, func(t Torrent) bool { return t.State != torrent.Checking })
	if err != nil {
		return t, err
	}
	if !ok {
		return t, failf("%s is still checking the files after %s", sentenceName(c.Name()), s.moveTiming.check)
	}
	return t, nil
}

// unreachable reports whether err means a client can't be reached or logged
// into: worth trying again later, not a reason to give up on a torrent.
func unreachable(err error) bool {
	return errors.Is(err, ErrUnreachable) || errors.Is(err, ErrLogin)
}

// refused turns what a client said about a request into a failure of the
// move, unless the client couldn't be reached (then it is tried again).
func refused(err error) error {
	if err == nil || unreachable(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &moveFailure{err.Error()}
}

// ---- step 1: stop

// source finds the torrent file or magnet link to give the new client: what
// was kept at grab time, or what the old client can hand back. What it had to
// ask for is kept, encrypted, like the rest.
func (s *Service) source(ctx context.Context, e *moveEnv, d *Download) (Source, error) {
	if src, ok, err := s.storedSource(ctx, d.ID); err != nil || ok {
		return src, err
	}
	src, err := e.old.Source(ctx, d.Category, d.Hash)
	if err != nil {
		var ns *NoSourceError
		if errors.As(err, &ns) {
			return Source{}, failf("%s", ns.Reason)
		}
		return Source{}, err
	}
	if err := s.storeSource(ctx, d.ID, src); err != nil {
		s.logger.Warn("couldn't keep a torrent for the move", "release", d.ReleaseTitle, "error", err)
	}
	return src, nil
}

// mvStop is step 1.
func (s *Service) mvStop(ctx context.Context, e *moveEnv, d *Download) error {
	t, found, err := torrentIn(ctx, e.old, d.Category, d.Hash)
	if err != nil {
		return err
	}
	if !found {
		return s.mvGone(ctx, e, d)
	}
	if t.State == torrent.MissingFiles || t.State == torrent.Error {
		why := ""
		if t.Message != "" {
			why = " (" + strings.TrimRight(t.Message, ". ") + ")"
		}
		return failf("%s reports an error or missing files for it%s, so there's nothing safe to check", sentenceName(e.oldName), why)
	}
	// Know how to give the new client the torrent before disturbing the old.
	if _, err := s.source(ctx, e, d); err != nil {
		return err
	}
	wasPaused := t.Stopped()
	if !wasPaused {
		if err := e.old.Stop(ctx, d.Category, []string{d.Hash}); err != nil {
			return err
		}
	}
	// A torrent that was stopped only now may still be writing pieces.
	t, stopped, err := s.waitStopped(ctx, e.old, d)
	if err != nil {
		return err
	}
	if !stopped {
		// Stop was asked for, so a failure has to undo it.
		d.MoveState, d.Message = moveRollback, fmt.Sprintf("%s didn't stop it in time", sentenceName(e.oldName))
		return s.saveMove(ctx, d, "move_state", "message")
	}
	// Only now is what it has verified, and what it has seeded, settled.
	d.MoveWasPaused = wasPaused
	d.MoveOldBytes = t.Verified
	d.SeedSecondsOffset += int64(t.SeedingTime / time.Second)
	d.UploadedOffset += t.Uploaded
	d.MoveState = moveStopped
	return s.saveMove(ctx, d, "move_was_paused", "move_old_bytes", "seed_seconds_offset", "uploaded_offset", "move_state")
}

// mvGone handles a torrent that is no longer in the old client: someone
// removed it there, so there is nothing to move.
func (s *Service) mvGone(ctx context.Context, e *moveEnv, d *Download) error {
	d.MoveState, d.MoveTo, d.MoveStartedAt = "", "", nil
	if err := s.saveMove(ctx, d, "move_state", "move_to", "move_started_at"); err != nil {
		return err
	}
	s.finish(ctx, d, StatusRemoved, "It was removed from "+e.oldName+" outside Reelkeeper.", grab.EventRemoved)
	return nil
}

// ---- step 2: find the files

// locate finds where a torrent's files are, from the old client's list and the
// folders it may have put them in.
func (s *Service) locate(lay Layout, t Torrent, oldBytes int64) (moveLayout, error) {
	ml := moveLayout{Final: lay.Folder, Look: lay.Look, Done: t.Done(), Size: t.Size, NoDetails: t.State == torrent.Metadata || len(lay.Files) == 0}
	found := map[string][]movedFile{} // by folder
	var missing []string
	for _, f := range lay.Files {
		var hits []string // "folder|name as found"
		for _, dir := range lay.Look {
			var here []string
			for _, suffix := range unfinishedSuffixes {
				if s.exists(dir + "/" + f.Name + suffix) {
					here = append(here, f.Name+suffix)
				}
			}
			if len(here) > 1 {
				return ml, failf("both %s and %s are in %s, and Reelkeeper won't choose between them", here[0], here[1], dir)
			}
			if len(here) == 1 {
				hits = append(hits, dir+"|"+here[0])
			}
		}
		switch {
		case len(hits) == 0:
			missing = append(missing, f.Name)
		case len(hits) > 1:
			return ml, failf("%s is in more than one folder (%s), and Reelkeeper can't tell which is right", f.Name, strings.ReplaceAll(strings.Join(hits, ", "), "|", "/"))
		default:
			dir, was, _ := strings.Cut(hits[0], "|")
			found[dir] = append(found[dir], movedFile{Name: f.Name, Was: was})
		}
	}
	if len(found) > 1 {
		dirs := make([]string, 0, len(found))
		for dir := range found {
			dirs = append(dirs, dir)
		}
		slices.Sort(dirs)
		return ml, failf("its files are in more than one folder (%s)", strings.Join(dirs, ", "))
	}
	for dir, files := range found {
		ml.Dir = dir
		ml.Files = files
	}
	switch {
	case len(found) == 0 && !ml.NoDetails && (oldBytes > 0 || t.Done()):
		return ml, failf("Reelkeeper can't find its files (looked in %s); the new client could only check what it can see at the same paths", strings.Join(lay.Look, ", "))
	case len(found) == 0:
		ml.Dir = lay.Folder // nothing downloaded yet
	case len(missing) > 0 && t.Done():
		return ml, failf("it is finished, but %s is missing from %s", missing[0], ml.Dir)
	}
	if ml.Final == "" {
		ml.Final = ml.Dir
	}
	return ml, nil
}

// mvPlan finds the files and records where they are (step 2).
func (s *Service) mvPlan(ctx context.Context, e *moveEnv, d *Download) error {
	t, found, err := torrentIn(ctx, e.old, d.Category, d.Hash)
	if err != nil {
		return err
	}
	if !found {
		return s.mvGone(ctx, e, d)
	}
	lay, err := e.old.Layout(ctx, d.Category, d.Hash)
	if err != nil {
		return err
	}
	ml, err := s.locate(lay, t, d.MoveOldBytes)
	if err != nil {
		return err
	}
	b, err := json.Marshal(ml)
	if err != nil {
		return err
	}
	d.MoveLayout, d.MoveState = string(b), moveRenaming
	return s.saveMove(ctx, d, "move_layout", "move_state")
}

// mvRename gives unfinished files their plain names, in the folder they are
// in (step 2, continued). It can be done again: a file already renamed is
// left alone.
func (s *Service) mvRename(ctx context.Context, _ *moveEnv, d *Download) error {
	ml, err := d.layout()
	if err != nil {
		return failf("its recorded file names can't be read: %v", err)
	}
	for _, f := range ml.Files {
		if f.Was == "" || f.Was == f.Name {
			continue
		}
		from, to := ml.Dir+"/"+f.Was, ml.Dir+"/"+f.Name
		if !s.exists(from) {
			if s.exists(to) {
				continue // done before a restart
			}
			return failf("%s has gone from %s", f.Was, ml.Dir)
		}
		if s.exists(to) {
			return failf("both %s and %s are in %s, and Reelkeeper won't choose between them", f.Was, f.Name, ml.Dir)
		}
		if err := s.rename(from, to); err != nil {
			return failf("couldn't rename %s to %s: %v", from, f.Name, err)
		}
	}
	d.MoveState = moveRenamed
	return s.saveMove(ctx, d, "move_state")
}

// ---- step 3: add to the new client, stopped

// tagsFor are the labels the torrent carries in the old client, or the usual
// ones when they can't be read, and "requested" when Reelkeeper's record says
// it was.
func tagsFor(t Torrent, d *Download) []string {
	tags := t.Tags
	if len(tags) == 0 {
		tags = []string{"reelkeeper"}
		if d.Indexer != "" {
			tags = append(tags, d.Indexer)
		}
	}
	// Deluge has no tags to carry "requested" (Reelkeeper's own record does),
	// so a torrent coming from it gets the tag from there.
	if d.Requested && !slices.Contains(tags, "requested") {
		tags = append(slices.Clone(tags), "requested")
	}
	return tags
}

func (s *Service) mvAdd(ctx context.Context, e *moveEnv, d *Download) error {
	ml, err := d.layout()
	if err != nil {
		return failf("its recorded file names can't be read: %v", err)
	}
	src, err := s.source(ctx, e, d)
	if err != nil {
		return err
	}
	ot, found, err := torrentIn(ctx, e.old, d.Category, d.Hash)
	if err != nil {
		return err
	}
	if !found {
		return failf("%s no longer lists it", sentenceName(e.oldName))
	}
	if err := e.new.EnsureCategory(ctx, d.Category, ""); err != nil {
		return refused(err)
	}
	hash, err := e.new.Add(ctx, Add{
		Magnet: src.Magnet, Torrent: src.Torrent, Category: d.Category, Tags: tagsFor(ot, d),
		Stopped: true, SavePath: ml.Final, FilesIn: ml.Dir,
	})
	if err != nil {
		return refused(err)
	}
	if hash != d.Hash {
		return failf("%s took it as a different torrent (%s)", sentenceName(e.newName), hash)
	}
	// Stopped means stopped: a new client may first check what it was given.
	if _, stopped, err := s.waitStopped(ctx, e.new, d); err != nil {
		return err
	} else if !stopped {
		return failf("%s didn't settle after adding it", sentenceName(e.newName))
	}
	// A magnet link has no details until the new client has fetched them,
	// and it can't check files before it has them.
	if src.Torrent == nil && !ml.NoDetails {
		if err := s.fetchDetails(ctx, e, d); err != nil {
			return err
		}
	}
	d.MoveState = moveAdded
	return s.saveMove(ctx, d, "move_state")
}

// fetchDetails lets a torrent added as a magnet link get its details, and
// stops it again, so it can be checked.
func (s *Service) fetchDetails(ctx context.Context, e *moveEnv, d *Download) error {
	if err := e.new.Start(ctx, d.Category, []string{d.Hash}); err != nil {
		return refused(err)
	}
	if _, ok, err := s.waitFor(ctx, e.new, d, s.moveTiming.metadata, func(t Torrent) bool {
		return t.State != torrent.Metadata && t.Size > 0
	}); err != nil {
		return err
	} else if !ok {
		_ = e.new.Stop(ctx, d.Category, []string{d.Hash})
		return failf("%s couldn't get the torrent's details from the magnet link in %s", sentenceName(e.newName), s.moveTiming.metadata)
	}
	if err := e.new.Stop(ctx, d.Category, []string{d.Hash}); err != nil {
		return refused(err)
	}
	if _, ok, err := s.waitStopped(ctx, e.new, d); err != nil {
		return err
	} else if !ok {
		return failf("%s didn't stop the torrent after getting its details", sentenceName(e.newName))
	}
	return nil
}

// ---- step 4 and 5: check, then judge

// enough says whether a check verified what the old client had.
func (s *Service) enough(ctx context.Context, d *Download, ml moveLayout, t Torrent) (bool, string) {
	if ml.Done {
		if t.Left == 0 && t.Size > 0 && t.Verified >= t.Size {
			return true, ""
		}
		return false, fmt.Sprintf("it was finished, but the check found %s of %s", bytesText(t.Verified), bytesText(t.Size))
	}
	if t.Verified >= d.MoveOldBytes {
		return true, ""
	}
	return false, fmt.Sprintf("the old client had %s, but the check found %s", bytesText(d.MoveOldBytes), bytesText(t.Verified))
}

func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d bytes", n)
}

// mvCheck is step 4 and the first half of 5.
func (s *Service) mvCheck(ctx context.Context, e *moveEnv, d *Download) error {
	ml, err := d.layout()
	if err != nil {
		return failf("its recorded file names can't be read: %v", err)
	}
	if ml.NoDetails {
		// Nothing was downloaded yet, so there is nothing to check.
		d.MoveState = moveFinishing
		return s.saveMove(ctx, d, "move_state")
	}
	d.MoveState = moveChecking
	if err := s.saveMove(ctx, d, "move_state"); err != nil {
		return err
	}
	t, err := s.check(ctx, e.new, d)
	if err != nil {
		return refused(err)
	}
	if ok, why := s.enough(ctx, d, ml, t); !ok {
		return failf("%s checked the files where they are and %s", sentenceName(e.newName), why)
	}
	// Unfinished files may sit in a temporary folder: they belong in the
	// final one, which Transmission and Deluge have to be told (qBittorrent isn't).
	if ml.Final != "" && ml.Final != ml.Dir {
		if err := e.new.SetFolder(ctx, d.Category, d.Hash, ml.Final); err != nil {
			return refused(err)
		}
		if t, err = s.afterFolderMove(ctx, e, d, ml); err != nil {
			return err
		}
		if ok, why := s.enough(ctx, d, ml, t); !ok {
			return failf("after %s moved the files to %s, its check %s", e.newName, ml.Final, why)
		}
	}
	d.MoveState = moveFinishing
	return s.saveMove(ctx, d, "move_state")
}

// afterFolderMove waits for a client that moves a torrent's files to its
// final folder to be done, then checks them there.
func (s *Service) afterFolderMove(ctx context.Context, e *moveEnv, d *Download, ml moveLayout) (Torrent, error) {
	// The client moves the files in the background. Wait for the first file
	// that was in Dir to show up in Final (some clients don't move them, and
	// then the check below tells).
	if len(ml.Files) > 0 {
		first := ml.Files[0]
		deadline := time.Now().Add(s.moveTiming.files)
		for time.Now().Before(deadline) {
			arrived := false
			for _, suffix := range unfinishedSuffixes {
				arrived = arrived || s.exists(ml.Final+"/"+first.Name+suffix)
			}
			if arrived {
				break
			}
			if err := sleep(ctx, s.moveTiming.poll); err != nil {
				return Torrent{}, err
			}
		}
	}
	t, err := s.check(ctx, e.new, d)
	return t, refused(err)
}

// ---- step 5: hand over

func (s *Service) mvFinish(ctx context.Context, e *moveEnv, d *Download) error {
	ot, oldHas, err := torrentIn(ctx, e.old, d.Category, d.Hash)
	if err != nil {
		return err
	}
	hash := []string{d.Hash}
	// Reelkeeper's cleanup decides when its torrents stop seeding. This
	// starts the torrent too.
	if err := e.new.NoShareLimits(ctx, d.Category, hash); err != nil {
		return refused(err)
	}
	if d.MoveWasPaused {
		if err := e.new.Stop(ctx, d.Category, hash); err != nil {
			return refused(err)
		}
	} else if d.Requested || (oldHas && slices.Contains(ot.Tags, "requested")) {
		// Something somebody asked for (Seerr) goes to the front of the queue.
		// Reelkeeper's own record says so; the old client's tag covers torrents
		// grabbed before Reelkeeper noted it (qBittorrent, Transmission).
		if err := e.new.TopPriority(ctx, d.Category, hash); err != nil {
			s.logger.Warn("couldn't move a requested download to the top of the new download client's queue", "client", e.newName, "error", err)
		}
	}
	// Out of the old client, files and all left alone.
	if oldHas {
		if err := e.old.Remove(ctx, d.Category, hash, false); err != nil {
			return err
		}
	}
	now := s.now().UTC()
	msg := fmt.Sprintf("Moved from %s to %s: its files were checked in place, nothing was downloaded again and nothing was deleted.", e.oldName, e.newName)
	d.Client, d.MoveState, d.MoveTo, d.MoveLayout, d.MoveOldBytes, d.MoveWasPaused, d.MoveStartedAt = e.newType, "", "", "", 0, false, nil
	d.MovedAt, d.LimitsSet, d.CleanupTrack = &now, true, "{}"
	if err := s.saveMove(ctx, d, "client", "move_state", "move_to", "move_layout", "move_started_at", "move_old_bytes", "move_was_paused", "moved_at", "limits_set", "cleanup_track"); err != nil {
		return err
	}
	s.record(ctx, d, grab.EventMoved, msg)
	s.logger.Info("moved a torrent to another download client", "release", d.ReleaseTitle, "from", e.oldName, "to", e.newName)
	e.moved++
	return nil
}

// ---- failure: put everything back

// mvRollback undoes a move that can't go ahead (see the top of the file).
func (s *Service) mvRollback(ctx context.Context, e *moveEnv, d *Download) error {
	touched := d.MoveState == moveRollback
	reason := d.Message
	ml, err := d.layout()
	if err != nil {
		return err
	}
	// The new client lets go of it, files and all left alone.
	if touched {
		if _, found, err := torrentIn(ctx, e.new, d.Category, d.Hash); err != nil {
			return err
		} else if found {
			if err := e.new.Remove(ctx, d.Category, []string{d.Hash}, false); err != nil {
				return err
			}
		}
		if err := s.restoreNames(ml); err != nil {
			return err
		}
	}
	// The old client takes it back.
	note := ""
	ot, found, err := torrentIn(ctx, e.old, d.Category, d.Hash)
	if err != nil {
		return err
	}
	seeding := d.Status == StatusImported
	if found {
		if !ot.Stopped() && (touched || !seeding) {
			if err := e.old.Stop(ctx, d.Category, []string{d.Hash}); err != nil {
				return err
			}
			if _, _, err := s.waitStopped(ctx, e.old, d); err != nil {
				return err
			}
		}
		if d.MoveLayout != "" {
			// The files were renamed or looked at by another client: have
			// the old client verify what it has again.
			t, err := s.check(ctx, e.old, d)
			if err != nil {
				return err
			}
			note = fmt.Sprintf(" %s checked its files again and has %s of %s.", sentenceName(e.oldName), bytesText(t.Verified), bytesText(t.Size))
			if t.Verified < d.MoveOldBytes {
				note += fmt.Sprintf(" That is less than the %s it had before; look at the files in %s.", bytesText(d.MoveOldBytes), ml.Dir)
			}
		}
		// A download in the Queue stays stopped, so it doesn't keep
		// downloading behind Reelkeeper's back; a seeding one carries on.
		if seeding && touched && !d.MoveWasPaused {
			if err := e.old.Start(ctx, d.Category, []string{d.Hash}); err != nil {
				return err
			}
		}
	}
	d.MoveState, d.MoveTo, d.MoveLayout, d.MoveStartedAt = "", "", "", nil
	if err := s.saveMove(ctx, d, "move_state", "move_to", "move_layout", "move_started_at"); err != nil {
		return err
	}
	var msg string
	status := StatusRemoved
	if seeding {
		status = StatusSeeded
		msg = fmt.Sprintf("Couldn't move to %s: %s. It stays in %s, which Reelkeeper no longer tracks, and nothing was deleted.%s", e.newName, reason, e.oldName, note)
	} else {
		msg = fmt.Sprintf("Couldn't move to %s: %s. It is stopped in %s, which Reelkeeper no longer talks to, and nothing was deleted. Reelkeeper will search for it again and grab it in %s.%s",
			e.newName, reason, e.oldName, e.newName, note)
	}
	s.finish(ctx, d, status, msg, grab.EventMoveFailed)
	e.failed++
	return nil
}

// mvGiveUp ends a move whose undoing can't be finished an hour after it began
// (the old client is gone for good, a file can't be found to put back). It
// touches nothing more and deletes nothing: the download is finished like a
// rolled-back one, and History says where things were left so the files can
// be put right by hand.
func (s *Service) mvGiveUp(ctx context.Context, e *moveEnv, d *Download, last error) error {
	ml, _ := d.layout() // unreadable: say less, not nothing
	early := d.MoveState == moveRollbackEarly
	reason := d.Message
	d.MoveState, d.MoveTo, d.MoveLayout, d.MoveStartedAt = "", "", "", nil
	if err := s.saveMove(ctx, d, "move_state", "move_to", "move_layout", "move_started_at"); err != nil {
		return err
	}
	seeding := d.Status == StatusImported
	where := fmt.Sprintf("It may still be in %s (stopped, or running if it wouldn't stop) and in %s (added stopped).", e.oldName, e.newName)
	if early {
		where = fmt.Sprintf("Reelkeeper hadn't changed anything: it may still be in %s, running as before.", e.oldName)
	}
	var files []string
	for _, f := range ml.Files {
		if f.Was != "" {
			files = append(files, f.Was)
		}
	}
	if len(files) > 5 {
		files = append(files[:5], fmt.Sprintf("and %d more", len(files)-5))
	}
	var folder string
	switch {
	case ml.Dir != "" && len(files) > 0:
		folder = fmt.Sprintf(" Its files should be in %s, named %s.", ml.Dir, strings.Join(files, ", "))
	case ml.Dir != "":
		folder = fmt.Sprintf(" Its files should be in %s.", ml.Dir)
	}
	next := fmt.Sprintf(" Reelkeeper will search for it again; remove any stopped copy from %s first.", e.newName)
	if seeding {
		next = " Reelkeeper no longer tracks it."
	}
	msg := fmt.Sprintf("Couldn't move to %s: %s. Undoing it didn't work either (%v), so Reelkeeper gave up after %s. %s%s Nothing was deleted.%s",
		e.newName, reason, last, hoursText(s.moveTiming.giveUp), where, folder, next)
	status := StatusRemoved
	if seeding {
		status = StatusSeeded
	}
	s.finish(ctx, d, status, msg, grab.EventMoveFailed)
	s.logger.Warn("gave up moving a torrent; left as it was", "release", d.ReleaseTitle, "error", last)
	e.failed++
	return nil
}

// restoreNames gives every file back the name and folder it had before the
// move. A file may have been renamed by Reelkeeper, renamed (to ".part") by
// the new client while it checked, or moved by it to the final folder.
func (s *Service) restoreNames(ml moveLayout) error {
	places := []string{ml.Dir, ml.Final}
	places = append(places, ml.Look...)
	for _, f := range ml.Files {
		if f.Was == "" {
			continue
		}
		target := ml.Dir + "/" + f.Was
		if s.exists(target) {
			continue
		}
		moved := false
		for _, dir := range places {
			for _, suffix := range unfinishedSuffixes {
				from := dir + "/" + f.Name + suffix
				if dir == "" || from == target || !s.exists(from) {
					continue
				}
				if err := s.rename(from, target); err != nil {
					return fmt.Errorf("couldn't put %s back as %s: %w", from, target, err)
				}
				moved = true
				break
			}
			if moved {
				break
			}
		}
		if !moved {
			return fmt.Errorf("couldn't find %s to put it back as %s", f.Name, target)
		}
	}
	return nil
}

// ---- who may touch a moving download

// errMoving is what the Queue's buttons answer for a download on its way to
// another client.
func movingError(d *Download) error {
	if d.MoveState == "" {
		return nil
	}
	return fmt.Errorf("this download is moving to %s; wait until it's done (System → Tasks)", settings.ClientDisplayName(d.MoveTo))
}
