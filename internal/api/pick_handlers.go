package api

import (
	"errors"
	"net/http"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
)

// "Choose a release" on a movie version: search now, list every release
// with the profile's verdict, and grab the one the user picks.

// GetMovieChoices searches for a movie version and lists what was found.
func (h *Handlers) GetMovieChoices(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	l, err := h.Grab.MovieChoices(r.Context(), id, versionID)
	if err != nil {
		h.writePickError(w, r, err)
		return
	}
	out := gen.MovieChoices{Releases: make([]gen.MovieChoice, len(l.Releases)), Hidden: l.Hidden, More: l.More}
	if len(l.IndexerErrors) > 0 {
		out.IndexerErrors = &l.IndexerErrors
	}
	for i, c := range l.Releases {
		reasons := c.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		out.Releases[i] = gen.MovieChoice{
			Token: c.Token, Title: c.Title, Size: c.Size, Quality: c.Quality, Indexer: c.Indexer, Seeders: c.Seeders,
			Score: c.Score, Accepted: c.Accepted, Reasons: reasons,
		}
		if c.Usenet {
			out.Releases[i].Usenet = &c.Usenet
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// GrabMovieChoice downloads the release the user picked.
func (h *Handlers) GrabMovieChoice(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	var body gen.GrabMovieChoiceJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	res, err := h.Grab.GrabMovieChoice(r.Context(), id, versionID, body.Token)
	if err != nil {
		h.writePickError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toPickResult(res))
}

func toPickResult(res grab.PickResult) gen.PickResult {
	out := gen.PickResult{Message: res.Message, DryRun: res.DryRun}
	if res.UpgradesOff {
		out.UpgradesOff = &res.UpgradesOff
	}
	return out
}

// writePickError answers for the errors "Choose a release" returns on
// purpose.
func (h *Handlers) writePickError(w http.ResponseWriter, r *http.Request, err error) {
	var refusal grab.Refusal
	switch {
	case errors.Is(err, grab.ErrOptionGone):
		writeError(w, http.StatusNotFound, "option_expired", "That list has expired. Choose a release again to search afresh.", nil)
	case errors.As(err, &refusal):
		writeError(w, http.StatusConflict, "refused", refusal.Error(), nil)
	default:
		h.writeLibraryError(w, r, err)
	}
}
