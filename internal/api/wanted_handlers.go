package api

import (
	"net/http"
	"slices"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
)

// maxWantedSearches is how many versions one bulk search may name (Dan,
// 2026-10-27: 10, as for bulk edits, so one click doesn't flood the
// Cloudflare solver; the rest are left to the hourly search).
const maxWantedSearches = bulkSearchesNow

// GetWanted lists everything Reelkeeper is still looking for.
func (h *Handlers) GetWanted(w http.ResponseWriter, r *http.Request, params gen.GetWantedParams) {
	countsOnly := params.CountsOnly != nil && *params.CountsOnly
	l, err := h.Grab.Wanted(r.Context(), !countsOnly)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.WantedList{
		Missing:  make([]gen.WantedItem, 0, len(l.Missing)),
		Upgrades: make([]gen.WantedItem, 0, len(l.Upgrades)),
		Counts:   gen.WantedCounts{MissingMovies: l.MissingMovies, MissingEpisodes: l.MissingEpisodes, Upgrades: len(l.Upgrades)},
	}
	if !countsOnly {
		for _, it := range l.Missing {
			out.Missing = append(out.Missing, toWantedItem(it))
		}
		for _, it := range l.Upgrades {
			out.Upgrades = append(out.Upgrades, toWantedItem(it))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func toWantedItem(it grab.WantedItem) gen.WantedItem {
	out := gen.WantedItem{
		Kind: gen.WantedItemKind(it.Kind), TitleId: it.TitleID, VersionId: it.VersionID, Title: it.Title, Year: it.Year,
		PosterPath: it.PosterPath, Profile: it.Profile, Date: it.Date, State: gen.WantedItemState(it.State), Status: it.Status,
		LastSearchedAt: it.LastSearchedAt, LastSearchMessage: it.LastSearchMessage, NextSearchAt: it.NextSearchAt,
		Season: it.Season,
	}
	if it.Quality != "" {
		out.Quality, out.Target = &it.Quality, &it.Target
	}
	if it.Score != nil {
		formats := make([]gen.TestReleaseFormat, len(it.Formats))
		for i, f := range it.Formats {
			formats[i] = gen.TestReleaseFormat{Name: f.Name, Score: f.Score}
		}
		out.Score, out.TargetScore, out.Formats = it.Score, it.TargetScore, &formats
	}
	if it.Kind == grab.WantedEpisodes {
		eps := make([]gen.WantedEpisode, len(it.Episodes))
		for i, e := range it.Episodes {
			eps[i] = gen.WantedEpisode{EpisodeId: e.EpisodeID, Season: e.Season, Number: e.Number, Name: e.Name, AirDate: e.AirDate}
		}
		out.Episodes = &eps
	}
	return out
}

// SearchWanted queues a search for each named version.
func (h *Handlers) SearchWanted(w http.ResponseWriter, r *http.Request) {
	var body gen.SearchWantedJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	ids := slices.Compact(slices.Sorted(slices.Values(body.VersionIds)))
	switch {
	case len(ids) == 0:
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Choose at least one movie or show to search for.",
			map[string]string{"versionIds": "Choose at least one."})
		return
	case len(ids) > maxWantedSearches:
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Search for up to 10 at a time; choose fewer.",
			map[string]string{"versionIds": "At most 10 at a time."})
		return
	}
	refs, err := h.Library.VersionRefs(r.Context(), ids)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	for _, id := range ids {
		if _, ok := refs[id]; !ok {
			h.writeLibraryError(w, r, library.ErrNotFound)
			return
		}
	}
	var res gen.WantedSearchResult
	for _, id := range ids {
		_, created, err := grab.QueueSearch(r.Context(), h.Jobs, id, grab.TriggerManual)
		if err != nil {
			h.writeServiceError(w, r, err)
			return
		}
		if created {
			res.Queued++
		} else {
			res.AlreadyQueued++
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// UnmonitorWanted stops looking for the named movies and episodes.
func (h *Handlers) UnmonitorWanted(w http.ResponseWriter, r *http.Request) {
	var body gen.UnmonitorWantedJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	var movies []int64
	if body.Movies != nil {
		movies = *body.Movies
	}
	var episodes []library.EpisodeSelection
	if body.Episodes != nil {
		for _, e := range *body.Episodes {
			episodes = append(episodes, library.EpisodeSelection{VersionID: e.VersionId, EpisodeIDs: e.EpisodeIds})
		}
	}
	if len(movies) == 0 && len(episodes) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Choose at least one movie or episode.", nil)
		return
	}
	nm, ne, err := h.Library.UnmonitorWanted(r.Context(), movies, episodes)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.WantedUnmonitorResult{Movies: nm, Episodes: ne})
}
