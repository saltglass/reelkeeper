// Package ui serves the web app built from web/ (see `just build`).
//
// Vite writes the build into dist/, which is embedded into the binary.
// Asset URLs in the build are relative; index.html gets a <base href> with
// the URL base, so the same build works at "/" or under "/reelkeeper".
package ui

import (
	"bytes"
	"embed"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// notBuilt is served when the binary was built without the UI, such as
// during `go test` or a backend-only `go run`.
const notBuilt = `<!doctype html><meta charset="utf-8"><title>Reelkeeper</title>
<body style="font-family:system-ui;padding:2rem;background:#07060b;color:#ece9f5">
<h1>The web interface isn't built into this binary</h1>
<p>Run <code>just build</code> for a complete binary, or <code>just dev</code> while developing.
The API is running.</p>`

// Handler serves static files from the build and index.html for every
// other path, so the app's own router can handle deep links such as
// /settings/security.
func Handler(urlBase string) http.Handler { return apiNotFound(urlBase, pages(urlBase)) }

// apiNotFound answers API addresses nobody handles with a real "not
// found", never the web page: other apps (Seerr calling a Radarr API, say)
// would read the page as a broken answer.
func apiNotFound(urlBase string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(strings.TrimPrefix(path.Clean("/"+r.URL.Path), urlBase), "/")
		if p == "api" || p == "compat" || strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "compat/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pages(urlBase string) http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed path is fixed at compile time
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	page := injectBase(index, urlBase)
	static := http.FileServerFS(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), urlBase)
		p = strings.TrimPrefix(p, "/")
		if p != "" && p != "index.html" {
			if info, err := fs.Stat(files, p); err == nil && !info.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					// Vite puts a content hash in these names, so they never change.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + p
				static.ServeHTTP(w, r2)
				return
			}
			// A missing file with an extension (an old asset, a typo) is a
			// real 404, not a page for the app's router.
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})
}

// injectBase puts <base href="{urlBase}/"> where index.html has the
// <!--reelkeeper:base--> marker.
func injectBase(index []byte, urlBase string) []byte {
	tag := `<base href="` + html.EscapeString(urlBase+"/") + `">`
	return bytes.Replace(index, []byte("<!--reelkeeper:base-->"), []byte(tag), 1)
}
