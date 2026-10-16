package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// Deleting a movie removes it from Reelkeeper: its versions, files' records,
// history and blocklist. What happens on disk is the user's choice, made
// in the Delete dialog each time (2026-10-09):
//
//   - Keep the files: nothing on disk changes. The movie's folders go on
//     the skip list, so the next scan doesn't add the movie straight back.
//   - Delete the files: the movie's video files, and files named after
//     them (subtitles and the like), are deleted for good. The movie's
//     folder goes too, but only when no other video is left in it, so a
//     folder holding something else never disappears with it. Only media
//     folders with "Manage files" on are touched; otherwise nothing is
//     deleted at all.
//
// Downloads in progress are the caller's job (the API removes them from
// qBittorrent first).

// SkippedFolder is a folder scans leave alone.
type SkippedFolder struct {
	bun.BaseModel `bun:"table:skipped_folders"`

	ID           int64     `bun:"id,pk,autoincrement" json:"id"`
	RootFolderID int64     `bun:"root_folder_id" json:"rootFolderId"`
	Path         string    `bun:"path" json:"path"`
	Title        string    `bun:"title" json:"title"`
	CreatedAt    time.Time `bun:"created_at" json:"createdAt"`
}

// DeleteResult says what deleting did.
type DeleteResult struct {
	// Deleted are the files and folders removed from disk.
	Deleted []string
	// Skipped are the folders scans will now leave alone.
	Skipped []string
}

// deletePlace is a folder (as scans see it: the first level under the
// media folder) that belongs to a movie being deleted.
type deletePlace struct {
	root *RootFolder
	unit string
}

// deletePlan is what deleting a movie (or one version of it) involves.
type deletePlan struct {
	title *Title
	// versions are the ones being deleted; whole is set when that's all of
	// them, so the movie itself goes too.
	versions []Version
	whole    bool
	files    []MediaFile
	roots    map[int64]*RootFolder
	places   []deletePlace
}

// planDelete plans deleting a movie, or just one of its versions when
// versionID isn't 0.
func (s *Service) planDelete(ctx context.Context, titleID, versionID int64, mediaType string) (*deletePlan, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("id = ? AND media_type = ?", titleID, mediaType).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plan := &deletePlan{title: t, roots: map[int64]*RootFolder{}}
	var all, versions, others []Version
	if err := s.db.DB.NewSelect().Model(&all).Where("title_id = ?", t.ID).Scan(ctx); err != nil {
		return nil, err
	}
	for _, v := range all {
		if versionID == 0 || v.ID == versionID {
			versions = append(versions, v)
		} else {
			others = append(others, v)
		}
	}
	if versionID != 0 && len(versions) == 0 {
		return nil, ErrNotFound
	}
	plan.versions, plan.whole = versions, len(others) == 0
	if len(versions) > 0 {
		ids := make([]int64, len(versions))
		for i, v := range versions {
			ids[i] = v.ID
		}
		if err := s.db.DB.NewSelect().Model(&plan.files).Where("version_id IN (?)", bunList(ids)).Scan(ctx); err != nil {
			return nil, err
		}
	}
	for _, v := range all {
		if plan.roots[v.RootFolderID] == nil {
			r, err := s.RootFolder(ctx, v.RootFolderID)
			if err != nil {
				return nil, err
			}
			plan.roots[r.ID] = r
		}
	}
	add := func(root *RootFolder, rel string) {
		unit := strings.Split(filepath.ToSlash(rel), "/")[0]
		for _, p := range plan.places {
			if p.root.ID == root.ID && p.unit == unit {
				return
			}
		}
		plan.places = append(plan.places, deletePlace{root, unit})
	}
	for _, f := range plan.files {
		add(plan.roots[f.RootFolderID], f.Path)
	}
	for _, v := range versions {
		if v.Folder != "" {
			if _, err := os.Stat(filepath.Join(plan.roots[v.RootFolderID].Path, v.Folder)); err == nil {
				add(plan.roots[v.RootFolderID], v.Folder)
			}
		}
	}
	// A folder another version of the movie still uses stays: it's neither
	// deleted nor skipped by scans.
	for _, o := range others {
		if o.Folder == "" {
			continue
		}
		unit := strings.Split(filepath.ToSlash(o.Folder), "/")[0]
		plan.places = slices.DeleteFunc(plan.places, func(p deletePlace) bool {
			return p.root.ID == o.RootFolderID && p.unit == unit
		})
	}
	return plan, nil
}

// canDeleteFiles reports why a plan's files can't be deleted, or nil.
func (p *deletePlan) canDeleteFiles() error {
	for _, pl := range p.places {
		if !pl.root.ManageFiles {
			return fmt.Errorf("%w: %s. Delete the movie without its files, or turn on \"Manage files\" for that folder first", ErrNotManaged, pl.root.Path)
		}
	}
	return nil
}

// CheckDeleteFiles says whether DeleteMovie could delete a movie's files,
// without changing anything, so callers can stop before doing anything
// else (such as removing its downloads).
func (s *Service) CheckDeleteFiles(ctx context.Context, titleID int64) error {
	plan, err := s.planDelete(ctx, titleID, 0, Movie)
	if err != nil {
		return err
	}
	return plan.canDeleteFiles()
}

// DeleteMovie deletes a movie from Reelkeeper, and its files when deleteFiles
// is set.
func (s *Service) DeleteMovie(ctx context.Context, titleID int64, deleteFiles bool) (DeleteResult, error) {
	return s.deleteVersions(ctx, titleID, 0, Movie, deleteFiles)
}

// DeleteVersion deletes one version of a movie (the Remux copy, say), and
// its files when deleteFiles is set; the movie goes too when it was the
// last version. The Radarr-compatible API uses it: each Seerr "Radarr" is
// one version.
func (s *Service) DeleteVersion(ctx context.Context, titleID, versionID int64, deleteFiles bool) (DeleteResult, error) {
	if versionID == 0 {
		return DeleteResult{}, ErrNotFound
	}
	return s.deleteVersions(ctx, titleID, versionID, Movie, deleteFiles)
}

// DeleteSeriesVersion removes one version of a series from Reelkeeper (the
// series goes too when it was the last version), always keeping its files:
// its folder goes on the skip list. The Sonarr-compatible API uses it when
// Seerr removes a show; deleting a series' files isn't offered anywhere.
func (s *Service) DeleteSeriesVersion(ctx context.Context, titleID, versionID int64) (DeleteResult, error) {
	if versionID == 0 {
		return DeleteResult{}, ErrNotFound
	}
	return s.deleteVersions(ctx, titleID, versionID, Series, false)
}

func (s *Service) deleteVersions(ctx context.Context, titleID, versionID int64, mediaType string, deleteFiles bool) (DeleteResult, error) {
	var res DeleteResult
	plan, err := s.planDelete(ctx, titleID, versionID, mediaType)
	if err != nil {
		return res, err
	}
	t, files, roots, places := plan.title, plan.files, plan.roots, plan.places

	if deleteFiles {
		if err := plan.canDeleteFiles(); err != nil {
			return res, err
		}
		for _, f := range files {
			gone, err := s.deleteVideo(roots[f.RootFolderID].Path, f.Path)
			res.Deleted = append(res.Deleted, gone...)
			if err != nil {
				return res, fmt.Errorf("stopped after deleting %d files: %w", len(res.Deleted), err)
			}
		}
		for _, p := range places {
			dir := filepath.Join(p.root.Path, p.unit)
			st, err := os.Stat(dir)
			if err != nil || !st.IsDir() || filepath.Clean(dir) == filepath.Clean(p.root.Path) {
				continue
			}
			if left := videosUnder(p.root.Path, p.unit, 0); len(left) > 0 {
				s.logger.Info("kept a deleted movie's folder: other videos are in it", "folder", dir, "videos", len(left))
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				return res, fmt.Errorf("deleted the files, but couldn't remove the folder %s: %w", dir, err)
			}
			res.Deleted = append(res.Deleted, dir)
			s.logger.Info("deleted folder", "folder", dir, "reason", "movie deleted with its files")
		}
	}

	now := s.now().UTC()
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		for _, p := range places {
			if _, err := tx.NewDelete().Model((*ScanItem)(nil)).
				Where("root_folder_id = ? AND path = ?", p.root.ID, p.unit).Exec(ctx); err != nil {
				return err
			}
			if deleteFiles {
				continue
			}
			sf := &SkippedFolder{RootFolderID: p.root.ID, Path: p.unit, Title: fmt.Sprintf("%s (%d)", t.Title, t.Year), CreatedAt: now}
			if _, err := tx.NewInsert().Model(sf).On("CONFLICT (root_folder_id, path) DO NOTHING").Exec(ctx); err != nil {
				return err
			}
			res.Skipped = append(res.Skipped, filepath.Join(p.root.Path, p.unit))
		}
		if plan.whole {
			// Versions (and their files' records) go with the title.
			_, err := tx.NewDelete().Model((*Title)(nil)).Where("id = ?", t.ID).Exec(ctx)
			return err
		}
		ids := make([]int64, len(plan.versions))
		for i, v := range plan.versions {
			ids[i] = v.ID
		}
		_, err := tx.NewDelete().Model((*Version)(nil)).Where("id IN (?)", bunList(ids)).Exec(ctx)
		return err
	})
	if err != nil {
		return res, err
	}
	s.logger.Info(mediaType+" deleted", "title", t.Title, "year", t.Year, "files_deleted", deleteFiles, "deleted", res.Deleted, "skipped", res.Skipped)
	s.onChange()
	return res, nil
}

// deleteVideo deletes a video file and the files named after it in the
// same folder (subtitles, .nfo...), returning what it deleted.
func (s *Service) deleteVideo(root, rel string) ([]string, error) {
	path := filepath.Join(root, rel)
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var deleted []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (name != filepath.Base(path) && !strings.HasPrefix(name, stem+".")) {
			continue
		}
		p := filepath.Join(filepath.Dir(path), name)
		if err := os.Remove(p); err != nil {
			return deleted, err
		}
		deleted = append(deleted, p)
		s.logger.Info("deleted file", "file", p, "reason", "movie deleted with its files")
	}
	return deleted, nil
}

// SkippedFolders lists the folders scans leave alone.
func (s *Service) SkippedFolders(ctx context.Context) ([]SkippedFolder, error) {
	var out []SkippedFolder
	return out, s.db.DB.NewSelect().Model(&out).Order("root_folder_id", "path").Scan(ctx)
}

// UnskipFolder lets scans see a folder again; the next scan adds what's in
// it.
func (s *Service) UnskipFolder(ctx context.Context, id int64) error {
	res, err := s.db.DB.NewDelete().Model((*SkippedFolder)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// skipped is the set of a media folder's skipped paths.
func (s *Service) skipped(ctx context.Context, rootID int64) (map[string]bool, error) {
	var rows []SkippedFolder
	if err := s.db.DB.NewSelect().Model(&rows).Where("root_folder_id = ?", rootID).Scan(ctx); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Path] = true
	}
	return out, nil
}
