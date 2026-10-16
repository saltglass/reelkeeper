package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// ReviewItems lists the folders waiting for someone to say what they are.
func (s *Service) ReviewItems(ctx context.Context) ([]ScanItem, error) {
	var out []ScanItem
	err := s.db.DB.NewSelect().Model(&out).Where("status = ?", NeedsReview).Order("path").Scan(ctx)
	return out, err
}

// ReviewItem returns one review item.
func (s *Service) ReviewItem(ctx context.Context, id int64) (*ScanItem, error) {
	it := new(ScanItem)
	err := s.db.DB.NewSelect().Model(it).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

// ResolveReview says which TMDB movie a reviewed folder is, and adds it.
func (s *Service) ResolveReview(ctx context.Context, id int64, tmdbID int) (*Title, error) {
	it, err := s.ReviewItem(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := s.RootFolder(ctx, it.RootFolderID)
	if err != nil {
		return nil, err
	}
	u := unit{Path: it.Path}
	for _, rel := range it.FileList() {
		st, err := os.Stat(filepath.Join(root.Path, rel))
		if err != nil {
			continue // gone since the scan
		}
		u.Files = append(u.Files, unitFile{Path: rel, Size: st.Size(), ModTime: st.ModTime().UTC().Truncate(time.Second)})
	}
	if len(u.Files) == 0 {
		return nil, validation.Errors{"tmdbId": "The files in this folder are gone. Scan again to refresh the list."}
	}

	// In a TV folder the chosen TMDB ID is a series.
	ensure := s.ensureTitle
	if root.MediaType == Series {
		ensure = s.ensureSeries
	}
	title, err := ensure(ctx, tmdbID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, validation.Errors{"tmdbId": "TMDB doesn't know that title."}
	}
	if err != nil {
		return nil, err
	}
	folder := u.Path
	if u.loose() {
		folder = ""
	}
	v, conflict, err := s.ensureVersion(ctx, title, root, folder)
	if err != nil {
		return nil, err
	}
	if conflict != "" {
		return nil, validation.Errors{"tmdbId": conflict}
	}
	if root.MediaType == Series {
		if err := s.saveSeriesFiles(ctx, root.ID, v, probeSeriesFiles(root.Path, u, u.Files), map[string]*MediaFile{}); err != nil {
			return nil, err
		}
	} else {
		for _, f := range u.Files {
			if err := s.saveFile(ctx, root.ID, v.ID, probe(root.Path, f), nil); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.db.DB.NewDelete().Model(it).WherePK().Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("review resolved", "path", it.Path, "title", title.Title, "year", title.Year)
	s.onChange()
	return title, nil
}

// IgnoreReview hides a folder from review and future scans (for home
// videos and the like). Rescanning won't bring it back.
func (s *Service) IgnoreReview(ctx context.Context, id int64) error {
	it, err := s.ReviewItem(ctx, id)
	if err != nil {
		return err
	}
	it.Status, it.UpdatedAt = Ignored, s.now().UTC()
	_, err = s.db.DB.NewUpdate().Model(it).Column("status", "updated_at").WherePK().Exec(ctx)
	if err == nil {
		s.onChange()
	}
	return err
}
