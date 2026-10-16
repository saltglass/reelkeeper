package api

import (
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// requireKind answers 404 (and returns false) unless the ID is a title of
// the given kind. A series ID on a movie endpoint, or the other way round,
// is "not found" like any other unknown ID.
func (h *Handlers) requireKind(w http.ResponseWriter, r *http.Request, id int64, kind string) bool {
	got, err := h.Library.MediaTypeOf(r.Context(), id)
	if err == nil && got != kind {
		err = library.ErrNotFound
	}
	if err != nil {
		h.writeLibraryError(w, r, err)
		return false
	}
	return true
}

// writeTMDBOrLibraryError is for calls that ask TMDB: a missing series is
// 404, anything else that goes wrong is reported as TMDB's problem (502),
// the way refreshing a movie does.
func (h *Handlers) writeTMDBOrLibraryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, library.ErrNotFound) {
		h.writeLibraryError(w, r, err)
		return
	}
	if _, ok := validation.As(err); ok {
		h.writeLibraryError(w, r, err)
		return
	}
	writeError(w, http.StatusBadGateway, "tmdb", tmdbMessage(err), nil)
}

// GetSeriesList lists every series.
func (h *Handlers) GetSeriesList(w http.ResponseWriter, r *http.Request) {
	list, err := h.Library.SeriesList(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.SeriesSummary, 0, len(list))
	for _, s := range list {
		out = append(out, toSeriesSummary(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetSeries returns one series with its seasons, episodes and files.
func (h *Handlers) GetSeries(w http.ResponseWriter, r *http.Request, id int64) {
	d, err := h.Library.SeriesDetailByID(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.seriesDetail(r.Context(), d))
}

// AddSeries adds a series by TMDB ID.
func (h *Handlers) AddSeries(w http.ResponseWriter, r *http.Request) {
	var body gen.AddSeriesJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	monitored := body.Monitored == nil || *body.Monitored
	d, err := h.Library.AddSeries(r.Context(), body.TmdbId, body.ProfileId, body.RootFolderId, monitored)
	if errors.Is(err, library.ErrInUse) {
		writeError(w, http.StatusConflict, "in_library", "That series is already in the library with this profile.", nil)
		return
	}
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	for _, v := range d.Versions {
		if v.ProfileID == body.ProfileId {
			h.searchOnAddTV(r.Context(), v.ID)
		}
	}
	writeJSON(w, http.StatusOK, h.seriesDetail(r.Context(), d))
}

// LookupSeries searches TMDB for series.
func (h *Handlers) LookupSeries(w http.ResponseWriter, r *http.Request, params gen.LookupSeriesParams) {
	res, err := h.Library.LookupSeries(r.Context(), params.Q)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tmdb", tmdbMessage(err), nil)
		return
	}
	out := make([]gen.SeriesLookupResult, 0, len(res))
	for _, x := range res {
		lr := gen.SeriesLookupResult{TmdbId: x.ID, Title: x.Name, Year: x.Year(), Overview: x.Overview, PosterPath: x.PosterPath, InLibrary: x.SeriesID != 0}
		if x.SeriesID != 0 {
			id := x.SeriesID
			lr.SeriesId = &id
		}
		out = append(out, lr)
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateSeries changes whether a series is named by episode or by date.
func (h *Handlers) UpdateSeries(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.UpdateSeriesJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if err := h.Library.SetSeriesType(r.Context(), id, string(body.SeriesType)); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	h.writeSeriesDetail(w, r, id)
}

// RefreshSeries re-fetches a series' details and episodes.
func (h *Handlers) RefreshSeries(w http.ResponseWriter, r *http.Request, id int64) {
	d, err := h.Library.RefreshSeries(r.Context(), id)
	if err != nil {
		h.writeTMDBOrLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.seriesDetail(r.Context(), d))
}

// GetEpisodeOrders lists the episode orders a series could follow.
func (h *Handlers) GetEpisodeOrders(w http.ResponseWriter, r *http.Request, id int64) {
	orders, err := h.Library.EpisodeOrders(r.Context(), id)
	if err != nil {
		h.writeTMDBOrLibraryError(w, r, err)
		return
	}
	out := make([]gen.EpisodeOrder, 0, len(orders))
	for _, o := range orders {
		out = append(out, gen.EpisodeOrder{
			Id: o.ID, Name: o.Name, Type: o.Type, Description: o.Description,
			Episodes: o.Episodes, Fits: o.Fits, Problems: o.Problems, Current: o.Current,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// SetEpisodeOrder chooses the order a series' files follow.
func (h *Handlers) SetEpisodeOrder(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.SetEpisodeOrderJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if err := h.Library.SetEpisodeOrder(r.Context(), id, body.EpisodeOrder); err != nil {
		h.writeTMDBOrLibraryError(w, r, err)
		return
	}
	h.writeSeriesDetail(w, r, id)
}

func (h *Handlers) writeSeriesDetail(w http.ResponseWriter, r *http.Request, id int64) {
	d, err := h.Library.SeriesDetailByID(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.seriesDetail(r.Context(), d))
}

// GetSeriesRename shows what renaming the episode files would do.
func (h *Handlers) GetSeriesRename(w http.ResponseWriter, r *http.Request, id int64) {
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	plan, err := h.Library.RenamePlan(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRenames(plan))
}

// RenameSeries renames the episode files (in managed media folders only).
func (h *Handlers) RenameSeries(w http.ResponseWriter, r *http.Request, id int64) {
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	plan, err := h.Library.Rename(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	h.tellRenamed(r, id, plan)
	writeJSON(w, http.StatusOK, toRenames(plan))
}

// RemoveSeriesVersion removes a version without files.
func (h *Handlers) RemoveSeriesVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	h.removeVersion(w, r, id, versionID)
}

// --- Mapping ----------------------------------------------------------------

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toSeriesSummary(s library.SeriesView) gen.SeriesSummary {
	versions := make([]gen.VersionSummary, 0, len(s.Versions))
	for _, v := range s.Versions {
		versions = append(versions, toVersionSummary(v))
	}
	return gen.SeriesSummary{
		Id: s.ID, TmdbId: s.TMDBID, TvdbId: s.TVDBID, ImdbId: s.IMDbID, Title: s.Title.Title, SortTitle: s.SortTitle,
		Year: s.Year, Status: s.Status, Network: s.Network, SeriesType: gen.SeriesType(s.SeriesType),
		Genres: orEmpty(s.GenreList()), PosterPath: s.PosterPath, Seasons: s.Seasons, AiredEpisodes: s.Aired,
		Problems: s.Problems, AddedAt: s.CreatedAt, Versions: versions,
	}
}

func toSeriesDetail(d library.SeriesDetail) gen.SeriesDetail {
	out := gen.SeriesDetail{
		Id: d.ID, TmdbId: d.TMDBID, TvdbId: d.TVDBID, ImdbId: d.IMDbID, Title: d.Title.Title, SortTitle: d.SortTitle,
		OriginalTitle: d.OriginalTitle, Year: d.Year, Overview: d.Overview, Runtime: d.Runtime, Status: d.Status,
		Network: d.Network, SeriesType: gen.SeriesType(d.SeriesType), Genres: orEmpty(d.GenreList()),
		Certification: d.Certification, FirstAirDate: d.ReleaseDate, LastAirDate: d.LastAirDate,
		PosterPath: d.PosterPath, BackdropPath: d.BackdropPath, VoteAverage: float32(d.VoteAverage),
		EpisodeOrder: d.EpisodeOrder, EpisodeOrderName: d.EpisodeOrderName,
		Seasons: d.Seasons, AiredEpisodes: d.Aired, Problems: d.Problems,
		MetadataUpdatedAt: d.MetadataUpdatedAt, AddedAt: d.CreatedAt,
		Versions:   make([]gen.VersionDetail, 0, len(d.Versions)),
		SeasonList: make([]gen.SeasonDetail, 0, len(d.SeasonList)),
		Unordered:  make([]gen.EpisodeDetail, 0, len(d.Unordered)),
	}
	for _, v := range d.Versions {
		out.Versions = append(out.Versions, toVersionDetail(v))
	}
	for _, se := range d.SeasonList {
		gs := gen.SeasonDetail{
			Number: se.Number, Name: se.Name, Overview: se.Overview, AirDate: se.AirDate, PosterPath: se.PosterPath,
			Episodes: make([]gen.EpisodeDetail, 0, len(se.Episodes)),
		}
		for _, e := range se.Episodes {
			gs.Episodes = append(gs.Episodes, toEpisodeDetail(e))
		}
		out.SeasonList = append(out.SeasonList, gs)
	}
	for _, e := range d.Unordered {
		out.Unordered = append(out.Unordered, toEpisodeDetail(e))
	}
	return out
}

func toEpisodeDetail(e library.EpisodeView) gen.EpisodeDetail {
	files := e.FileIDs
	if files == nil {
		files = []int64{}
	}
	return gen.EpisodeDetail{
		Id: e.ID, TmdbId: e.TMDBID, Season: e.Season, Number: e.Number,
		AiredSeason: e.AiredSeason, AiredNumber: e.AiredNumber, Title: e.Name, Overview: e.Overview,
		AirDate: e.AirDate, Runtime: e.Runtime, StillPath: e.StillPath, EpisodeType: e.EpisodeType, FileIds: files,
	}
}
