package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/migrate"
)

// PreviewRadarrImport reads a Radarr and reports what bringing it over
// would do. Nothing changes.
func (h *Handlers) PreviewRadarrImport(w http.ResponseWriter, r *http.Request) {
	var body gen.PreviewRadarrImportJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	opts, ok := h.radarrOptions(w, r, body)
	if !ok {
		return
	}
	rep, err := h.Migrate.Preview(r.Context(), opts)
	if err != nil {
		h.writeRadarrError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRadarrReport(rep))
}

// ImportFromRadarr brings the Radarr's wanted movies, monitoring, tags,
// history and blocklist over. It can take a minute or two for a big
// library, and runs on the request's own context (the server sets no
// write timeout), so it stops only if the browser gives up.
func (h *Handlers) ImportFromRadarr(w http.ResponseWriter, r *http.Request) {
	var body gen.ImportFromRadarrJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	opts, ok := h.radarrOptions(w, r, body)
	if !ok {
		return
	}
	rep, err := h.Migrate.Import(r.Context(), opts)
	if err != nil {
		h.writeRadarrError(w, r, err)
		return
	}
	h.Logger.Info("imported from Radarr", "instance", rep.Instance, "added", rep.Added,
		"history", rep.HistoryAdded, "blocklist", rep.BlocklistAdded)
	writeJSON(w, http.StatusOK, toRadarrReport(rep))
}

// radarrOptions checks the request's fields and the profile, answering 422
// itself when something is wrong. The API key is never logged or echoed.
func (h *Handlers) radarrOptions(w http.ResponseWriter, r *http.Request, b gen.RadarrImportRequest) (migrate.Options, bool) {
	return h.importOptions(w, r, "Radarr", library.Movie, b.Url, b.ApiKey, b.ProfileId)
}

// sonarrOptions is the same for Sonarr, whose series need a TV profile.
func (h *Handlers) sonarrOptions(w http.ResponseWriter, r *http.Request, b gen.SonarrImportRequest) (migrate.Options, bool) {
	return h.importOptions(w, r, "Sonarr", library.Series, b.Url, b.ApiKey, b.ProfileId)
}

// importOptions checks an import request for app ("Radarr" or "Sonarr"),
// whose titles are of mediaType.
func (h *Handlers) importOptions(w http.ResponseWriter, r *http.Request, app, mediaType, url, apiKey string, profileID int64) (migrate.Options, bool) {
	fields := map[string]string{}
	if strings.TrimSpace(url) == "" {
		fields["url"] = "Enter the " + app + "'s address."
	}
	if strings.TrimSpace(apiKey) == "" {
		fields["apiKey"] = "Enter the " + app + "'s API key."
	}
	profiles, err := h.Library.Profiles(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return migrate.Options{}, false
	}
	found := false
	for _, p := range profiles {
		if p.ID == profileID {
			found = true
			if p.AppliesTo != "both" && p.AppliesTo != mediaType {
				if mediaType == library.Movie {
					fields["profileId"] = p.Name + " isn't for movies. Choose a movie profile."
				} else {
					fields["profileId"] = p.Name + " isn't for TV. Choose a TV profile."
				}
			}
		}
	}
	if !found {
		fields["profileId"] = "Choose a quality profile."
	}
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Some fields need attention.", fields)
		return migrate.Options{}, false
	}
	return migrate.Options{URL: strings.TrimSpace(url), APIKey: apiKey, ProfileID: profileID}, true
}

// writeRadarrError and writeSonarrError answer a failed read of Radarr or
// Sonarr with its plain-English reason. The migrate package's messages never
// contain the API key.
func (h *Handlers) writeRadarrError(w http.ResponseWriter, r *http.Request, err error) {
	h.writeArrError(w, r, err, "radarr")
}

func (h *Handlers) writeSonarrError(w http.ResponseWriter, r *http.Request, err error) {
	h.writeArrError(w, r, err, "sonarr")
}

func (h *Handlers) writeArrError(w http.ResponseWriter, r *http.Request, err error, app string) {
	if !errors.Is(err, migrate.ErrArr) {
		// Reelkeeper's own failure (the database, TMDB...), not something the
		// user can fix in the form.
		h.Logger.Error(app+" import failed", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "import_failed", capitalize(err.Error())+".", nil)
		return
	}
	h.Logger.Warn(app+" import failed", "path", r.URL.Path, "error", err)
	text := capitalize(strings.TrimSpace(err.Error()))
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	writeError(w, http.StatusUnprocessableEntity, app, text, nil)
}

func toRadarrReport(r *migrate.Report) gen.RadarrImportReport {
	out := gen.RadarrImportReport{
		Instance: r.Instance, Version: r.Version, Movies: r.Movies, WithFile: r.WithFile, Monitored: r.Monitored, Wanted: r.Wanted, ToAdd: r.ToAdd,
		History: r.History, Blocklist: r.Blocklist, Imported: r.Imported, Added: r.Added,
		MonitorChanged: r.MonitorChanged, HistoryAdded: r.HistoryAdded, BlocklistAdded: r.BlocklistAdded,
		Issues: make([]gen.RadarrImportIssue, 0, len(r.Issues)),
	}
	for _, is := range r.Issues {
		out.Issues = append(out.Issues, gen.RadarrImportIssue{
			TmdbId: is.TMDBID, Title: is.Title, Year: is.Year, Kind: gen.RadarrImportIssueKind(is.Kind), Detail: is.Detail,
		})
	}
	return out
}

// PreviewSonarrImport reads a Sonarr and reports what bringing it over
// would do. Nothing changes.
func (h *Handlers) PreviewSonarrImport(w http.ResponseWriter, r *http.Request) {
	var body gen.PreviewSonarrImportJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	opts, ok := h.sonarrOptions(w, r, body)
	if !ok {
		return
	}
	rep, err := h.Migrate.PreviewSonarr(r.Context(), opts)
	if err != nil {
		h.writeSonarrError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSonarrReport(rep))
}

// ImportFromSonarr brings the Sonarr's monitored episodes, tags, history and
// blocklist over. Like Radarr's it can take a minute or two and runs on the
// request's own context. It searches for nothing.
func (h *Handlers) ImportFromSonarr(w http.ResponseWriter, r *http.Request) {
	var body gen.ImportFromSonarrJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	opts, ok := h.sonarrOptions(w, r, body)
	if !ok {
		return
	}
	rep, err := h.Migrate.ImportSonarr(r.Context(), opts)
	if err != nil {
		h.writeSonarrError(w, r, err)
		return
	}
	h.Logger.Info("imported from Sonarr", "instance", rep.Instance, "added", rep.Added, "newlyWanted", rep.NewlyWanted,
		"history", rep.HistoryAdded, "blocklist", rep.BlocklistAdded)
	writeJSON(w, http.StatusOK, toSonarrReport(rep))
}

func toSonarrReport(r *migrate.SonarrReport) gen.SonarrImportReport {
	out := gen.SonarrImportReport{
		Instance: r.Instance, Version: r.Version, Series: r.Series, ToAdd: r.ToAdd, MonitoredSeries: r.MonitoredSeries,
		Episodes: r.Episodes, Matched: r.Matched, MatchedByDate: r.MatchedByDate, Unmatched: r.Unmatched,
		NewlyWanted: r.NewlyWanted, AlreadyWanted: r.AlreadyWanted, TurnedOff: r.TurnedOff,
		History: r.History, Blocklist: r.Blocklist, Imported: r.Imported, Added: r.Added, MonitorChanged: r.MonitorChanged,
		EpisodesTurnedOn: r.EpisodesTurnedOn, EpisodesTurnedOff: r.EpisodesTurnedOff,
		HistoryAdded: r.HistoryAdded, BlocklistAdded: r.BlocklistAdded,
		Wanted: make([]gen.SonarrWantedSeries, 0, len(r.Wanted)),
		Issues: make([]gen.SonarrImportIssue, 0, len(r.Issues)),
	}
	for _, w := range r.Wanted {
		ws := gen.SonarrWantedSeries{Title: w.Title, Year: w.Year, Seasons: make([]gen.SonarrWantedSeason, 0, len(w.Seasons))}
		for _, s := range w.Seasons {
			ws.Seasons = append(ws.Seasons, gen.SonarrWantedSeason{Season: s.Season, Episodes: s.Episodes})
		}
		out.Wanted = append(out.Wanted, ws)
	}
	for _, is := range r.Issues {
		out.Issues = append(out.Issues, gen.SonarrImportIssue{
			TmdbId: is.TMDBID, Title: is.Title, Year: is.Year, Kind: gen.SonarrImportIssueKind(is.Kind), Detail: is.Detail,
		})
	}
	return out
}
