package api

import (
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
)

// GetBackfill returns the backfill queue.
func (h *Handlers) GetBackfill(w http.ResponseWriter, r *http.Request) {
	l, err := h.Grab.BackfillListAll(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.Backfill{
		Next: make([]gen.BackfillShow, len(l.Next)), Decision: make([]gen.BackfillShow, len(l.Decision)),
		GrabsToday: l.GrabsToday, GrabsPerDay: l.GrabsPerDay,
	}
	if l.Now != nil {
		now := toBackfillShow(*l.Now)
		out.Now = &now
	}
	for i, s := range l.Next {
		out.Next[i] = toBackfillShow(s)
	}
	for i, s := range l.Decision {
		out.Decision[i] = toBackfillShow(s)
	}
	writeJSON(w, http.StatusOK, out)
}

func toBackfillShow(s grab.BackfillShow) gen.BackfillShow {
	return gen.BackfillShow{
		Id: s.ID, VersionId: s.VersionID, TitleId: s.TitleID, Title: s.Title, Year: s.Year, PosterPath: s.PosterPath,
		State: gen.BackfillShowState(s.State), Found: s.Found, Missing: s.Missing, Downloading: s.Downloading,
		StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
	}
}

// writeBackfillError answers for the errors backfill returns on purpose.
func (h *Handlers) writeBackfillError(w http.ResponseWriter, r *http.Request, err error) {
	var refusal grab.BackfillRefusal
	switch {
	case errors.Is(err, grab.ErrBackfillNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That show isn't in the backfill queue (any more).", nil)
	case errors.Is(err, grab.ErrBackfillQueued):
		writeError(w, http.StatusConflict, "already_queued", "That show is already in the backfill queue.", nil)
	case errors.Is(err, grab.ErrOptionGone):
		writeError(w, http.StatusNotFound, "option_expired", "Those options have expired. Open \"Show options\" again.", nil)
	case errors.As(err, &refusal):
		writeError(w, http.StatusConflict, "refused", refusal.Error(), nil)
	default:
		h.writeServiceError(w, r, err)
	}
}

// AddBackfill puts a series version at the end of the queue.
func (h *Handlers) AddBackfill(w http.ResponseWriter, r *http.Request) {
	var body gen.AddBackfillJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	row, err := h.Grab.BackfillAdd(r.Context(), body.VersionId)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	// A show that starts at once is searched without waiting for the task's
	// next turn.
	if h.Jobs != nil {
		if _, _, err := h.Jobs.RunNow(r.Context(), grab.BackfillKind); err != nil {
			h.Logger.Warn("couldn't start the Backfill task", "err", err)
		}
	}
	l, err := h.Grab.BackfillListAll(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	// The answer is the new entry as the list shows it.
	shows := append(append([]grab.BackfillShow{}, l.Next...), l.Decision...)
	if l.Now != nil {
		shows = append(shows, *l.Now)
	}
	for _, s := range shows {
		if s.ID == row.ID {
			writeJSON(w, http.StatusCreated, toBackfillShow(s))
			return
		}
	}
	h.writeBackfillError(w, r, grab.ErrBackfillNotFound)
}

// RemoveBackfill takes a show out of the queue.
func (h *Handlers) RemoveBackfill(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Grab.BackfillRemove(r.Context(), id); err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	// If the show that was being worked on went, the next one has started.
	if h.Jobs != nil {
		if _, _, err := h.Jobs.RunNow(r.Context(), grab.BackfillKind); err != nil {
			h.Logger.Warn("couldn't start the Backfill task", "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// MoveBackfill moves a waiting show up or down.
func (h *Handlers) MoveBackfill(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.MoveBackfillJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if body.Direction != "up" && body.Direction != "down" {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Move it up or down.", map[string]string{"direction": "Choose up or down."})
		return
	}
	if err := h.Grab.BackfillMove(r.Context(), id, string(body.Direction)); err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HideBackfill takes a finished show off the Needs a decision list.
func (h *Handlers) HideBackfill(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Grab.BackfillHide(r.Context(), id); err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetBackfillOptions searches for the releases a show's profile turned down.
func (h *Handlers) GetBackfillOptions(w http.ResponseWriter, r *http.Request, id int64) {
	opts, err := h.Grab.BackfillOptions(r.Context(), id)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	out := gen.BackfillOptions{Units: make([]gen.BackfillUnit, len(opts.Units)), Omitted: opts.Omitted}
	for i, u := range opts.Units {
		unit := gen.BackfillUnit{Label: u.Label, EpisodeIds: u.EpisodeIDs, Options: make([]gen.BackfillOption, len(u.Options))}
		for j, o := range u.Options {
			unit.Options[j] = gen.BackfillOption{
				Token: o.Token, Title: o.Title, Size: o.Size, Quality: o.Quality, Indexer: o.Indexer, Seeders: o.Seeders,
				Reasons: o.Reasons,
			}
		}
		out.Units[i] = unit
	}
	writeJSON(w, http.StatusOK, out)
}

// GrabBackfillOption downloads one of the offered releases anyway.
func (h *Handlers) GrabBackfillOption(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.GrabBackfillOptionJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	msg, err := h.Grab.BackfillGrabOption(r.Context(), id, body.Token)
	if err != nil {
		h.writeBackfillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": msg})
}
