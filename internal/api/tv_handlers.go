package api

import (
	"context"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// TV monitoring and searching (M7). The library decides which episodes a
// version wants (library.SeriesVersions); these handlers change that and
// show it.

// seriesDetail is a series' page, with each version's monitoring and
// every episode's state in it.
func (h *Handlers) seriesDetail(ctx context.Context, d *library.SeriesDetail) gen.SeriesDetail {
	out := toSeriesDetail(*d)
	ids := make([]int64, len(d.Versions))
	for i, v := range d.Versions {
		ids[i] = v.ID
	}
	if len(ids) == 0 {
		return out
	}
	svs, err := h.Library.SeriesVersions(ctx, ids...)
	if err != nil {
		h.Logger.Warn("couldn't work out the series' episode states", "series", d.ID, "err", err)
		return out
	}
	active := map[int64]map[int64]bool{}
	if h.Downloads != nil {
		if active, err = h.Downloads.ActiveEpisodes(ctx); err != nil {
			h.Logger.Warn("couldn't read the queue for episode states", "err", err)
		}
	}
	byID := map[int64]library.SeriesVersion{}
	for _, sv := range svs {
		byID[sv.Version.ID] = sv
	}
	for i := range out.Versions {
		sv, ok := byID[out.Versions[i].Id]
		if !ok {
			continue
		}
		preset, from := monitorPreset(sv.Version)
		out.Versions[i].Monitor = &preset
		out.Versions[i].MonitorFrom = from
		states := episodeStates(sv, active[sv.Version.ID])
		out.Versions[i].Episodes = &states
	}
	return out
}

// monitorPreset describes a version's date rule as its preset.
func monitorPreset(v library.Version) (gen.VersionDetailMonitor, *string) {
	switch {
	case v.MonitorFrom == nil:
		return gen.VersionDetailMonitorNone, nil
	case *v.MonitorFrom == library.AllEpisodes:
		return gen.VersionDetailMonitorAll, nil
	}
	return gen.VersionDetailMonitorFuture, v.MonitorFrom
}

// episodeStates says where each episode stands in a version. A file wins
// (TV doesn't upgrade), then a download in progress.
func episodeStates(sv library.SeriesVersion, downloading map[int64]bool) []gen.EpisodeState {
	wanted := map[int64]bool{}
	for _, e := range sv.Wanted {
		wanted[e.ID] = true
	}
	out := make([]gen.EpisodeState, 0, len(sv.Episodes))
	for _, e := range sv.Episodes {
		st := gen.EpisodeState{EpisodeId: e.ID, Monitored: sv.Monitored[e.ID]}
		switch {
		case sv.HasFile[e.ID]:
			st.State = gen.EpisodeStateStateHasFile
		case downloading[e.ID]:
			st.State = gen.EpisodeStateStateDownloading
		case wanted[e.ID]:
			st.State = gen.EpisodeStateStateWanted
		case st.Monitored:
			st.State = gen.EpisodeStateStateNotAired
		default:
			st.State = gen.EpisodeStateStateUnmonitored
		}
		out = append(out, st)
	}
	return out
}

// seriesVersion finds a version of this series, or answers 404.
func (h *Handlers) seriesVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) (*library.SeriesVersion, bool) {
	svs, err := h.Library.SeriesVersions(r.Context(), versionID)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return nil, false
	}
	if len(svs) != 1 || svs[0].Title.ID != id {
		h.writeLibraryError(w, r, library.ErrNotFound)
		return nil, false
	}
	return &svs[0], true
}

// UpdateSeriesVersion turns monitoring on or off, and applies a monitoring
// preset to the version's episodes.
func (h *Handlers) UpdateSeriesVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	var body gen.UpdateSeriesVersionJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if body.Monitored == nil && body.Monitor == nil {
		h.writeServiceError(w, r, validation.Errors{"monitored": "Send monitored, monitor, or both."})
		return
	}
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	if _, ok := h.seriesVersion(w, r, id, versionID); !ok {
		return
	}
	ctx := r.Context()
	if body.Monitored != nil {
		if err := h.Library.SetMonitored(ctx, id, versionID, *body.Monitored); err != nil {
			h.writeLibraryError(w, r, err)
			return
		}
	}
	if body.Monitor != nil {
		if err := h.Library.ApplyMonitorPreset(ctx, versionID, string(*body.Monitor)); err != nil {
			h.writeLibraryError(w, r, err)
			return
		}
	}
	h.searchOnAddTV(ctx, versionID)
	w.WriteHeader(http.StatusNoContent)
}

// MonitorEpisodes turns seasons or episodes on or off for a version.
func (h *Handlers) MonitorEpisodes(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	var body gen.MonitorEpisodesJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if (body.Seasons == nil || len(*body.Seasons) == 0) && (body.EpisodeIds == nil || len(*body.EpisodeIds) == 0) {
		h.writeServiceError(w, r, validation.Errors{"seasons": "Choose at least one season or episode."})
		return
	}
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	sv, ok := h.seriesVersion(w, r, id, versionID)
	if !ok {
		return
	}
	ctx := r.Context()
	if body.Seasons != nil {
		for _, n := range *body.Seasons {
			if err := h.Library.SetSeasonMonitored(ctx, versionID, n, body.Monitored); err != nil {
				h.writeLibraryError(w, r, err)
				return
			}
		}
	}
	if body.EpisodeIds != nil {
		// Only this series' episodes.
		mine := map[int64]bool{}
		for _, e := range sv.Episodes {
			mine[e.ID] = true
		}
		for _, eid := range *body.EpisodeIds {
			if !mine[eid] {
				h.writeServiceError(w, r, validation.Errors{"episodeIds": "That episode isn't part of this series."})
				return
			}
		}
		if err := h.Library.SetEpisodesMonitored(ctx, versionID, *body.EpisodeIds, body.Monitored); err != nil {
			h.writeLibraryError(w, r, err)
			return
		}
	}
	if body.Monitored {
		h.searchOnAddTV(ctx, versionID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// SearchSeriesVersion queues a search for a version's wanted episodes.
func (h *Handlers) SearchSeriesVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Series) {
		return
	}
	if _, ok := h.seriesVersion(w, r, id, versionID); !ok {
		return
	}
	job, _, err := grab.QueueSearch(r.Context(), h.Jobs, versionID, grab.TriggerManual)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ToJob(h.Jobs, *job))
}

// searchOnAddTV queues a search for a series version when it wants an aired
// episode now. Like searchOnAdd, it does nothing while TV grabbing is off
// and never fails the caller.
func (h *Handlers) searchOnAddTV(ctx context.Context, versionID int64) {
	if h.Settings.Grabbing().TVMode == settings.GrabOff {
		return
	}
	svs, err := h.Library.SeriesVersions(ctx, versionID)
	if err != nil || len(svs) != 1 || !svs[0].Version.Monitored || len(svs[0].Wanted) == 0 {
		return
	}
	if _, _, err := grab.QueueSearch(ctx, h.Jobs, versionID, grab.TriggerAdded); err != nil {
		h.Logger.Warn("couldn't queue a search for a series version", "version", versionID, "err", err)
	}
}
