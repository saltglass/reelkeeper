package api

import (
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/settings"
)

func toCleanupSettings(c settings.Cleanup) gen.CleanupSettings {
	return gen.CleanupSettings{
		Mode: gen.CleanupSettingsMode(c.Mode), SeedRatio: c.SeedRatio, SeedHours: c.SeedHours,
		StalledHours: c.StalledHours, SlowKBps: c.SlowKBps, SlowHours: c.SlowHours,
		MetadataHours: c.MetadataHours, ImportFailedDays: c.ImportFailedDays,
		Unregistered: c.Unregistered, MissingFiles: c.MissingFiles, EmptyReplacedDays: c.EmptyReplacedDays,
		KeepFreeGB: &c.KeepFreeGB,
	}
}

// GetCleanupSettings returns the cleanup settings.
func (h *Handlers) GetCleanupSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toCleanupSettings(h.Settings.Cleanup()))
}

// PutCleanupSettings saves the cleanup settings.
func (h *Handlers) PutCleanupSettings(w http.ResponseWriter, r *http.Request) {
	var b gen.PutCleanupSettingsJSONRequestBody
	if !decode(w, r, &b) {
		return
	}
	keepFree := h.Settings.Cleanup().KeepFreeGB
	if b.KeepFreeGB != nil {
		keepFree = *b.KeepFreeGB
	}
	saved, err := h.Settings.SaveCleanup(r.Context(), settings.Cleanup{
		Mode: string(b.Mode), SeedRatio: b.SeedRatio, SeedHours: b.SeedHours,
		StalledHours: b.StalledHours, SlowKBps: b.SlowKBps, SlowHours: b.SlowHours,
		MetadataHours: b.MetadataHours, ImportFailedDays: b.ImportFailedDays,
		Unregistered: b.Unregistered, MissingFiles: b.MissingFiles, EmptyReplacedDays: b.EmptyReplacedDays,
		KeepFreeGB: keepFree,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCleanupSettings(saved))
}

// GetCleanupEvents lists what cleanup did, newest first.
func (h *Handlers) GetCleanupEvents(w http.ResponseWriter, r *http.Request, params gen.GetCleanupEventsParams) {
	limit := 50
	if params.Limit != nil && *params.Limit >= 1 && *params.Limit <= 200 {
		limit = *params.Limit
	}
	var before int64
	if params.Before != nil {
		before = *params.Before
	}
	// One extra row tells whether there is another page.
	rows, err := h.Downloads.CleanupEvents(r.Context(), before, limit+1)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.CleanupEventPage{Events: []gen.CleanupEvent{}}
	if len(rows) > limit {
		rows = rows[:limit]
		next := rows[len(rows)-1].ID
		out.NextBefore = &next
	}
	var ids []int64
	for _, e := range rows {
		if e.TitleID != nil {
			ids = append(ids, *e.TitleID)
		}
	}
	titles, _, err := h.movieRefs(r.Context(), ids, nil)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	for _, e := range rows {
		ev := gen.CleanupEvent{
			Id: e.ID, CreatedAt: e.CreatedAt, ReleaseTitle: e.ReleaseTitle,
			Action: gen.CleanupEventAction(e.Action), Rule: e.Rule, Message: e.Message,
		}
		if e.TitleID != nil {
			if t, ok := titles[*e.TitleID]; ok {
				ev.Movie = &t
			}
		}
		out.Events = append(out.Events, ev)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetCleanupSeeding lists imported torrents still in qBittorrent.
func (h *Handlers) GetCleanupSeeding(w http.ResponseWriter, r *http.Request) {
	items, err := h.Downloads.Seeding(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	var ids []int64
	for _, it := range items {
		if it.Download.TitleID != nil {
			ids = append(ids, *it.Download.TitleID)
		}
	}
	titles, _, err := h.movieRefs(r.Context(), ids, nil)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.SeedingItem, 0, len(items))
	for _, it := range items {
		d := it.Download
		s := gen.SeedingItem{
			DownloadId: d.ID, ReleaseTitle: d.ReleaseTitle, Indexer: d.Indexer, Quality: d.Quality,
			ImportedAt: d.ImportedAt, SeedRatio: d.SeedRatio, SeedTimeMinutes: d.SeedTimeMinutes, Waiting: it.Waiting,
			MediaType: gen.SeedingItemMediaTypeMovie, Episodes: d.EpisodesLabel,
		}
		if d.Category == settings.TVCategory {
			s.MediaType = gen.SeedingItemMediaTypeSeries
		}
		if d.TitleID != nil {
			if t, ok := titles[*d.TitleID]; ok {
				s.Movie = &t
			}
		}
		if t := it.Torrent; t != nil {
			s.Torrent = &gen.SeedingTorrent{
				Ratio: t.Ratio, SeedingTimeSeconds: int64(t.SeedingTime.Seconds()), State: gen.TorrentState(t.State), Size: t.Size,
			}
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// GetCleanupOrphans lists what's in Reelkeeper's category or folder that Reelkeeper
// doesn't track.
func (h *Handlers) GetCleanupOrphans(w http.ResponseWriter, r *http.Request) {
	o, err := h.Downloads.FindOrphans(r.Context())
	out := gen.Orphans{Torrents: []gen.OrphanTorrent{}, Files: []string{}}
	if err != nil {
		// A missing or unreachable client is shown on the page, not an error.
		msg := capitalize(err.Error()) + "."
		if errors.Is(err, download.ErrNoClient) {
			msg = "No download client is set up yet (Settings → Download Client)."
		}
		out.ClientError = &msg
		if o.Folder != "" {
			out.Folder = &o.Folder
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, t := range o.Torrents {
		out.Torrents = append(out.Torrents, gen.OrphanTorrent{Hash: t.Hash, Name: t.Name, Size: t.Size, Progress: t.Progress, State: gen.TorrentState(t.State)})
	}
	if o.Files != nil {
		out.Files = o.Files
	}
	if o.Folder != "" {
		out.Folder = &o.Folder
	}
	writeJSON(w, http.StatusOK, out)
}

// RemoveOrphanTorrent removes an untracked torrent when asked.
func (h *Handlers) RemoveOrphanTorrent(w http.ResponseWriter, r *http.Request, hash string) {
	if err := h.Downloads.RemoveOrphanTorrent(r.Context(), hash); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "remove_failed", capitalize(err.Error())+".", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveOrphanFile deletes a stray file or folder when asked.
func (h *Handlers) RemoveOrphanFile(w http.ResponseWriter, r *http.Request) {
	var b gen.RemoveOrphanFileJSONRequestBody
	if !decode(w, r, &b) {
		return
	}
	if err := h.Downloads.DeleteStrayFile(r.Context(), b.Name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "remove_failed", capitalize(err.Error())+".", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
