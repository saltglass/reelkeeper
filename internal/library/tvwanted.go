package library

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// AllEpisodes as a Version.MonitorFrom date wants every episode, however
// old.
const AllEpisodes = "0001-01-01"

// SeriesVersion is a series version with what automatic searching needs:
// the series, the version, its episodes and which of them it wants.
type SeriesVersion struct {
	Version Version
	Title   Title
	// Episodes are every episode in the series' chosen order (specials
	// included), sorted by season and number.
	Episodes []Episode
	// HasFile is the episodes this version has a present file for.
	HasFile map[int64]bool
	// Wanted is the episodes it wants now: monitored, aired, no file.
	// (TV stops at the first acceptable file.)
	Wanted []Episode
	// Searched is when each wanted episode was last searched for.
	Searched map[int64]time.Time
	// Monitored is every episode the version wants, aired or not.
	Monitored map[int64]bool
}

// Monitored reports whether a version wants an episode (aired or not).
func Monitored(v Version, e Episode, override map[int64]bool) bool {
	if m, ok := override[e.ID]; ok {
		return m
	}
	if v.MonitorFrom == nil || e.Season == nil || *e.Season == 0 || e.AirDate == "" {
		return false // specials and undated episodes only through a choice
	}
	return e.AirDate >= *v.MonitorFrom
}

// SeriesVersions lists monitored series versions with their wanted
// episodes, or just the given ones (monitored or not, for "Search now").
func (s *Service) SeriesVersions(ctx context.Context, ids ...int64) ([]SeriesVersion, error) {
	var versions []Version
	q := s.db.DB.NewSelect().Model(&versions).
		Join("JOIN titles t ON t.id = version.title_id").
		Where("t.media_type = ?", Series).
		OrderExpr("version.id")
	if len(ids) > 0 {
		q = q.Where("version.id IN (?)", bunList(ids))
	} else {
		q = q.Where("version.monitored = ?", true)
	}
	if err := q.Scan(ctx); err != nil || len(versions) == 0 {
		return nil, err
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
	titleByID := map[int64]Title{}
	for _, t := range titles {
		titleByID[t.ID] = t
	}
	var episodes []Episode
	if err := s.db.DB.NewSelect().Model(&episodes).Where("title_id IN (?)", bunList(titleIDs)).
		Where("season IS NOT NULL AND number IS NOT NULL").Scan(ctx); err != nil {
		return nil, err
	}
	sort.Slice(episodes, func(i, j int) bool {
		a, b := episodes[i], episodes[j]
		if *a.Season != *b.Season {
			return *a.Season < *b.Season
		}
		return *a.Number < *b.Number
	})
	episodesOf := map[int64][]Episode{}
	for _, e := range episodes {
		episodesOf[e.TitleID] = append(episodesOf[e.TitleID], e)
	}
	// Episodes each version has a present file for.
	var links []struct {
		VersionID int64 `bun:"version_id"`
		EpisodeID int64 `bun:"episode_id"`
	}
	if err := s.db.DB.NewSelect().Table("episode_files").
		ColumnExpr("mf.version_id, episode_files.episode_id").
		Join("JOIN media_files mf ON mf.id = episode_files.media_file_id").
		Where("mf.version_id IN (?)", bunList(versionIDs)).Where("mf.missing_since IS NULL").
		Scan(ctx, &links); err != nil {
		return nil, err
	}
	hasFile := map[int64]map[int64]bool{}
	for _, l := range links {
		if hasFile[l.VersionID] == nil {
			hasFile[l.VersionID] = map[int64]bool{}
		}
		hasFile[l.VersionID][l.EpisodeID] = true
	}
	overrides, err := s.monitoringOverrides(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	var searches []struct {
		VersionID  int64     `bun:"version_id"`
		EpisodeID  int64     `bun:"episode_id"`
		SearchedAt time.Time `bun:"searched_at"`
	}
	if err := s.db.DB.NewSelect().Table("episode_searches").Column("version_id", "episode_id", "searched_at").
		Where("version_id IN (?)", bunList(versionIDs)).Scan(ctx, &searches); err != nil {
		return nil, err
	}
	searched := map[int64]map[int64]time.Time{}
	for _, r := range searches {
		if searched[r.VersionID] == nil {
			searched[r.VersionID] = map[int64]time.Time{}
		}
		searched[r.VersionID][r.EpisodeID] = r.SearchedAt
	}
	today := s.today()
	out := make([]SeriesVersion, 0, len(versions))
	for _, v := range versions {
		sv := SeriesVersion{Version: v, Title: titleByID[v.TitleID], Episodes: episodesOf[v.TitleID], HasFile: hasFile[v.ID],
			Searched: searched[v.ID]}
		if sv.HasFile == nil {
			sv.HasFile = map[int64]bool{}
		}
		if sv.Searched == nil {
			sv.Searched = map[int64]time.Time{}
		}
		sv.Monitored = map[int64]bool{}
		for _, e := range sv.Episodes {
			if !Monitored(v, e, overrides[v.ID]) {
				continue
			}
			sv.Monitored[e.ID] = true
			if aired := e.AirDate != "" && e.AirDate <= today; aired && !sv.HasFile[e.ID] {
				sv.Wanted = append(sv.Wanted, e)
			}
		}
		out = append(out, sv)
	}
	return out, nil
}

// monitoringOverrides returns each version's per-episode choices.
func (s *Service) monitoringOverrides(ctx context.Context, versionIDs []int64) (map[int64]map[int64]bool, error) {
	var rows []EpisodeMonitoring
	if err := s.db.DB.NewSelect().Model(&rows).Where("version_id IN (?)", bunList(versionIDs)).Scan(ctx); err != nil {
		return nil, err
	}
	out := map[int64]map[int64]bool{}
	for _, r := range rows {
		if out[r.VersionID] == nil {
			out[r.VersionID] = map[int64]bool{}
		}
		out[r.VersionID][r.EpisodeID] = r.Monitored
	}
	return out, nil
}

// SetEpisodesMonitored records, for one series version, whether these
// episodes are wanted. It overrides the version's date rule for them.
func (s *Service) SetEpisodesMonitored(ctx context.Context, versionID int64, episodeIDs []int64, monitored bool) error {
	if len(episodeIDs) == 0 {
		return nil
	}
	rows := make([]EpisodeMonitoring, len(episodeIDs))
	for i, id := range episodeIDs {
		rows[i] = EpisodeMonitoring{VersionID: versionID, EpisodeID: id, Monitored: monitored}
	}
	_, err := s.db.DB.NewInsert().Model(&rows).
		On("CONFLICT (version_id, episode_id) DO UPDATE").Set("monitored = EXCLUDED.monitored").Exec(ctx)
	if err == nil {
		s.onChange()
	}
	return err
}

// SetSeasonMonitored turns every episode of one season (in the series'
// chosen order) on or off for a version.
func (s *Service) SetSeasonMonitored(ctx context.Context, versionID int64, season int, monitored bool) error {
	var v Version
	if err := s.db.DB.NewSelect().Model(&v).Where("id = ?", versionID).Scan(ctx); err != nil {
		return err
	}
	var ids []int64
	if err := s.db.DB.NewSelect().Model((*Episode)(nil)).Column("id").
		Where("title_id = ? AND season = ?", v.TitleID, season).Scan(ctx, &ids); err != nil {
		return err
	}
	return s.SetEpisodesMonitored(ctx, versionID, ids, monitored)
}

// Monitoring presets for a whole series version (Sonarr's names).
const (
	MonitorAll    = "all"    // every episode, aired or not
	MonitorFuture = "future" // episodes airing from today
	MonitorNone   = "none"   // nothing until turned on per season or episode
)

// ApplyMonitorPreset sets a version's monitoring to a preset, replacing any
// choices made per season or episode.
func (s *Service) ApplyMonitorPreset(ctx context.Context, versionID int64, preset string) error {
	var from *string
	switch preset {
	case MonitorAll:
		from = ptr(AllEpisodes)
	case MonitorFuture:
		from = ptr(s.today())
	case MonitorNone:
	default:
		return validation.Errors{"monitor": fmt.Sprintf("Choose all, future or none (not %q).", preset)}
	}
	err := s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("monitor_from = ?", from).Where("id = ?", versionID).Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewDelete().Model((*EpisodeMonitoring)(nil)).Where("version_id = ?", versionID).Exec(ctx)
		return err
	})
	if err == nil {
		s.onChange()
	}
	return err
}

// SetMonitorFrom changes the date rule for new episodes ("" turns it off).
func (s *Service) SetMonitorFrom(ctx context.Context, versionID int64, from string) error {
	var val *string
	if from != "" {
		val = &from
	}
	_, err := s.db.DB.NewUpdate().Model((*Version)(nil)).Set("monitor_from = ?", val).Where("id = ?", versionID).Exec(ctx)
	if err == nil {
		s.onChange()
	}
	return err
}

// MarkEpisodesSearched records when episodes were searched for.
func (s *Service) MarkEpisodesSearched(ctx context.Context, versionID int64, episodeIDs []int64, at time.Time) error {
	if len(episodeIDs) == 0 {
		return nil
	}
	type row struct {
		bun.BaseModel `bun:"table:episode_searches"`
		VersionID     int64     `bun:"version_id,pk"`
		EpisodeID     int64     `bun:"episode_id,pk"`
		SearchedAt    time.Time `bun:"searched_at"`
	}
	rows := make([]row, len(episodeIDs))
	for i, id := range episodeIDs {
		rows[i] = row{VersionID: versionID, EpisodeID: id, SearchedAt: at.UTC()}
	}
	_, err := s.db.DB.NewInsert().Model(&rows).
		On("CONFLICT (version_id, episode_id) DO UPDATE").Set("searched_at = EXCLUDED.searched_at").Exec(ctx)
	return err
}

// Today is the date (UTC, "2006-01-02") the library counts episodes as
// aired by.
func (s *Service) Today() string { return s.today() }

// EpisodeOverrides returns a version's per-episode choices (episode ID →
// wanted), the part of Monitored that the date rule doesn't decide.
func (s *Service) EpisodeOverrides(ctx context.Context, versionID int64) (map[int64]bool, error) {
	all, err := s.monitoringOverrides(ctx, []int64{versionID})
	if err != nil {
		return nil, err
	}
	if all[versionID] == nil {
		return map[int64]bool{}, nil
	}
	return all[versionID], nil
}
