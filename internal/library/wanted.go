package library

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// MovieVersion is a monitored movie version with what automatic searching
// needs to know: the movie, the version and the files it has now.
type MovieVersion struct {
	Version Version
	Title   Title
	// Files are the version's files that are present (missing ones are
	// left out: they can't be watched, so they don't count as having the
	// movie).
	Files []MediaFile
}

// MonitoredMovieVersions lists every monitored movie version, or just the
// given ones (monitored or not, for "Search now"). Whether a version still
// wants something is the decision engine's call, since it depends on the
// profile.
func (s *Service) MonitoredMovieVersions(ctx context.Context, ids ...int64) ([]MovieVersion, error) {
	var versions []Version
	q := s.db.DB.NewSelect().Model(&versions).
		Join("JOIN titles t ON t.id = version.title_id").
		Where("t.media_type = ?", Movie).
		OrderExpr("version.id")
	if len(ids) > 0 {
		q = q.Where("version.id IN (?)", bunList(ids))
	} else {
		q = q.Where("version.monitored = ?", true)
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
	byID := map[int64]Title{}
	for _, t := range titles {
		byID[t.ID] = t
	}
	var files []MediaFile
	if err := s.db.DB.NewSelect().Model(&files).
		Where("version_id IN (?)", bunList(versionIDs)).Where("missing_since IS NULL").
		OrderExpr("id").Scan(ctx); err != nil {
		return nil, err
	}
	filesOf := map[int64][]MediaFile{}
	for _, f := range files {
		filesOf[f.VersionID] = append(filesOf[f.VersionID], f)
	}
	out := make([]MovieVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, MovieVersion{Version: v, Title: byID[v.TitleID], Files: filesOf[v.ID]})
	}
	return out, nil
}

// MarkSearched records when a version was searched.
func (s *Service) MarkSearched(ctx context.Context, versionID int64, at time.Time) error {
	_, err := s.db.DB.NewUpdate().Model((*Version)(nil)).
		Set("last_search_at = ?", at.UTC()).Where("id = ?", versionID).Exec(ctx)
	return err
}

// Upcoming is a movie with a release date coming up that Reelkeeper wants but
// doesn't have yet.
type Upcoming struct {
	TitleID int64
	Title   string
	Year    int
	// Kind is "in cinemas", "digital" or "physical".
	Kind string
	// Date is the release date, "YYYY-MM-DD".
	Date string
}

// UpcomingMovies lists movies with a monitored version that has no file
// yet and a release date from `from` to `to` (both "YYYY-MM-DD", inclusive).
// A movie appears once, with its earliest date in that window. The list is
// sorted by date, then title, and cut to `limit` entries.
func (s *Service) UpcomingMovies(ctx context.Context, from, to string, limit int) ([]Upcoming, error) {
	mvs, err := s.MonitoredMovieVersions(ctx)
	if err != nil {
		return nil, err
	}
	best := map[int64]Upcoming{}
	for _, mv := range mvs {
		if len(mv.Files) > 0 {
			continue
		}
		t := mv.Title
		// Listed in this order, so on a tie the earlier kind wins.
		for _, c := range []struct{ kind, date string }{
			{"in cinemas", t.TheatricalRelease}, {"digital", t.DigitalRelease}, {"physical", t.PhysicalRelease},
		} {
			if len(c.date) < 10 || c.date[:10] < from || c.date[:10] > to {
				continue
			}
			if cur, ok := best[t.ID]; !ok || c.date[:10] < cur.Date {
				best[t.ID] = Upcoming{TitleID: t.ID, Title: t.Title, Year: t.Year, Kind: c.kind, Date: c.date[:10]}
			}
		}
	}
	out := make([]Upcoming, 0, len(best))
	for _, u := range best {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].TitleID < out[j].TitleID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UpcomingEpisode is an episode airing soon that a monitored series
// version wants.
type UpcomingEpisode struct {
	TitleID int64
	Title   string // the series
	Season  int
	Number  int
	Name    string
	// Date is the air date, "YYYY-MM-DD".
	Date string
}

// UpcomingEpisodes lists episodes a monitored series version wants and
// has no file for, airing from `from` to `to` (both "YYYY-MM-DD",
// inclusive). An episode wanted by two versions appears once. The list is
// sorted by date, then series and number, and cut to `limit` entries.
func (s *Service) UpcomingEpisodes(ctx context.Context, from, to string, limit int) ([]UpcomingEpisode, error) {
	svs, err := s.SeriesVersions(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []UpcomingEpisode
	for _, sv := range svs {
		for _, e := range sv.Episodes {
			if seen[e.ID] || !sv.Monitored[e.ID] || sv.HasFile[e.ID] || e.AirDate == "" || e.AirDate < from || e.AirDate > to {
				continue
			}
			seen[e.ID] = true
			out = append(out, UpcomingEpisode{TitleID: sv.Title.ID, Title: sv.Title.Title, Season: *e.Season, Number: *e.Number,
				Name: e.Name, Date: e.AirDate})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Date != b.Date:
			return a.Date < b.Date
		case a.Title != b.Title:
			return a.Title < b.Title
		case a.Season != b.Season:
			return a.Season < b.Season
		}
		return a.Number < b.Number
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// VersionRef says which title a version belongs to and whether it's a
// movie or a series.
type VersionRef struct {
	VersionID int64  `bun:"version_id"`
	TitleID   int64  `bun:"title_id"`
	MediaType string `bun:"media_type"`
}

// VersionRefs looks up versions by id in one query. Ids that don't exist
// are left out of the result.
func (s *Service) VersionRefs(ctx context.Context, ids []int64) (map[int64]VersionRef, error) {
	out := map[int64]VersionRef{}
	if len(ids) == 0 {
		return out, nil
	}
	var refs []VersionRef
	err := s.db.DB.NewSelect().Model((*Version)(nil)).
		ColumnExpr("version.id AS version_id, version.title_id, t.media_type").
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.id IN (?)", bunList(ids)).Scan(ctx, &refs)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		out[r.VersionID] = r
	}
	return out, nil
}

// EpisodeSelection is some episodes of one series version.
type EpisodeSelection struct {
	VersionID  int64
	EpisodeIDs []int64
}

// UnmonitorWanted stops looking for movie versions and for episodes of
// series versions (the Wanted page's "Stop monitoring"). Everything is
// checked before anything changes: ErrNotFound when a version doesn't
// exist, a validation error when a movie id is given as a series version
// or the other way round, or an episode isn't the version's series'. It
// returns how many movie versions and episodes were turned off.
func (s *Service) UnmonitorWanted(ctx context.Context, movies []int64, episodes []EpisodeSelection) (movieCount, episodeCount int, err error) {
	ids := append([]int64{}, movies...)
	for _, e := range episodes {
		ids = append(ids, e.VersionID)
	}
	refs, err := s.VersionRefs(ctx, ids)
	if err != nil {
		return 0, 0, err
	}
	var problems validation.Errors
	for _, id := range ids {
		if _, ok := refs[id]; !ok {
			return 0, 0, ErrNotFound
		}
	}
	for _, id := range movies {
		if refs[id].MediaType != Movie {
			problems.Add("movies", fmt.Sprintf("Version %d is a series, not a movie.", id))
		}
	}
	for _, e := range episodes {
		if refs[e.VersionID].MediaType != Series {
			problems.Add("episodes", fmt.Sprintf("Version %d is a movie, not a series.", e.VersionID))
		}
	}
	if err := problems.Err(); err != nil {
		return 0, 0, err
	}
	// The episodes must belong to their version's series.
	for _, e := range episodes {
		if len(e.EpisodeIDs) == 0 {
			continue
		}
		n, err := s.db.DB.NewSelect().Model((*Episode)(nil)).
			Where("title_id = ?", refs[e.VersionID].TitleID).Where("id IN (?)", bunList(e.EpisodeIDs)).Count(ctx)
		if err != nil {
			return 0, 0, err
		}
		if int(n) != len(uniqueIDs(e.EpisodeIDs)) {
			problems.Add("episodes", fmt.Sprintf("Some episodes aren't part of series version %d.", e.VersionID))
		}
	}
	if err := problems.Err(); err != nil {
		return 0, 0, err
	}
	for _, id := range uniqueIDs(movies) {
		if err := s.SetMonitored(ctx, refs[id].TitleID, id, false); err != nil {
			return movieCount, episodeCount, err
		}
		movieCount++
	}
	for _, e := range episodes {
		unique := uniqueIDs(e.EpisodeIDs)
		if err := s.SetEpisodesMonitored(ctx, e.VersionID, unique, false); err != nil {
			return movieCount, episodeCount, err
		}
		episodeCount += len(unique)
	}
	return movieCount, episodeCount, nil
}

func uniqueIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
