package api

import (
	"context"
	"net/http"
	"time"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/settings"
)

func toUsenetClient(u settings.UsenetClient) gen.UsenetClient {
	return gen.UsenetClient{Type: gen.UsenetClientType(u.ClientType()), Url: u.URL, ApiKeySet: u.APIKey != ""}
}

// usenetUpdateFrom is what the request asks for. A request without a type
// keeps the current one.
func usenetUpdateFrom(b gen.UsenetClientUpdate) settings.UsenetClientUpdate {
	u := settings.UsenetClientUpdate{URL: b.Url, APIKey: b.ApiKey}
	if b.Type != nil {
		u.Type = string(*b.Type)
	}
	return u
}

// GetUsenetClient returns the Usenet client connection, without the API key.
func (h *Handlers) GetUsenetClient(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toUsenetClient(h.Settings.UsenetClient()))
}

// PutUsenetClient saves the Usenet client connection. It is separate from
// the torrent client: nothing moves when it changes.
func (h *Handlers) PutUsenetClient(w http.ResponseWriter, r *http.Request) {
	var body gen.PutUsenetClientJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveUsenetClient(r.Context(), usenetUpdateFrom(body))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUsenetClient(saved))
}

// TestUsenetClient tries unsaved Usenet client values.
func (h *Handlers) TestUsenetClient(w http.ResponseWriter, r *http.Request) {
	var body gen.TestUsenetClientJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	cfg, err := h.Settings.ApplyUsenetClient(usenetUpdateFrom(body))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if !cfg.Configured() {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Enter SABnzbd's address first.",
			map[string]string{"url": "Enter SABnzbd's address, like http://192.168.1.20:8080."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	version, warnings, err := h.Downloads.TestUsenet(ctx, cfg)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "usenet_client", capitalize(err.Error())+".", nil)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version  string   `json:"version"`
		Warnings []string `json:"warnings"`
	}{version, nonNil(warnings)})
}
