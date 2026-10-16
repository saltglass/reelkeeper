// Package radarr is Reelkeeper's Radarr-compatible API, so Seerr and dashboards
// can use Reelkeeper as if it were Radarr 6.
//
// Each quality profile is its own "Radarr", at
// /compat/radarr/<profile-slug>/api/v3/..., showing only that profile's
// version of each movie. Seerr tracks requests per server, and skips adding
// a movie a server says it already has: if one shared endpoint answered for
// every profile, a remux request for a movie with a 1080p copy would look
// done and never download.
//
// Translation only: everything is done by the library, grab and download
// services. Covered: what Seerr (v3.5) and the Dashboard call (see
// testdata/compat/radarr for Radarr's recorded answers). Anything else is
// "not found".
package radarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/torrent"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Version is the Radarr version Reelkeeper reports: the one it replaces (Seerr
// changes behaviour by version).
const Version = "6.4.4.10685"

// Library is what the API needs from the library.
type Library interface {
	ProfileBySlug(ctx context.Context, slug string) (*library.Profile, error)
	CompiledProfile(ctx context.Context, id int64) (*decision.Profile, error)
	ProfileMovies(ctx context.Context, profileID int64, f library.ProfileMovieFilter) ([]library.ProfileMovie, error)
	RootFolders(ctx context.Context) ([]library.RootFolder, error)
	Tags(ctx context.Context) ([]library.Tag, error)
	CreateTag(ctx context.Context, label string) (*library.Tag, error)
	RenameTag(ctx context.Context, id int64, label string) (*library.Tag, error)
	SetVersionTags(ctx context.Context, versionID int64, tagIDs []int64) error
	TitleByTMDB(ctx context.Context, tmdbID int) (*library.Title, error)
	TMDBMovie(ctx context.Context, tmdbID int) (*library.Title, error)
	Lookup(ctx context.Context, query string) ([]library.LookupResult, error)
	AddMovie(ctx context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*library.MovieView, error)
	SetMonitored(ctx context.Context, titleID, versionID int64, monitored bool) error
	DeleteVersion(ctx context.Context, titleID, versionID int64, deleteFiles bool) (library.DeleteResult, error)
}

// Downloads is what the API needs from the download queue.
type Downloads interface {
	Queue(ctx context.Context) ([]download.Item, error, error)
	RemoveForVersion(ctx context.Context, versionID int64) error
	// ClientName is the download client's name, shown as the queue's
	// "downloadClient".
	ClientName() string
}

// Deps are the API's dependencies.
type Deps struct {
	Library   Library
	Downloads Downloads
	// Search queues a search for a version (grab.QueueSearch).
	Search func(ctx context.Context, versionID int64) error
	// CheckDownloads asks for an immediate download check (Seerr's
	// RefreshMonitoredDownloads).
	CheckDownloads func(ctx context.Context) error
	// URLBase is Reelkeeper's own URL base ("" or "/reelkeeper").
	URLBase string
	Logger  *slog.Logger
}

type handler struct {
	Deps
	now func() time.Time
}

type ctxKey struct{}

type scope struct {
	profile *library.Profile
	comp    *decision.Profile
	base    string // this endpoint's URL base, for system/status
}

// Router serves /compat/radarr/{slug}/api/v3/...; mount it at /compat.
func Router(d Deps) http.Handler {
	h := &handler{Deps: d, now: time.Now}
	r := chi.NewRouter()
	r.Route("/radarr/{slug}/api/v3", func(r chi.Router) {
		r.Use(h.authorized, h.scoped)
		r.Get("/system/status", h.status)
		for _, p := range []string{"/qualityProfile", "/qualityprofile"} {
			r.Get(p, h.qualityProfiles)
		}
		for _, p := range []string{"/rootfolder", "/rootFolder"} {
			r.Get(p, h.rootFolders)
		}
		r.Get("/tag", h.tags)
		r.Get("/tag/{id}", h.tag)
		r.Post("/tag", h.createTag)
		r.Put("/tag/{id}", h.renameTag)
		r.Get("/movie", h.movies)
		r.Post("/movie", h.addMovie)
		r.Put("/movie", h.updateMovie)
		r.Get("/movie/lookup", h.lookup)
		r.Get("/movie/{id}", h.movie)
		r.Put("/movie/{id}", h.updateMovie)
		r.Delete("/movie/{id}", h.deleteMovie)
		r.Get("/queue", h.queue)
		r.Get("/calendar", h.calendar)
		r.Post("/command", h.command)
		r.NotFound(notFound)
	})
	r.NotFound(notFound)
	return r
}

// authorized requires the API key (or a signed-in session), as Radarr does.
func (h *handler) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.Info(r.Context()).Identity == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// scoped finds the profile this "Radarr" stands for.
func (h *handler) scoped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
		p, err := h.Library.ProfileBySlug(r.Context(), slug)
		if err != nil || p.AppliesTo == library.Series {
			notFound(w, r)
			return
		}
		comp, err := h.Library.CompiledProfile(r.Context(), p.ID)
		if err != nil {
			h.fail(w, err)
			return
		}
		sc := &scope{profile: p, comp: comp, base: h.URLBase + "/compat/radarr/" + slug}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sc)))
	})
}

func scopeOf(r *http.Request) *scope { return r.Context().Value(ctxKey{}).(*scope) }

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "NotFound"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// validationFailure is Radarr's 400 answer for a bad request.
func validationFailure(w http.ResponseWriter, property, message string) {
	writeJSON(w, http.StatusBadRequest, []map[string]any{{
		"propertyName": property, "errorMessage": message, "severity": "error",
	}})
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	var v validation.Errors
	switch {
	case errors.Is(err, library.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "NotFound"})
	case errors.As(err, &v):
		for field, msg := range v {
			validationFailure(w, field, msg)
			return
		}
	default:
		h.Logger.Error("radarr-compatible API failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	now := h.now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"appName": "Radarr", "instanceName": "Reelkeeper (" + sc.profile.Name + ")", "version": Version,
		"buildTime": radarrTime(now), "isDebug": false, "isProduction": true, "isAdmin": false,
		"isUserInteractive": false, "startupPath": "/app", "appData": "/config", "osName": "reelkeeper", "osVersion": "",
		"isNetCore": true, "isLinux": true, "isOsx": false, "isWindows": false, "isDocker": true,
		"isContainerized": true, "mode": "console", "branch": "master", "databaseType": "postgreSQL",
		"databaseVersion": "", "authentication": "forms", "migrationVersion": 242, "urlBase": sc.base,
		"runtimeVersion": "", "startTime": radarrTime(now), "packageUpdateMechanism": "docker",
		"packageAuthor": "Reelkeeper", "packageVersion": Version, "runtimeName": "reelkeeper",
	})
}

func (h *handler) qualityProfiles(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	var items []map[string]any
	cutoff := 0
	for i, it := range sc.comp.Items {
		var members []map[string]any
		for _, name := range itemMembers(it) {
			q, _ := decision.QualityByName(name)
			members = append(members, map[string]any{"quality": qualityOf(q), "items": []any{}, "allowed": it.Allowed})
		}
		entry := map[string]any{"items": []any{}, "allowed": it.Allowed}
		if len(it.Qualities) > 0 {
			entry["name"], entry["items"], entry["id"] = it.Name, members, 1000+i
		} else {
			q, _ := decision.QualityByName(it.Name)
			entry["quality"] = qualityOf(q)
		}
		if strings.EqualFold(it.Name, sc.comp.UpgradeUntil) {
			if len(it.Qualities) > 0 {
				cutoff = 1000 + i
			} else {
				q, _ := decision.QualityByName(it.Name)
				cutoff = q.ID
			}
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"id": sc.profile.ID, "name": sc.profile.Name, "upgradeAllowed": sc.comp.UpgradesAllowed, "cutoff": cutoff,
		"items": items, "minFormatScore": sc.comp.MinScore, "cutoffFormatScore": sc.comp.UpgradeUntilScore,
		"minUpgradeFormatScore": sc.comp.MinUpgradeStep, "formatItems": []any{},
		"language": map[string]any{"id": -1, "name": "Any"},
	}})
}

func itemMembers(it decision.Item) []string {
	if len(it.Qualities) > 0 {
		return it.Qualities
	}
	return []string{it.Name}
}

// movieRoots are the movie media folders this profile files into (those
// whose default profile it is), or every movie folder if none is.
func (h *handler) movieRoots(ctx context.Context, sc *scope) ([]library.RootFolder, error) {
	all, err := h.Library.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	var mine, movies []library.RootFolder
	for _, r := range all {
		if r.MediaType != library.Movie {
			continue
		}
		movies = append(movies, r)
		if r.DefaultProfileID == sc.profile.ID {
			mine = append(mine, r)
		}
	}
	if len(mine) > 0 {
		return mine, nil
	}
	return movies, nil
}

func (h *handler) rootFolders(w http.ResponseWriter, r *http.Request) {
	roots, err := h.movieRoots(r.Context(), scopeOf(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []map[string]any{}
	for _, root := range roots {
		var st syscall.Statfs_t
		free, accessible := int64(0), syscall.Statfs(root.Path, &st) == nil
		if accessible {
			free = int64(st.Bavail * uint64(st.Bsize)) //nolint:gosec // free bytes fit in int64; Bsize is int64 on Linux, uint32 on macOS
		}
		out = append(out, map[string]any{
			"id": root.ID, "path": root.Path, "accessible": accessible, "freeSpace": free, "unmappedFolders": []any{},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) tags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.Library.Tags(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	if tags == nil {
		tags = []library.Tag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

func (h *handler) tag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	tags, err := h.Library.Tags(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	for _, t := range tags {
		if t.ID == id {
			writeJSON(w, http.StatusOK, t)
			return
		}
	}
	notFound(w, r)
}

func (h *handler) createTag(w http.ResponseWriter, r *http.Request) {
	var body struct{ Label string }
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		validationFailure(w, "label", "The request isn't valid JSON.")
		return
	}
	t, err := h.Library.CreateTag(r.Context(), body.Label)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *handler) renameTag(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var body struct{ Label string }
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		validationFailure(w, "label", "The request isn't valid JSON.")
		return
	}
	t, err := h.Library.RenameTag(r.Context(), id, body.Label)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (h *handler) movies(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	f := library.ProfileMovieFilter{}
	if v := r.URL.Query().Get("tmdbId"); v != "" {
		f.TMDBID, _ = strconv.Atoi(v)
		if f.TMDBID == 0 {
			writeJSON(w, http.StatusOK, []movie{})
			return
		}
	}
	pms, err := h.Library.ProfileMovies(r.Context(), sc.profile.ID, f)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]movie, 0, len(pms))
	for i := range pms {
		out = append(out, h.movieResource(pms[i].Title, &pms[i], sc.comp))
	}
	writeJSON(w, http.StatusOK, out)
}

// one finds this profile's version with the given ID.
func (h *handler) one(ctx context.Context, sc *scope, id int64) (*library.ProfileMovie, error) {
	if id <= 0 {
		return nil, library.ErrNotFound
	}
	pms, err := h.Library.ProfileMovies(ctx, sc.profile.ID, library.ProfileMovieFilter{VersionID: id})
	if err != nil {
		return nil, err
	}
	if len(pms) == 0 {
		return nil, library.ErrNotFound
	}
	return &pms[0], nil
}

func (h *handler) movie(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	pm, err := h.one(r.Context(), sc, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.movieResource(pm.Title, pm, sc.comp))
}

// lookup answers "tmdb:603", "imdb:tt0133093" or a title search. A movie
// this profile already has comes back as the library entry (with its id),
// which is how Seerr tells "already there" from "not added".
func (h *handler) lookup(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	var tmdbIDs []int
	switch {
	case strings.HasPrefix(term, "tmdb:"):
		if id, err := strconv.Atoi(strings.TrimPrefix(term, "tmdb:")); err == nil {
			tmdbIDs = []int{id}
		}
	case term != "":
		res, err := h.Library.Lookup(ctx, strings.TrimPrefix(term, "imdb:"))
		if err != nil {
			h.fail(w, err)
			return
		}
		for _, r := range res {
			tmdbIDs = append(tmdbIDs, r.ID)
		}
	}
	out := []movie{}
	for _, id := range tmdbIDs {
		pms, err := h.Library.ProfileMovies(ctx, sc.profile.ID, library.ProfileMovieFilter{TMDBID: id})
		if err != nil {
			h.fail(w, err)
			return
		}
		if len(pms) > 0 {
			m := h.movieResource(pms[0].Title, &pms[0], sc.comp)
			m.Folder = pms[0].Version.Folder
			if pms[0].Title.PosterPath != "" {
				m.RemotePoster = imageBase + pms[0].Title.PosterPath
			}
			out = append(out, m)
			continue
		}
		t, err := h.Library.TitleByTMDB(ctx, id)
		if errors.Is(err, library.ErrNotFound) {
			t, err = h.Library.TMDBMovie(ctx, id)
		}
		if errors.Is(err, library.ErrNotFound) {
			continue
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		out = append(out, h.movieResource(*t, nil, sc.comp))
	}
	writeJSON(w, http.StatusOK, out)
}

// movieBody is what Seerr sends to add or update a movie.
type movieBody struct {
	ID               int64   `json:"id"`
	TmdbID           int     `json:"tmdbId"`
	Title            string  `json:"title"`
	Year             int     `json:"year"`
	QualityProfileID int64   `json:"qualityProfileId"`
	RootFolderPath   string  `json:"rootFolderPath"`
	Monitored        *bool   `json:"monitored"`
	Tags             []int64 `json:"tags"`
	AddOptions       *struct {
		SearchForMovie bool `json:"searchForMovie"`
	} `json:"addOptions"`
}

func (h *handler) addMovie(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body movieBody
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.TmdbID <= 0 {
		validationFailure(w, "tmdbId", "A TMDB ID is required.")
		return
	}
	if existing, err := h.Library.ProfileMovies(ctx, sc.profile.ID, library.ProfileMovieFilter{TMDBID: body.TmdbID}); err != nil {
		h.fail(w, err)
		return
	} else if len(existing) > 0 {
		validationFailure(w, "TmdbId", "This movie has already been added")
		return
	}
	roots, err := h.movieRoots(ctx, sc)
	if err != nil {
		h.fail(w, err)
		return
	}
	i := slices.IndexFunc(roots, func(root library.RootFolder) bool {
		return strings.TrimRight(root.Path, "/") == strings.TrimRight(body.RootFolderPath, "/")
	})
	if i < 0 {
		validationFailure(w, "RootFolderPath", fmt.Sprintf("Root folder '%s' does not exist", body.RootFolderPath))
		return
	}
	monitored := body.Monitored == nil || *body.Monitored
	mv, err := h.Library.AddMovie(ctx, body.TmdbID, sc.profile.ID, roots[i].ID, monitored)
	if err != nil {
		h.fail(w, err)
		return
	}
	var versionID int64
	for _, v := range mv.Versions {
		if v.ProfileID == sc.profile.ID {
			versionID = v.ID
		}
	}
	if len(body.Tags) > 0 {
		if err := h.Library.SetVersionTags(ctx, versionID, body.Tags); err != nil {
			h.fail(w, err)
			return
		}
	}
	h.Logger.Info("movie added through the Radarr-compatible API", "title", mv.Title.Title, "profile", sc.profile.Name)
	if monitored && body.AddOptions != nil && body.AddOptions.SearchForMovie {
		h.search(ctx, versionID)
	}
	pm, err := h.one(ctx, sc, versionID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.movieResource(pm.Title, pm, sc.comp))
}

// updateMovie applies what Reelkeeper can change through Radarr's API:
// monitored and tags (and a search, when asked). Moving a movie to another
// folder or profile isn't done this way in Reelkeeper; those fields are ignored.
func (h *handler) updateMovie(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body movieBody
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		validationFailure(w, "id", "The request isn't valid JSON.")
		return
	}
	if id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64); err == nil && id > 0 {
		body.ID = id
	}
	pm, err := h.one(ctx, sc, body.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if body.Monitored != nil && *body.Monitored != pm.Version.Monitored {
		if err := h.Library.SetMonitored(ctx, pm.Title.ID, pm.Version.ID, *body.Monitored); err != nil {
			h.fail(w, err)
			return
		}
	}
	if body.Tags != nil {
		if err := h.Library.SetVersionTags(ctx, pm.Version.ID, body.Tags); err != nil {
			h.fail(w, err)
			return
		}
	}
	if body.AddOptions != nil && body.AddOptions.SearchForMovie {
		h.search(ctx, pm.Version.ID)
	}
	if pm, err = h.one(ctx, sc, body.ID); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.movieResource(pm.Title, pm, sc.comp))
}

// deleteMovie removes this profile's version. Seerr asks to delete files
// too; Reelkeeper keeps them (the rule: never lose media through a side door),
// and the folder goes on the skip list so scans don't add it back.
func (h *handler) deleteMovie(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	pm, err := h.one(ctx, sc, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Downloads.RemoveForVersion(ctx, pm.Version.ID); err != nil && !errors.Is(err, download.ErrNoClient) {
		h.fail(w, err)
		return
	}
	if _, err := h.Library.DeleteVersion(ctx, pm.Title.ID, pm.Version.ID, false); err != nil {
		h.fail(w, err)
		return
	}
	h.Logger.Info("movie version removed through the Radarr-compatible API (files kept)",
		"title", pm.Title.Title, "profile", sc.profile.Name, "deleteFilesAsked", r.URL.Query().Get("deleteFiles"))
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) search(ctx context.Context, versionID int64) {
	if h.Search == nil {
		return
	}
	if err := h.Search(ctx, versionID); err != nil {
		h.Logger.Warn("couldn't queue a search from the Radarr-compatible API", "error", err)
	}
}

func (h *handler) command(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body struct {
		Name     string  `json:"name"`
		MovieIDs []int64 `json:"movieIds"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name == "" {
		validationFailure(w, "name", "A command name is required.")
		return
	}
	switch body.Name {
	case "MoviesSearch":
		for _, id := range body.MovieIDs {
			if pm, err := h.one(ctx, sc, id); err == nil {
				h.search(ctx, pm.Version.ID)
			}
		}
	case "RefreshMonitoredDownloads":
		if h.CheckDownloads != nil {
			if err := h.CheckDownloads(ctx); err != nil {
				h.Logger.Warn("couldn't queue a download check", "error", err)
			}
		}
	}
	// Other commands (refreshes, rescans) have nothing to do in Reelkeeper,
	// which keeps itself up to date; they're accepted so callers carry on.
	now := radarrTime(h.now())
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": 1, "name": body.Name, "commandName": body.Name, "status": "queued", "queued": now, "started": now,
		"trigger": "manual", "priority": "normal", "body": map[string]any{"movieIds": body.MovieIDs},
	})
}

// queue lists downloads for this profile's versions, the way Radarr's
// /queue pages them (10 per page unless asked).
func (h *handler) queue(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 1)
	if q.Get("pageSize") == "" {
		size = 10
	}
	items, _, err := h.Downloads.Queue(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	pms, err := h.Library.ProfileMovies(ctx, sc.profile.ID, library.ProfileMovieFilter{})
	if err != nil {
		h.fail(w, err)
		return
	}
	byVersion := map[int64]*library.ProfileMovie{}
	for i := range pms {
		byVersion[pms[i].Version.ID] = &pms[i]
	}
	includeMovie := q.Get("includeMovie") == "true"
	var records []map[string]any
	for _, it := range items {
		if it.Download.VersionID == nil || byVersion[*it.Download.VersionID] == nil {
			continue
		}
		pm := byVersion[*it.Download.VersionID]
		rec := h.queueRecord(it, pm)
		if includeMovie {
			rec["movie"] = h.movieResource(pm.Title, pm, sc.comp)
		}
		records = append(records, rec)
	}
	total := len(records)
	from := min((page-1)*size, total)
	to := min(from+size, total)
	pageRecords := records[from:to]
	if pageRecords == nil {
		pageRecords = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"page": page, "pageSize": size, "sortKey": "timeleft", "sortDirection": "ascending",
		"totalRecords": total, "records": pageRecords,
	})
}

func (h *handler) queueRecord(it download.Item, pm *library.ProfileMovie) map[string]any {
	d := it.Download
	q, _ := decision.QualityByName(d.Quality)
	rec := map[string]any{
		"id": d.ID, "movieId": pm.Version.ID, "title": d.ReleaseTitle, "downloadId": strings.ToUpper(d.Hash),
		"protocol": "torrent", "downloadClient": h.Downloads.ClientName(), "downloadClientHasPostImportCategory": false,
		"indexer": d.Indexer, "added": radarrTime(d.CreatedAt), "size": d.Size, "sizeleft": d.Size,
		"languages":     []language{{decision.LanguageEnglish, "English"}},
		"quality":       qualityModel{Quality: qualityOf(q), Revision: revision{Version: 1}},
		"customFormats": []any{}, "customFormatScore": d.Score, "statusMessages": []any{},
		"status": "queued", "trackedDownloadStatus": "ok", "trackedDownloadState": "downloading", "outputPath": "",
	}
	if t := it.Torrent; t != nil {
		rec["size"], rec["sizeleft"], rec["outputPath"] = t.Size, t.Left, t.ContentPath
		rec["status"] = queueStatus(t.State, t.Done())
		if t.ETA > 0 {
			rec["timeleft"] = fmt.Sprintf("%02d:%02d:%02d", int(t.ETA.Hours()), int(t.ETA.Minutes())%60, int(t.ETA.Seconds())%60)
			rec["estimatedCompletionTime"] = radarrTime(h.now().Add(t.ETA))
		}
		if t.Done() {
			rec["trackedDownloadState"] = "importPending"
		}
	}
	if d.Status == download.StatusImportFailed {
		rec["trackedDownloadStatus"], rec["trackedDownloadState"] = "warning", "importBlocked"
		rec["statusMessages"] = []map[string]any{{"title": d.ReleaseTitle, "messages": []string{d.Message}}}
	}
	return rec
}

// queueStatus is Radarr's download status for a torrent state.
func queueStatus(state torrent.State, done bool) string {
	switch {
	case done:
		return "completed"
	case state == torrent.Queued:
		return "queued"
	case state == torrent.Paused:
		return "paused"
	case state == torrent.Error || state == torrent.MissingFiles:
		return "warning"
	}
	return "downloading"
}

// calendar lists this profile's movies with a release date between start
// and end (by default only monitored ones), as the Dashboard shows them.
func (h *handler) calendar(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	q := r.URL.Query()
	day := func(s, def string) string {
		if len(s) >= 10 {
			return s[:10]
		}
		return def
	}
	today := h.now().UTC().Format("2006-01-02")
	start, end := day(q.Get("start"), today), day(q.Get("end"), h.now().UTC().AddDate(0, 0, 7).Format("2006-01-02"))
	unmonitored := q.Get("unmonitored") == "true"
	pms, err := h.Library.ProfileMovies(r.Context(), sc.profile.ID, library.ProfileMovieFilter{})
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []movie{}
	for i := range pms {
		pm := &pms[i]
		if !pm.Version.Monitored && !unmonitored {
			continue
		}
		for _, d := range []string{pm.Title.TheatricalRelease, pm.Title.DigitalRelease, pm.Title.PhysicalRelease} {
			if d != "" && d >= start && d <= end {
				out = append(out, h.movieResource(pm.Title, pm, sc.comp))
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
