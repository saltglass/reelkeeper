package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Moving a movie version to another media folder ("Move…" on the movie's
// page; Dan, 2026-11-22: a movie downloaded into the kids' folder that
// isn't for kids). The safety rules:
//
//   - Both media folders must have "Reelkeeper manages files here" on.
//   - The plan is shown first, and checked again when the move runs.
//   - Nothing is ever overwritten: a name already taken at the destination
//     stops the move before anything changes, and every rename refuses to
//     replace a file (renameNoReplace).
//   - The folder keeps its name; only the media folder around it changes.
//     If the version has the folder to itself, everything in it moves
//     (subtitles, artwork, extras); if it shares it with another version,
//     only its videos and the files named after them do.
//   - On the same disk the files are renamed into place. Across disks they
//     are copied into a hidden folder at the destination, each copy's size
//     checked, then renamed into place; the old copies are deleted only
//     after the library records the new place, and only when the new copy
//     is there with the same size (Dan chose deleting over keeping them in
//     .reelkeeper-replaced: they are the same file).
//   - If anything fails before the library records the move, everything
//     goes back as it was. The task can be retried after a restart: each
//     step looks at what is already done.
//   - While a version moves, imports into it wait, and renames, deletes
//     and scans leave its folder alone (Moving).

// MoveKind is the move task (stored in the jobs table; never rename).
const MoveKind = "library.move"

// movingDir is the hidden folder, inside the destination media folder,
// where copies wait until they are complete.
const movingDir = ".reelkeeper-moving"

// ErrMoving means the movie is being moved to another media folder.
var ErrMoving = errors.New("this movie is being moved to another media folder; try again once that's done")

// MoveArgs are the move task's arguments.
type MoveArgs struct {
	VersionID  int64 `json:"versionId"`
	FromRootID int64 `json:"fromRootId"`
	ToRootID   int64 `json:"toRootId"`
}

// MovePlan is what moving a version would do.
type MovePlan struct {
	VersionID int64
	TitleID   int64
	From, To  RootFolder
	// Folder is the version's folder, the same in both media folders; ""
	// for a movie file loose in the media folder.
	Folder string
	// WholeFolder: the version has the folder to itself, so everything in
	// it moves. Otherwise only its videos and their subtitles do.
	WholeFolder bool
	Files       []MoveFile // what moves, relative to the media folder
	Bytes       int64
	// Copy: the folders are on different disks, so the files are copied
	// and the old copies deleted. False: they are renamed into place.
	Copy      bool
	FreeSpace int64 // at the destination; -1 when it can't be told
	// Problems are why the move can't be done; empty when it can.
	Problems []string
}

// MoveFile is one file a move takes along.
type MoveFile struct {
	Path string // relative to the media folder
	Size int64
	// placed: already at the destination (a retry after a restart).
	placed bool
}

// MoveResult is what a move did.
type MoveResult struct {
	TitleID int64
	Title   string
	// FromDir and ToDir are the movie's folder before and after, in full.
	FromDir, ToDir string
	Moved          int
	// Left are files that stayed behind in the old folder, with why.
	Left    []string
	Message string
}

// movingVersion is a version whose move is running.
type movingVersion struct {
	from, to int64
	// paths are what Scan leaves alone in both media folders: the
	// version's folder, or its loose files.
	paths []string
}

// Moving reports whether a version is being moved.
func (s *Service) Moving(versionID int64) bool {
	s.movingMu.Lock()
	defer s.movingMu.Unlock()
	_, ok := s.moving[versionID]
	return ok
}

// movingPaths lists the paths (relative to the media folder) of moves
// running into or out of a media folder.
func (s *Service) movingPaths(rootID int64) []string {
	s.movingMu.Lock()
	defer s.movingMu.Unlock()
	var out []string
	for _, m := range s.moving {
		if m.from == rootID || m.to == rootID {
			out = append(out, m.paths...)
		}
	}
	return out
}

// startMoving marks a version as moving; false if it already is.
func (s *Service) startMoving(versionID int64, m movingVersion) bool {
	s.movingMu.Lock()
	defer s.movingMu.Unlock()
	if _, ok := s.moving[versionID]; ok {
		return false
	}
	s.moving[versionID] = m
	return true
}

func (s *Service) stopMoving(versionID int64) {
	s.movingMu.Lock()
	defer s.movingMu.Unlock()
	delete(s.moving, versionID)
}

// OnMoved registers fn to be called after a move finishes (to tell the
// media servers). Only one listener.
func (s *Service) OnMoved(fn func(context.Context, MoveResult)) { s.onMoved = fn }

// under reports whether p is dir or inside it (relative paths; dir "" is
// never matched, as it would be the whole media folder).
func under(p, dir string) bool {
	return dir != "" && (p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)))
}

// PlanMove shows what moving a movie version to another media folder would
// do, without doing it.
func (s *Service) PlanMove(ctx context.Context, titleID, versionID, toRootID int64) (*MovePlan, error) {
	v, from, to, err := s.moveParts(ctx, titleID, versionID, toRootID)
	if err != nil {
		return nil, err
	}
	p, err := s.planMove(ctx, v, from, to, false)
	if err != nil {
		return nil, err
	}
	if s.Moving(v.ID) {
		p.Problems = append([]string{"This movie is already being moved; see System → Tasks."}, p.Problems...)
	}
	return p, nil
}

// QueueMove checks the move and queues it as a background task.
func (s *Service) QueueMove(ctx context.Context, m *jobs.Manager, titleID, versionID, toRootID int64) (*jobs.Job, error) {
	p, err := s.PlanMove(ctx, titleID, versionID, toRootID)
	if err != nil {
		return nil, err
	}
	if len(p.Problems) > 0 {
		return nil, &MoveRefused{Problems: p.Problems}
	}
	j, _, err := m.Enqueue(ctx, MoveKind, MoveArgs{VersionID: versionID, FromRootID: p.From.ID, ToRootID: toRootID},
		jobs.Manual, fmt.Sprintf("%s:%d", MoveKind, versionID))
	return j, err
}

// moveProblem is a reason, in plain words, that a move can't be done.
type moveProblem string

func (p moveProblem) Error() string { return string(p) }

// MoveRefused is a move that can't be done, with why.
type MoveRefused struct{ Problems []string }

func (e *MoveRefused) Error() string { return strings.Join(e.Problems, " ") }

// moveParts loads a movie version and the two media folders.
func (s *Service) moveParts(ctx context.Context, titleID, versionID, toRootID int64) (*Version, *RootFolder, *RootFolder, error) {
	v := new(Version)
	err := s.db.DB.NewSelect().Model(v).
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.id = ? AND version.title_id = ? AND t.media_type = ?", versionID, titleID, Movie).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, nil, err
	}
	from, err := s.RootFolder(ctx, v.RootFolderID)
	if err != nil {
		return nil, nil, nil, err
	}
	to, err := s.RootFolder(ctx, toRootID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, nil, validation.Errors{"rootFolderId": "That media folder doesn't exist (any more)."}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return v, from, to, nil
}

// planMove works out a move. With resume (a retry after a restart), a file
// already at the destination with the source's size and time counts as
// placed by the earlier attempt, not as someone else's.
func (s *Service) planMove(ctx context.Context, v *Version, from, to *RootFolder, resume bool) (*MovePlan, error) {
	p := &MovePlan{VersionID: v.ID, TitleID: v.TitleID, From: *from, To: *to, Folder: v.Folder, FreeSpace: -1}
	switch {
	case to.ID == from.ID:
		p.Problems = append(p.Problems, "It's already in "+to.Path+".")
		return p, nil
	case to.MediaType != Movie:
		p.Problems = append(p.Problems, to.Path+" is a TV media folder; movies can only move to a movie folder.")
		return p, nil
	}
	if !from.ManageFiles {
		p.Problems = append(p.Problems, "Reelkeeper only reads "+from.Path+", so it can't move files out of it. "+
			`Turn on "Reelkeeper manages files here" for it in Settings → Media Folders first.`)
	}
	if !to.ManageFiles {
		p.Problems = append(p.Problems, "Reelkeeper only reads "+to.Path+", so it can't put files there. "+
			`Turn on "Reelkeeper manages files here" for it in Settings → Media Folders first.`)
	}

	files, owned, err := s.moveSources(ctx, v, from)
	var problem moveProblem
	if errors.As(err, &problem) {
		p.Problems = append(p.Problems, string(problem))
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	p.WholeFolder = owned
	var problems []string
	for _, f := range files {
		dst := filepath.Join(to.Path, f.Path)
		dstInfo, err := os.Lstat(dst)
		if err != nil {
			p.Files = append(p.Files, f)
			p.Bytes += f.Size
			continue
		}
		if resume {
			if srcInfo, err := os.Lstat(filepath.Join(from.Path, f.Path)); err == nil && dstInfo.Mode().IsRegular() &&
				dstInfo.Size() == srcInfo.Size() && dstInfo.ModTime().Equal(srcInfo.ModTime()) {
				f.placed = true
				p.Files = append(p.Files, f)
				continue
			}
		}
		problems = append(problems, f.Path)
	}
	if len(problems) > 0 {
		p.Problems = append(p.Problems, fmt.Sprintf("%s already in %s, and Reelkeeper never overwrites files: %s.",
			oneOrMany(len(problems), "A file with the same name is", "Files with the same names are"), to.Path, listSome(problems, 3)))
	}
	// Another movie's file in the destination folder would end up side by
	// side with this one, and the media server would show two copies (the
	// same rule as imports). This movie's other versions are fine there.
	if v.Folder != "" {
		mine, err := s.titlePaths(ctx, v.TitleID, to.ID)
		if err != nil {
			return nil, err
		}
		var strays []string
		for _, f := range videosUnder(to.Path, v.Folder, 0) {
			if !mine[f.Path] && !slices.ContainsFunc(p.Files, func(m MoveFile) bool { return m.Path == f.Path && m.placed }) {
				strays = append(strays, filepath.Base(f.Path))
			}
		}
		if len(strays) > 0 {
			p.Problems = append(p.Problems, fmt.Sprintf("%s already has a folder %s holding %s Reelkeeper doesn't count as this movie's (%s). "+
				`Run "Scan media folders" (System → Tasks), or move or remove it, first.`,
				to.Path, v.Folder, oneOrMany(len(strays), "a video", "videos"), listSome(strays, 3)))
		}
	}
	p.Copy = !s.onSameDisk(from.Path, to.Path)
	if p.Copy {
		p.FreeSpace = FreeSpace(to.Path)
		if p.FreeSpace >= 0 && p.FreeSpace < p.Bytes {
			p.Problems = append(p.Problems, fmt.Sprintf("%s has %s free, and the movie needs %s.", to.Path, sizeText(p.FreeSpace), sizeText(p.Bytes)))
		}
	}
	return p, nil
}

// moveSources lists the files moving a version takes along, from the old
// media folder: everything in its folder when it has the folder to itself
// (owned), else its present videos and the files named after them.
func (s *Service) moveSources(ctx context.Context, v *Version, from *RootFolder) (files []MoveFile, owned bool, err error) {
	owned, err = s.ownsFolder(ctx, v, from.ID)
	if err != nil {
		return nil, false, err
	}
	if owned {
		dir := filepath.Join(from.Path, v.Folder)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil // no folder on disk yet: nothing to move
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".reelkeeper-") {
				return nil // an import's or rename's temporary file
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(from.Path, path)
			if !info.Mode().IsRegular() {
				return moveProblem(rel + " is a link or a special file, which Reelkeeper doesn't move; move or remove it first.")
			}
			files = append(files, MoveFile{Path: rel, Size: info.Size()})
			return nil
		})
		return files, true, err
	}
	var mine []MediaFile
	// Paths are relative to the media folder, so they are the same before
	// and after the move is recorded.
	if err := s.db.DB.NewSelect().Model(&mine).Where("version_id = ?", v.ID).Order("path").Scan(ctx); err != nil {
		return nil, false, err
	}
	for _, f := range mine {
		for _, rel := range append([]string{f.Path}, sidecars(from.Path, f.Path)...) {
			if slices.ContainsFunc(files, func(m MoveFile) bool { return m.Path == rel }) {
				continue
			}
			info, err := os.Lstat(filepath.Join(from.Path, rel))
			if err != nil || !info.Mode().IsRegular() {
				continue // missing, or already moved by an earlier attempt
			}
			files = append(files, MoveFile{Path: rel, Size: info.Size()})
		}
	}
	return files, false, nil
}

// ownsFolder reports whether a version has its folder to itself in a media
// folder: no other version uses it, and no other version's file is in it.
func (s *Service) ownsFolder(ctx context.Context, v *Version, rootID int64) (bool, error) {
	if v.Folder == "" {
		return false, nil // a loose file: the "folder" is the media folder
	}
	var folders []string
	if err := s.db.DB.NewSelect().Model((*Version)(nil)).Column("folder").
		Where("root_folder_id = ? AND id <> ?", rootID, v.ID).Scan(ctx, &folders); err != nil {
		return false, err
	}
	for _, f := range folders {
		if f == v.Folder || under(f, v.Folder) || under(v.Folder, f) {
			return false, nil
		}
	}
	var paths []string
	if err := s.db.DB.NewSelect().Model((*MediaFile)(nil)).Column("path").
		Where("root_folder_id = ? AND version_id <> ?", rootID, v.ID).Scan(ctx, &paths); err != nil {
		return false, err
	}
	return !slices.ContainsFunc(paths, func(p string) bool { return under(p, v.Folder) }), nil
}

// titlePaths lists the paths of a title's files in a media folder.
func (s *Service) titlePaths(ctx context.Context, titleID, rootID int64) (map[string]bool, error) {
	var paths []string
	err := s.db.DB.NewSelect().Model((*MediaFile)(nil)).Column("media_file.path").
		Join("JOIN versions v ON v.id = media_file.version_id").
		Where("v.title_id = ? AND media_file.root_folder_id = ?", titleID, rootID).Scan(ctx, &paths)
	out := map[string]bool{}
	for _, p := range paths {
		out[p] = true
	}
	return out, err
}

// MoveVersion does a move (the move task). progress gets the percentage
// done and what is happening.
func (s *Service) MoveVersion(ctx context.Context, a MoveArgs, progress Progress) (MoveResult, error) {
	var res MoveResult
	if progress == nil {
		progress = func(int, string) {}
	}
	v := new(Version)
	if err := s.db.DB.NewSelect().Model(v).Where("id = ?", a.VersionID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, errors.New("the movie was removed from Reelkeeper, so nothing was moved")
		}
		return res, err
	}
	t := new(Title)
	if err := s.db.DB.NewSelect().Model(t).Where("id = ?", v.TitleID).Scan(ctx); err != nil {
		return res, err
	}
	from, err := s.RootFolder(ctx, a.FromRootID)
	if err != nil {
		return res, fmt.Errorf("the media folder it was in is gone, so nothing was moved: %w", err)
	}
	to, err := s.RootFolder(ctx, a.ToRootID)
	if err != nil {
		return res, fmt.Errorf("the media folder it was going to is gone, so nothing was moved: %w", err)
	}
	res.TitleID, res.Title = t.ID, fmt.Sprintf("%s (%d)", t.Title, t.Year)
	res.FromDir, res.ToDir = filepath.Join(from.Path, v.Folder), filepath.Join(to.Path, v.Folder)

	busy := movingVersion{from: from.ID, to: to.ID, paths: []string{v.Folder}}
	if v.Folder == "" {
		busy.paths = nil
		var mine []MediaFile
		_ = s.db.DB.NewSelect().Model(&mine).Where("version_id = ?", v.ID).Scan(ctx)
		for _, f := range mine {
			busy.paths = append(busy.paths, f.Path)
		}
	}
	if !s.startMoving(v.ID, busy) {
		return res, errors.New("this movie is already being moved")
	}
	defer s.stopMoving(v.ID)

	switch v.RootFolderID {
	case to.ID:
		// An earlier attempt recorded the move and was cut short while
		// clearing the old folder: finish that.
		progress(90, "Removing the old copy")
		res.Moved, res.Left = s.clearOld(context.WithoutCancel(ctx), v, from, to)
		return s.moved(ctx, res), nil
	case from.ID:
	default:
		return res, errors.New("the movie has been moved to another media folder since, so nothing was moved")
	}

	p, err := s.planMove(ctx, v, from, to, true)
	if err != nil {
		return res, err
	}
	if len(p.Problems) > 0 {
		return res, fmt.Errorf("nothing was moved: %s", strings.Join(p.Problems, " "))
	}

	progress(0, fmt.Sprintf("Moving %s to %s", res.Title, to.Path))
	var undo func()
	if !p.Copy {
		undo, err = s.renameInto(p, from, to)
		if errors.Is(err, syscall.EXDEV) {
			// Different disks after all (two mounts of one disk, as in a
			// container): copy instead. Nothing was moved yet.
			p.Copy = true
			if free := FreeSpace(to.Path); free >= 0 && free < p.Bytes {
				return res, fmt.Errorf("nothing was moved: the folders are on different disks, and %s has %s free while the movie needs %s",
					to.Path, sizeText(free), sizeText(p.Bytes))
			}
		}
	}
	if p.Copy {
		undo, err = s.copyInto(ctx, p, from, to, progress)
	}
	if err != nil {
		return res, err
	}

	// Record the new place. From here on, the move stands.
	progress(90, "Recording the new place")
	rec := context.WithoutCancel(ctx)
	err = s.db.DB.RunInTx(rec, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("root_folder_id = ?", to.ID).Where("id = ?", v.ID).Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*MediaFile)(nil)).Set("root_folder_id = ?", to.ID).Where("version_id = ?", v.ID).Exec(ctx)
		return err
	})
	if err != nil {
		undo()
		return res, fmt.Errorf("couldn't record the new place, so the files were put back: %w", err)
	}
	res.Moved, res.Left = s.clearOld(rec, v, from, to)
	return s.moved(rec, res), nil
}

// moved finishes a move: the log, the message, and the listeners.
func (s *Service) moved(ctx context.Context, res MoveResult) MoveResult {
	res.Message = fmt.Sprintf("Moved %s to %s.", res.Title, res.ToDir)
	if len(res.Left) > 0 {
		res.Message += fmt.Sprintf(" Left in the old folder: %s.", strings.Join(res.Left, "; "))
	}
	s.logger.Info("moved a movie to another media folder", "movie", res.Title, "from", res.FromDir, "to", res.ToDir,
		"files", res.Moved, "left", res.Left, "reason", "move requested")
	s.onChange()
	if s.onMoved != nil {
		s.onMoved(ctx, res)
	}
	return res
}

// renameInto moves the plan's files by renaming them (same disk). The
// returned func puts them back. If one can't be moved, the ones moved so
// far are put back and the error says why.
func (s *Service) renameInto(p *MovePlan, from, to *RootFolder) (undo func(), err error) {
	var done [][2]string
	undo = func() {
		for i := len(done) - 1; i >= 0; i-- {
			if err := s.rename(done[i][1], done[i][0]); err != nil {
				s.logger.Error("couldn't put a moved file back", "file", done[i][1], "wanted", done[i][0], "error", err)
			}
		}
	}
	for _, f := range p.Files {
		if f.placed {
			continue
		}
		src, dst := filepath.Join(from.Path, f.Path), filepath.Join(to.Path, f.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil { //nolint:gosec // group-writable: the library may be shared with a group (for example on a NAS)
			undo()
			return nil, fmt.Errorf("nothing was moved: can't create the folder in %s: %w", to.Path, err)
		}
		if err := s.rename(src, dst); err != nil {
			undo()
			if errors.Is(err, syscall.EXDEV) && len(done) == 0 {
				return nil, err
			}
			if errors.Is(err, fs.ErrExist) {
				return nil, fmt.Errorf("nothing was moved: a file named %s appeared in %s; Reelkeeper never overwrites files", filepath.Base(dst), filepath.Dir(dst))
			}
			return nil, fmt.Errorf("nothing was moved: couldn't move %s: %w", f.Path, err)
		}
		s.logger.Info("moved file", "from", src, "to", dst, "reason", "movie moved to another media folder")
		done = append(done, [2]string{src, dst})
	}
	return undo, nil
}

// copyInto copies the plan's files to the destination (different disks):
// into a hidden folder first, each copy's size checked, then renamed into
// place. The source files are untouched. The returned func removes the
// copies again.
func (s *Service) copyInto(ctx context.Context, p *MovePlan, from, to *RootFolder, progress Progress) (undo func(), err error) {
	stage := filepath.Join(to.Path, movingDir, strconv.FormatInt(p.VersionID, 10))
	// Only ever partial copies of files still in the old folder: an earlier
	// attempt's, cut short.
	if err := os.RemoveAll(stage); err != nil {
		return nil, fmt.Errorf("nothing was moved: can't clear %s: %w", stage, err)
	}
	defer func() {
		_ = os.RemoveAll(stage)
		_ = os.Remove(filepath.Join(to.Path, movingDir)) // only if empty
	}()

	var copied int64
	for _, f := range p.Files {
		if f.placed {
			continue
		}
		tmp := filepath.Join(stage, f.Path)
		if err := os.MkdirAll(filepath.Dir(tmp), 0o775); err != nil { //nolint:gosec // see renameInto
			return nil, fmt.Errorf("nothing was moved: can't write into %s: %w", to.Path, err)
		}
		base := copied
		err := copyExact(ctx, filepath.Join(from.Path, f.Path), tmp, f.Size, func(n int64) {
			if p.Bytes > 0 {
				progress(int((base+n)*85/p.Bytes), fmt.Sprintf("Copying %s (%s of %s)", filepath.Base(f.Path), sizeText(base+n), sizeText(p.Bytes)))
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("nothing was moved: copying %s failed: %w", f.Path, err)
		}
		copied += f.Size
	}

	var placed []string
	undo = func() {
		for _, dst := range placed {
			if err := os.Remove(dst); err != nil {
				s.logger.Error("couldn't remove a copy after the move failed", "file", dst, "error", err)
			}
		}
	}
	for _, f := range p.Files {
		dst := filepath.Join(to.Path, f.Path)
		if f.placed {
			placed = append(placed, dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil { //nolint:gosec // see renameInto
			undo()
			return nil, fmt.Errorf("nothing was moved: can't create the folder in %s: %w", to.Path, err)
		}
		if err := s.rename(filepath.Join(stage, f.Path), dst); err != nil {
			undo()
			if errors.Is(err, fs.ErrExist) {
				return nil, fmt.Errorf("nothing was moved: a file named %s appeared in %s; Reelkeeper never overwrites files", filepath.Base(dst), filepath.Dir(dst))
			}
			return nil, fmt.Errorf("nothing was moved: couldn't put %s in place: %w", f.Path, err)
		}
		placed = append(placed, dst)
	}
	return undo, nil
}

// clearOld deletes what a recorded move left in the old media folder: each
// file whose copy is at the destination with the same size (so on the same
// disk, where the files were renamed, there is nothing to delete), then the
// folders left empty. Files without such a copy stay, and are listed with
// why.
func (s *Service) clearOld(ctx context.Context, v *Version, from, to *RootFolder) (moved int, left []string) {
	files, _, err := s.moveSources(ctx, v, from)
	if err != nil {
		s.logger.Error("couldn't list the old copy after a move", "folder", from.Path, "error", err)
		return 0, []string{"couldn't look through the old folder: " + err.Error()}
	}
	for _, f := range files {
		src, dst := filepath.Join(from.Path, f.Path), filepath.Join(to.Path, f.Path)
		di, err := os.Lstat(dst)
		if err != nil || di.Size() != f.Size {
			left = append(left, f.Path+" (no matching copy in the new folder)")
			continue
		}
		// Never the only copy: if both names are one file in one folder
		// (two media folders that are really the same place), keep it. Two
		// names of one file in different folders (a hard link from an
		// interrupted rename) are fine to tidy.
		if si, err := os.Lstat(src); err == nil && os.SameFile(si, di) && sameDir(filepath.Dir(src), filepath.Dir(dst)) {
			left = append(left, f.Path+" (the old and new folders are the same place)")
			continue
		}
		if err := os.Remove(src); err != nil {
			left = append(left, f.Path+" ("+err.Error()+")")
			continue
		}
		s.logger.Info("deleted the old copy of a moved file", "file", src, "copy", dst, "reason", "movie moved to another media folder")
	}
	if v.Folder != "" {
		removeEmptyDirs(filepath.Join(from.Path, v.Folder))
	}
	n, _ := s.db.DB.NewSelect().Model((*MediaFile)(nil)).Where("version_id = ? AND missing_since IS NULL", v.ID).Count(ctx)
	return int(n), left
}

// sameDir reports whether two paths are the same folder on disk.
func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// removeEmptyDirs removes dir and the folders inside it, deepest first,
// where they are empty. Folders with anything in them stay.
func removeEmptyDirs(dir string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // fails, harmlessly, when not empty
	}
}

// copyExact copies src to a new file dst (never replacing one), keeps the
// source's modification time (so a scan sees the file unchanged), and
// checks the copy has exactly want bytes. seen is told the bytes copied so
// far.
func copyExact(ctx context.Context, src, dst string, want int64, seen func(int64)) error {
	in, err := os.Open(src) //nolint:gosec // a file in a media folder Reelkeeper manages
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664) //nolint:gosec // a new file in Reelkeeper's hidden staging folder; O_EXCL never overwrites
	if err != nil {
		return err
	}
	n, err := io.Copy(out, &countingReader{r: ctxReader{ctx, in}, seen: seen})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("wrote %d bytes of %d", n, want)
	}
	if got, err := os.Stat(dst); err != nil || got.Size() != want {
		return errors.New("the copy's size doesn't match the original")
	}
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}

// countingReader tells seen how many bytes have been read so far.
type countingReader struct {
	r    io.Reader
	n    int64
	seen func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.seen != nil && n > 0 {
		c.seen(c.n)
	}
	return n, err
}

// sizeText is a size for messages ("812 MB", "41.3 GB").
func sizeText(n int64) string {
	if n < 1<<30 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// oneOrMany picks the wording for one thing or several.
func oneOrMany(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// moveTask is the background task that does moves.
func (s *Service) moveTask() jobs.Task {
	return jobs.Task{
		Kind: MoveKind,
		Name: "Move a movie to another media folder",
		Description: `Moves a movie's files to another media folder, started with "Move…" on the movie's page. ` +
			"On the same disk the files are renamed; across disks they are copied, checked, and the old copy deleted. Nothing is ever overwritten.",
		Run: func(ctx context.Context, r *jobs.Run) error {
			var args MoveArgs
			_ = r.Args(&args)
			if args.VersionID == 0 {
				r.Progress(100, `Nothing to move. Start a move with "Move…" on a movie's page.`)
				return nil
			}
			res, err := s.MoveVersion(ctx, args, r.Progress)
			if err != nil {
				return err
			}
			r.Progress(100, res.Message)
			return nil
		},
	}
}
