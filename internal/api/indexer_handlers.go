package api

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/cardigann"
	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/newznab"
	"github.com/saltglass/reelkeeper/internal/indexer/solver"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// --- indexers ----------------------------------------------------------------

// GetIndexers lists every indexer with its health and recent activity.
func (h *Handlers) GetIndexers(w http.ResponseWriter, r *http.Request) {
	list, err := h.Indexers.List(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	stats, err := h.Indexers.StatsOf(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.Indexer, 0, len(list))
	for i := range list {
		out = append(out, h.toIndexer(&list[i], stats[list[i].ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetIndexer returns one indexer.
func (h *Handlers) GetIndexer(w http.ResponseWriter, r *http.Request, id int64) {
	ix, err := h.Indexers.Get(r.Context(), id)
	if err != nil {
		h.writeIndexerError(w, r, err)
		return
	}
	h.writeIndexer(w, r, ix)
}

// AddIndexer adds an indexer.
func (h *Handlers) AddIndexer(w http.ResponseWriter, r *http.Request) {
	var body gen.AddIndexerJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	ix, err := h.Indexers.Create(r.Context(), toIndexerInput(body))
	if err != nil {
		h.writeIndexerError(w, r, err)
		return
	}
	h.writeIndexer(w, r, ix)
}

// UpdateIndexer saves changes to an indexer.
func (h *Handlers) UpdateIndexer(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.UpdateIndexerJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	ix, err := h.Indexers.Update(r.Context(), id, toIndexerInput(body))
	if err != nil {
		h.writeIndexerError(w, r, err)
		return
	}
	h.writeIndexer(w, r, ix)
}

// DeleteIndexer removes an indexer.
func (h *Handlers) DeleteIndexer(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Indexers.Delete(r.Context(), id); err != nil {
		h.writeIndexerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestIndexer checks a saved indexer works; the outcome updates its health.
func (h *Handlers) TestIndexer(w http.ResponseWriter, r *http.Request, id int64) {
	h.writeTestResult(w, r, h.Indexers.Test(r.Context(), id))
}

// TestIndexerInput checks unsaved settings work, without saving them.
func (h *Handlers) TestIndexerInput(w http.ResponseWriter, r *http.Request) {
	var body gen.TestIndexerInputJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	var id int64
	if body.Id != nil {
		id = *body.Id
	}
	h.writeTestResult(w, r, h.Indexers.TestInput(r.Context(), id, toIndexerInput(gen.IndexerInput{
		BaseUrl: body.BaseUrl, Categories: body.Categories, Definition: body.Definition, Enabled: body.Enabled,
		GrabLimit: body.GrabLimit, Kind: body.Kind, Name: body.Name, PackSeedTimeMinutes: body.PackSeedTimeMinutes,
		Priority: body.Priority, QueryLimit: body.QueryLimit, SeedRatio: body.SeedRatio,
		SeedTimeMinutes: body.SeedTimeMinutes, Settings: body.Settings, UseSolver: body.UseSolver,
	})))
}

// writeTestResult answers a test: a failing indexer is still a 200 with
// ok false, because the question "does it work?" was answered. Only a
// missing indexer or invalid input is an error.
func (h *Handlers) writeTestResult(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, indexer.ErrNotFound) {
		h.writeIndexerError(w, r, err)
		return
	}
	if _, ok := validation.As(err); ok {
		h.writeServiceError(w, r, err)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, gen.CheckResult{Ok: false, Message: indexer.Sentence(err)})
		return
	}
	writeJSON(w, http.StatusOK, gen.CheckResult{Ok: true, Message: "Works: found releases."})
}

// SearchIndexer searches one indexer, for diagnostics.
func (h *Handlers) SearchIndexer(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.SearchIndexerJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if _, err := h.Indexers.Get(r.Context(), id); err != nil {
		h.writeIndexerError(w, r, err)
		return
	}
	q := core.Query{Type: "search"}
	if body.Type != nil {
		q.Type = string(*body.Type)
	}
	if body.Query != nil {
		q.Keywords = *body.Query
	}
	if body.Year != nil {
		q.Year = *body.Year
	}
	if body.Season != nil {
		q.Season = *body.Season
	}
	if body.Episode != nil {
		q.Episode = *body.Episode
	}
	if body.ImdbId != nil {
		q.IMDbID = *body.ImdbId
	}
	if body.TmdbId != nil {
		q.TMDBID = *body.TmdbId
	}
	if body.TvdbId != nil {
		q.TVDBID = *body.TvdbId
	}
	if body.Categories != nil {
		q.Categories = *body.Categories
	}
	found, failures, err := h.Indexers.Search(r.Context(), q, indexer.SearchOptions{IndexerIDs: []int64{id}})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if len(failures) > 0 {
		writeError(w, http.StatusBadGateway, "indexer", failures[0].Message, nil)
		return
	}
	out := make([]gen.IndexerRelease, 0, len(found))
	for _, rel := range found {
		out = append(out, toIndexerRelease(rel))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) writeIndexer(w http.ResponseWriter, r *http.Request, ix *indexer.Indexer) {
	stats, err := h.Indexers.StatsOf(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toIndexer(ix, stats[ix.ID]))
}

func (h *Handlers) writeIndexerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, indexer.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "That indexer doesn't exist (any more).", nil)
		return
	}
	h.writeServiceError(w, r, err)
}

func toIndexerInput(b gen.IndexerInput) indexer.Input {
	in := indexer.Input{
		Name: b.Name, Kind: string(b.Kind), Enabled: b.Enabled, Settings: b.Settings,
		SeedTimeMinutes: b.SeedTimeMinutes, PackSeedTimeMinutes: b.PackSeedTimeMinutes,
		QueryLimit: b.QueryLimit, GrabLimit: b.GrabLimit,
	}
	if b.Definition != nil {
		in.Definition = *b.Definition
	}
	if b.BaseUrl != nil {
		in.BaseURL = *b.BaseUrl
	}
	if b.Priority != nil {
		in.Priority = *b.Priority // 0 means the default
	}
	if b.UseSolver != nil {
		in.UseSolver = *b.UseSolver
	}
	if b.Categories != nil {
		in.Categories = *b.Categories
	}
	if b.SeedRatio != nil {
		v := float64(*b.SeedRatio)
		in.SeedRatio = &v
	}
	return in
}

func (h *Handlers) toIndexer(ix *indexer.Indexer, st indexer.Stats) gen.Indexer {
	health := h.Indexers.HealthOf(ix)
	settingsMap := ix.SettingsMap()
	if settingsMap == nil {
		settingsMap = map[string]string{}
	}
	secretsSet := h.Indexers.SecretNames(ix)
	if secretsSet == nil {
		secretsSet = []string{}
	}
	out := gen.Indexer{
		Id: ix.ID, Name: ix.Name, Kind: gen.IndexerKind(ix.Kind), Definition: ix.Definition,
		BaseUrl: ix.BaseURL, Enabled: ix.Enabled, Priority: ix.Priority, UseSolver: ix.UseSolver,
		Settings: settingsMap, SecretsSet: secretsSet, Categories: ix.CategoryList(),
		SeedTimeMinutes: ix.SeedTimeMinutes, PackSeedTimeMinutes: ix.PackSeedTimeMinutes,
		QueryLimit: ix.QueryLimit, GrabLimit: ix.GrabLimit,
		Health: gen.IndexerHealth{Status: gen.IndexerHealthStatus(health.Status), Message: health.Message, RetryAfter: health.RetryAfter},
		Stats: gen.IndexerStats{
			QueriesToday: st.QueriesToday, GrabsToday: st.GrabsToday, Queries30d: st.Queries30d,
			Failures30d: st.Failures30d, AvgMs: st.AvgMS,
		},
		LastSuccessAt: ix.LastSuccessAt, LastFailureAt: ix.LastFailureAt, CreatedAt: ix.CreatedAt,
	}
	if ix.SeedRatio != nil {
		v := float32(*ix.SeedRatio)
		out.SeedRatio = &v
	}
	// A definition that's gone from the catalog mustn't break the list.
	if ix.Kind == indexer.KindCardigann {
		if def, err := h.Indexers.Catalog().Get(ix.Definition); err == nil {
			out.DefinitionName = def.Name
		}
	}
	return out
}

func toIndexerRelease(r indexer.Release) gen.IndexerRelease {
	out := gen.IndexerRelease{
		Title: r.Title, Guid: r.GUID, Size: r.Size, Seeders: r.Seeders, Leechers: r.Leechers,
		Categories:     r.Categories,
		DownloadFactor: float32(r.DownloadFactor), UploadFactor: float32(r.UploadFactor),
		IndexerId: r.IndexerID, Indexer: r.Indexer,
	}
	if out.Categories == nil {
		out.Categories = []int{}
	}
	str := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	num := func(n int) *int {
		if n == 0 {
			return nil
		}
		return &n
	}
	out.DetailsUrl, out.DownloadUrl, out.MagnetUrl, out.InfoHash = str(r.DetailsURL), str(r.DownloadURL), str(r.MagnetURL), str(r.InfoHash)
	out.Grabs, out.ImdbId, out.TmdbId, out.TvdbId = num(r.Grabs), num(r.IMDbID), num(r.TMDBID), num(r.TVDBID)
	if !r.PublishDate.IsZero() {
		t := r.PublishDate
		out.PublishDate = &t
	}
	if r.MinimumRatio != 0 {
		v := float32(r.MinimumRatio)
		out.MinimumRatio = &v
	}
	if r.MinimumSeedTime != 0 {
		v := r.MinimumSeedTime
		out.MinimumSeedTime = &v
	}
	return out
}

// --- definitions -------------------------------------------------------------

// GetIndexerDefinitions lists the trackers Reelkeeper has definitions for.
func (h *Handlers) GetIndexerDefinitions(w http.ResponseWriter, _ *http.Request) {
	cat := h.Indexers.Catalog()
	list := cat.List()
	out := gen.DefinitionList{Count: cat.Len(), Definitions: make([]gen.DefinitionSummary, 0, len(list))}
	if t := cat.UpdatedAt(); !t.IsZero() {
		out.UpdatedAt = &t
	}
	for _, d := range list {
		links := d.Links
		if links == nil {
			links = []string{}
		}
		out.Definitions = append(out.Definitions, gen.DefinitionSummary{
			Id: d.ID, Name: d.Name, Description: d.Description, Type: d.Type, Language: d.Language,
			Links: links, Custom: d.Custom,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetIndexerDefinition returns what the add/edit form needs for a tracker.
func (h *Handlers) GetIndexerDefinition(w http.ResponseWriter, r *http.Request, id string) {
	def, err := h.Indexers.Catalog().Get(id)
	if errors.Is(err, catalog.ErrUnknown) {
		writeError(w, http.StatusNotFound, "not_found", "There's no tracker definition with that id.", nil)
		return
	}
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := gen.DefinitionDetail{
		Id: def.ID, Name: def.Name, Description: def.Description, Type: def.Type, Language: def.Language,
		Links:      append([]string{}, def.Links...),
		Settings:   make([]gen.DefinitionSetting, 0, len(def.Settings)),
		Categories: definitionCategories(def),
	}
	if def.Login != nil {
		// A login block without a method is a form login.
		out.Login = def.Login.Method
		if out.Login == "" {
			out.Login = "form"
		}
	}
	for _, st := range def.Settings {
		out.Settings = append(out.Settings, toDefinitionSetting(st))
	}
	writeJSON(w, http.StatusOK, out)
}

func toDefinitionSetting(st cardigann.Setting) gen.DefinitionSetting {
	ds := gen.DefinitionSetting{Name: st.Name, Label: st.Label, Type: st.Type, Secret: indexer.IsSecret(st)}
	// Definitions mark standard help texts with their own types
	// (info_flaresolverr, info_cookie, ...). The UI knows them by name, so
	// every kind of help text goes out as plain "info".
	if strings.HasPrefix(st.Type, "info") {
		ds.Type = "info"
	}
	ds.Options = make([]struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}, 0, len(st.Options))
	for _, o := range st.Options {
		ds.Options = append(ds.Options, struct {
			Label string `json:"label"`
			Value string `json:"value"`
		}{Label: o.Value, Value: o.Key})
	}
	switch d := st.SettingDefault().(type) {
	case nil:
		// An unticked checkbox has no default; other types have "".
		if st.Type == "checkbox" {
			ds.Default = "false"
		}
	case string:
		ds.Default = d
		if st.Type == "checkbox" {
			ds.Default = strconv.FormatBool(d == cardigann.CheckboxOn)
		}
	}
	return ds
}

// definitionCategories lists the standard categories a tracker's
// categories map to, once each, by id.
func definitionCategories(def *cardigann.Definition) []gen.Category {
	seen := map[int]bool{}
	add := func(name string) {
		if id := newznab.ID(name); id != 0 {
			seen[id] = true
		}
	}
	for _, m := range def.Caps.CategoryMappings {
		add(m.Cat)
	}
	for _, name := range def.Caps.Categories {
		add(name)
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]gen.Category, 0, len(ids))
	for _, id := range ids {
		out = append(out, gen.Category{Id: id, Name: newznab.Name(id)})
	}
	return out
}

// --- Prowlarr import ---------------------------------------------------------

// PreviewProwlarrImport reads Prowlarr's indexers and shows what importing
// them would do.
func (h *Handlers) PreviewProwlarrImport(w http.ResponseWriter, r *http.Request) {
	var body gen.PreviewProwlarrImportJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	prev, err := h.Indexers.PreviewProwlarr(r.Context(), body.Url, body.ApiKey)
	if err != nil {
		h.writeProwlarrError(w, r, err)
		return
	}
	out := gen.ProwlarrPreview{Indexers: make([]gen.ProwlarrCandidate, 0, len(prev.Indexers))}
	if prev.SolverURL != "" {
		u, t := prev.SolverURL, prev.SolverTimeout
		out.SolverUrl, out.SolverTimeoutSeconds = &u, &t
	}
	for _, pi := range prev.Indexers {
		out.Indexers = append(out.Indexers, toProwlarrCandidate(pi))
	}
	writeJSON(w, http.StatusOK, out)
}

// ImportFromProwlarr creates the chosen indexers, and optionally copies
// Prowlarr's solver address.
func (h *Handlers) ImportFromProwlarr(w http.ResponseWriter, r *http.Request) {
	var body gen.ImportFromProwlarrJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	created, skipped, err := h.Indexers.ImportProwlarr(r.Context(), body.Url, body.ApiKey, body.Ids)
	if err != nil {
		h.writeProwlarrError(w, r, err)
		return
	}
	if body.UseSolverSettings {
		// The import has read Prowlarr already; ask again for the solver
		// rather than widening the service's answer.
		if prev, err := h.Indexers.PreviewProwlarr(r.Context(), body.Url, body.ApiKey); err == nil && prev.SolverURL != "" {
			timeout := min(max(prev.SolverTimeout, 10), 300)
			if prev.SolverTimeout == 0 {
				timeout = 60
			}
			if _, err := h.Settings.SaveSolver(r.Context(), settings.Solver{URL: prev.SolverURL, TimeoutSeconds: timeout}); err != nil {
				h.writeServiceError(w, r, err)
				return
			}
		}
	}
	out := gen.ProwlarrImportResult{Created: make([]gen.Indexer, 0, len(created))}
	stats, err := h.Indexers.StatsOf(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	for i := range created {
		out.Created = append(out.Created, h.toIndexer(&created[i], stats[created[i].ID]))
	}
	ids := make([]int, 0, len(skipped))
	for id := range skipped {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out.Skipped = make([]struct {
		ProwlarrId int    `json:"prowlarrId"`
		Reason     string `json:"reason"`
	}, 0, len(ids))
	for _, id := range ids {
		out.Skipped = append(out.Skipped, struct {
			ProwlarrId int    `json:"prowlarrId"`
			Reason     string `json:"reason"`
		}{ProwlarrId: id, Reason: skipped[id]})
	}
	writeJSON(w, http.StatusOK, out)
}

// writeProwlarrError: bad input is 422; anything else means Prowlarr
// couldn't be reached or answered strangely, and its message says how.
func (h *Handlers) writeProwlarrError(w http.ResponseWriter, r *http.Request, err error) {
	if _, ok := validation.As(err); ok {
		h.writeServiceError(w, r, err)
		return
	}
	writeError(w, http.StatusBadGateway, "prowlarr", indexer.Sentence(err), nil)
}

func toProwlarrCandidate(pi indexer.ProwlarrIndexer) gen.ProwlarrCandidate {
	c := gen.ProwlarrCandidate{
		ProwlarrId: pi.ProwlarrID, Name: pi.Input.Name, Definition: pi.Input.Definition,
		Enabled: pi.Input.Enabled, UseSolver: pi.Input.UseSolver, Supported: pi.Supported,
		Reason: pi.Reason, Exists: pi.Exists,
		MissingSecrets: append([]string{}, pi.MissingSecrets...), Notes: append([]string{}, pi.Notes...),
	}
	if pi.Supported {
		c.Kind = pi.Input.Kind
	}
	return c
}

// --- solver ------------------------------------------------------------------

// GetSolverSettings returns the Cloudflare solver's settings.
func (h *Handlers) GetSolverSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toSolverSettings(h.Settings.Solver()))
}

// PutSolverSettings saves the Cloudflare solver's settings.
func (h *Handlers) PutSolverSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutSolverSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveSolver(r.Context(), settings.Solver{URL: body.Url, TimeoutSeconds: body.TimeoutSeconds})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSolverSettings(saved))
}

func toSolverSettings(s settings.Solver) gen.SolverSettings {
	return gen.SolverSettings{Url: s.URL, TimeoutSeconds: s.TimeoutSeconds}
}

// CheckSolver asks the solver (the given address, or the saved one)
// whether it's running.
func (h *Handlers) CheckSolver(w http.ResponseWriter, r *http.Request) {
	var body gen.CheckSolverJSONRequestBody
	// The body is optional: an empty one means "check the saved address".
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	saved := h.Settings.Solver()
	url := saved.URL
	if body.Url != nil && *body.Url != "" {
		url = *body.Url
	}
	if url == "" {
		writeJSON(w, http.StatusOK, gen.CheckResult{Ok: false, Message: "No solver address is set."})
		return
	}
	c := solver.Client{URL: url, Timeout: time.Duration(saved.TimeoutSeconds) * time.Second}
	if err := c.Check(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, gen.CheckResult{Ok: false, Message: indexer.Sentence(err)})
		return
	}
	writeJSON(w, http.StatusOK, gen.CheckResult{Ok: true, Message: "The solver is running."})
}
