// Package server builds Reelkeeper's HTTP handler: routing, middleware and the
// endpoints that don't belong to a feature package (such as /ping).
//
// Layout below the URL base:
//
//	/ping      healthcheck (also at the root, whatever the URL base)
//	/api/v1/…  native API (internal/api)
//	/…         the web UI (single-page app)
package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Options configures the handler.
type Options struct {
	// URLBase is the normalized path prefix ("" or "/something").
	URLBase string
	Logger  *slog.Logger
	// Identify works out who is making each request (auth.Service.Identify).
	// Optional; tests of /ping leave it out.
	Identify func(http.Handler) http.Handler
	// API serves /api/v1. Optional.
	API http.Handler
	// Compat serves /compat: the Radarr- and Sonarr-compatible APIs.
	// Optional.
	Compat http.Handler
	// UI serves the web app. Optional.
	UI http.Handler
	// ClientAddr returns a request's real client address for the log
	// (auth.Service.ClientAddr, which honours trusted proxies). Optional;
	// without it the connection's address is logged.
	ClientAddr func(*http.Request) string
}

// APIPrefix is where the native API is mounted below the URL base.
const APIPrefix = "/api/v1"

// New returns the root HTTP handler.
func New(opts Options) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(opts.Logger, opts.URLBase+"/ping", opts.ClientAddr))
	r.Use(securityHeaders)

	// /ping is always served at the root as well as under the URL base, so
	// `reelkeeper healthcheck` and container healthchecks work however the URL base
	// is set.
	r.Get("/ping", ping)

	app := chi.NewRouter()
	if opts.Identify != nil {
		app.Use(opts.Identify)
	}
	if opts.URLBase != "" {
		app.Get("/ping", ping)
	}
	if opts.API != nil {
		app.Mount(APIPrefix, opts.API)
	}
	if opts.Compat != nil {
		app.Mount("/compat", opts.Compat)
	}
	if opts.UI != nil {
		app.Handle("/*", opts.UI)
	}

	if opts.URLBase == "" {
		r.Mount("/", app)
	} else {
		r.Mount(opts.URLBase, app)
		// Visiting the bare host goes to the app.
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, opts.URLBase+"/", http.StatusFound)
		})
	}
	return r
}

// ping is unauthenticated and does no I/O, so a healthcheck never fails
// because of a slow dependency. The body matches Radarr's /ping.
func ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// securityHeaders sets browser protections on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs one line per request. Healthcheck pings are logged at
// debug level so they don't flood the log.
func requestLogger(logger *slog.Logger, basePing string, clientAddr func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			level := slog.LevelInfo
			if r.URL.Path == "/ping" || r.URL.Path == basePing {
				level = slog.LevelDebug
			}
			client := r.RemoteAddr
			if clientAddr != nil {
				if c := clientAddr(r); c != "" {
					client = c
				}
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Duration("duration", time.Since(start)),
				slog.String("client", client),
			)
		})
	}
}
