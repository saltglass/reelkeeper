package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// The profile editor's data. The decision package's types carry the same
// JSON tags as the API schemas, so they are converted by a JSON round trip
// instead of field by field; the tests pin the shapes.

// convert copies src into dst through JSON.
func convert(src, dst any) error {
	b, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// resolutionLabels name the resolutions the simple editor offers.
var resolutionLabels = []gen.ProfileResolution{
	{Value: 480, Label: "SD (480p/576p, DVD)"},
	{Value: 720, Label: "720p (HD)"},
	{Value: 1080, Label: "1080p (Full HD)"},
	{Value: 2160, Label: "2160p (4K)"},
}

// GetProfileOptions lists everything the profile editor needs to draw itself.
func (h *Handlers) GetProfileOptions(w http.ResponseWriter, r *http.Request) {
	out := gen.ProfileOptions{
		Presets:     []gen.ProfilePreset{},
		Features:    []gen.ProfileFeature{},
		Tiers:       []gen.ProfileTier{},
		Resolutions: resolutionLabels,
		Languages:   []string{},
		Qualities:   []string{},
		Choices: []gen.ProfileChoice{
			gen.ProfileChoice(decision.Prefer), gen.ProfileChoice(decision.Avoid),
			gen.ProfileChoice(decision.Require), gen.ProfileChoice(decision.Block),
		},
	}
	if err := convert(decision.Presets, &out.Presets); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if err := convert(decision.Features, &out.Features); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	for _, id := range decision.Tiers {
		out.Tiers = append(out.Tiers, gen.ProfileTier{Id: id, Name: decision.TierNames[id]})
	}
	for _, l := range decision.Languages {
		out.Languages = append(out.Languages, l.Name)
	}
	slices.SortFunc(out.Languages, func(a, b string) int {
		// English first, the rest alphabetical.
		switch {
		case a == b:
			return 0
		case a == "English":
			return -1
		case b == "English":
			return 1
		}
		return strings.Compare(a, b)
	})
	for _, q := range decision.Qualities {
		out.Qualities = append(out.Qualities, q.Name)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) writeProfileSettings(w http.ResponseWriter, r *http.Request, ps library.ProfileSettings, err error) {
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	out := gen.ProfileSettings{Mode: gen.ProfileSettingsMode(ps.Mode)}
	if err := convert(ps.Simple, &out.Simple); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if err := convert(ps.Compiled, &out.Compiled); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetProfileSettings returns a profile's settings in both forms.
func (h *Handlers) GetProfileSettings(w http.ResponseWriter, r *http.Request, id int64) {
	ps, err := h.Library.ProfileSettings(r.Context(), id)
	h.writeProfileSettings(w, r, ps, err)
}

// PutProfileSimpleSettings saves simple settings (and returns to simple mode).
func (h *Handlers) PutProfileSimpleSettings(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.ProfileSimple
	if !decode(w, r, &body) {
		return
	}
	var simple decision.Simple
	if err := convert(body, &simple); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	ps, err := h.Library.SaveProfileSimple(r.Context(), id, simple)
	h.writeProfileSettings(w, r, ps, err)
}

// PutProfileAdvancedSettings saves an edited advanced form.
func (h *Handlers) PutProfileAdvancedSettings(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.ProfileCompiled
	if !decode(w, r, &body) {
		return
	}
	var adv decision.Profile
	if err := convert(body, &adv); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Some fields need attention.",
			validation.Errors{"advanced": err.Error()})
		return
	}
	ps, err := h.Library.SaveProfileAdvanced(r.Context(), id, adv)
	h.writeProfileSettings(w, r, ps, err)
}

// SwitchProfileToAdvanced moves a profile to advanced mode without changing
// what it does.
func (h *Handlers) SwitchProfileToAdvanced(w http.ResponseWriter, r *http.Request, id int64) {
	ps, err := h.Library.SwitchProfileToAdvanced(r.Context(), id)
	h.writeProfileSettings(w, r, ps, err)
}

// TestRelease runs a release name through a profile's saved settings.
func (h *Handlers) TestRelease(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.TestReleaseInput
	if !decode(w, r, &body) {
		return
	}
	var errs validation.Errors
	name := strings.TrimSpace(body.Name)
	if name == "" {
		errs.Add("name", "Type or paste a release name to test.")
	}
	runtime := 120
	if body.RuntimeMinutes != nil {
		runtime = *body.RuntimeMinutes
		if runtime < 1 || runtime > 1000 {
			errs.Add("runtimeMinutes", "The runtime must be between 1 and 1000 minutes.")
		}
	}
	var size int64
	if body.SizeGB != nil {
		if *body.SizeGB < 0 {
			errs.Add("sizeGB", "The size can't be negative.")
		}
		size = int64(*body.SizeGB * 1024 * 1024 * 1024)
	}
	lang := decision.LanguageEnglish
	if body.OriginalLanguage != nil && strings.TrimSpace(*body.OriginalLanguage) != "" {
		lang = decision.LanguageByName(strings.TrimSpace(*body.OriginalLanguage))
		if lang == decision.LanguageUnknown {
			errs.Add("originalLanguage", "That language isn't one Reelkeeper knows.")
		}
	}
	if len(errs) > 0 {
		h.writeServiceError(w, r, errs)
		return
	}
	p, err := h.Library.CompiledProfile(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	d := decision.TryName(p, name, size, runtime, lang)
	rel := d.Release
	out := gen.TestReleaseResult{
		Accepted: d.Accepted, Quality: d.Quality.Name, Score: d.Score, Upgrade: d.Upgrade,
		Rejections: nonNil(d.Rejections), Formats: []gen.TestReleaseFormat{},
		Parsed: gen.TestReleaseParsed{
			Title: rel.Title, Year: rel.Year, Resolution: rel.Resolution, Source: string(rel.Source),
			Group: rel.Group, Edition: rel.Edition, Languages: nonNil(rel.Languages), Hdr: nonNil(rel.HDR),
			Audio: nonNil(rel.Audio), VideoCodec: rel.VideoCodec, Revision: d.Revision,
		},
	}
	for _, f := range d.Formats {
		out.Formats = append(out.Formats, gen.TestReleaseFormat{Name: f.Name, Score: f.Score})
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
