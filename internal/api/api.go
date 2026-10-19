// Package api serves Reelkeeper's native REST API under /api/v1.
//
// Handlers only translate between HTTP and the services (auth, settings,
// ...); business rules live in those packages. Request and response types
// are generated from api/openapi.yaml into internal/api/gen.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/backup"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/events"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/migrate"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// maxBodyBytes caps request bodies. Nothing in the API needs more.
const maxBodyBytes = 1 << 20

// Deps are the services the handlers use.
type Deps struct {
	Store    *store.Store
	Settings *settings.Service
	Auth     *auth.Service
	Events   *events.Broker
	Jobs     *jobs.Manager
	Library  *library.Service
	Indexers *indexer.Service
	// Grab runs searches; History is what they (and later grabs and
	// imports) recorded.
	Grab    *grab.Service
	History *grab.History
	// Downloads is the download client and Reelkeeper's queue.
	Downloads *download.Service
	// Migrate imports from Radarr at cutover.
	Migrate *migrate.Service
	// Backups makes, lists and restores Reelkeeper's own backups.
	Backups *backup.Service
	// Notify sends Emby, Jellyfin, email and webhook notifications.
	Notify *notify.Service
	// TMDBBaseURL and ImageBaseURL override TMDB's addresses (tests use
	// fakes); empty means the real ones.
	TMDBBaseURL  string
	ImageBaseURL string
	Logger       *slog.Logger
	URLBase      string
	DataDir      string
	StartedAt    time.Time
}

// Handlers implements gen.ServerInterface.
type Handlers struct {
	Deps
}

var _ gen.ServerInterface = (*Handlers)(nil)

// publicOperations need no sign-in, keyed by "METHOD /path" below /api/v1.
// They must match the operations marked `security: []` in openapi.yaml;
// TestPublicOperationsMatchSpec checks that.
var publicOperations = map[string]bool{
	"GET /auth/state":          true,
	"POST /auth/setup":         true,
	"POST /auth/setup/restore": true,
	"POST /auth/login":         true,
	"POST /auth/logout":        true,
	"GET /auth/oidc/login":     true,
	"GET /auth/oidc/callback":  true,
}

// Router returns the API handler. Mount it at URLBase + "/api/v1". It expects
// auth.Identify to have run already.
func Router(d Deps) http.Handler {
	if d.ImageBaseURL == "" {
		d.ImageBaseURL = metadata.ImageBase
	}
	h := &Handlers{Deps: d}
	r := chi.NewRouter()
	r.Use(limitBody)
	// Refuses state-changing requests made by other websites in the
	// browser (CSRF). API clients such as Seerr send no browser headers and
	// pass through.
	r.Use(http.NewCrossOriginProtection().Handler)
	r.Use(h.requireAuth)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "There is no such API endpoint.", nil)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint doesn't support that method.", nil)
	})
	return gen.HandlerWithOptions(h, gen.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error(), nil)
		},
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Uploading a backup is the one request that is allowed to be big;
		// its handler sets its own, larger limit.
		if r.Method != http.MethodPost || !largeUploads[routePath(r)] {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth refuses requests that aren't signed in, except public ones.
func (h *Handlers) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicOperations[r.Method+" "+routePath(r)] {
			next.ServeHTTP(w, r)
			return
		}
		ri := auth.Info(r.Context())
		switch {
		case ri.BadAPIKey:
			writeError(w, http.StatusUnauthorized, "invalid_api_key", "The API key is wrong. Copy it again from Settings → Security.", nil)
		case ri.Identity == nil:
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.", nil)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// routePath is the request path below the API prefix.
func routePath(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	return r.URL.Path
}

// decode reads a JSON body into v, answering 400 itself on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The request is too large.", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_json", "The request body isn't valid JSON: "+err.Error(), nil)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	e := gen.Error{Code: code, Message: message}
	if len(fields) > 0 {
		e.Fields = &fields
	}
	writeJSON(w, status, e)
}

// writeServiceError maps errors from the services to responses. Anything
// unexpected is logged and reported as a 500 without internal details.
func (h *Handlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errs, ok := validation.As(err); ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Some fields need attention.", errs)
		return
	}
	h.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong. The details are in the log.", nil)
}

// cookiePath scopes cookies to Reelkeeper's URL base.
func (h *Handlers) cookiePath() string { return h.URLBase + "/" }
