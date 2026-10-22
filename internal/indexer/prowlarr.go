package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/cardigann"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Importing from Prowlarr: Reelkeeper reads Prowlarr's indexers through its API
// (read only) and turns each into an Reelkeeper indexer. A preview comes first;
// nothing is saved until the user picks what to import.

// ProwlarrIndexer is one Prowlarr indexer as Reelkeeper would import it.
type ProwlarrIndexer struct {
	ProwlarrID int
	Input      Input
	// Supported is false for kinds Reelkeeper can't run; Reason says why.
	Supported bool
	Reason    string
	// Exists: an Reelkeeper indexer already has this name (it won't be
	// imported twice).
	Exists bool
	// MissingSecrets lists secrets Prowlarr didn't reveal; they need
	// entering after the import.
	MissingSecrets []string
	// Notes are small changes the import makes, in plain words.
	Notes []string
}

// ProwlarrPreview is what an import would do.
type ProwlarrPreview struct {
	Indexers []ProwlarrIndexer
	// SolverURL is the address of Prowlarr's FlareSolverr proxy, if it has
	// one; Reelkeeper's solver setting can start from it.
	SolverURL     string
	SolverTimeout int // seconds
}

type prowlarrField struct {
	Name    string          `json:"name"`
	Value   json.RawMessage `json:"value"`
	Privacy string          `json:"privacy"`
}

type prowlarrIndexer struct {
	ID             int             `json:"id"`
	Name           string          `json:"name"`
	Implementation string          `json:"implementation"`
	DefinitionName string          `json:"definitionName"`
	Enable         bool            `json:"enable"`
	Priority       int             `json:"priority"`
	Protocol       string          `json:"protocol"`
	Tags           []int           `json:"tags"`
	Fields         []prowlarrField `json:"fields"`
}

type prowlarrProxy struct {
	Implementation string          `json:"implementation"`
	Tags           []int           `json:"tags"`
	Fields         []prowlarrField `json:"fields"`
}

func (f prowlarrField) str() string {
	var s string
	if json.Unmarshal(f.Value, &s) == nil {
		return s
	}
	return strings.Trim(string(f.Value), `"`)
}

func (f prowlarrField) isNull() bool {
	v := strings.TrimSpace(string(f.Value))
	return v == "" || v == "null"
}

func (f prowlarrField) num() (float64, bool) {
	var n float64
	if err := json.Unmarshal(f.Value, &n); err != nil {
		return 0, false
	}
	return n, true
}

// prowlarrGet calls Prowlarr's API.
func (s *Service) prowlarrGet(ctx context.Context, base, apiKey, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req) //nolint:gosec // the Prowlarr address the user entered
	if err != nil {
		return fmt.Errorf("can't reach Prowlarr at %s: %w", base, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return validation.Errors{"apiKey": "Prowlarr refused the API key. Copy it from Prowlarr's Settings → General."}
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("prowlarr answered HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("prowlarr's answer couldn't be read (is that Prowlarr's address?): %w", err)
	}
	return nil
}

// PreviewProwlarr reads Prowlarr's indexers and shows what importing them
// would create. Nothing is saved.
func (s *Service) PreviewProwlarr(ctx context.Context, base, apiKey string) (*ProwlarrPreview, error) {
	base, apiKey = strings.TrimSpace(base), strings.TrimSpace(apiKey)
	var errs validation.Errors
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		errs.Add("url", "Enter Prowlarr's address, like http://192.168.1.21:9696.")
	}
	if apiKey == "" {
		errs.Add("apiKey", "Enter Prowlarr's API key (Prowlarr → Settings → General).")
	}
	if errs != nil {
		return nil, errs
	}
	var list []prowlarrIndexer
	if err := s.prowlarrGet(ctx, base, apiKey, "indexer", &list); err != nil {
		return nil, err
	}
	var proxies []prowlarrProxy
	_ = s.prowlarrGet(ctx, base, apiKey, "indexerProxy", &proxies) // older Prowlarrs may lack it

	out := &ProwlarrPreview{}
	solverTags := map[int]bool{}
	for _, p := range proxies {
		if !strings.EqualFold(p.Implementation, "FlareSolverr") {
			continue
		}
		for _, t := range p.Tags {
			solverTags[t] = true
		}
		for _, f := range p.Fields {
			switch f.Name {
			case "host":
				out.SolverURL = strings.TrimRight(f.str(), "/")
			case "requestTimeout":
				if n, ok := f.num(); ok {
					out.SolverTimeout = int(n)
				}
			}
		}
	}

	existing, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		pi := s.mapProwlarr(p, solverTags)
		pi.Exists = slices.ContainsFunc(existing, func(ix Indexer) bool { return strings.EqualFold(ix.Name, pi.Input.Name) })
		out.Indexers = append(out.Indexers, pi)
	}
	return out, nil
}

// mapProwlarr turns one Prowlarr indexer into an Reelkeeper one.
func (s *Service) mapProwlarr(p prowlarrIndexer, solverTags map[int]bool) ProwlarrIndexer {
	pi := ProwlarrIndexer{ProwlarrID: p.ID, Supported: true}
	in := Input{Name: p.Name, Enabled: p.Enable, Priority: p.Priority, Settings: map[string]string{}}
	if in.Priority < 1 || in.Priority > 50 {
		in.Priority = 25
	}
	for _, t := range p.Tags {
		if solverTags[t] {
			in.UseSolver = true
		}
	}
	fields := map[string]prowlarrField{}
	for _, f := range p.Fields {
		fields[f.Name] = f
	}
	in.BaseURL = fields["baseUrl"].str()

	// Seeding rules and limits.
	if f, ok := fields["torrentBaseSettings.seedRatio"]; ok && !f.isNull() {
		if n, ok := f.num(); ok {
			in.SeedRatio = &n
		}
	}
	for name, dst := range map[string]**int{
		"torrentBaseSettings.seedTime": &in.SeedTimeMinutes, "torrentBaseSettings.packSeedTime": &in.PackSeedTimeMinutes,
		"baseSettings.queryLimit": &in.QueryLimit, "baseSettings.grabLimit": &in.GrabLimit,
	} {
		if f, ok := fields[name]; ok && !f.isNull() {
			if n, ok := f.num(); ok {
				v := int(n)
				*dst = &v
			}
		}
	}
	if u, ok := fields["baseSettings.limitsUnit"].num(); ok && u == 1 {
		// Prowlarr's limit was per hour; Reelkeeper's are per day.
		for _, lim := range []*int{in.QueryLimit, in.GrabLimit} {
			if lim != nil {
				*lim *= 24
			}
		}
		if in.QueryLimit != nil || in.GrabLimit != nil {
			pi.Notes = append(pi.Notes, "Prowlarr's hourly limits become daily ones (×24).")
		}
	}

	masked := func(f prowlarrField) bool {
		v := f.str()
		return v == "" || strings.Trim(v, "*") == "" || v == "(removed)"
	}

	switch strings.ToLower(p.Implementation) {
	case "cardigann":
		in.Kind = KindCardigann
		in.Definition = firstNonEmpty(fields["definitionFile"].str(), p.DefinitionName)
		def, err := s.catalog.Get(in.Definition)
		if err != nil {
			pi.Supported = false
			pi.Reason = fmt.Sprintf("Reelkeeper doesn't have the %q tracker definition (it may have been renamed or removed).", in.Definition)
			break
		}
		for _, st := range def.Settings {
			f, ok := fields[st.Name]
			if !ok || strings.HasPrefix(st.Type, "info") {
				continue
			}
			if IsSecret(st) {
				if masked(f) {
					pi.MissingSecrets = append(pi.MissingSecrets, st.Label)
				} else {
					in.Settings[st.Name] = f.str()
				}
				continue
			}
			in.Settings[st.Name] = prowlarrSetting(st, f)
		}
		if links := def.Links; in.BaseURL != "" && len(links) > 0 && strings.TrimRight(in.BaseURL, "/") == strings.TrimRight(links[0], "/") {
			in.BaseURL = "" // the definition's own address: follow it if it changes
		}
	case "knaben":
		in.Kind, in.BaseURL = KindKnaben, ""
	case "torznab", "newznab":
		in.Kind = KindTorznab
		if strings.EqualFold(p.Implementation, "newznab") {
			in.Kind = KindNewznab
		}
		in.BaseURL = strings.TrimRight(in.BaseURL, "/") + "/" + strings.TrimLeft(firstNonEmpty(fields["apiPath"].str(), "/api"), "/")
		if f := fields["apiKey"]; masked(f) {
			pi.MissingSecrets = append(pi.MissingSecrets, "API key")
		} else {
			in.Settings["apiKey"] = f.str()
		}
	default:
		pi.Supported = false
		pi.Reason = fmt.Sprintf("Prowlarr's built-in %s indexer has no Reelkeeper equivalent yet.", p.Implementation)
	}
	if strings.EqualFold(p.Protocol, "usenet") && pi.Supported {
		pi.Notes = append(pi.Notes, "A Usenet indexer: it's searched once a Usenet client is set up (Settings → Download Client).")
	}
	pi.Input = in
	return pi
}

// prowlarrSetting converts a Prowlarr field value to Reelkeeper's form. Prowlarr
// stores a select as the position of the chosen option in the options
// sorted by key, and a checkbox as true/false.
func prowlarrSetting(st cardigann.Setting, f prowlarrField) string {
	switch st.Type {
	case "checkbox":
		var b bool
		_ = json.Unmarshal(f.Value, &b)
		return strconv.FormatBool(b)
	case "select":
		keys := make([]string, 0, len(st.Options))
		for _, kv := range st.Options {
			keys = append(keys, kv.Key)
		}
		slices.Sort(keys)
		if n, ok := f.num(); ok && int(n) >= 0 && int(n) < len(keys) {
			return keys[int(n)]
		}
		if v := f.str(); slices.Contains(keys, v) {
			return v
		}
		return fmt.Sprint(st.SettingDefault())
	}
	return f.str()
}

// ImportProwlarr creates the chosen indexers (by Prowlarr ID). Ones that
// already exist by name, or that Reelkeeper can't run, are skipped. It returns
// what was created and why others weren't.
func (s *Service) ImportProwlarr(ctx context.Context, base, apiKey string, ids []int) ([]Indexer, map[int]string, error) {
	prev, err := s.PreviewProwlarr(ctx, base, apiKey)
	if err != nil {
		return nil, nil, err
	}
	var created []Indexer
	skipped := map[int]string{}
	for _, pi := range prev.Indexers {
		if !slices.Contains(ids, pi.ProwlarrID) {
			continue
		}
		switch {
		case !pi.Supported:
			skipped[pi.ProwlarrID] = pi.Reason
			continue
		case pi.Exists:
			skipped[pi.ProwlarrID] = "An indexer with this name is already in Reelkeeper."
			continue
		}
		ix, err := s.Create(ctx, pi.Input)
		if err != nil {
			var verrs validation.Errors
			if errors.As(err, &verrs) {
				skipped[pi.ProwlarrID] = verrs.Error()
				continue
			}
			return created, skipped, err
		}
		created = append(created, *ix)
	}
	if len(created) > 0 {
		s.logger.Info("indexers imported from Prowlarr", "count", len(created))
	}
	return created, skipped, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
