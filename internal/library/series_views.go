package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// SeriesView is a series with its versions and episode counts, for lists.
// Counts are of regular episodes (not Specials) in the series' chosen
// order.
type SeriesView struct {
	Title
	Versions []VersionView
	Seasons  int // regular seasons
	Aired    int // regular episodes that have aired
	Problems int // files with a note (unsure which episode, or a different order)
}

// SeriesList returns every series with its versions and counts, sorted by
// title. A fixed handful of queries, whatever the library's size.
func (s *Service) SeriesList(ctx context.Context) ([]SeriesView, error) {
	var titles []Title
	if err := s.db.DB.NewSelect().Model(&titles).Where("media_type = ?", Series).Order("sort_title").Scan(ctx); err != nil {
		return nil, err
	}
	versions, err := s.versionViews(ctx, nil, false)
	if err != nil {
		return nil, err
	}
	withFiles, err := s.episodesWithFiles(ctx, nil)
	if err != nil {
		return nil, err
	}
	byTitle := map[int64][]VersionView{}
	for _, v := range versions {
		v.EpisodesWithFiles = withFiles[v.ID]
		byTitle[v.TitleID] = append(byTitle[v.TitleID], v)
	}

	var counts []struct {
		TitleID int64 `bun:"title_id"`
		Seasons int   `bun:"seasons"`
		Aired   int   `bun:"aired"`
	}
	err = s.db.DB.NewSelect().Model((*Episode)(nil)).
		ColumnExpr("title_id, COUNT(DISTINCT season) AS seasons").
		ColumnExpr("SUM(CASE WHEN air_date <> '' AND air_date <= ? THEN 1 ELSE 0 END) AS aired", s.today()).
		Where("season > 0 AND number IS NOT NULL").
		Group("title_id").Scan(ctx, &counts)
	if err != nil {
		return nil, err
	}
	var problems []struct {
		TitleID int64 `bun:"title_id"`
		N       int   `bun:"n"`
	}
	err = s.db.DB.NewSelect().Model((*MediaFile)(nil)).
		ColumnExpr("v.title_id, COUNT(*) AS n").
		Join("JOIN versions AS v ON v.id = media_file.version_id").
		Where("media_file.match_note <> ''").Where("media_file.missing_since IS NULL").
		Group("v.title_id").Scan(ctx, &problems)
	if err != nil {
		return nil, err
	}

	out := make([]SeriesView, 0, len(titles))
	idx := map[int64]int{}
	for _, t := range titles {
		idx[t.ID] = len(out)
		out = append(out, SeriesView{Title: t, Versions: byTitle[t.ID]})
	}
	for _, c := range counts {
		if i, ok := idx[c.TitleID]; ok {
			out[i].Seasons, out[i].Aired = c.Seasons, c.Aired
		}
	}
	for _, p := range problems {
		if i, ok := idx[p.TitleID]; ok {
			out[i].Problems = p.N
		}
	}
	return out, nil
}

// episodesWithFiles counts, per version, the regular episodes that have at
// least one present file. titleID limits it to one series.
func (s *Service) episodesWithFiles(ctx context.Context, titleID *int64) (map[int64]int, error) {
	var rows []struct {
		VersionID int64 `bun:"version_id"`
		N         int   `bun:"n"`
	}
	q := s.db.DB.NewSelect().TableExpr("episode_files AS ef").
		ColumnExpr("mf.version_id, COUNT(DISTINCT ef.episode_id) AS n").
		Join("JOIN media_files AS mf ON mf.id = ef.media_file_id").
		Join("JOIN episodes AS e ON e.id = ef.episode_id").
		Where("mf.missing_since IS NULL AND e.season > 0").
		Group("mf.version_id")
	if titleID != nil {
		q = q.Where("e.title_id = ?", *titleID)
	}
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.VersionID] = r.N
	}
	return out, nil
}

// today is the date episodes must have aired by to count as aired: the
// local date (the TZ Reelkeeper runs with), the same day the
// searches and the grabs-today count use. TMDB's air dates are local dates
// too; in UTC, an evening in the Americas is already tomorrow, so "from
// today" would skip that night's episode.
func (s *Service) today() string { return s.now().Local().Format("2006-01-02") }

// SeriesDetail is one series with its versions, files and episodes.
type SeriesDetail struct {
	SeriesView
	SeasonList []SeasonView
	// Unordered are episodes the chosen order leaves out (only with an
	// order other than TMDB's aired one).
	Unordered []EpisodeView
}

// SeasonView is a season in the series' chosen order.
type SeasonView struct {
	Number     int
	Name       string
	Overview   string
	AirDate    string
	PosterPath string
	Episodes   []EpisodeView
}

// EpisodeView is an episode and the files holding it.
type EpisodeView struct {
	Episode
	FileIDs []int64
}

// SeriesDetailByID returns one series with everything its page shows.
func (s *Service) SeriesDetailByID(ctx context.Context, id int64) (*SeriesDetail, error) {
	t, err := s.series(ctx, id)
	if err != nil {
		return nil, err
	}
	versions, err := s.versionViews(ctx, &id, true)
	if err != nil {
		return nil, err
	}
	withFiles, err := s.episodesWithFiles(ctx, &id)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		versions[i].EpisodesWithFiles = withFiles[versions[i].ID]
	}

	var eps []Episode
	if err := s.db.DB.NewSelect().Model(&eps).Where("title_id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	var seasons []Season
	if err := s.db.DB.NewSelect().Model(&seasons).Where("title_id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	var links []EpisodeFile
	if err := s.db.DB.NewSelect().Model(&links).
		Where("episode_id IN (SELECT id FROM episodes WHERE title_id = ?)", id).Scan(ctx); err != nil {
		return nil, err
	}
	files := map[int64][]int64{}
	for _, l := range links {
		files[l.EpisodeID] = append(files[l.EpisodeID], l.MediaFileID)
	}

	d := &SeriesDetail{SeriesView: SeriesView{Title: *t, Versions: versions}}
	meta := map[int]Season{}
	for _, se := range seasons {
		meta[se.SeasonNumber] = se
	}
	bySeason := map[int][]EpisodeView{}
	for _, e := range eps {
		ev := EpisodeView{Episode: e, FileIDs: files[e.ID]}
		if e.Season == nil || e.Number == nil {
			d.Unordered = append(d.Unordered, ev)
			continue
		}
		bySeason[*e.Season] = append(bySeason[*e.Season], ev)
	}
	numbers := make([]int, 0, len(bySeason))
	for n := range bySeason {
		numbers = append(numbers, n)
	}
	// Regular seasons first, Specials last, as Sonarr and Emby show them.
	sort.Slice(numbers, func(i, j int) bool {
		a, b := numbers[i], numbers[j]
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	today := s.today()
	for _, n := range numbers {
		list := bySeason[n]
		sort.Slice(list, func(i, j int) bool { return *list[i].Number < *list[j].Number })
		sv := SeasonView{Number: n, Episodes: list, Name: seasonName(n)}
		// TMDB's season details only fit when the order is TMDB's own.
		if m, ok := meta[n]; ok && t.EpisodeOrder == "" {
			sv.Name, sv.Overview, sv.AirDate, sv.PosterPath = m.Name, m.Overview, m.AirDate, m.PosterPath
		}
		d.SeasonList = append(d.SeasonList, sv)
		if n > 0 {
			d.Seasons++
			for _, e := range list {
				if e.AirDate != "" && e.AirDate <= today {
					d.Aired++
				}
			}
		}
	}
	sort.Slice(d.Unordered, func(i, j int) bool {
		a, b := d.Unordered[i], d.Unordered[j]
		if a.AiredSeason != b.AiredSeason {
			return a.AiredSeason < b.AiredSeason
		}
		return a.AiredNumber < b.AiredNumber
	})
	for _, v := range versions {
		for _, f := range v.MediaFiles {
			if f.MatchNote != "" && f.MissingSince == nil {
				d.Problems++
			}
		}
	}
	return d, nil
}

func seasonName(n int) string {
	if n == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", n)
}

// SeriesLookupResult is a TMDB series search result, marked if already in
// the library.
type SeriesLookupResult struct {
	metadata.SeriesResult
	SeriesID int64 // 0 if not in the library
}

// LookupSeries searches TMDB for series to add. "Doctor Who 2005" searches
// for Doctor Who starting in 2005.
func (s *Service) LookupSeries(ctx context.Context, query string) ([]SeriesLookupResult, error) {
	title, year := splitYear(strings.TrimSpace(query))
	if title == "" {
		return nil, nil
	}
	results, err := s.tmdb.SearchTV(ctx, title, year)
	if err != nil {
		return nil, err
	}
	have := map[int]int64{}
	var rows []Title
	if err := s.db.DB.NewSelect().Model(&rows).Column("id", "tmdb_id").Where("media_type = ?", Series).Scan(ctx); err != nil {
		return nil, err
	}
	for _, r := range rows {
		have[r.TMDBID] = r.ID
	}
	out := make([]SeriesLookupResult, 0, len(results))
	for _, r := range results {
		out = append(out, SeriesLookupResult{SeriesResult: r, SeriesID: have[r.ID]})
	}
	return out, nil
}

// splitYear reads a trailing year off a search: "Dune 2021" → ("Dune", 2021).
func splitYear(query string) (string, int) {
	f := strings.Fields(query)
	if len(f) > 1 {
		if y := f[len(f)-1]; len(y) == 4 && (strings.HasPrefix(y, "19") || strings.HasPrefix(y, "20")) {
			return strings.Join(f[:len(f)-1], " "), atoi(y)
		}
	}
	return query, 0
}

// AddSeries adds a series from TMDB as a version in a TV media folder,
// with no files yet. Its folder name comes from the series folder
// template; nothing is created on disk.
func (s *Service) AddSeries(ctx context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*SeriesDetail, error) {
	var errs validation.Errors
	if tmdbID <= 0 {
		errs.Add("tmdbId", "Choose a series.")
	}
	if err := s.checkProfile(ctx, profileID, Series, "profileId", &errs); err != nil {
		return nil, err
	}
	root, err := s.RootFolder(ctx, rootID)
	switch {
	case errors.Is(err, ErrNotFound):
		errs.Add("rootFolderId", "Choose a media folder.")
	case err != nil:
		return nil, err
	case root.MediaType != Series:
		errs.Add("rootFolderId", "Choose a TV media folder.")
	}
	if errs != nil {
		return nil, errs
	}
	t, err := s.ensureSeries(ctx, tmdbID)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, validation.Errors{"tmdbId": "TMDB doesn't know that series."}
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
	p, err := s.profile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	folder := naming.FormatSeries(s.naming().SeriesFolder, seriesFacts(t, p.Name))
	v := &Version{TitleID: t.ID, ProfileID: profileID, RootFolderID: root.ID, Folder: folder, Monitored: monitored, CreatedAt: s.now().UTC()}
	if monitored {
		// Added on purpose: every episode is wanted, aired or not.
		v.MonitorFrom = ptr(AllEpisodes)
	}
	if _, err := s.db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("series added", "title", t.Title, "year", t.Year, "profile", p.Name)
	s.onChange()
	return s.SeriesDetailByID(ctx, t.ID)
}

// RefreshSeries fetches a series' latest details and episodes from TMDB.
func (s *Service) RefreshSeries(ctx context.Context, id int64) (*SeriesDetail, error) {
	t, err := s.series(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.refreshSeries(ctx, t); err != nil {
		return nil, err
	}
	s.onChange()
	return s.SeriesDetailByID(ctx, id)
}

// SetSeriesType says whether a series is named by episode number
// ("standard") or by air date ("daily", for talk and news shows).
func (s *Service) SetSeriesType(ctx context.Context, id int64, seriesType string) error {
	if seriesType != "standard" && seriesType != "daily" {
		return validation.Errors{"seriesType": "Choose standard or daily."}
	}
	t, err := s.series(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.DB.NewUpdate().Model(t).Set("series_type = ?", seriesType).WherePK().Exec(ctx); err != nil {
		return err
	}
	s.onChange()
	return nil
}

// MediaTypeOf says whether a title is a movie or a series, so callers can
// refuse an ID of the wrong kind (a series ID on a movie endpoint).
func (s *Service) MediaTypeOf(ctx context.Context, id int64) (string, error) {
	var mt string
	err := s.db.DB.NewSelect().Model((*Title)(nil)).Column("media_type").Where("id = ?", id).Scan(ctx, &mt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return mt, err
}
