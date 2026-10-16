package api

import (
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/buildinfo"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// GetGeneralSettings returns Settings → General.
func (h *Handlers) GetGeneralSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toGeneral(h.Settings.General()))
}

// PutGeneralSettings saves Settings → General.
func (h *Handlers) PutGeneralSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutGeneralSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveGeneral(r.Context(), settings.General{
		InstanceName: body.InstanceName,
		LogLevel:     string(body.LogLevel),
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGeneral(saved))
}

func toGeneral(g settings.General) gen.GeneralSettings {
	return gen.GeneralSettings{InstanceName: g.InstanceName, LogLevel: gen.LogLevel(g.LogLevel)}
}

// GetSecuritySettings returns Settings → Security.
func (h *Handlers) GetSecuritySettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.toSecurity(r, h.Settings.Security()))
}

// PutSecuritySettings saves Settings → Security.
func (h *Handlers) PutSecuritySettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutSecuritySettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveSecurity(r.Context(), settings.SecurityUpdate{
		LocalLoginEnabled: body.LocalLoginEnabled,
		BypassNetworks:    body.BypassNetworks,
		TrustedProxies:    body.TrustedProxies,
		SessionDays:       body.SessionDays,
		OIDC: settings.OIDCUpdate{
			Enabled:       body.Oidc.Enabled,
			IssuerURL:     body.Oidc.IssuerUrl,
			ClientID:      body.Oidc.ClientId,
			ClientSecret:  body.Oidc.ClientSecret,
			Scopes:        body.Oidc.Scopes,
			GroupsClaim:   body.Oidc.GroupsClaim,
			RequiredGroup: body.Oidc.RequiredGroup,
			ButtonLabel:   body.Oidc.ButtonLabel,
		},
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toSecurity(r, saved))
}

func (h *Handlers) toSecurity(r *http.Request, sec settings.Security) gen.SecuritySettings {
	unreadable := h.Settings.UnreadableCredentials()
	redirect := auth.CallbackURL(r, settings.Prefixes(sec.TrustedProxies), h.URLBase)
	apiKey := sec.APIKey
	return gen.SecuritySettings{
		LocalLoginEnabled: sec.LocalLoginEnabled,
		BypassNetworks:    sec.BypassNetworks,
		TrustedProxies:    sec.TrustedProxies,
		SessionDays:       sec.SessionDays,
		ApiKey:            &apiKey,
		Oidc: gen.OidcSettings{
			Enabled:         sec.OIDC.Enabled,
			IssuerUrl:       sec.OIDC.IssuerURL,
			ClientId:        sec.OIDC.ClientID,
			ClientSecretSet: sec.OIDC.ClientSecret != "" || slices.Contains(unreadable, "oidc.clientSecret"),
			Scopes:          sec.OIDC.Scopes,
			GroupsClaim:     sec.OIDC.GroupsClaim,
			RequiredGroup:   sec.OIDC.RequiredGroup,
			ButtonLabel:     sec.OIDC.ButtonLabel,
			RedirectUrl:     &redirect,
			LastSuccessAt:   sec.OIDC.LastSuccessAt,
		},
		UnreadableCredentials: &unreadable,
	}
}

// RegenerateApiKey replaces the API key.
func (h *Handlers) RegenerateApiKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.Settings.RegenerateAPIKey(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	h.Logger.Warn("API key regenerated; apps using the old key must be updated")
	writeJSON(w, http.StatusOK, map[string]string{"apiKey": key})
}

// CheckOidc tests an identity provider's discovery document.
func (h *Handlers) CheckOidc(w http.ResponseWriter, r *http.Request) {
	var body gen.CheckOidcJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	ok, msg := h.Auth.CheckOIDC(r.Context(), body.IssuerUrl)
	writeJSON(w, http.StatusOK, gen.CheckResult{Ok: ok, Message: msg})
}

// GetSystemStatus reports version, database and memory.
func (h *Handlers) GetSystemStatus(w http.ResponseWriter, r *http.Request) {
	schema, err := h.Store.SchemaVersion(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	out := gen.SystemStatus{
		Version:       buildinfo.Version,
		GoVersion:     goVersion(),
		Os:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		StartedAt:     h.StartedAt.UTC(),
		UptimeSeconds: int64(time.Since(h.StartedAt).Seconds()),
		UrlBase:       h.URLBase,
		DataDir:       absPath(h.DataDir),
	}
	out.Database.Engine = gen.SystemStatusDatabaseEngine(h.Store.Engine)
	out.Database.SchemaVersion = schema
	out.Memory.HeapBytes = int64(mem.HeapAlloc) // #nosec G115 -- far below int64 max
	out.Memory.TotalBytes = int64(mem.Sys)      // #nosec G115 -- far below int64 max
	writeJSON(w, http.StatusOK, out)
}

// StreamEvents streams live updates.
func (h *Handlers) StreamEvents(w http.ResponseWriter, r *http.Request) {
	h.Events.ServeHTTP(w, r)
}

func goVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.GoVersion
	}
	return runtime.Version()
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// GetLabelSettings returns what the quality labels on the Movies page show.
func (h *Handlers) GetLabelSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toLabelSettings(h.Settings.Labels()))
}

// PutLabelSettings saves what the quality labels show.
func (h *Handlers) PutLabelSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutLabelSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	parts := make([]string, 0, len(body.Parts))
	for _, p := range body.Parts {
		parts = append(parts, string(p))
	}
	l, err := h.Settings.SaveLabels(r.Context(), settings.Labels{Parts: parts})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLabelSettings(l))
}

func toLabelSettings(l settings.Labels) gen.LabelSettings {
	parts := make([]gen.LabelPart, 0, len(l.Parts))
	for _, p := range l.Parts {
		parts = append(parts, gen.LabelPart(p))
	}
	return gen.LabelSettings{Parts: parts}
}
