// Package sonarr is Reelkeeper's Sonarr-compatible API, so Seerr can use
// Reelkeeper as if it were Sonarr 4.
//
// Each quality profile is its own "Sonarr", at
// /compat/sonarr/<profile-slug>/api/v3/..., showing only that profile's
// version of each series (the same reasoning as the Radarr one: Seerr
// tracks requests per server).
//
// Translation only: everything is done by the library, grab and download
// services. Covered: what Seerr (v3.5) calls (its Sonarr client, scanner,
// availability sync and download tracker; see
// testdata/compat/sonarr for Sonarr's recorded answers). Anything
// else is "not found".
//
// Some things work differently underneath, and the API only translates:
//
//   - IDs: a series' id is its version's ID in Reelkeeper; an episode's id is
//     Reelkeeper's episode ID; an episode file's id is the media file's.
//   - Seerr names series by their TVDB ID. Reelkeeper looks that up on TMDB.
//   - Monitoring: Sonarr ticks seasons and episodes; Reelkeeper keeps a date
//     rule ("episodes airing from this day on") plus choices per episode.
//     A ticked season turns all its episodes on; Sonarr's "monitor new
//     items" is the date rule. A season shows as monitored when any of
//     its episodes is.
//   - Removing a series keeps its files, whatever Seerr asks (the rule:
//     never lose media through a side door).
package sonarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
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

// Version is the Sonarr version Reelkeeper reports: the one it replaces
// (Seerr changes behaviour by version; from 4 on it skips language
// profiles).
const Version = "4.0.20.3014"

// Library is what the API needs from the library.
type Library interface {
	ProfileBySlug(ctx context.Context, slug string) (*library.Profile, error)
	CompiledProfile(ctx context.Context, id int64) (*decision.Profile, error)
	ProfileSeries(ctx context.Context, profileID int64, f library.ProfileSeriesFilter) ([]library.ProfileSeries, error)
	RootFolders(ctx context.Context) ([]library.RootFolder, error)
	Tags(ctx context.Context) ([]library.Tag, error)
	CreateTag(ctx context.Context, label string) (*library.Tag, error)
	RenameTag(ctx context.Context, id int64, label string) (*library.Tag, error)
	SetVersionTags(ctx context.Context, versionID int64, tagIDs []int64) error
	SeriesTMDBID(ctx context.Context, source, id string) (int, error)
	SeriesPreview(ctx context.Context, tmdbID int) (*library.SeriesPreview, error)
	LookupSeries(ctx context.Context, query string) ([]library.SeriesLookupResult, error)
	AddSeries(ctx context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*library.SeriesDetail, error)
	ApplySeriesMonitoring(ctx context.Context, versionID int64, m library.SeriesMonitoring) error
	SetEpisodesMonitored(ctx context.Context, versionID int64, episodeIDs []int64, monitored bool) error
	DeleteSeriesVersion(ctx context.Context, titleID, versionID int64) (library.DeleteResult, error)
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
	// Search queues a search for a version's wanted episodes
	// (grab.QueueSearch).
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

// lookupLimit caps how many title-search results a lookup describes in
// full (each one is a TMDB request).
const lookupLimit = 5

// Router serves /compat/sonarr/{slug}/api/v3/...; mount it at /compat.
func Router(d Deps) http.Handler {
	h := &handler{Deps: d, now: time.Now}
	r := chi.NewRouter()
	r.Route("/sonarr/{slug}/api/v3", func(r chi.Router) {
		r.Use(h.authorized, h.scoped)
		r.Get("/system/status", h.status)
		for _, p := range []string{"/qualityProfile", "/qualityprofile"} {
			r.Get(p, h.qualityProfiles)
		}
		for _, p := range []string{"/rootfolder", "/rootFolder"} {
			r.Get(p, h.rootFolders)
		}
		for _, p := range []string{"/languageprofile", "/languageProfile"} {
			r.Get(p, h.languageProfiles)
		}
		r.Get("/tag", h.tags)
		r.Get("/tag/{id}", h.tag)
		r.Post("/tag", h.createTag)
		r.Put("/tag/{id}", h.renameTag)
		r.Get("/series", h.seriesList)
		r.Post("/series", h.addSeries)
		r.Put("/series", h.updateSeries)
		r.Get("/series/lookup", h.lookup)
		r.Get("/series/{id}", h.oneSeries)
		r.Put("/series/{id}", h.updateSeries)
		r.Delete("/series/{id}", h.deleteSeries)
		r.Get("/episode", h.episodes)
		r.Put("/episode/monitor", h.monitorEpisodes)
		r.Get("/queue", h.queue)
		r.Get("/calendar", h.calendar)
		r.Post("/command", h.command)
		r.NotFound(notFound)
	})
	r.NotFound(notFound)
	return r
}

// authorized requires the API key (or a signed-in session), as Sonarr does.
func (h *handler) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.Info(r.Context()).Identity == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// scoped finds the profile this "Sonarr" stands for: one that applies to
// TV.
func (h *handler) scoped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
		p, err := h.Library.ProfileBySlug(r.Context(), slug)
		if err != nil || p.AppliesTo == library.Movie {
			notFound(w, r)
			return
		}
		comp, err := h.Library.CompiledProfile(r.Context(), p.ID)
		if err != nil {
			h.fail(w, err)
			return
		}
		sc := &scope{profile: p, comp: comp, base: h.URLBase + "/compat/sonarr/" + slug}
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

// validationFailure is Sonarr's 400 answer for a bad request.
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
	case errors.Is(err, library.ErrInUse):
		validationFailure(w, "TvdbId", "This series has already been added")
	case errors.As(err, &v):
		for field, msg := range v {
			validationFailure(w, field, msg)
			return
		}
	default:
		h.Logger.Error("sonarr-compatible API failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
	}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	now := h.now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"appName": "Sonarr", "instanceName": "Reelkeeper (" + sc.profile.Name + ")", "version": Version,
		"buildTime": sonarrTime(now), "isDebug": false, "isProduction": true, "isAdmin": false,
		"isUserInteractive": false, "startupPath": "/app", "appData": "/config", "osName": "reelkeeper", "osVersion": "",
		"isNetCore": true, "isLinux": true, "isOsx": false, "isWindows": false, "isDocker": true,
		"mode": "console", "branch": "main", "authentication": "forms", "migrationVersion": 217,
		"urlBase": sc.base, "runtimeVersion": "", "runtimeName": "reelkeeper", "startTime": sonarrTime(now),
		"packageVersion": Version, "packageAuthor": "Reelkeeper", "packageUpdateMechanism": "docker",
		"databaseVersion": "", "databaseType": "postgreSQL",
	})
}

// qualityProfiles lists just this endpoint's profile, in Sonarr's
// qualities. Reelkeeper qualities Sonarr doesn't have are left out.
func (h *handler) qualityProfiles(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	var items []map[string]any
	cutoff := 0
	seen := map[int]bool{}
	for i, it := range sc.comp.Items {
		var members []map[string]any
		for _, name := range itemMembers(it) {
			q, ok := qualityOf(name)
			if !ok || seen[q.ID] {
				continue
			}
			seen[q.ID] = true
			members = append(members, map[string]any{"quality": q, "items": []any{}, "allowed": it.Allowed})
		}
		if len(members) == 0 {
			continue
		}
		entry := map[string]any{"items": []any{}, "allowed": it.Allowed}
		if len(it.Qualities) > 0 {
			entry["name"], entry["items"], entry["id"] = it.Name, members, 1000+i
		} else {
			entry["quality"] = members[0]["quality"]
		}
		if strings.EqualFold(it.Name, sc.comp.UpgradeUntil) {
			if len(it.Qualities) > 0 {
				cutoff = 1000 + i
			} else {
				cutoff = members[0]["quality"].(qualityDef).ID
			}
		}
		items = append(items, entry)
	}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"id": sc.profile.ID, "name": sc.profile.Name, "upgradeAllowed": sc.comp.UpgradesAllowed, "cutoff": cutoff,
		"items": items, "minFormatScore": sc.comp.MinScore, "cutoffFormatScore": sc.comp.UpgradeUntilScore,
		"minUpgradeFormatScore": sc.comp.MinUpgradeStep, "formatItems": []any{},
	}})
}

func itemMembers(it decision.Item) []string {
	if len(it.Qualities) > 0 {
		return it.Qualities
	}
	return []string{it.Name}
}

// languageProfiles is Sonarr 4's answer: language profiles are gone, and
// one placeholder is left for older clients.
func (h *handler) languageProfiles(w http.ResponseWriter, _ *http.Request) {
	english := map[string]any{"id": 1, "name": "English"}
	writeJSON(w, http.StatusOK, []map[string]any{{
		"name": "Deprecated", "upgradeAllowed": true, "cutoff": english,
		"languages": []map[string]any{{"language": english, "allowed": true}}, "id": 1,
	}})
}

// tvRoots are the TV media folders this profile files into (those whose
// default profile it is), or every TV folder if none is.
func (h *handler) tvRoots(ctx context.Context, sc *scope) ([]library.RootFolder, error) {
	all, err := h.Library.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	var mine, tv []library.RootFolder
	for _, r := range all {
		if r.MediaType != library.Series {
			continue
		}
		tv = append(tv, r)
		if r.DefaultProfileID == sc.profile.ID {
			mine = append(mine, r)
		}
	}
	if len(mine) > 0 {
		return mine, nil
	}
	return tv, nil
}

func (h *handler) rootFolders(w http.ResponseWriter, r *http.Request) {
	roots, err := h.tvRoots(r.Context(), scopeOf(r))
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

func (h *handler) resource(ps *library.ProfileSeries) series {
	return h.seriesResource(ps.Title, ps.Episodes, ps)
}

func (h *handler) seriesList(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	f := library.ProfileSeriesFilter{}
	if v := r.URL.Query().Get("tvdbId"); v != "" {
		f.TVDBID, _ = strconv.Atoi(v)
		if f.TVDBID == 0 {
			writeJSON(w, http.StatusOK, []series{})
			return
		}
	}
	list, err := h.Library.ProfileSeries(r.Context(), sc.profile.ID, f)
	if err != nil {
		h.fail(w, err)
		return
	}
	out := make([]series, 0, len(list))
	for i := range list {
		out = append(out, h.resource(&list[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// one finds this profile's version with the given ID.
func (h *handler) one(ctx context.Context, sc *scope, id int64) (*library.ProfileSeries, error) {
	if id <= 0 {
		return nil, library.ErrNotFound
	}
	list, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{VersionID: id})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, library.ErrNotFound
	}
	return &list[0], nil
}

func (h *handler) oneSeries(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ps, err := h.one(r.Context(), sc, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.resource(ps))
}

// lookup answers "tvdb:81189", "tmdb:1396", "imdb:tt0903747" or a title
// search. A series this profile already has comes back as the library
// entry (with its id), which is how Seerr tells "already there" from "not
// added".
func (h *handler) lookup(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	var tmdbIDs []int
	source, id, _ := strings.Cut(term, ":")
	switch source = strings.ToLower(source); {
	case term == "":
	case source == "tmdb":
		if n, err := strconv.Atoi(id); err == nil {
			tmdbIDs = []int{n}
		}
	case source == "tvdb" || source == "imdb":
		n, err := h.Library.SeriesTMDBID(ctx, source, strings.TrimSpace(id))
		if err != nil && !errors.Is(err, library.ErrNotFound) {
			h.fail(w, err)
			return
		}
		if err == nil {
			tmdbIDs = []int{n}
		}
	default:
		res, err := h.Library.LookupSeries(ctx, term)
		if err != nil {
			h.fail(w, err)
			return
		}
		for _, r := range res[:min(len(res), lookupLimit)] {
			tmdbIDs = append(tmdbIDs, r.ID)
		}
	}
	out := []series{}
	for _, id := range tmdbIDs {
		list, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{TMDBID: id})
		if err != nil {
			h.fail(w, err)
			return
		}
		if len(list) > 0 {
			s := h.resource(&list[0])
			s.Folder = list[0].Version.Folder
			if list[0].Title.PosterPath != "" {
				s.RemotePoster = imageBase + list[0].Title.PosterPath
			}
			out = append(out, s)
			continue
		}
		p, err := h.Library.SeriesPreview(ctx, id)
		if errors.Is(err, library.ErrNotFound) {
			continue
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		out = append(out, h.seriesResource(p.Title, p.Episodes, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// seriesBody is what Seerr sends to add or update a series.
type seriesBody struct {
	ID               int64   `json:"id"`
	TvdbID           int     `json:"tvdbId"`
	TmdbID           int     `json:"tmdbId"`
	Title            string  `json:"title"`
	QualityProfileID int64   `json:"qualityProfileId"`
	RootFolderPath   string  `json:"rootFolderPath"`
	Monitored        *bool   `json:"monitored"`
	MonitorNewItems  string  `json:"monitorNewItems"`
	Tags             []int64 `json:"tags"`
	Seasons          []struct {
		SeasonNumber int  `json:"seasonNumber"`
		Monitored    bool `json:"monitored"`
	} `json:"seasons"`
	AddOptions *struct {
		SearchForMissingEpisodes bool `json:"searchForMissingEpisodes"`
	} `json:"addOptions"`
}

func (h *handler) addSeries(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body seriesBody
	if json.NewDecoder(r.Body).Decode(&body) != nil || (body.TvdbID <= 0 && body.TmdbID <= 0) {
		validationFailure(w, "tvdbId", "A TVDB ID is required.")
		return
	}
	tmdbID := body.TmdbID
	if body.TvdbID > 0 {
		id, err := h.Library.SeriesTMDBID(ctx, "tvdb", strconv.Itoa(body.TvdbID))
		if errors.Is(err, library.ErrNotFound) {
			validationFailure(w, "TvdbId", fmt.Sprintf("TMDB doesn't know a series with TVDB ID %d", body.TvdbID))
			return
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		tmdbID = id
	}
	if existing, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{TMDBID: tmdbID}); err != nil {
		h.fail(w, err)
		return
	} else if len(existing) > 0 {
		validationFailure(w, "TvdbId", "This series has already been added")
		return
	}
	roots, err := h.tvRoots(ctx, sc)
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
	// Added unmonitored, then given exactly the requested seasons, so
	// nothing searches for every episode in between.
	d, err := h.Library.AddSeries(ctx, tmdbID, sc.profile.ID, roots[i].ID, false)
	if err != nil {
		h.fail(w, err)
		return
	}
	var versionID int64
	for _, v := range d.Versions {
		if v.ProfileID == sc.profile.ID {
			versionID = v.ID
		}
	}
	monitored := body.Monitored == nil || *body.Monitored
	newEpisodes := body.MonitorNewItems != "none"
	m := library.SeriesMonitoring{Monitored: &monitored, NewEpisodes: &newEpisodes, Seasons: map[int]bool{}}
	for _, s := range body.Seasons {
		m.Seasons[s.SeasonNumber] = s.Monitored
	}
	if len(body.Seasons) == 0 && monitored {
		// No seasons named: every season but the specials, as Sonarr does.
		for _, s := range d.SeasonList {
			m.Seasons[s.Number] = s.Number > 0
		}
	}
	if err := h.Library.ApplySeriesMonitoring(ctx, versionID, m); err != nil {
		h.fail(w, err)
		return
	}
	if len(body.Tags) > 0 {
		if err := h.Library.SetVersionTags(ctx, versionID, body.Tags); err != nil {
			h.fail(w, err)
			return
		}
	}
	h.Logger.Info("series added through the Sonarr-compatible API", "title", d.Title.Title, "profile", sc.profile.Name,
		"seasons", requestedSeasons(m.Seasons))
	if monitored && body.AddOptions != nil && body.AddOptions.SearchForMissingEpisodes {
		h.search(ctx, versionID)
	}
	ps, err := h.one(ctx, sc, versionID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.resource(ps))
}

func requestedSeasons(m map[int]bool) []int {
	var out []int
	for n, on := range m {
		if on {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// updateSeries applies what Reelkeeper can change through Sonarr's API:
// monitored, seasons ticked or unticked, and tags. A season whose tick
// changed turns all its episodes on or off; untouched seasons keep their
// own choices. Moving a series to another folder or profile isn't done
// this way in Reelkeeper, and "monitor new items" is only read when a
// series is added; those fields are ignored.
func (h *handler) updateSeries(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body seriesBody
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		validationFailure(w, "id", "The request isn't valid JSON.")
		return
	}
	if id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64); err == nil && id > 0 {
		body.ID = id
	}
	ps, err := h.one(ctx, sc, body.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	current := h.resource(ps)
	m := library.SeriesMonitoring{Seasons: map[int]bool{}}
	if body.Monitored != nil && *body.Monitored != ps.Version.Monitored {
		m.Monitored = body.Monitored
	}
	for _, s := range body.Seasons {
		for _, c := range current.Seasons {
			if c.SeasonNumber == s.SeasonNumber && c.Monitored != s.Monitored {
				m.Seasons[s.SeasonNumber] = s.Monitored
			}
		}
	}
	if m.Monitored != nil || len(m.Seasons) > 0 {
		if err := h.Library.ApplySeriesMonitoring(ctx, ps.Version.ID, m); err != nil {
			h.fail(w, err)
			return
		}
	}
	if body.Tags != nil {
		if err := h.Library.SetVersionTags(ctx, ps.Version.ID, body.Tags); err != nil {
			h.fail(w, err)
			return
		}
	}
	if ps, err = h.one(ctx, sc, body.ID); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.resource(ps))
}

// deleteSeries removes this profile's version. Seerr asks to delete files
// too; Reelkeeper keeps them, and the folder goes on the skip list so
// scans don't add it back.
func (h *handler) deleteSeries(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	ps, err := h.one(ctx, sc, id)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Downloads.RemoveForVersion(ctx, ps.Version.ID); err != nil && !errors.Is(err, download.ErrNoClient) {
		h.fail(w, err)
		return
	}
	if _, err := h.Library.DeleteSeriesVersion(ctx, ps.Title.ID, ps.Version.ID); err != nil {
		h.fail(w, err)
		return
	}
	h.Logger.Info("series version removed through the Sonarr-compatible API (files kept)",
		"title", ps.Title.Title, "profile", sc.profile.Name, "deleteFilesAsked", r.URL.Query().Get("deleteFiles"))
	writeJSON(w, http.StatusOK, map[string]any{})
}

// episodes lists a series' episodes (?seriesId=, optionally
// &seasonNumber=), or the given ones (?episodeIds=1,2).
func (h *handler) episodes(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	q := r.URL.Query()
	var wantIDs map[int64]bool
	if v := q.Get("episodeIds"); v != "" {
		wantIDs = map[int64]bool{}
		for _, s := range strings.Split(v, ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				wantIDs[n] = true
			}
		}
	}
	var list []library.ProfileSeries
	if v := q.Get("seriesId"); v != "" {
		id, _ := strconv.ParseInt(v, 10, 64)
		ps, err := h.one(ctx, sc, id)
		if err != nil {
			h.fail(w, err)
			return
		}
		list = []library.ProfileSeries{*ps}
	} else if wantIDs != nil {
		var err error
		if list, err = h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{}); err != nil {
			h.fail(w, err)
			return
		}
	} else {
		validationFailure(w, "seriesId", "seriesId or episodeIds is required.")
		return
	}
	season, seasonErr := strconv.Atoi(q.Get("seasonNumber"))
	out := []episode{}
	for i := range list {
		ps := &list[i]
		files := episodeFiles(ps)
		for _, e := range ps.Episodes {
			if (wantIDs != nil && !wantIDs[e.ID]) || (seasonErr == nil && *e.Season != season) {
				continue
			}
			out = append(out, episodeResource(ps, e, files))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// monitorEpisodes turns episodes on or off ({"episodeIds": [...],
// "monitored": true}), for whichever of this profile's series they're in.
func (h *handler) monitorEpisodes(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body struct {
		EpisodeIDs []int64 `json:"episodeIds"`
		Monitored  bool    `json:"monitored"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		validationFailure(w, "episodeIds", "The request isn't valid JSON.")
		return
	}
	list, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{})
	if err != nil {
		h.fail(w, err)
		return
	}
	want := map[int64]bool{}
	for _, id := range body.EpisodeIDs {
		want[id] = true
	}
	for i := range list {
		var ids []int64
		for _, e := range list[i].Episodes {
			if want[e.ID] {
				ids = append(ids, e.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if err := h.Library.SetEpisodesMonitored(ctx, list[i].Version.ID, ids, body.Monitored); err != nil {
			h.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, []any{})
}

func (h *handler) search(ctx context.Context, versionID int64) {
	if h.Search == nil {
		return
	}
	if err := h.Search(ctx, versionID); err != nil {
		h.Logger.Warn("couldn't queue a search from the Sonarr-compatible API", "error", err)
	}
}

// command runs Sonarr's search commands (each searches the series'
// wanted episodes; Reelkeeper decides between season packs and episodes
// itself) and the download check. Other commands (refreshes, rescans) have
// nothing to do in Reelkeeper, which keeps itself up to date; they're
// accepted so callers carry on.
func (h *handler) command(w http.ResponseWriter, r *http.Request) {
	sc := scopeOf(r)
	ctx := r.Context()
	var body struct {
		Name         string  `json:"name"`
		SeriesID     int64   `json:"seriesId"`
		SeriesIDs    []int64 `json:"seriesIds"`
		SeasonNumber *int    `json:"seasonNumber"`
		EpisodeIDs   []int64 `json:"episodeIds"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name == "" {
		validationFailure(w, "name", "A command name is required.")
		return
	}
	switch body.Name {
	case "MissingEpisodeSearch", "SeriesSearch", "SeasonSearch":
		ids := body.SeriesIDs
		if body.SeriesID > 0 {
			ids = append(ids, body.SeriesID)
		}
		for _, id := range ids {
			if ps, err := h.one(ctx, sc, id); err == nil {
				h.search(ctx, ps.Version.ID)
			}
		}
	case "EpisodeSearch":
		list, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{})
		if err != nil {
			h.fail(w, err)
			return
		}
		for i := range list {
			if slices.ContainsFunc(list[i].Episodes, func(e library.Episode) bool { return slices.Contains(body.EpisodeIDs, e.ID) }) {
				h.search(ctx, list[i].Version.ID)
			}
		}
	case "RefreshMonitoredDownloads":
		if h.CheckDownloads != nil {
			if err := h.CheckDownloads(ctx); err != nil {
				h.Logger.Warn("couldn't queue a download check", "error", err)
			}
		}
	}
	now := sonarrTime(h.now())
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": 1, "name": body.Name, "commandName": body.Name, "status": "queued", "queued": now, "started": now,
		"trigger": "manual", "priority": "normal",
		"body": map[string]any{"seriesId": body.SeriesID, "seasonNumber": body.SeasonNumber, "episodeIds": body.EpisodeIDs},
	})
}

// queue lists downloads for this profile's series the way Sonarr does:
// one record per episode (a season pack is a record for each of its
// episodes), 10 per page unless asked.
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
	list, err := h.Library.ProfileSeries(ctx, sc.profile.ID, library.ProfileSeriesFilter{})
	if err != nil {
		h.fail(w, err)
		return
	}
	byVersion := map[int64]*library.ProfileSeries{}
	for i := range list {
		byVersion[list[i].Version.ID] = &list[i]
	}
	includeEpisode, includeSeries := q.Get("includeEpisode") == "true", q.Get("includeSeries") == "true"
	var records []map[string]any
	for _, it := range items {
		if it.Download.VersionID == nil || byVersion[*it.Download.VersionID] == nil {
			continue
		}
		ps := byVersion[*it.Download.VersionID]
		files := episodeFiles(ps)
		for _, e := range ps.Episodes {
			if !slices.Contains(it.EpisodeIDs, e.ID) {
				continue
			}
			rec := h.queueRecord(it, ps, e)
			if includeEpisode {
				rec["episode"] = episodeResource(ps, e, files)
			}
			if includeSeries {
				rec["series"] = h.resource(ps)
			}
			records = append(records, rec)
		}
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

func (h *handler) queueRecord(it download.Item, ps *library.ProfileSeries, e library.Episode) map[string]any {
	d := it.Download
	q, _ := qualityOf(d.Quality)
	rec := map[string]any{
		// Unique per download and episode, as Sonarr's ids are per record.
		"id": d.ID*1_000_000 + e.ID, "seriesId": ps.Version.ID, "episodeId": e.ID, "seasonNumber": *e.Season,
		"title": d.ReleaseTitle, "downloadId": strings.ToUpper(d.Hash),
		"protocol": "torrent", "downloadClient": h.Downloads.ClientName(), "downloadClientHasPostImportCategory": false,
		"indexer": d.Indexer, "added": sonarrTime(d.CreatedAt), "size": d.Size, "sizeleft": d.Size,
		"languages":     []language{{decision.LanguageEnglish, "English"}},
		"quality":       qualityModel{Quality: q, Revision: revision{Version: 1}},
		"customFormats": []any{}, "customFormatScore": d.Score, "statusMessages": []any{},
		"status": "queued", "trackedDownloadStatus": "ok", "trackedDownloadState": "downloading", "outputPath": "",
		"episodeHasFile": ps.HasFile[e.ID],
	}
	if t := it.Torrent; t != nil {
		rec["size"], rec["sizeleft"], rec["outputPath"] = t.Size, t.Left, t.ContentPath
		rec["status"] = queueStatus(t.State, t.Done())
		if t.ETA > 0 {
			rec["timeleft"] = fmt.Sprintf("%02d:%02d:%02d", int(t.ETA.Hours()), int(t.ETA.Minutes())%60, int(t.ETA.Seconds())%60)
			rec["estimatedCompletionTime"] = sonarrTime(h.now().Add(t.ETA))
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

// queueStatus is Sonarr's download status for a torrent state.
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

// calendar lists this profile's episodes airing between start and end
// (by default only monitored ones of monitored series), by air date.
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
	start, end := day(q.Get("start"), today), day(q.Get("end"), h.now().UTC().AddDate(0, 0, 2).Format("2006-01-02"))
	unmonitored, includeSeries := q.Get("unmonitored") == "true", q.Get("includeSeries") == "true"
	list, err := h.Library.ProfileSeries(r.Context(), sc.profile.ID, library.ProfileSeriesFilter{})
	if err != nil {
		h.fail(w, err)
		return
	}
	out := []episode{}
	for i := range list {
		ps := &list[i]
		files := episodeFiles(ps)
		var res *series
		for _, e := range ps.Episodes {
			if e.AirDate == "" || e.AirDate < start || e.AirDate > end {
				continue
			}
			if !unmonitored && (!ps.Version.Monitored || !ps.Monitored[e.ID]) {
				continue
			}
			ep := episodeResource(ps, e, files)
			if includeSeries {
				if res == nil {
					s := h.resource(ps)
					res = &s
				}
				ep.Series = res
			}
			out = append(out, ep)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AirDate < out[j].AirDate })
	writeJSON(w, http.StatusOK, out)
}
