package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// TMDB is what the library needs from internal/metadata; tests use a fake.
type TMDB interface {
	SearchMovies(ctx context.Context, query string, year int) ([]metadata.MovieResult, error)
	Movie(ctx context.Context, id int) (*metadata.Movie, error)
	FindByIMDb(ctx context.Context, imdbID string) (*metadata.MovieResult, error)

	SearchTV(ctx context.Context, query string, year int) ([]metadata.SeriesResult, error)
	Series(ctx context.Context, id int) (*metadata.Series, error)
	FindSeriesByTVDB(ctx context.Context, tvdbID int) (*metadata.SeriesResult, error)
	FindSeriesByIMDb(ctx context.Context, imdbID string) (*metadata.SeriesResult, error)
	EpisodeGroup(ctx context.Context, id string) (*metadata.EpisodeGroup, error)
}

// Errors for the API layer.
var (
	ErrNotFound = errors.New("not found")
	ErrInUse    = errors.New("already in the library")
)

// Service manages the library.
type Service struct {
	db       *store.Store
	tmdb     TMDB
	logger   *slog.Logger
	now      func() time.Time
	onChange func()
	naming   func() naming.Templates
}

// New returns a library Service.
func New(db *store.Store, tmdb TMDB, logger *slog.Logger) *Service {
	return &Service{db: db, tmdb: tmdb, logger: logger, now: time.Now, onChange: func() {}, naming: naming.Defaults}
}

// OnChange registers fn to be called after the library changes (a scan
// added titles, a review was resolved...). Only one listener.
func (s *Service) OnChange(fn func()) { s.onChange = fn }

// Profiles lists the quality profiles.
func (s *Service) Profiles(ctx context.Context) ([]Profile, error) {
	var out []Profile
	err := s.db.DB.NewSelect().Model(&out).Order("id").Scan(ctx)
	return out, err
}

// RootFolders lists the root folders.
func (s *Service) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var out []RootFolder
	err := s.db.DB.NewSelect().Model(&out).Order("path").Scan(ctx)
	return out, err
}

// RootFolder returns one root folder.
func (s *Service) RootFolder(ctx context.Context, id int64) (*RootFolder, error) {
	r := new(RootFolder)
	err := s.db.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RootStats are counts shown next to a root folder.
type RootStats struct {
	Titles    int
	Files     int
	Missing   int
	Review    int
	Size      int64
	FreeSpace int64 // bytes free on the filesystem, -1 if unknown
}

// Stats returns counts for a root folder.
func (s *Service) Stats(ctx context.Context, r RootFolder) (RootStats, error) {
	var st RootStats
	n, err := s.db.DB.NewSelect().Model((*Version)(nil)).Where("root_folder_id = ?", r.ID).Count(ctx)
	if err != nil {
		return st, err
	}
	st.Titles = int(n)
	var agg struct {
		Files   int64 `bun:"files"`
		Missing int64 `bun:"missing"`
		Size    int64 `bun:"size"`
	}
	err = s.db.DB.NewSelect().Model((*MediaFile)(nil)).
		ColumnExpr("COUNT(*) AS files").
		ColumnExpr("COUNT(missing_since) AS missing").
		ColumnExpr("COALESCE(SUM(size), 0) AS size").
		Where("root_folder_id = ?", r.ID).Scan(ctx, &agg)
	if err != nil {
		return st, err
	}
	st.Files, st.Missing, st.Size = int(agg.Files), int(agg.Missing), agg.Size
	rv, err := s.db.DB.NewSelect().Model((*ScanItem)(nil)).
		Where("root_folder_id = ? AND status = ?", r.ID, NeedsReview).Count(ctx)
	if err != nil {
		return st, err
	}
	st.Review = int(rv)
	st.FreeSpace = freeSpace(r.Path)
	return st, nil
}

// RootInput is what's needed to add or change a root folder.
type RootInput struct {
	Path             string
	MediaType        string
	DefaultProfileID int64
	ManageFiles      bool
	// UpgradeFound (movie folders): whether movies found here are upgraded.
	// Series folders store true and ignore it.
	UpgradeFound bool
}

// AddRootFolder validates and adds a root folder.
func (s *Service) AddRootFolder(ctx context.Context, in RootInput) (*RootFolder, error) {
	var errs validation.Errors
	p := filepath.Clean(strings.TrimSpace(in.Path))
	switch {
	case strings.TrimSpace(in.Path) == "":
		errs.Add("path", "Enter the folder's path, as Reelkeeper sees it (inside the container, e.g. /media/movies).")
	case !filepath.IsAbs(p):
		errs.Add("path", "Use a full path starting with /.")
	default:
		st, err := os.Stat(p)
		switch {
		case err != nil:
			errs.Add("path", "Reelkeeper can't find that folder. Check the path and that it's mounted into the container.")
		case !st.IsDir():
			errs.Add("path", "That's a file, not a folder.")
		default:
			if _, err := os.ReadDir(p); err != nil {
				errs.Add("path", "Reelkeeper can't read that folder. Check its permissions.")
			}
		}
	}
	switch in.MediaType {
	case Movie, Series:
	default:
		errs.Add("mediaType", "Choose Movies or TV.")
	}
	if errs == nil {
		roots, err := s.RootFolders(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range roots {
			if r.Path == p {
				errs.Add("path", "That folder is already added.")
			} else if within(p, r.Path) || within(r.Path, p) {
				errs.Add("path", fmt.Sprintf("That folder overlaps %s, which is already added. Root folders can't be inside each other.", r.Path))
			}
		}
	}
	if err := s.checkProfile(ctx, in.DefaultProfileID, in.MediaType, "defaultProfileId", &errs); err != nil {
		return nil, err
	}
	if errs != nil {
		return nil, errs
	}
	r := &RootFolder{Path: p, MediaType: in.MediaType, DefaultProfileID: in.DefaultProfileID, ManageFiles: in.ManageFiles,
		UpgradeFound: in.MediaType == Series || in.UpgradeFound, CreatedAt: s.now().UTC()}
	if _, err := s.db.DB.NewInsert().Model(r).Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("root folder added", "path", p)
	return r, nil
}

// UpdateRootFolder changes a root folder's profile or file management, and,
// for a movie folder, whether its movies are upgraded (upgradeFound nil:
// unchanged). Changing that sets Upgrades on every movie version in the
// folder, including ones Reelkeeper downloaded. It starts no searches: the
// scheduled pass picks the movies up at its usual pace.
func (s *Service) UpdateRootFolder(ctx context.Context, id int64, profileID int64, manageFiles bool, upgradeFound *bool) (*RootFolder, error) {
	r, err := s.RootFolder(ctx, id)
	if err != nil {
		return nil, err
	}
	var errs validation.Errors
	if err := s.checkProfile(ctx, profileID, r.MediaType, "defaultProfileId", &errs); err != nil {
		return nil, err
	}
	if errs != nil {
		return nil, errs
	}
	oldProfile := r.DefaultProfileID
	if profileID != oldProfile {
		if msg, err := s.profileSwitchClash(ctx, r, oldProfile, profileID); err != nil {
			return nil, err
		} else if msg != "" {
			return nil, validation.Errors{"defaultProfileId": msg}
		}
	}
	if r.ManageFiles != manageFiles {
		s.logger.Warn("root folder file management changed", "path", r.Path, "manage_files", manageFiles)
	}
	r.DefaultProfileID, r.ManageFiles = profileID, manageFiles
	// A series folder ignores the setting (it stays true).
	upgradeChanged := upgradeFound != nil && r.MediaType == Movie && *upgradeFound != r.UpgradeFound
	if upgradeChanged {
		r.UpgradeFound = *upgradeFound
	}
	var moved, upgraded int64
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewUpdate().Model(r).Column("default_profile_id", "manage_files", "upgrade_found").WherePK().Exec(ctx); err != nil {
			return err
		}
		if upgradeChanged {
			res, err := tx.NewUpdate().Model((*Version)(nil)).Set("upgrades = ?", r.UpgradeFound).
				Where("root_folder_id = ?", r.ID).Exec(ctx)
			if err != nil {
				return err
			}
			upgraded, _ = res.RowsAffected()
		}
		if profileID == oldProfile {
			return nil
		}
		// The folder's movies move to the new profile with it. Otherwise
		// they'd stay under the old one, and the next scan would add empty
		// duplicates under the new one. Movies added here by hand with some
		// other profile keep theirs. No files are touched.
		res, err := tx.NewUpdate().Model((*Version)(nil)).Set("profile_id = ?", profileID).
			Where("root_folder_id = ? AND profile_id = ?", r.ID, oldProfile).Exec(ctx)
		if err != nil {
			return err
		}
		moved, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if upgradeChanged {
		s.logger.Info("root folder upgrades changed", "path", r.Path, "upgrade_found", r.UpgradeFound, "versions_changed", upgraded)
	}
	if profileID != oldProfile || upgradeChanged {
		if profileID != oldProfile {
			s.logger.Info("root folder profile changed", "path", r.Path, "from", oldProfile, "to", profileID, "versions_moved", moved)
		}
		s.onChange()
	}
	return r, nil
}

// profileSwitchClash explains why a root folder can't switch from one
// profile to another, or returns "". A title holds at most one version per
// profile, so a movie here that already has a copy under the new profile
// (in another folder) blocks the switch.
func (s *Service) profileSwitchClash(ctx context.Context, r *RootFolder, from, to int64) (string, error) {
	var titles []string
	err := s.db.DB.NewSelect().Model((*Version)(nil)).
		ColumnExpr("t.title").
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.root_folder_id = ? AND version.profile_id = ?", r.ID, from).
		Where("EXISTS (SELECT 1 FROM versions o WHERE o.title_id = version.title_id AND o.profile_id = ?)", to).
		OrderExpr("t.sort_title").
		Scan(ctx, &titles)
	if err != nil || len(titles) == 0 {
		return "", err
	}
	p, err := s.profile(ctx, to)
	if err != nil {
		return "", err
	}
	examples := strings.Join(titles, ", ")
	if len(titles) > 3 {
		examples = strings.Join(titles[:3], ", ") + ", …"
	}
	what := plural(len(titles), "movie here already has", "movies here already have")
	return fmt.Sprintf("%s a %s copy in another folder (%s). A movie can have only one copy per profile, so this folder keeps its current profile.",
		what, p.Name, examples), nil
}

// RemoveRootFolder forgets a root folder and everything Reelkeeper knows about
// its contents. Files on disk are never touched.
func (s *Service) RemoveRootFolder(ctx context.Context, id int64) error {
	r, err := s.RootFolder(ctx, id)
	if err != nil {
		return err
	}
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewDelete().Model((*Version)(nil)).Where("root_folder_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*RootFolder)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		// Titles known only from this folder.
		_, err := tx.NewDelete().Model((*Title)(nil)).
			Where("NOT EXISTS (SELECT 1 FROM versions v WHERE v.title_id = title.id)").Exec(ctx)
		return err
	})
	if err == nil {
		s.logger.Info("root folder removed (files untouched)", "path", r.Path)
		s.onChange()
	}
	return err
}

// checkProfile records a problem under field if the profile doesn't exist
// or isn't meant for mediaType (an unknown mediaType skips that second check;
// the caller reports it).
func (s *Service) checkProfile(ctx context.Context, id int64, mediaType, field string, errs *validation.Errors) error {
	p, err := s.profile(ctx, id)
	if errors.Is(err, ErrNotFound) {
		errs.Add(field, "Choose a quality profile.")
		return nil
	}
	if err != nil {
		return err
	}
	if (mediaType == Movie || mediaType == Series) && !fits(p.AppliesTo, mediaType) {
		errs.Add(field, fmt.Sprintf("%s isn't for %s. Choose a profile that applies to %s.", p.Name, mediaWord(mediaType), mediaWord(mediaType)))
	}
	return nil
}

// within reports whether path is inside dir (or equal to it).
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Folder is a directory entry for the folder picker.
type Folder struct {
	Name string
	Path string
}

// BrowseFolders lists the sub-folders of dir, for choosing a root folder.
// Hidden folders are left out.
func BrowseFolders(dir string) ([]Folder, error) {
	if dir == "" {
		dir = "/"
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return nil, errors.New("use a full path")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Folder
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			out = append(out, Folder{Name: e.Name(), Path: full})
		}
	}
	return out, nil
}
