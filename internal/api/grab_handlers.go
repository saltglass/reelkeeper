package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// GetGrabbingSettings returns the automatic grabbing settings.
func (h *Handlers) GetGrabbingSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toGrabbingSettings(h.Settings.Grabbing()))
}

// PutGrabbingSettings saves the automatic grabbing settings.
func (h *Handlers) PutGrabbingSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutGrabbingSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	// Either client will do: one that downloads torrents, or Usenet.
	if !h.Settings.DownloadClient().Configured() && !h.Settings.UsenetClient().Configured() {
		fields := map[string]string{}
		if body.Mode == gen.GrabbingSettingsModeOn {
			fields["mode"] = "Set up the download client first (below)."
		}
		if body.TvMode != nil && *body.TvMode == gen.GrabbingSettingsTvModeOn {
			fields["tvMode"] = "Set up the download client first (below)."
		}
		if len(fields) > 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "Some fields need attention.", fields)
			return
		}
	}
	before := h.Settings.Grabbing()
	tvMode := before.TVMode
	if body.TvMode != nil {
		tvMode = string(*body.TvMode)
	}
	saved, err := h.Settings.SaveGrabbing(r.Context(), settings.Grabbing{
		Mode: string(body.Mode), TVMode: tvMode, QueueLimit: body.QueueLimit, MinSeeders: body.MinSeeders,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	// Raising the queue limit (or turning Grabbing on) searches straight
	// away instead of waiting for the next hourly pass.
	if grab.LimitLifted(before, saved) && h.Jobs != nil {
		if _, _, err := h.Jobs.RunNow(r.Context(), grab.SearchKind); err != nil {
			h.Logger.Warn("couldn't start a search after the grabbing settings changed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, toGrabbingSettings(saved))
}

func toGrabbingSettings(g settings.Grabbing) gen.GrabbingSettings {
	tv := gen.GrabbingSettingsTvMode(g.TVMode)
	return gen.GrabbingSettings{Mode: gen.GrabbingSettingsMode(g.Mode), TvMode: &tv, QueueLimit: g.QueueLimit, MinSeeders: g.MinSeeders}
}

// GetHistory lists History events, newest first.
func (h *Handlers) GetHistory(w http.ResponseWriter, r *http.Request, params gen.GetHistoryParams) {
	limit := 50
	if params.Limit != nil && *params.Limit >= 1 && *params.Limit <= 200 {
		limit = *params.Limit
	}
	opts := grab.ListOptions{TitleID: params.TitleId, Limit: limit}
	if params.Before != nil {
		opts.Before = *params.Before
	}
	if params.Event != nil {
		for _, e := range *params.Event {
			if !e.Valid() {
				writeError(w, http.StatusBadRequest, "invalid", "Unknown event kind: "+string(e)+".", nil)
				return
			}
			opts.Events = append(opts.Events, string(e))
		}
	}
	events, err := h.History.List(r.Context(), opts)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	titles, profiles, err := h.historyLookups(r.Context(), events)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.HistoryPage{Events: make([]gen.HistoryEvent, 0, len(events))}
	for _, e := range events {
		ev := gen.HistoryEvent{
			Id: e.ID, CreatedAt: e.CreatedAt, TitleId: e.TitleID, VersionId: e.VersionID,
			Event: gen.HistoryEventKind(e.Event), SourceTitle: e.SourceTitle, Indexer: e.Indexer,
			Quality: e.Quality, Score: e.Score, Message: e.Message,
		}
		if e.Data != "" {
			// Written by History.Add from grab.EventData, whose JSON matches.
			_ = json.Unmarshal([]byte(e.Data), &ev.Data)
		}
		if e.TitleID != nil {
			if t, ok := titles[*e.TitleID]; ok {
				title, year, kind := t.Title, t.Year, gen.HistoryEventMediaType(t.MediaType)
				ev.Title, ev.Year, ev.MediaType = &title, &year, &kind
			}
		}
		if e.VersionID != nil {
			if p, ok := profiles[*e.VersionID]; ok {
				ev.Profile = &p
			}
		}
		out.Events = append(out.Events, ev)
	}
	if len(events) == limit {
		last := events[len(events)-1].ID
		out.NextBefore = &last
	}
	writeJSON(w, http.StatusOK, out)
}

// historyLookups finds the titles and version profile names the events
// mention, in two queries however many events there are.
func (h *Handlers) historyLookups(ctx context.Context, events []grab.Event) (map[int64]library.Title, map[int64]string, error) {
	var titleIDs, versionIDs []int64
	for _, e := range events {
		if e.TitleID != nil {
			titleIDs = append(titleIDs, *e.TitleID)
		}
		if e.VersionID != nil {
			versionIDs = append(versionIDs, *e.VersionID)
		}
	}
	titles := map[int64]library.Title{}
	if len(titleIDs) > 0 {
		var ts []library.Title
		if err := h.Store.DB.NewSelect().Model(&ts).Where("id IN (?)", bun.List(titleIDs)).Scan(ctx); err != nil {
			return nil, nil, err
		}
		for _, t := range ts {
			titles[t.ID] = t
		}
	}
	profiles := map[int64]string{}
	if len(versionIDs) > 0 {
		var vs []library.Version
		if err := h.Store.DB.NewSelect().Model(&vs).Where("id IN (?)", bun.List(versionIDs)).Scan(ctx); err != nil {
			return nil, nil, err
		}
		all, err := h.Library.Profiles(ctx)
		if err != nil {
			return nil, nil, err
		}
		names := map[int64]string{}
		for _, p := range all {
			names[p.ID] = p.Name
		}
		for _, v := range vs {
			profiles[v.ID] = names[v.ProfileID]
		}
	}
	return titles, profiles, nil
}

// SearchVersion queues a search for one version of a movie.
func (h *Handlers) SearchVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	m, err := h.Library.MovieDetail(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	found := false
	for _, v := range m.Versions {
		found = found || v.ID == versionID
	}
	if !found {
		h.writeLibraryError(w, r, library.ErrNotFound)
		return
	}
	job, _, err := grab.QueueSearch(r.Context(), h.Jobs, versionID, grab.TriggerManual)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ToJob(h.Jobs, *job))
}

// searchOnAdd queues a search for a version that just became wanted. It
// does nothing while Grabbing is off, and never fails the caller: the
// scheduled pass will find the version anyway.
func (h *Handlers) searchOnAdd(ctx context.Context, versionID int64) {
	if h.Settings.Grabbing().Mode == settings.GrabOff {
		return
	}
	if _, _, err := grab.QueueSearch(ctx, h.Jobs, versionID, grab.TriggerAdded); err != nil {
		h.Logger.Warn("couldn't queue a search for a new version", "version", versionID, "err", err)
	}
}
