package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Series: a title with seasons and episodes from TMDB. Episodes are
// numbered in the series' chosen order (TMDB's aired order unless the user
// picks another), and files are linked to episodes by placeFiles.

// ensureSeries returns the title for a TMDB series, fetching its details
// and episodes the first time.
func (s *Service) ensureSeries(ctx context.Context, tmdbID int) (*Title, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("media_type = ? AND tmdb_id = ?", Series, tmdbID).Scan(ctx)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	m, err := s.tmdb.Series(ctx, tmdbID)
	if stop := stopping(err); stop != nil {
		return nil, stop
	}
	if err != nil {
		return nil, err
	}
	t = titleFromSeries(m, s.now().UTC())
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewInsert().Model(t).Exec(ctx); err != nil {
			return err
		}
		return syncEpisodes(ctx, tx, t.ID, m)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func titleFromSeries(m *metadata.Series, now time.Time) *Title {
	t := &Title{
		MediaType: Series, TMDBID: m.ID, IMDbID: m.IMDbID, TVDBID: m.TVDBID, Title: m.Name,
		OriginalTitle: m.OriginalName, OriginalLanguage: m.OriginalLanguage, SortTitle: sortTitle(m.Name), Year: m.Year(), Overview: m.Overview,
		Runtime: m.Runtime, Status: m.Status, Genres: encodeList(m.Genres), Certification: m.Certification,
		ReleaseDate: m.FirstAirDate, LastAirDate: m.LastAirDate, PosterPath: m.PosterPath,
		BackdropPath: m.BackdropPath, Popularity: m.Popularity, VoteAverage: m.VoteAverage,
		AltTitles: encodeList(m.AlternativeTitles), MetadataUpdatedAt: &now, CreatedAt: now,
		SeriesType: "standard",
	}
	if len(m.Networks) > 0 {
		t.Network = m.Networks[0]
	}
	// Talk and news shows are named by air date, not by episode number.
	if m.Type == "Talk Show" || m.Type == "News" {
		t.SeriesType = "daily"
	}
	return t
}

// syncEpisodes brings a series' seasons and episodes in line with TMDB:
// new ones are added, changed ones updated, and ones TMDB no longer has
// removed (their file links go with them; placeFiles re-links). New
// episodes get their aired numbers; applyOrder renumbers them if the
// series follows another order.
func syncEpisodes(ctx context.Context, db bunIDB, titleID int64, m *metadata.Series) error {
	var have []Episode
	if err := db.NewSelect().Model(&have).Where("title_id = ?", titleID).Scan(ctx); err != nil {
		return err
	}
	byTMDB := make(map[int]*Episode, len(have))
	for i := range have {
		byTMDB[have[i].TMDBID] = &have[i]
	}
	keep := map[int]bool{}
	for _, season := range m.Seasons {
		row := &Season{TitleID: titleID, SeasonNumber: season.Number, Name: season.Name, Overview: season.Overview,
			AirDate: season.AirDate, PosterPath: season.PosterPath}
		if _, err := db.NewInsert().Model(row).
			On("CONFLICT (title_id, season_number) DO UPDATE").
			Set("name = EXCLUDED.name, overview = EXCLUDED.overview, air_date = EXCLUDED.air_date, poster_path = EXCLUDED.poster_path").
			Exec(ctx); err != nil {
			return err
		}
		for _, e := range season.Episodes {
			keep[e.ID] = true
			row := byTMDB[e.ID]
			if row == nil {
				row = &Episode{TitleID: titleID, TMDBID: e.ID, Season: ptr(e.Season), Number: ptr(e.Number)}
			}
			row.AiredSeason, row.AiredNumber = e.Season, e.Number
			row.Name, row.Overview, row.AirDate, row.Runtime = e.Name, e.Overview, e.AirDate, e.Runtime
			row.StillPath, row.EpisodeType = e.StillPath, e.Type
			var err error
			if row.ID == 0 {
				_, err = db.NewInsert().Model(row).Exec(ctx)
			} else {
				_, err = db.NewUpdate().Model(row).WherePK().
					Column("aired_season", "aired_number", "name", "overview", "air_date", "runtime", "still_path", "episode_type").Exec(ctx)
			}
			if err != nil {
				return err
			}
		}
	}
	var gone []int64
	for _, e := range have {
		if !keep[e.TMDBID] {
			gone = append(gone, e.ID)
		}
	}
	if len(gone) > 0 {
		if _, err := db.NewDelete().Model((*Episode)(nil)).Where("id IN (?)", bunList(gone)).Exec(ctx); err != nil {
			return err
		}
	}
	// Seasons TMDB dropped.
	var numbers []int
	for _, season := range m.Seasons {
		numbers = append(numbers, season.Number)
	}
	q := db.NewDelete().Model((*Season)(nil)).Where("title_id = ?", titleID)
	if len(numbers) > 0 {
		q = q.Where("season_number NOT IN (?)", bunList(numbers))
	}
	_, err := q.Exec(ctx)
	return err
}

func ptr[T any](v T) *T { return &v }

// applyOrder numbers a series' episodes by an order: aired (groupID "")
// or a TMDB episode group. Episodes the group leaves out get no numbers.
func applyOrder(ctx context.Context, db bunIDB, titleID int64, group *metadata.EpisodeGroup) error {
	if group == nil {
		_, err := db.NewUpdate().Model((*Episode)(nil)).
			Set("season = aired_season").Set("number = aired_number").
			Where("title_id = ?", titleID).Exec(ctx)
		return err
	}
	if _, err := db.NewUpdate().Model((*Episode)(nil)).Set("season = NULL").Set("number = NULL").
		Where("title_id = ?", titleID).Exec(ctx); err != nil {
		return err
	}
	for _, ge := range group.Episodes {
		if _, err := db.NewUpdate().Model((*Episode)(nil)).Set("season = ?", ge.Season).Set("number = ?", ge.Number).
			Where("title_id = ? AND tmdb_id = ?", titleID, ge.TMDBID).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// orderLabel names an order in notes: "TMDB's aired order", "the DVD Order".
func orderLabel(t *Title) string {
	if t.EpisodeOrder == "" {
		return "TMDB's aired order"
	}
	name := t.EpisodeOrderName
	if name == "" {
		name = "chosen episode order"
	}
	if !strings.HasPrefix(strings.ToLower(name), "the ") {
		name = "the " + name
	}
	return name
}

// placeFiles links every file of a series to its episode(s) and stores
// each file's note. Only links and notes change; files are never touched.
func (s *Service) placeFiles(ctx context.Context, t *Title) error {
	var eps []Episode
	if err := s.db.DB.NewSelect().Model(&eps).Where("title_id = ?", t.ID).Scan(ctx); err != nil {
		return err
	}
	var files []MediaFile
	err := s.db.DB.NewSelect().Model(&files).Column("id", "episode_claim", "match_note").
		Where("version_id IN (SELECT id FROM versions WHERE title_id = ?)", t.ID).Scan(ctx)
	if err != nil || len(files) == 0 {
		return err
	}
	idx := indexEpisodes(eps, chosenNumbers, orderLabel(t))
	ids := make([]int64, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	return s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewDelete().Model((*EpisodeFile)(nil)).Where("media_file_id IN (?)", bunList(ids)).Exec(ctx); err != nil {
			return err
		}
		var links []EpisodeFile
		for _, f := range files {
			p := idx.place(f.Claim())
			for _, id := range p.Episodes {
				links = append(links, EpisodeFile{EpisodeID: id, MediaFileID: f.ID})
			}
			if p.Note != f.MatchNote {
				if _, err := tx.NewUpdate().Model((*MediaFile)(nil)).Set("match_note = ?", p.Note).
					Where("id = ?", f.ID).Exec(ctx); err != nil {
					return err
				}
			}
		}
		if len(links) == 0 {
			return nil
		}
		_, err := tx.NewInsert().Model(&links).Exec(ctx)
		return err
	})
}

// OrderOption is one episode order a series could follow, and how well
// its files fit it.
type OrderOption struct {
	ID          string // "" for TMDB's aired order
	Name        string
	Type        string // "DVD", "Absolute"...; "" for aired
	Description string
	Episodes    int  // episodes the order places
	Fits        int  // files placed without a problem
	Problems    int  // files with a note
	Current     bool // the order the series follows now
}

// EpisodeOrders lists the orders a series could follow (TMDB's aired order
// and every episode group TMDB has for it), each with how many of the
// series' files fit it. Nothing is changed.
func (s *Service) EpisodeOrders(ctx context.Context, titleID int64) ([]OrderOption, error) {
	t, err := s.series(ctx, titleID)
	if err != nil {
		return nil, err
	}
	m, err := s.tmdb.Series(ctx, t.TMDBID)
	if err != nil {
		return nil, err
	}
	var eps []Episode
	if err := s.db.DB.NewSelect().Model(&eps).Where("title_id = ?", t.ID).Scan(ctx); err != nil {
		return nil, err
	}
	var files []MediaFile
	if err := s.db.DB.NewSelect().Model(&files).Column("id", "episode_claim").
		Where("version_id IN (SELECT id FROM versions WHERE title_id = ?)", t.ID).
		Where("missing_since IS NULL").Scan(ctx); err != nil {
		return nil, err
	}

	score := func(o *OrderOption, idx *episodeIndex) {
		o.Episodes = len(idx.ordered)
		for _, f := range files {
			if p := idx.place(f.Claim()); p.Note == "" && len(p.Episodes) > 0 {
				o.Fits++
			} else {
				o.Problems++
			}
		}
	}

	aired := OrderOption{Name: "Aired order (TMDB's default)", Current: t.EpisodeOrder == ""}
	score(&aired, indexEpisodes(eps, func(e *Episode) (*int, *int) { return &e.AiredSeason, &e.AiredNumber }, "TMDB's aired order"))
	out := []OrderOption{aired}
	for _, info := range m.EpisodeGroups {
		g, err := s.tmdb.EpisodeGroup(ctx, info.ID)
		if err != nil {
			s.logger.Warn("couldn't fetch an episode order", "series", t.Title, "order", info.Name, "error", err)
			continue
		}
		numbers := map[int]epKey{}
		for _, ge := range g.Episodes {
			numbers[ge.TMDBID] = epKey{ge.Season, ge.Number}
		}
		o := OrderOption{ID: info.ID, Name: info.Name, Type: info.TypeName(), Description: info.Description, Current: t.EpisodeOrder == info.ID}
		score(&o, indexEpisodes(eps, func(e *Episode) (*int, *int) {
			k, ok := numbers[e.TMDBID]
			if !ok {
				return nil, nil
			}
			return ptr(k.season), ptr(k.number)
		}, "the "+info.Name))
		out = append(out, o)
	}
	return out, nil
}

// SetEpisodeOrder makes a series follow an order: "" for TMDB's aired
// order, or one of its TMDB episode groups. Files are re-linked to
// episodes by their numbers in the new order; no file is renamed or moved.
func (s *Service) SetEpisodeOrder(ctx context.Context, titleID int64, groupID string) error {
	t, err := s.series(ctx, titleID)
	if err != nil {
		return err
	}
	var group *metadata.EpisodeGroup
	name := ""
	if groupID != "" {
		if group, err = s.tmdb.EpisodeGroup(ctx, groupID); err != nil {
			if errors.Is(err, metadata.ErrNotFound) {
				return validation.Errors{"episodeOrder": "TMDB doesn't have that episode order."}
			}
			return err
		}
		name = group.Name
	}
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if err := applyOrder(ctx, tx, t.ID, group); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*Title)(nil)).Set("episode_order = ?", groupID).Set("episode_order_name = ?", name).
			Where("id = ?", t.ID).Exec(ctx)
		return err
	})
	if err != nil {
		return err
	}
	t.EpisodeOrder, t.EpisodeOrderName = groupID, name
	if err := s.placeFiles(ctx, t); err != nil {
		return err
	}
	s.logger.Info("episode order changed", "series", t.Title, "order", orderLabel(t))
	s.onChange()
	return nil
}

// refreshSeries fetches a series' latest details and episodes, keeps its
// chosen order, and re-links its files.
func (s *Service) refreshSeries(ctx context.Context, t *Title) error {
	m, err := s.tmdb.Series(ctx, t.TMDBID)
	if err != nil {
		return err
	}
	fresh := titleFromSeries(m, s.now().UTC())
	fresh.ID, fresh.CreatedAt = t.ID, t.CreatedAt
	// The user's choices survive a refresh.
	fresh.SeriesType, fresh.EpisodeOrder, fresh.EpisodeOrderName = t.SeriesType, t.EpisodeOrder, t.EpisodeOrderName

	var group *metadata.EpisodeGroup
	if t.EpisodeOrder != "" {
		group, err = s.tmdb.EpisodeGroup(ctx, t.EpisodeOrder)
		if err != nil {
			// The order may have been deleted on TMDB. Keep the numbers
			// the episodes have rather than switching order silently.
			s.logger.Warn("couldn't fetch the series' episode order; keeping the current numbers",
				"series", t.Title, "order", t.EpisodeOrderName, "error", err)
			group = nil
		}
	}
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewUpdate().Model(fresh).WherePK().ExcludeColumn("id", "created_at").Exec(ctx); err != nil {
			return err
		}
		if err := syncEpisodes(ctx, tx, t.ID, m); err != nil {
			return err
		}
		if t.EpisodeOrder == "" || group != nil {
			return applyOrder(ctx, tx, t.ID, group)
		}
		return nil
	})
	if err != nil {
		return err
	}
	*t = *fresh
	return s.placeFiles(ctx, t)
}

// series loads a series title.
func (s *Service) series(ctx context.Context, id int64) (*Title, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("id = ? AND media_type = ?", id, Series).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func encodeClaim(c *EpisodeClaim) string {
	if c == nil {
		return "{}"
	}
	b, _ := json.Marshal(c)
	return string(b)
}
