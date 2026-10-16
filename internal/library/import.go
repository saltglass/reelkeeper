package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// Importing puts a finished download into the library. The rules, from
// CLAUDE.md ("Never lose or corrupt media"):
//
//  1. Copy, never move: the download keeps seeding, and the download disk
//     and the library are different filesystems anyway.
//  2. Copy to a temporary name in the movie's folder, check the size, then
//     rename it into place in one step, so a half-copied file never
//     appears in the library (or in Emby).
//  3. Never overwrite. A file the version had before (an upgrade) is moved
//     aside into ReplacedDir, not deleted; anything else already using the
//     new name stops the import.
//  4. Only media folders Reelkeeper manages are written to.

// ReplacedDir is where files replaced by upgrades go, inside the media
// folder: hidden (a dot folder, which Emby and Jellyfin skip), and on the
// same disk, so moving a file there is instant. Emptying it is up to you,
// for now.
const ReplacedDir = ".reelkeeper-replaced"

// ImportRequest is a finished download to import into a movie version.
type ImportRequest struct {
	VersionID int64
	// Source is the downloaded file, or the download's folder.
	Source string
	// ReleaseName is the release's name, which often says more about its
	// quality than the file name does.
	ReleaseName string
	// ClientName is the download client that reported Source, for the
	// error shown when it isn't there ("" reads as "the download client").
	ClientName string
}

// ImportResult says what the import did.
type ImportResult struct {
	// Path is the new file, relative to its media folder; FullPath is the
	// same with the media folder.
	Path     string
	FullPath string
	// Replaced are the files moved into ReplacedDir, relative to the media
	// folder.
	Replaced []string
}

// ErrNotManaged means the version's media folder is read-only to Reelkeeper.
var ErrNotManaged = errors.New("reelkeeper doesn't manage files in this media folder")

// ImportMovie copies a finished download into a movie version's folder,
// names it with the naming templates, and records it.
func (s *Service) ImportMovie(ctx context.Context, req ImportRequest) (ImportResult, error) {
	var res ImportResult
	v := new(Version)
	if err := s.db.DB.NewSelect().Model(v).Where("id = ?", req.VersionID).Scan(ctx); err != nil {
		return res, fmt.Errorf("the movie version is gone: %w", err)
	}
	t := new(Title)
	if err := s.db.DB.NewSelect().Model(t).Where("id = ?", v.TitleID).Scan(ctx); err != nil {
		return res, err
	}
	root, err := s.RootFolder(ctx, v.RootFolderID)
	if err != nil {
		return res, err
	}
	if !root.ManageFiles {
		return res, fmt.Errorf("%w: %s. Turn on \"Manage files\" for it under Settings → Media Folders to let Reelkeeper import there", ErrNotManaged, root.Path)
	}
	profile := new(Profile)
	_ = s.db.DB.NewSelect().Model(profile).Where("id = ?", v.ProfileID).Scan(ctx)

	src, size, err := mainVideo(req.Source, req.ClientName)
	if err != nil {
		return res, err
	}
	if err := s.discImageAllowed(ctx, v.ProfileID, src); err != nil {
		return res, err
	}

	// What the file is: the release name if it says more, the headers for
	// what they can tell.
	name := req.ReleaseName
	if name == "" || parser.Parse(name).Quality() == "Unknown" {
		name = filepath.Base(src)
	}
	p := probe(filepath.Dir(src), unitFile{Path: filepath.Base(src), Size: size})
	p.release = parser.Parse(name)
	draft := MediaFile{Edition: p.release.Edition, ReleaseGroup: p.release.Group}
	draft.Resolution, draft.Source, draft.Quality = quality(p.release, p.media)
	if b, err := jsonOf(p.release); err == nil {
		draft.ReleaseInfo = b
	}
	if p.media != nil {
		if b, err := jsonOf(p.media); err == nil {
			draft.MediaInfo = b
		}
	}

	folder := v.Folder
	if folder == "" {
		folder = naming.Format(s.naming().MovieFolder, movieFacts(t, profile.Name), naming.File{})
	}
	base := naming.Format(s.naming().MovieFile, movieFacts(t, profile.Name), fileFacts(draft))
	rel := filepath.Join(folder, base+strings.ToLower(filepath.Ext(src)))
	dest := filepath.Join(root.Path, rel)

	var old []MediaFile
	if err := s.db.DB.NewSelect().Model(&old).Where("version_id = ?", v.ID).Scan(ctx); err != nil {
		return res, err
	}
	// Another movie file in the folder that Reelkeeper doesn't know about
	// (copied in by hand, say) would end up side by side with the import,
	// and Emby would show two copies. Stop and say so instead.
	for _, f := range videosUnder(root.Path, folder, 0) {
		if !slices.ContainsFunc(old, func(o MediaFile) bool { return o.Path == f.Path }) {
			return res, fmt.Errorf("%s is already in the movie's folder, and Reelkeeper doesn't know it; "+
				"run \"Scan media folders\" (System → Tasks) so Reelkeeper can count it, or remove it, then retry the import",
				filepath.Base(f.Path))
		}
	}
	ownsDest := slices.ContainsFunc(old, func(f MediaFile) bool { return f.Path == rel })
	if _, err := os.Lstat(dest); err == nil && !ownsDest {
		return res, fmt.Errorf("a file named %s is already in %s and isn't this movie's; Reelkeeper never overwrites files", filepath.Base(dest), filepath.Dir(dest))
	}

	// Group-writable: the library may be shared with a group (for example on a NAS).
	if err := os.MkdirAll(filepath.Dir(dest), 0o775); err != nil { //nolint:gosec // see above
		return res, fmt.Errorf("can't create the movie's folder: %w", err)
	}
	tmp := filepath.Join(filepath.Dir(dest), ".reelkeeper-importing-"+filepath.Base(dest))
	if err := copyFile(ctx, src, tmp, size); err != nil {
		_ = os.Remove(tmp)
		return res, err
	}

	// Move the old files aside, then put the new one in place.
	oldPaths := make([]string, len(old))
	for i, f := range old {
		oldPaths[i] = f.Path
	}
	replaced, undoAside, err := s.moveAside(root.Path, oldPaths)
	if err != nil {
		_ = os.Remove(tmp)
		return ImportResult{}, err
	}
	res.Replaced = replaced
	undo := func() {
		undoAside()
		_ = os.Remove(tmp)
	}
	if err := os.Rename(tmp, dest); err != nil {
		undo()
		return ImportResult{}, fmt.Errorf("couldn't put the new file in place: %w", err)
	}

	// Record it: the version's folder, the new file, and the old ones gone.
	now := s.now().UTC()
	st, _ := os.Stat(dest)
	mod := now
	if st != nil {
		mod = st.ModTime().UTC().Truncate(time.Second)
	}
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if v.Folder != folder {
			if _, err := tx.NewUpdate().Model(v).Set("folder = ?", folder).WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		if len(old) > 0 {
			ids := make([]int64, len(old))
			for i, f := range old {
				ids[i] = f.ID
			}
			if _, err := tx.NewDelete().Model((*MediaFile)(nil)).Where("id IN (?)", bunList(ids)).Exec(ctx); err != nil {
				return err
			}
		}
		f := draft
		f.VersionID, f.RootFolderID, f.Path, f.Size, f.ModifiedAt = v.ID, root.ID, rel, size, mod
		f.AddedAt, f.ProbedAt, f.ProbeError = now, &now, p.err
		if f.MediaInfo == "" {
			f.MediaInfo = "{}"
		}
		f.EpisodeClaim = encodeClaim(nil) // a movie: no episode (JSON, as the scanner writes it)
		_, err := tx.NewInsert().Model(&f).Exec(ctx)
		return err
	})
	if err != nil {
		// The file is in place but unrecorded; the next scan finds it. The
		// old files stay aside: putting them back now could clash with it.
		s.logger.Error("imported a file but couldn't record it; the next scan will", "file", dest, "error", err)
		return res, fmt.Errorf("the file was copied but couldn't be recorded (a scan will pick it up): %w", err)
	}
	res.Path, res.FullPath = rel, dest
	s.logger.Info("imported file", "movie", t.Title, "file", dest, "from", src, "replaced", res.Replaced)
	s.onChange()
	return res, nil
}

// moveAside moves files (paths relative to the media folder) into a new
// batch folder in ReplacedDir, keeping their folder layout, and returns
// where they went (relative to the media folder). Files already missing are
// skipped. If one can't be moved, the ones moved so far are put back and
// nothing has changed. The returned func puts them all back, for when what
// was meant to replace them fails.
func (s *Service) moveAside(rootPath string, rels []string) (replaced []string, undo func(), err error) {
	stamp := s.now().UTC().Format(replacedStamp)
	var moved []struct{ from, to string }
	undo = func() {
		for i := len(moved) - 1; i >= 0; i-- {
			if err := os.Rename(moved[i].to, moved[i].from); err != nil {
				s.logger.Error("couldn't put a replaced file back", "file", moved[i].from, "error", err)
			}
		}
	}
	for _, rel := range rels {
		from := filepath.Join(rootPath, rel)
		if _, err := os.Lstat(from); err != nil {
			continue // already missing
		}
		asideRel := filepath.Join(ReplacedDir, stamp, rel)
		to := filepath.Join(rootPath, asideRel)
		if err := os.MkdirAll(filepath.Dir(to), 0o775); err == nil { //nolint:gosec // group-writable: the library may be shared with a group (for example on a NAS)
			err = os.Rename(from, to)
			if err == nil {
				moved = append(moved, struct{ from, to string }{from, to})
				replaced = append(replaced, asideRel)
				continue
			}
		}
		undo()
		return nil, nil, fmt.Errorf("couldn't move the old file %s aside, so nothing was changed", rel)
	}
	return replaced, undo, nil
}

// sidecars lists the files that go with a video: those in its folder named
// after it ("Show - S01E01.en.srt", ".nfo"), as paths relative to the media
// folder. Other videos are not sidecars.
func sidecars(rootPath, rel string) []string {
	dir, name := filepath.Split(rel)
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	entries, err := os.ReadDir(filepath.Join(rootPath, dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == name || parser.IsVideo(n) || !strings.HasPrefix(n, stem+".") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// orClient is the download client's name for a sentence, or "the download
// client" when it isn't known.
func orClient(name string) string {
	if name == "" {
		return "the download client"
	}
	return name
}

// mainVideo finds the movie in a download: the file itself, or the largest
// main video in its folder (samples, trailers and extras are skipped).
func mainVideo(source, clientName string) (string, int64, error) {
	st, err := os.Stat(source)
	if err != nil {
		return "", 0, fmt.Errorf("the download isn't where %s says (%s): %w", orClient(clientName), source, err)
	}
	if !st.IsDir() {
		if _, ok := videoFile(filepath.Base(source), st); !ok {
			return "", 0, fmt.Errorf("%s isn't a movie file", filepath.Base(source))
		}
		return source, st.Size(), nil
	}
	files := videosUnder(source, "", 0)
	if len(files) == 0 {
		return "", 0, errors.New("the download has no movie file in it (only samples, extras or other files)")
	}
	best := slices.MaxFunc(files, func(a, b unitFile) int { return int(a.Size>>20 - b.Size>>20) })
	return filepath.Join(source, best.Path), best.Size, nil
}

// discImageAllowed refuses a disc image (.iso) unless the version's profile
// allows BR-DISK. A release name doesn't always say it's a disc
// ("Insidious.2010.1080p.BluRay-R-US" held an .iso), so this is the last
// check; the failed import then lets cleanup blocklist it and search again.
func (s *Service) discImageAllowed(ctx context.Context, profileID int64, src string) error {
	if !strings.EqualFold(filepath.Ext(src), ".iso") {
		return nil
	}
	p, err := s.CompiledProfile(ctx, profileID)
	if err != nil {
		return fmt.Errorf("can't check the quality profile: %w", err)
	}
	if bd, _ := decision.QualityByName("BR-DISK"); p.Allowed(bd) {
		return nil
	}
	return fmt.Errorf("%s is a disc image (BR-DISK), not a movie file, and the profile doesn't allow BR-DISK", filepath.Base(src))
}

// copyFile copies src to dst, syncs it to disk and checks the size.
func copyFile(ctx context.Context, src, dst string, want int64) error {
	in, err := os.Open(src) //nolint:gosec // the download qBittorrent reported, inside its download folder
	if err != nil {
		return fmt.Errorf("can't read the download: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664) //nolint:gosec // a new temporary file in the movie's folder; O_EXCL never overwrites
	if err != nil {
		return fmt.Errorf("can't write into the movie's folder: %w", err)
	}
	n, err := io.Copy(out, ctxReader{ctx, in})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("copying failed: %w", err)
	}
	if n != want {
		return fmt.Errorf("copying failed: wrote %d bytes of %d", n, want)
	}
	if st, err := os.Stat(dst); err != nil || st.Size() != want {
		return errors.New("copying failed: the copy's size doesn't match the download")
	}
	return nil
}

// ctxReader stops a long copy when the import is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// replacedStamp is the name of each batch folder in ReplacedDir.
const replacedStamp = "20060102-150405"

// EmptyReplaced deletes batches in ReplacedDir older than maxAge, in media
// folders Reelkeeper manages (it never wrote anywhere else). With dryRun it
// only lists what it would delete. Folders whose names Reelkeeper didn't make
// are left alone.
func (s *Service) EmptyReplaced(ctx context.Context, maxAge time.Duration, dryRun bool) ([]string, error) {
	roots, err := s.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out []string
	for _, r := range roots {
		if !r.ManageFiles {
			continue
		}
		dir := filepath.Join(r.Path, ReplacedDir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // none yet
		}
		for _, e := range entries {
			at, err := time.Parse(replacedStamp, e.Name())
			if err != nil || !e.IsDir() || now.Sub(at) < maxAge {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if !dryRun {
				if err := os.RemoveAll(p); err != nil {
					return out, fmt.Errorf("couldn't empty %s: %w", p, err)
				}
				s.logger.Info("deleted replaced files", "folder", p, "reason", "older than the cleanup setting")
			}
			out = append(out, p)
		}
		if !dryRun {
			_ = os.Remove(dir) // only succeeds when empty
		}
	}
	return out, nil
}
