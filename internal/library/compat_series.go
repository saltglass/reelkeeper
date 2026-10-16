package library

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/saltglass/reelkeeper/internal/metadata"
)

// Queries and changes for the Sonarr-compatible API (internal/compat):
// each Seerr "Sonarr" is one quality profile, and shows that profile's
// version of each series.

// ProfileSeries is one series version with everything about it.
type ProfileSeries struct {
	SeriesVersion
	Root RootFolder
	// Files are the version's present files, and FileEpisodes the episodes
	// each holds (by file ID; a double episode holds two).
	Files        []MediaFile
	FileEpisodes map[int64][]int64
	Tags         []int64
}

// ProfileSeriesFilter narrows ProfileSeries.
type ProfileSeriesFilter struct {
	TVDBID    int   // 0: any
	TMDBID    int   // 0: any
	VersionID int64 // 0: any
}

// ProfileSeries lists a profile's series versions, by title.
func (s *Service) ProfileSeries(ctx context.Context, profileID int64, f ProfileSeriesFilter) ([]ProfileSeries, error) {
	var ids []int64
	q := s.db.DB.NewSelect().Model((*Version)(nil)).ColumnExpr("version.id").
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.profile_id = ? AND t.media_type = ?", profileID, Series).
		OrderExpr("t.sort_title, version.id")
	if f.TVDBID != 0 {
		q = q.Where("t.tvdb_id = ?", f.TVDBID)
	}
	if f.TMDBID != 0 {
		q = q.Where("t.tmdb_id = ?", f.TMDBID)
	}
	if f.VersionID != 0 {
		q = q.Where("version.id = ?", f.VersionID)
	}
	if err := q.Scan(ctx, &ids); err != nil || len(ids) == 0 {
		return nil, err
	}
	svs, err := s.SeriesVersions(ctx, ids...)
	if err != nil {
		return nil, err
	}
	byVersion := map[int64]SeriesVersion{}
	for _, sv := range svs {
		byVersion[sv.Version.ID] = sv
	}
	var files []MediaFile
	if err := s.db.DB.NewSelect().Model(&files).Where("version_id IN (?)", bunList(ids)).
		Where("missing_since IS NULL").OrderExpr("id").Scan(ctx); err != nil {
		return nil, err
	}
	filesOf := map[int64][]MediaFile{}
	var fileIDs []int64
	for _, mf := range files {
		filesOf[mf.VersionID] = append(filesOf[mf.VersionID], mf)
		fileIDs = append(fileIDs, mf.ID)
	}
	episodesOf := map[int64][]int64{}
	if len(fileIDs) > 0 {
		var links []EpisodeFile
		if err := s.db.DB.NewSelect().Model(&links).Where("media_file_id IN (?)", bunList(fileIDs)).
			OrderExpr("media_file_id, episode_id").Scan(ctx); err != nil {
			return nil, err
		}
		for _, l := range links {
			episodesOf[l.MediaFileID] = append(episodesOf[l.MediaFileID], l.EpisodeID)
		}
	}
	roots, err := s.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	rootByID := map[int64]RootFolder{}
	for _, r := range roots {
		rootByID[r.ID] = r
	}
	tags, err := s.VersionTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileSeries, 0, len(ids))
	for _, id := range ids {
		sv, ok := byVersion[id]
		if !ok {
			continue
		}
		ps := ProfileSeries{SeriesVersion: sv, Root: rootByID[sv.Version.RootFolderID], Files: filesOf[id],
			FileEpisodes: map[int64][]int64{}, Tags: tags[id]}
		for _, mf := range ps.Files {
			ps.FileEpisodes[mf.ID] = episodesOf[mf.ID]
		}
		out = append(out, ps)
	}
	return out, nil
}

// SeriesTMDBID finds a series' TMDB ID from another site's ID ("tvdb" or
// "imdb"): Seerr names series by their TVDB ID. The library is asked
// first, then TMDB.
func (s *Service) SeriesTMDBID(ctx context.Context, source, id string) (int, error) {
	column := map[string]string{"tvdb": "tvdb_id", "imdb": "imdb_id"}[source]
	if column == "" || id == "" || id == "0" {
		return 0, ErrNotFound
	}
	var tmdbID int
	q := s.db.DB.NewSelect().Model((*Title)(nil)).Column("tmdb_id").Where("media_type = ?", Series).Limit(1)
	if source == "tvdb" {
		q = q.Where("tvdb_id = ?", atoi(id))
	} else {
		q = q.Where("imdb_id = ?", id)
	}
	err := q.Scan(ctx, &tmdbID)
	if err == nil {
		return tmdbID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var r *metadata.SeriesResult
	if source == "tvdb" {
		r, err = s.tmdb.FindSeriesByTVDB(ctx, atoi(id))
	} else {
		r, err = s.tmdb.FindSeriesByIMDb(ctx, id)
	}
	if errors.Is(err, metadata.ErrNotFound) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return r.ID, nil
}

// SeriesPreview is a series as a lookup shows it: its details and its
// episodes (sorted by season and number).
type SeriesPreview struct {
	Title    Title
	Episodes []Episode
}

// SeriesPreview fetches a series for a lookup: from the library when
// Reelkeeper has it (in the series' chosen order), otherwise from TMDB, in
// aired order, without adding anything.
func (s *Service) SeriesPreview(ctx context.Context, tmdbID int) (*SeriesPreview, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("media_type = ? AND tmdb_id = ?", Series, tmdbID).Scan(ctx)
	switch {
	case err == nil:
		var eps []Episode
		if err := s.db.DB.NewSelect().Model(&eps).Where("title_id = ?", t.ID).
			Where("season IS NOT NULL AND number IS NOT NULL").OrderExpr("season, number").Scan(ctx); err != nil {
			return nil, err
		}
		return &SeriesPreview{Title: *t, Episodes: eps}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	m, err := s.tmdb.Series(ctx, tmdbID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p := &SeriesPreview{Title: *titleFromSeries(m, s.now().UTC())}
	for _, season := range m.Seasons {
		for _, e := range season.Episodes {
			p.Episodes = append(p.Episodes, Episode{TMDBID: e.ID, AiredSeason: e.Season, AiredNumber: e.Number,
				Season: ptr(e.Season), Number: ptr(e.Number), Name: e.Name, Overview: e.Overview, AirDate: e.AirDate,
				Runtime: e.Runtime, StillPath: e.StillPath, EpisodeType: e.Type})
		}
	}
	sort.SliceStable(p.Episodes, func(i, j int) bool {
		a, b := p.Episodes[i], p.Episodes[j]
		if *a.Season != *b.Season {
			return *a.Season < *b.Season
		}
		return *a.Number < *b.Number
	})
	return p, nil
}

// SeriesMonitoring is a change to what a series version wants, put the way
// Sonarr (and so Seerr) describes it. Nil fields stay as they are.
type SeriesMonitoring struct {
	// Monitored turns the whole version on or off.
	Monitored *bool
	// Seasons turns every episode of these seasons on or off (in the
	// series' chosen order).
	Seasons map[int]bool
	// NewEpisodes is Sonarr's "monitor new items": true wants episodes
	// airing from today on (an existing rule that already wants them, such
	// as "all", is kept); false turns that rule off, leaving only what's
	// chosen per season or episode.
	NewEpisodes *bool
}

// ApplySeriesMonitoring makes one change to a series version's monitoring,
// all at once.
func (s *Service) ApplySeriesMonitoring(ctx context.Context, versionID int64, m SeriesMonitoring) error {
	var v Version
	if err := s.db.DB.NewSelect().Model(&v).Where("id = ?", versionID).Scan(ctx); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	err := s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if m.Monitored != nil {
			if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("monitored = ?", *m.Monitored).Where("id = ?", versionID).Exec(ctx); err != nil {
				return err
			}
		}
		if m.NewEpisodes != nil {
			var from *string
			switch {
			case !*m.NewEpisodes:
			case v.MonitorFrom != nil:
				from = v.MonitorFrom
			default:
				from = ptr(s.today())
			}
			if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("monitor_from = ?", from).Where("id = ?", versionID).Exec(ctx); err != nil {
				return err
			}
		}
		seasons := make([]int, 0, len(m.Seasons))
		for n := range m.Seasons {
			seasons = append(seasons, n)
		}
		sort.Ints(seasons)
		for _, n := range seasons {
			var ids []int64
			if err := tx.NewSelect().Model((*Episode)(nil)).Column("id").
				Where("title_id = ? AND season = ?", v.TitleID, n).Scan(ctx, &ids); err != nil {
				return err
			}
			if len(ids) == 0 {
				continue
			}
			rows := make([]EpisodeMonitoring, len(ids))
			for i, id := range ids {
				rows[i] = EpisodeMonitoring{VersionID: versionID, EpisodeID: id, Monitored: m.Seasons[n]}
			}
			if _, err := tx.NewInsert().Model(&rows).
				On("CONFLICT (version_id, episode_id) DO UPDATE").Set("monitored = EXCLUDED.monitored").Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.onChange()
	}
	return err
}
