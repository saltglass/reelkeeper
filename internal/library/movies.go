package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// VersionView is a version with its folder and file totals, for lists.
type VersionView struct {
	Version
	ProfileName string
	RootPath    string
	Files       int // present on disk
	Missing     int
	Size        int64
	// What the best (highest-resolution) file is, for labels.
	Quality       string
	Resolution    int
	HDR           []string
	VideoCodec    string
	AudioCodec    string // first audio track
	AudioChannels int
	ReleaseGroup  string
	Edition       string
	// Series only: regular episodes with at least one present file.
	EpisodesWithFiles int
	MediaFiles        []MediaFile // filled for the detail view only
}

// MovieView is a movie with its versions.
type MovieView struct {
	Title
	Versions []VersionView
}

// Movies returns every movie with version summaries, sorted by title.
// Three queries in total, whatever the library's size.
func (s *Service) Movies(ctx context.Context) ([]MovieView, error) {
	var titles []Title
	if err := s.db.DB.NewSelect().Model(&titles).Where("media_type = ?", Movie).Order("sort_title").Scan(ctx); err != nil {
		return nil, err
	}
	versions, err := s.versionViews(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	byTitle := map[int64][]VersionView{}
	for _, v := range versions {
		byTitle[v.TitleID] = append(byTitle[v.TitleID], v)
	}
	out := make([]MovieView, 0, len(titles))
	for _, t := range titles {
		out = append(out, MovieView{Title: t, Versions: byTitle[t.ID]})
	}
	return out, nil
}

// MovieDetail returns one movie with its versions and their files.
func (s *Service) MovieDetail(ctx context.Context, id int64) (*MovieView, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("id = ? AND media_type = ?", id, Movie).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	versions, err := s.versionViews(ctx, &id, true)
	if err != nil {
		return nil, err
	}
	return &MovieView{Title: *t, Versions: versions}, nil
}

// versionViews loads versions (all, or one title's) with their totals.
func (s *Service) versionViews(ctx context.Context, titleID *int64, withFiles bool) ([]VersionView, error) {
	var rows []struct {
		Version
		ProfileName string `bun:"profile_name"`
		RootPath    string `bun:"root_path"`
	}
	q := s.db.DB.NewSelect().Model((*Version)(nil)).
		ColumnExpr("version.*").
		ColumnExpr("p.name AS profile_name").
		ColumnExpr("r.path AS root_path").
		Join("JOIN profiles p ON p.id = version.profile_id").
		Join("JOIN root_folders r ON r.id = version.root_folder_id").
		Order("p.id")
	if titleID != nil {
		q = q.Where("version.title_id = ?", *titleID)
	}
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, err
	}

	var files []MediaFile
	fq := s.db.DB.NewSelect().Model(&files).Order("media_file.path")
	if titleID != nil {
		fq = fq.Where("media_file.version_id IN (SELECT id FROM versions WHERE title_id = ?)", *titleID)
	} else {
		// Lists only need the columns used for totals and badges.
		fq = fq.Column("id", "version_id", "size", "resolution", "quality", "edition", "release_group", "media_info", "missing_since")
	}
	if err := fq.Scan(ctx); err != nil {
		return nil, err
	}
	byVersion := map[int64][]MediaFile{}
	for _, f := range files {
		byVersion[f.VersionID] = append(byVersion[f.VersionID], f)
	}

	out := make([]VersionView, 0, len(rows))
	for _, r := range rows {
		v := VersionView{Version: r.Version, ProfileName: r.ProfileName, RootPath: r.RootPath}
		var best *MediaFile
		for i := range byVersion[v.ID] {
			f := &byVersion[v.ID][i]
			if f.MissingSince != nil {
				v.Missing++
				continue
			}
			v.Files++
			v.Size += f.Size
			if best == nil || f.Resolution > best.Resolution {
				best = f
			}
		}
		if best != nil {
			v.Quality, v.Resolution = best.Quality, best.Resolution
			v.ReleaseGroup, v.Edition = best.ReleaseGroup, best.Edition
			if m := best.Media(); m != nil {
				if m.Video != nil {
					v.HDR, v.VideoCodec = m.Video.HDR, m.Video.Codec
				}
				if len(m.Audio) > 0 {
					v.AudioCodec, v.AudioChannels = m.Audio[0].Codec, m.Audio[0].Channels
				}
			}
		}
		if withFiles {
			v.MediaFiles = byVersion[v.ID]
		}
		out = append(out, v)
	}
	return out, nil
}

// LookupResult is a TMDB search result, marked if already in the library.
type LookupResult struct {
	metadata.MovieResult
	MovieID int64 // 0 if not in the library
}

// Lookup searches TMDB for movies to add.
func (s *Service) Lookup(ctx context.Context, query string) ([]LookupResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	// "Dune 2021" searches for Dune from 2021.
	title, year := query, 0
	if f := strings.Fields(query); len(f) > 1 {
		if y := f[len(f)-1]; len(y) == 4 && (strings.HasPrefix(y, "19") || strings.HasPrefix(y, "20")) {
			year = atoi(y)
			title = strings.Join(f[:len(f)-1], " ")
		}
	}
	results, err := s.tmdb.SearchMovies(ctx, title, year)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 && year != 0 {
		if results, err = s.tmdb.SearchMovies(ctx, query, 0); err != nil {
			return nil, err
		}
	}
	ids := make([]int, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	have := map[int]int64{}
	if len(ids) > 0 {
		var rows []Title
		if err := s.db.DB.NewSelect().Model(&rows).Column("id", "tmdb_id").
			Where("media_type = ?", Movie).Where("tmdb_id IN (?)", bunList(ids)).Scan(ctx); err != nil {
			return nil, err
		}
		for _, r := range rows {
			have[r.TMDBID] = r.ID
		}
	}
	out := make([]LookupResult, 0, len(results))
	for _, r := range results {
		out = append(out, LookupResult{MovieResult: r, MovieID: have[r.ID]})
	}
	return out, nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// AddMovie adds a movie (or another version of one) by TMDB ID, monitored
// and with no files yet. The folder it would live in comes from the
// naming template; nothing is created on disk.
func (s *Service) AddMovie(ctx context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*MovieView, error) {
	var errs validation.Errors
	if tmdbID <= 0 {
		errs.Add("tmdbId", "Choose a movie.")
	}
	if err := s.checkProfile(ctx, profileID, Movie, "profileId", &errs); err != nil {
		return nil, err
	}
	root, err := s.RootFolder(ctx, rootID)
	if errors.Is(err, ErrNotFound) {
		errs.Add("rootFolderId", "Choose a media folder.")
	} else if err != nil {
		return nil, err
	}
	if errs != nil {
		return nil, errs
	}
	t, err := s.ensureTitle(ctx, tmdbID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, validation.Errors{"tmdbId": "TMDB doesn't know that movie."}
	}
	if err != nil {
		return nil, err
	}
	n, err := s.db.DB.NewSelect().Model((*Version)(nil)).Where("title_id = ? AND profile_id = ?", t.ID, profileID).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrInUse
	}
	v := &Version{TitleID: t.ID, ProfileID: profileID, RootFolderID: root.ID, Folder: s.folderName(ctx, t, profileID), Monitored: monitored, CreatedAt: s.now().UTC()}
	if _, err := s.db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("movie added", "title", t.Title, "year", t.Year, "profile", profileID)
	if err := s.adoptFiles(ctx, root, v); err != nil {
		return nil, err
	}
	s.onChange()
	return s.MovieDetail(ctx, t.ID)
}

// adoptFiles records video files already in a newly added version's
// folder, as a scan would, so Reelkeeper doesn't search for a movie that's
// already there (or knows what an upgrade would replace). Files another
// version already owns are left alone.
func (s *Service) adoptFiles(ctx context.Context, root *RootFolder, v *Version) error {
	if v.Folder == "" {
		return nil
	}
	for _, f := range videosUnder(root.Path, v.Folder, 0) {
		owned, err := s.db.DB.NewSelect().Model((*MediaFile)(nil)).
			Where("root_folder_id = ? AND path = ?", root.ID, f.Path).Exists(ctx)
		if err != nil {
			return err
		}
		if owned {
			continue
		}
		if err := s.saveFile(ctx, root.ID, v.ID, probe(root.Path, f), nil); err != nil {
			return err
		}
		s.logger.Info("found a file in a newly added movie's folder", "file", filepath.Join(root.Path, f.Path))
	}
	return nil
}

// RefreshMovie fetches the latest details from TMDB.
func (s *Service) RefreshMovie(ctx context.Context, id int64) (*MovieView, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("id = ? AND media_type = ?", id, Movie).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.refresh(ctx, t); err != nil {
		return nil, err
	}
	s.onChange()
	return s.MovieDetail(ctx, id)
}

func (s *Service) refresh(ctx context.Context, t *Title) error {
	m, err := s.tmdb.Movie(ctx, t.TMDBID)
	if err != nil {
		return err
	}
	fresh := titleFromMovie(m, s.now().UTC())
	fresh.ID, fresh.CreatedAt = t.ID, t.CreatedAt
	_, err = s.db.DB.NewUpdate().Model(fresh).WherePK().ExcludeColumn("id", "created_at").Exec(ctx)
	return err
}

// RefreshStale refreshes metadata for up to limit movies and series whose
// details are older than maxAge, oldest first. It's the daily metadata
// task. Series also get their new episodes, and their files re-linked.
func (s *Service) RefreshStale(ctx context.Context, maxAge time.Duration, limit int, progress Progress) (int, error) {
	var titles []Title
	cutoff := s.now().UTC().Add(-maxAge)
	// Series still on the air get new episodes every week, so they're
	// refreshed daily whatever maxAge says.
	airingCutoff := s.now().UTC().Add(-20 * time.Hour)
	err := s.db.DB.NewSelect().Model(&titles).
		WhereGroup(" AND ", func(q bunSelect) bunSelect {
			return q.Where("metadata_updated_at IS NULL").WhereOr("metadata_updated_at < ?", cutoff).
				WhereOr("media_type = ? AND status IN (?) AND metadata_updated_at < ?",
					Series, bunList([]string{"Returning Series", "In Production", "Planned", "Pilot"}), airingCutoff)
		}).
		OrderExpr("metadata_updated_at ASC NULLS FIRST").Limit(int64(limit)).Scan(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for i := range titles {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		if progress != nil {
			progress(i*100/max(len(titles), 1), "Refreshing "+titles[i].Title)
		}
		refresh := s.refresh
		if titles[i].MediaType == Series {
			refresh = s.refreshSeries
		}
		if err := refresh(ctx, &titles[i]); err != nil {
			if stop := stopping(err); stop != nil {
				return done, err
			}
			s.logger.Warn("couldn't refresh details", "title", titles[i].Title, "error", err)
			continue
		}
		done++
	}
	if done > 0 {
		s.onChange()
	}
	return done, nil
}

// SetMonitored turns monitoring of a version on or off.
func (s *Service) SetMonitored(ctx context.Context, titleID, versionID int64, monitored bool) error {
	res, err := s.db.DB.NewUpdate().Model((*Version)(nil)).Set("monitored = ?", monitored).
		Where("id = ? AND title_id = ?", versionID, titleID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.onChange()
	return nil
}

// ErrHasFiles means a version can't be removed because it has files.
var ErrHasFiles = errors.New("this version has files; the next scan would find them again")

// RemoveVersion removes a version that has no files, and the movie too if
// it was the last version.
func (s *Service) RemoveVersion(ctx context.Context, titleID, versionID int64) error {
	v := new(Version)
	err := s.db.DB.NewSelect().Model(v).Where("id = ? AND title_id = ?", versionID, titleID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	n, err := s.db.DB.NewSelect().Model((*MediaFile)(nil)).Where("version_id = ?", v.ID).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrHasFiles
	}
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewDelete().Model(v).WherePK().Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewDelete().Model((*Title)(nil)).Where("id = ?", titleID).
			Where("NOT EXISTS (SELECT 1 FROM versions v WHERE v.title_id = title.id)").Exec(ctx)
		return err
	})
	if err == nil {
		s.onChange()
	}
	return err
}

// FullPath joins a root folder and a relative path.
func FullPath(root, rel string) string { return filepath.Join(root, rel) }

// folderName is the folder a movie gets when added by hand, from the
// folder naming template.
func (s *Service) folderName(ctx context.Context, t *Title, profileID int64) string {
	p := new(Profile)
	_ = s.db.DB.NewSelect().Model(p).Where("id = ?", profileID).Scan(ctx) // a missing name only blanks {Profile Name}
	return naming.Format(s.naming().MovieFolder, movieFacts(t, p.Name), naming.File{})
}
