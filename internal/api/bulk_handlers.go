package api

import (
	"context"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// Bulk edits on the Movies and Series pages ("Select").

// bulkSearchesNow is how many titles a bulk edit that makes them wanted
// searches at once (Dan, 2026-10-27: 10, so one edit doesn't flood the
// Cloudflare solver). They run one after another on the task queue; the
// rest wait for the hourly search, which takes never-searched titles first.
const bulkSearchesNow = 10

// maxBulkTitles is how many titles one bulk edit may name.
const maxBulkTitles = 10000

// EditMovies changes monitoring or upgrades for several movies.
func (h *Handlers) EditMovies(w http.ResponseWriter, r *http.Request) {
	var body gen.EditMoviesJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	h.bulkEdit(w, r, library.BulkEdit{
		MediaType: library.Movie, TitleIDs: body.TitleIds, ProfileID: profileOf(body.ProfileId),
		Monitored: body.Monitored, Upgrades: body.Upgrades,
	}, h.Settings.Grabbing().Mode)
}

// EditSeries changes monitoring for several series.
func (h *Handlers) EditSeries(w http.ResponseWriter, r *http.Request) {
	var body gen.EditSeriesJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	e := library.BulkEdit{MediaType: library.Series, TitleIDs: body.TitleIds, ProfileID: profileOf(body.ProfileId), Monitored: body.Monitored}
	if body.Monitor != nil {
		e.Monitor = string(*body.Monitor)
	}
	h.bulkEdit(w, r, e, h.Settings.Grabbing().TVMode)
}

func (h *Handlers) bulkEdit(w http.ResponseWriter, r *http.Request, e library.BulkEdit, mode string) {
	if len(e.TitleIDs) > maxBulkTitles {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Choose at most 10,000 titles at a time.",
			map[string]string{"titleIds": "At most 10,000."})
		return
	}
	ctx := r.Context()
	res, err := h.Library.EditVersions(ctx, e)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	out := gen.BulkEditResult{Titles: res.Titles, Versions: res.Versions}
	if mode != settings.GrabOff && len(res.TurnedOn) > 0 {
		wanted := h.nowWanted(ctx, res.TurnedOn)
		for _, id := range wanted {
			if out.Searched == bulkSearchesNow {
				out.Later++
				continue
			}
			if _, _, err := grab.QueueSearch(ctx, h.Jobs, id, grab.TriggerAdded); err != nil {
				h.Logger.Warn("couldn't queue a search after a bulk edit", "version", id, "err", err)
				out.Later++
				continue
			}
			out.Searched++
		}
	}
	h.Logger.Info("bulk edit", "kind", e.MediaType, "titles", out.Titles, "versions", out.Versions,
		"searched", out.Searched, "later", out.Later)
	writeJSON(w, http.StatusOK, out)
}

// nowWanted keeps the versions the Wanted page lists as still wanted and
// not downloading, in the order given: a movie turned on that already has
// what its profile aims for, or a series with nothing aired to look for,
// needs no search.
func (h *Handlers) nowWanted(ctx context.Context, ids []int64) []int64 {
	l, err := h.Grab.Wanted(ctx, false)
	if err != nil {
		h.Logger.Warn("couldn't tell what a bulk edit made wanted", "err", err)
		return nil
	}
	wanted := map[int64]bool{}
	for _, list := range [][]grab.WantedItem{l.Missing, l.Upgrades} {
		for _, it := range list {
			if it.State != grab.StateDownloading {
				wanted[it.VersionID] = true
			}
		}
	}
	var out []int64
	for _, id := range ids {
		if wanted[id] {
			out = append(out, id)
		}
	}
	return out
}

// profileOf is the profile a bulk edit is limited to, 0 for none.
func profileOf(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}
