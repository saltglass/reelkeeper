package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
)

func toDownloadClient(d settings.DownloadClient) gen.DownloadClient {
	return gen.DownloadClient{
		Type: gen.DownloadClientType(d.ClientType()), Url: d.URL, Username: d.Username, PasswordSet: d.Password != "", Folder: d.Folder,
		ListenPort: d.ListenPort, MaxActiveDownloads: d.MaxActiveDownloads, MaxActiveSeeds: d.MaxActiveSeeds,
		DownloadLimitKBs: d.DownloadLimitKBs, UploadLimitKBs: d.UploadLimitKBs,
	}
}

// updateFrom is what the request asks for. A request without a type keeps
// the current one.
func updateFrom(b gen.DownloadClientUpdate) settings.DownloadClientUpdate {
	u := settings.DownloadClientUpdate{
		URL: b.Url, Username: b.Username, Password: b.Password, Folder: derefStr(b.Folder),
		ListenPort: b.ListenPort, MaxActiveDownloads: b.MaxActiveDownloads, MaxActiveSeeds: b.MaxActiveSeeds,
		DownloadLimitKBs: b.DownloadLimitKBs, UploadLimitKBs: b.UploadLimitKBs,
	}
	if b.Type != nil {
		u.Type = string(*b.Type)
	}
	return u
}

// GetDownloadClient returns the download client connection, without the
// password.
func (h *Handlers) GetDownloadClient(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toDownloadClient(h.Settings.DownloadClient()))
}

// PutDownloadClient saves the download client connection.
//
// Changing the client's type (qBittorrent, Transmission, Deluge or the
// built-in client) moves
// Reelkeeper's torrents to the new client: everything in the Queue, and the
// seeding ones unless the user chose to leave them (leaveSeeding). The move
// is a task (download.Service.RunMove); the old client's connection is kept
// with it until it is done, and grabbing, imports and cleanup wait meanwhile.
// With nothing to move the type just changes, and imported downloads still
// seeding in the old client (if left) are no longer tracked
// (download.Service.ClientChanged).
func (h *Handlers) PutDownloadClient(w http.ResponseWriter, r *http.Request) {
	var body gen.PutDownloadClientJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	update := updateFrom(body)
	old := h.Settings.DownloadClient()
	next, err := h.Settings.ApplyDownloadClient(update)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	leave := body.LeaveSeeding != nil && *body.LeaveSeeding
	switched := next.ClientType() != old.ClientType()
	// Changing the type, or removing the client, while torrents are still
	// moving would strand them.
	if (switched || !next.Configured()) && h.Downloads.Moving() {
		writeError(w, http.StatusUnprocessableEntity, "move_running", capitalize(download.ErrMoveRunning.Error())+".", nil)
		return
	}
	moving := 0
	if switched {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		err := h.Downloads.PlanMove(ctx, old, next, leave)
		cancel()
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "download_client", capitalize(err.Error())+".", nil)
			return
		}
		if moving, err = h.Downloads.BeginMove(r.Context(), old, next, leave); err != nil {
			h.writeServiceError(w, r, err)
			return
		}
	}
	saved, err := h.Settings.SaveDownloadClient(r.Context(), update)
	if err != nil {
		if moving > 0 {
			h.Downloads.CancelMove(r.Context())
		}
		h.writeServiceError(w, r, err)
		return
	}
	if switched {
		if _, err := h.Downloads.ClientChanged(r.Context(), old.ClientName(), saved.ClientName()); err != nil {
			h.Logger.Error("couldn't mark the downloads left in the old download client", "err", err)
		}
	}
	// With no download client, "On" would grab into nothing: drop movies
	// and TV back to a dry run.
	if !saved.Configured() {
		if g := h.Settings.Grabbing(); g.Mode == settings.GrabOn || g.TVMode == settings.GrabOn {
			if g.Mode == settings.GrabOn {
				g.Mode = settings.GrabDryRun
			}
			if g.TVMode == settings.GrabOn {
				g.TVMode = settings.GrabDryRun
			}
			if _, err := h.Settings.SaveGrabbing(r.Context(), g); err != nil {
				h.Logger.Warn("couldn't switch Grabbing back to a dry run", "err", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, toDownloadClient(saved))
}

// GetDownloadMovePreview says what changing the download client would move.
func (h *Handlers) GetDownloadMovePreview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := h.Downloads.MovePreview(ctx)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.DownloadMovePreview{Total: p.Total, Downloading: p.Downloading, Seeding: p.Seeding, CannotMove: make([]gen.DownloadMoveProblem, 0, len(p.CannotMove))}
	for _, m := range p.CannotMove {
		out.CannotMove = append(out.CannotMove, gen.DownloadMoveProblem{Release: m.Release, Reason: m.Reason, Seeding: m.Seeding})
	}
	if p.ClientError != "" {
		out.ClientError = &p.ClientError
	}
	writeJSON(w, http.StatusOK, out)
}

// TestDownloadClient tries unsaved download client values.
func (h *Handlers) TestDownloadClient(w http.ResponseWriter, r *http.Request) {
	var body gen.TestDownloadClientJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	cfg, err := h.Settings.ApplyDownloadClient(updateFrom(body))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if !cfg.Configured() && cfg.ClientType() == settings.ClientBuiltin {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Enter the built-in client's download folder first.", map[string]string{"folder": "Enter a full path inside Reelkeeper's container, like /downloads."})
		return
	}
	if !cfg.Configured() {
		address := "Web UI address"
		switch cfg.ClientType() {
		case settings.ClientTransmission:
			address = "RPC address"
		case settings.ClientDeluge:
			address = "web UI address"
		}
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Enter "+cfg.ClientName()+"'s "+address+" first.", map[string]string{"url": cfg.AddressPrompt()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	version, err := h.Downloads.Test(ctx, cfg)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "download_client", capitalize(err.Error())+".", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// movieRefs looks up the titles (as MovieRefs) and the version profile
// names for a list of ids, in two queries.
func (h *Handlers) movieRefs(ctx context.Context, titleIDs, versionIDs []int64) (map[int64]gen.MovieRef, map[int64]string, error) {
	titles := map[int64]gen.MovieRef{}
	if len(titleIDs) > 0 {
		var ts []library.Title
		if err := h.Store.DB.NewSelect().Model(&ts).Where("id IN (?)", bun.List(titleIDs)).Scan(ctx); err != nil {
			return nil, nil, err
		}
		for _, t := range ts {
			titles[t.ID] = gen.MovieRef{Id: t.ID, Title: t.Title, Year: t.Year}
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

// GetQueue lists Reelkeeper's unfinished downloads with live progress.
func (h *Handlers) GetQueue(w http.ResponseWriter, r *http.Request) {
	items, clientErr, err := h.Downloads.Queue(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	var titleIDs, versionIDs []int64
	for _, it := range items {
		if it.Download.TitleID != nil {
			titleIDs = append(titleIDs, *it.Download.TitleID)
		}
		if it.Download.VersionID != nil {
			versionIDs = append(versionIDs, *it.Download.VersionID)
		}
	}
	titles, profiles, err := h.movieRefs(r.Context(), titleIDs, versionIDs)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.Queue{Items: make([]gen.QueueItem, 0, len(items)), ClientName: h.Downloads.ClientName()}
	g := h.Settings.Grabbing()
	out.Limit, out.Full = g.QueueLimit, grab.QueueFull(g, len(items))
	if clientErr != nil {
		msg := capitalize(clientErr.Error()) + "."
		out.ClientError = &msg
	}
	for _, it := range items {
		d := it.Download
		q := gen.QueueItem{
			Id: d.ID, CreatedAt: d.CreatedAt, VersionId: d.VersionID, ReleaseTitle: d.ReleaseTitle,
			Indexer: d.Indexer, Quality: d.Quality, Score: d.Score, Size: d.Size, Upgrade: d.Upgrade,
			Status: gen.QueueItemStatus(d.Status), Message: d.Message,
			MediaType: gen.QueueItemMediaTypeMovie, Episodes: d.EpisodesLabel,
		}
		if to := d.MovingTo(); to != "" {
			q.MovingTo = &to
		}
		if d.Category == settings.TVCategory {
			q.MediaType = gen.QueueItemMediaTypeSeries
		}
		if d.TitleID != nil {
			if t, ok := titles[*d.TitleID]; ok {
				q.Movie = &t
			}
		}
		if d.VersionID != nil {
			if p, ok := profiles[*d.VersionID]; ok {
				q.Profile = &p
			}
		}
		if t := it.Torrent; t != nil {
			qt := &gen.QueueTorrent{
				State: gen.TorrentState(t.State), Progress: t.Progress, Size: t.Size, Left: t.Left, Speed: t.Speed,
				Seeds: t.Seeds, SwarmSeeds: &t.SwarmSeeds, Peers: t.Peers,
			}
			if t.ETA > 0 {
				eta := int(t.ETA / time.Second)
				qt.EtaSeconds = &eta
			}
			q.Torrent = qt
		}
		out.Items = append(out.Items, q)
	}
	writeJSON(w, http.StatusOK, out)
}

// RetryImport tries a failed import again.
func (h *Handlers) RetryImport(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Downloads.RetryImport(r.Context(), id); err != nil {
		if errors.Is(err, download.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "That download isn't in the queue.", nil)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "import_failed", capitalize(err.Error()), nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PauseDownload stops a download in the download client.
func (h *Handlers) PauseDownload(w http.ResponseWriter, r *http.Request, id int64) {
	h.queueAction(w, r, id, "pause_failed", h.Downloads.Pause)
}

// ResumeDownload starts a paused download again.
func (h *Handlers) ResumeDownload(w http.ResponseWriter, r *http.Request, id int64) {
	h.queueAction(w, r, id, "resume_failed", h.Downloads.Resume)
}

// MoveDownloadToTop puts a download first in the download client's queue.
func (h *Handlers) MoveDownloadToTop(w http.ResponseWriter, r *http.Request, id int64) {
	h.queueAction(w, r, id, "move_failed", h.Downloads.MoveToTop)
}

// queueAction runs one of the Queue page's buttons and answers for it.
func (h *Handlers) queueAction(w http.ResponseWriter, r *http.Request, id int64, code string, act func(context.Context, int64) error) {
	if err := act(r.Context(), id); err != nil {
		if errors.Is(err, download.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "That download isn't in the queue.", nil)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, code, capitalize(err.Error())+".", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveFromQueue removes a download from the download client, optionally
// blocklisting the release and searching again. Searching again always
// blocklists: otherwise the search would pick the same best release.
func (h *Handlers) RemoveFromQueue(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.RemoveFromQueueJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	d, err := h.Downloads.Remove(r.Context(), id, download.RemoveOptions{
		Blocklist: body.Blocklist || body.SearchAgain, Reason: "Removed from the queue by you",
	})
	if err != nil {
		if errors.Is(err, download.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "That download isn't in the queue.", nil)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "remove_failed", capitalize(err.Error())+".", nil)
		return
	}
	if body.SearchAgain && d.VersionID != nil {
		if _, _, err := grab.QueueSearch(r.Context(), h.Jobs, *d.VersionID, grab.TriggerManual); err != nil {
			h.Logger.Warn("couldn't queue a search after removing a download", "version", *d.VersionID, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetBlocklist lists the releases Reelkeeper skips: newest first, without
// the entries of titles with nothing wanted unless asked.
func (h *Handlers) GetBlocklist(w http.ResponseWriter, r *http.Request, params gen.GetBlocklistParams) {
	q := download.BlocklistQuery{IncludeDormant: params.Include != nil && *params.Include == "dormant"}
	if params.TitleId != nil {
		q.TitleID = *params.TitleId
	}
	page, err := h.Downloads.Blocklist(r.Context(), q)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	ids := make([]int64, 0, len(page.Items))
	for _, b := range page.Items {
		ids = append(ids, b.TitleID)
	}
	titles, _, err := h.movieRefs(r.Context(), ids, nil)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]gen.BlocklistItem, 0, len(page.Items))
	for _, b := range page.Items {
		it := gen.BlocklistItem{Id: b.ID, CreatedAt: b.CreatedAt, ReleaseTitle: b.ReleaseTitle,
			Indexer: b.Indexer, Quality: b.Quality, Reason: b.Reason, Rule: b.Rule,
			ExpiresAt: b.ExpiresAt, Permanent: b.Permanent(), Dormant: b.Dormant,
			MediaType: gen.BlocklistItemMediaType(b.MediaType), Episodes: b.Episodes}
		if t, ok := titles[b.TitleID]; ok {
			it.Movie = &t
		}
		items = append(items, it)
	}
	writeJSON(w, http.StatusOK, gen.Blocklist{Items: items, Total: page.Total, Dormant: page.Dormant, Temporary: page.Temporary})
}

// UnblockTemporary lets every temporary entry be grabbed again.
func (h *Handlers) UnblockTemporary(w http.ResponseWriter, r *http.Request, params gen.UnblockTemporaryParams) {
	var titleID int64
	if params.TitleId != nil {
		titleID = *params.TitleId
	}
	n, err := h.Downloads.UnblockTemporary(r.Context(), titleID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": n})
}

// DeleteBlocklistItem lets a release be grabbed again.
func (h *Handlers) DeleteBlocklistItem(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Downloads.Unblock(r.Context(), id); err != nil {
		if errors.Is(err, download.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "That release isn't on the blocklist.", nil)
			return
		}
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
