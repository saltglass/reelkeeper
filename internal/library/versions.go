package library

import (
	"context"
	"database/sql"
	"errors"

	"github.com/saltglass/reelkeeper/internal/metadata"
)

// Queries for the Radarr-compatible API (internal/compat): each Seerr
// "Radarr" is one quality profile, and shows that profile's version of each
// movie.

// ProfileBySlug finds a profile by its slug ("hd", "remux").
func (s *Service) ProfileBySlug(ctx context.Context, slug string) (*Profile, error) {
	p := new(Profile)
	err := s.db.DB.NewSelect().Model(p).Where("slug = ?", slug).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ProfileMovie is one movie version with everything about it.
type ProfileMovie struct {
	Title   Title
	Version Version
	Root    RootFolder
	Files   []MediaFile // present ones
	Tags    []int64
}

// ProfileMovieFilter narrows ProfileMovies.
type ProfileMovieFilter struct {
	TMDBID    int   // 0: any
	VersionID int64 // 0: any
}

// ProfileMovies lists a profile's movie versions, by title.
func (s *Service) ProfileMovies(ctx context.Context, profileID int64, f ProfileMovieFilter) ([]ProfileMovie, error) {
	var versions []Version
	q := s.db.DB.NewSelect().Model(&versions).
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.profile_id = ? AND t.media_type = ?", profileID, Movie).
		OrderExpr("t.sort_title, version.id")
	if f.TMDBID != 0 {
		q = q.Where("t.tmdb_id = ?", f.TMDBID)
	}
	if f.VersionID != 0 {
		q = q.Where("version.id = ?", f.VersionID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, nil
	}
	var titleIDs, versionIDs []int64
	for _, v := range versions {
		titleIDs = append(titleIDs, v.TitleID)
		versionIDs = append(versionIDs, v.ID)
	}
	var titles []Title
	if err := s.db.DB.NewSelect().Model(&titles).Where("id IN (?)", bunList(titleIDs)).Scan(ctx); err != nil {
		return nil, err
	}
	byTitle := map[int64]Title{}
	for _, t := range titles {
		byTitle[t.ID] = t
	}
	var files []MediaFile
	if err := s.db.DB.NewSelect().Model(&files).Where("version_id IN (?)", bunList(versionIDs)).
		Where("missing_since IS NULL").OrderExpr("id").Scan(ctx); err != nil {
		return nil, err
	}
	filesOf := map[int64][]MediaFile{}
	for _, f := range files {
		filesOf[f.VersionID] = append(filesOf[f.VersionID], f)
	}
	roots, err := s.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	rootByID := map[int64]RootFolder{}
	for _, r := range roots {
		rootByID[r.ID] = r
	}
	tags, err := s.VersionTags(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileMovie, 0, len(versions))
	for _, v := range versions {
		out = append(out, ProfileMovie{
			Title: byTitle[v.TitleID], Version: v, Root: rootByID[v.RootFolderID], Files: filesOf[v.ID], Tags: tags[v.ID],
		})
	}
	return out, nil
}

// TMDBMovie fetches a movie's details from TMDB without adding it, for
// lookups of movies not in the library.
func (s *Service) TMDBMovie(ctx context.Context, tmdbID int) (*Title, error) {
	m, err := s.tmdb.Movie(ctx, tmdbID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return titleFromMovie(m, s.now().UTC()), nil
}

// TitleByTMDB finds a movie in the library by its TMDB ID.
func (s *Service) TitleByTMDB(ctx context.Context, tmdbID int) (*Title, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("tmdb_id = ? AND media_type = ?", tmdbID, Movie).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// EnsureVersion makes sure a movie has a version in a profile: an existing
// one is returned as it is (created false); otherwise the movie (from TMDB)
// and the version are added in the given media folder, using folder if
// given (relative to it) or the folder template, with files already there
// recorded. The Radarr import uses it to bring over movies Radarr knows
// that Reelkeeper's scan didn't find (wanted ones, mostly).
func (s *Service) EnsureVersion(ctx context.Context, tmdbID int, profileID, rootID int64, folder string, monitored bool) (*Version, bool, error) {
	root, err := s.RootFolder(ctx, rootID)
	if err != nil {
		return nil, false, err
	}
	t, err := s.ensureTitle(ctx, tmdbID)
	if err != nil {
		return nil, false, err
	}
	v := new(Version)
	err = s.db.DB.NewSelect().Model(v).Where("title_id = ? AND profile_id = ?", t.ID, profileID).Scan(ctx)
	if err == nil {
		return v, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if folder == "" {
		folder = s.folderName(ctx, t, profileID)
	}
	v = &Version{TitleID: t.ID, ProfileID: profileID, RootFolderID: root.ID, Folder: folder, Monitored: monitored, CreatedAt: s.now().UTC()}
	if _, err := s.db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		return nil, false, err
	}
	if err := s.adoptFiles(ctx, root, v); err != nil {
		return nil, false, err
	}
	s.onChange()
	return v, true, nil
}
