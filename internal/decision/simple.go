package decision

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Simple is a quality profile as the simple editor shows it: plain choices
// instead of score math. Compile turns it into a Profile.
type Simple struct {
	// Resolutions to accept: 480 (SD, including DVD), 720, 1080, 2160.
	Resolutions []int `json:"resolutions"`
	// LowestSource and HighestSource bound the source ladder (see Tiers):
	// for example "webrip" to "bluray" accepts WEBRip, WEB-DL and Blu-ray
	// encodes but not remuxes.
	LowestSource  string `json:"lowestSource"`
	HighestSource string `json:"highestSource"`
	// WebEqual counts WEB-DL and WEBRip as the same quality, so neither
	// replaces the other.
	WebEqual bool `json:"webEqual"`

	// Upgrades lets Reelkeeper replace a file with a better release, up to
	// UpgradeUntil. Off, the first acceptable release is final.
	Upgrades     bool `json:"upgrades"`
	UpgradeUntil Rung `json:"upgradeUntil"`
	// PreferPropers grabs a PROPER or REPACK (a fixed release) over the
	// original, and replaces the original with it.
	PreferPropers bool `json:"preferPropers"`

	// Sizes limits release size per resolution (keys "480", "720"...), in
	// GB per hour of runtime. Missing means no limit.
	Sizes map[string]SizeRange `json:"sizes,omitempty"`

	Languages LanguageChoice `json:"languages"`

	// PreferGroups and BlockGroups are release group names, such as
	// "FraMeSToR".
	PreferGroups []string `json:"preferGroups,omitempty"`
	BlockGroups  []string `json:"blockGroups,omitempty"`

	// Features maps feature IDs (see Features) to a choice. Missing means
	// "don't care".
	Features map[string]Choice `json:"features,omitempty"`
}

// Rung is one step of the ladder: a resolution and a source tier.
type Rung struct {
	Resolution int    `json:"resolution"`
	Source     string `json:"source"`
}

// SizeRange bounds size in GB per hour of runtime; zero means no bound.
type SizeRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// LanguageChoice says which audio languages are acceptable.
type LanguageChoice struct {
	// Mode is "any", "original" (the movie's original language) or "list".
	Mode string `json:"mode"`
	// List names the acceptable languages for "list", such as "English".
	List []string `json:"list,omitempty"`
}

// Choice is what the profile thinks of a feature.
type Choice string

// Choices, strongest last.
const (
	Prefer  Choice = "prefer"
	Avoid   Choice = "avoid"
	Require Choice = "require"
	Block   Choice = "block"
)

// Tiers is the source ladder, worst first. "dvd" only exists at 480 (SD).
var Tiers = []string{"hdtv", "dvd", "webrip", "webdl", "bluray", "remux"}

// TierNames are the tiers as the editor writes them.
var TierNames = map[string]string{
	"hdtv": "HDTV", "dvd": "DVD", "webrip": "WEBRip", "webdl": "WEB-DL",
	"bluray": "Blu-ray encode", "remux": "Remux",
}

// ladder places each quality the simple editor can offer on a rung, worst
// first. Qualities not listed (CAM, BR-DISK, Raw-HD...) are never allowed
// by simple settings; Advanced can allow them.
var ladder = []struct {
	quality string
	rung    Rung
}{
	{"SDTV", Rung{480, "hdtv"}},
	{"DVD", Rung{480, "dvd"}},
	{"WEBRip-480p", Rung{480, "webrip"}},
	{"WEBDL-480p", Rung{480, "webdl"}},
	{"Bluray-480p", Rung{480, "bluray"}},
	{"Bluray-576p", Rung{480, "bluray"}},
	{"HDTV-720p", Rung{720, "hdtv"}},
	{"WEBRip-720p", Rung{720, "webrip"}},
	{"WEBDL-720p", Rung{720, "webdl"}},
	{"Bluray-720p", Rung{720, "bluray"}},
	{"HDTV-1080p", Rung{1080, "hdtv"}},
	{"WEBRip-1080p", Rung{1080, "webrip"}},
	{"WEBDL-1080p", Rung{1080, "webdl"}},
	{"Bluray-1080p", Rung{1080, "bluray"}},
	{"Remux-1080p", Rung{1080, "remux"}},
	{"HDTV-2160p", Rung{2160, "hdtv"}},
	{"WEBRip-2160p", Rung{2160, "webrip"}},
	{"WEBDL-2160p", Rung{2160, "webdl"}},
	{"Bluray-2160p", Rung{2160, "bluray"}},
	{"Remux-2160p", Rung{2160, "remux"}},
}

// Feature is one thing the simple editor lets you prefer, avoid, require or
// block, such as "Dolby Vision". It stands for one or more TRaSH Guides
// custom formats.
type Feature struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Group is the editor section: "picture", "sound", "release",
	// "edition".
	Group string `json:"group"`
	Help  string `json:"help,omitempty"`
	// Formats are file names in trash/ (without .json).
	Formats []string `json:"-"`
	// Weight is the score "prefer" gives (and "avoid" takes away). It
	// follows TRaSH Guides' scores, so preferences rank sensibly against
	// each other: Dolby Vision outweighs HDR10+. Zero means each format
	// keeps its own TRaSH score (group tiers: tier 1 above tier 2).
	Weight int `json:"-"`
	// CanRequire says whether "require" makes sense for it.
	CanRequire bool `json:"canRequire"`
}

// Features lists every feature, in the editor's order.
var Features = []Feature{
	{ID: "dv", Label: "Dolby Vision", Group: "picture", Formats: []string{"dv-boost"}, Weight: 1000, CanRequire: true},
	{ID: "dv-no-fallback", Label: "Dolby Vision with no HDR fallback", Group: "picture", Formats: []string{"dv-wo-hdr-fallback"}, Weight: 1000,
		Help: "Some web releases carry only Dolby Vision. On a screen without Dolby Vision their colours look wrong (purple and green)."},
	{ID: "hdr10plus", Label: "HDR10+", Group: "picture", Formats: []string{"hdr10plus-boost"}, Weight: 100, CanRequire: true},
	{ID: "hdr", Label: "HDR (any kind)", Group: "picture", Formats: []string{"hdr"}, Weight: 500, CanRequire: true,
		Help: "HDR10, HDR10+, HLG or Dolby Vision with an HDR fallback."},
	{ID: "sdr-4k", Label: "4K without HDR", Group: "picture", Formats: []string{"sdr"}, Weight: 500,
		Help: "Most of 4K's benefit comes from HDR."},
	{ID: "x265-hd", Label: "x265 at 1080p or lower", Group: "picture", Formats: []string{"x265-hd"}, Weight: 500,
		Help: "Smaller files, but usually re-encoded from an already compressed release; TRaSH Guides advises against them."},
	{ID: "av1", Label: "AV1", Group: "picture", Formats: []string{"av1"}, Weight: 500,
		Help: "Efficient, but many TVs and streaming boxes can't play it without converting."},
	{ID: "3d", Label: "3D", Group: "picture", Formats: []string{"3d"}, Weight: 500},
	{ID: "upscaled", Label: "Upscaled", Group: "picture", Formats: []string{"upscaled"}, Weight: 500,
		Help: "Lower-resolution video blown up to look like 4K."},

	{ID: "atmos", Label: "Dolby Atmos", Group: "sound", Formats: []string{"truehd-atmos", "ddplus-atmos"}, Weight: 3000, CanRequire: true},
	{ID: "lossless", Label: "Lossless audio", Group: "sound", Formats: []string{"truehd-atmos", "dts-x", "truehd", "dts-hd-ma", "flac", "pcm"}, Weight: 2500, CanRequire: true,
		Help: "TrueHD, DTS-HD MA, DTS:X, FLAC or PCM."},

	{ID: "lq", Label: "Low-quality groups", Group: "release", Formats: []string{"lq", "lq-release-title"}, Weight: 1000,
		Help: "Groups known for poor encodes, fake releases or broken files (TRaSH Guides' list)."},
	{ID: "junk", Label: "Retagged or renamed releases", Group: "release", Formats: []string{"retags", "obfuscated"}, Weight: 1000,
		Help: "Someone else's release under a new name; often incomplete or not what it claims."},
	{ID: "br-disk", Label: "Full Blu-ray discs", Group: "release", Formats: []string{"br-disk"}, Weight: 1000,
		Help: "Disc images rather than video files; most players can't use them."},
	{ID: "extras", Label: "Extras only", Group: "release", Formats: []string{"extras"}, Weight: 1000,
		Help: "Bonus material without the movie itself."},
	{ID: "no-group", Label: "No release group in the name", Group: "release", Formats: []string{"no-rlsgroup"}, Weight: 1000,
		Help: "Releases that don't say who made them are often re-uploads or rushed encodes."},
	{ID: "good-groups", Label: "Well-known good groups", Group: "release",
		Formats: []string{
			"web-tier-01", "web-tier-02", "web-tier-03", "hd-bluray-tier-01", "hd-bluray-tier-02", "hd-bluray-tier-03",
			"uhd-bluray-tier-01", "uhd-bluray-tier-02", "uhd-bluray-tier-03", "remux-tier-01", "remux-tier-02", "remux-tier-03",
		},
		Help: "Groups TRaSH Guides rates highly for web, Blu-ray and remux releases, in three tiers; better tiers rank higher."},
	{ID: "freeleech", Label: "Freeleech", Group: "release", Formats: []string{"freeleech"}, Weight: 10,
		Help: "Downloads that don't count against your ratio on a private tracker."},

	{ID: "imax", Label: "IMAX", Group: "edition", Formats: []string{"imax", "imax-enhanced"}, Weight: 800, CanRequire: true},
	{ID: "remaster", Label: "Remaster", Group: "edition", Formats: []string{"remaster"}, Weight: 25},
	{ID: "criterion", Label: "Criterion Collection", Group: "edition", Formats: []string{"criterion-collection"}, Weight: 25},
	{ID: "hybrid", Label: "Hybrid", Group: "edition", Formats: []string{"hybrid"}, Weight: 100,
		Help: "Combines the best parts of several sources, such as Blu-ray video with a web release's HDR."},
}

//go:embed trash/*.json
var trashFS embed.FS

// trashFormat loads one bundled TRaSH Guides format.
func trashFormat(file string) (CustomFormat, error) {
	cf, _, err := trashFormatScore(file)
	return cf, err
}

// trashFormatScore loads a bundled format and TRaSH's default score for
// it.
func trashFormatScore(file string) (CustomFormat, int, error) {
	b, err := trashFS.ReadFile("trash/" + file + ".json")
	if err != nil {
		return CustomFormat{}, 0, err
	}
	var cf CustomFormat
	if err := json.Unmarshal(b, &cf); err != nil {
		return CustomFormat{}, 0, fmt.Errorf("trash/%s.json: %w", file, err)
	}
	var scores struct {
		Scores map[string]int `json:"trash_scores"`
	}
	_ = json.Unmarshal(b, &scores)
	return cf, scores.Scores["default"], nil
}

func featureByID(id string) (Feature, bool) {
	i := slices.IndexFunc(Features, func(f Feature) bool { return f.ID == id })
	if i < 0 {
		return Feature{}, false
	}
	return Features[i], true
}

// Validate checks the settings, returning every problem as a message
// keyed by field, ready for the editor.
func (s Simple) Validate() map[string]string {
	errs := map[string]string{}
	if len(s.Resolutions) == 0 {
		errs["resolutions"] = "Choose at least one resolution."
	}
	for _, r := range s.Resolutions {
		if !slices.Contains([]int{480, 720, 1080, 2160}, r) {
			errs["resolutions"] = fmt.Sprintf("%d isn't a resolution Reelkeeper knows.", r)
		}
	}
	lo, hi := slices.Index(Tiers, s.LowestSource), slices.Index(Tiers, s.HighestSource)
	switch {
	case lo < 0 || hi < 0:
		errs["sources"] = "Choose the lowest and highest acceptable sources."
	case lo > hi:
		errs["sources"] = "The lowest source must not be better than the highest."
	}
	if len(errs) == 0 && len(s.allowedQualities()) == 0 {
		errs["sources"] = "These resolutions and sources don't include any quality (DVD exists only in SD)."
	}
	if s.Upgrades && len(errs) == 0 {
		if !slices.Contains(s.allowedQualities(), s.rungQuality(s.UpgradeUntil)) {
			errs["upgradeUntil"] = "\"Upgrade until\" must be one of the accepted qualities."
		}
	}
	for res, r := range s.Sizes {
		if n, err := strconv.Atoi(res); err != nil || !slices.Contains([]int{480, 720, 1080, 2160}, n) {
			errs["sizes"] = fmt.Sprintf("%q isn't a resolution.", res)
		} else if r.Min < 0 || r.Max < 0 || (r.Max > 0 && r.Min > r.Max) {
			errs["sizes"] = fmt.Sprintf("The size limits for %sp don't make sense.", res)
		}
	}
	switch s.Languages.Mode {
	case "any", "original":
	case "list":
		if len(s.Languages.List) == 0 {
			errs["languages"] = "Choose at least one language."
		}
		for _, l := range s.Languages.List {
			if LanguageByName(l) == LanguageUnknown {
				errs["languages"] = fmt.Sprintf("%q isn't a language Reelkeeper knows.", l)
			}
		}
	default:
		errs["languages"] = "Choose which languages to accept."
	}
	for id, c := range s.Features {
		f, ok := featureByID(id)
		switch {
		case !ok:
			errs["features"] = fmt.Sprintf("Unknown feature %q.", id)
		case !slices.Contains([]Choice{Prefer, Avoid, Require, Block}, c):
			errs["features"] = fmt.Sprintf("%s: choose prefer, avoid, require or block.", f.Label)
		case c == Require && !f.CanRequire:
			errs["features"] = fmt.Sprintf("%s can't be required.", f.Label)
		}
	}
	for _, g := range append(slices.Clone(s.PreferGroups), s.BlockGroups...) {
		if strings.TrimSpace(g) == "" || strings.ContainsAny(g, " ()[]|\\") {
			errs["groups"] = fmt.Sprintf("%q isn't a release group name.", g)
		}
	}
	return errs
}

// rungQuality names the item that holds rung r once compiled.
func (s Simple) rungQuality(r Rung) string {
	if s.WebEqual && (r.Source == "webdl" || r.Source == "webrip") {
		return webGroup(r.Resolution)
	}
	if r == (Rung{480, "bluray"}) {
		return "Bluray SD" // Bluray-480p and Bluray-576p share the rung
	}
	for _, l := range ladder {
		if l.rung == r {
			return l.quality
		}
	}
	return ""
}

func webGroup(res int) string { return fmt.Sprintf("WEB %dp", res) }

// allowedQualities are the item names the settings allow.
func (s Simple) allowedQualities() []string {
	lo, hi := slices.Index(Tiers, s.LowestSource), slices.Index(Tiers, s.HighestSource)
	var names []string
	for _, l := range ladder {
		t := slices.Index(Tiers, l.rung.Source)
		if slices.Contains(s.Resolutions, l.rung.Resolution) && t >= lo && t <= hi {
			if n := s.rungQuality(l.rung); !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}

// Compile turns simple settings into a profile. The result is always the
// same for the same settings, so the editor can show what they mean under
// Advanced, and switching to Advanced keeps the profile's behaviour.
func (s Simple) Compile() (Profile, error) {
	if errs := s.Validate(); len(errs) > 0 {
		var msgs []string
		for _, m := range errs {
			msgs = append(msgs, m)
		}
		slices.Sort(msgs)
		return Profile{}, fmt.Errorf("%s", strings.Join(msgs, " "))
	}
	p := Profile{
		UpgradesAllowed: s.Upgrades,
		PreferPropers:   s.PreferPropers,
		Scores:          map[string]int{},
		// Anything at or below BlockScore is rejected as "blocked"; avoids
		// only ever lower a release's rank.
		MinScore:       BlockScore + 1,
		MinUpgradeStep: 1,
		// Upgrades stop once the file reaches "upgrade until": prefer and
		// avoid choose between the releases a search finds, but never cause
		// another download by themselves (Dan, 2026-10-18: no movie upgraded
		// over and over). Only a file with a blocked format scores this low,
		// and it is still replaced. Advanced can set a higher score to keep
		// chasing better releases, as in Radarr.
		UpgradeUntilScore: BlockScore + 1,
	}

	// Quality ladder: qualities the simple editor never offers come first,
	// never allowed; then the ladder, worst first.
	allowed := s.allowedQualities()
	inLadder := map[string]bool{}
	for _, l := range ladder {
		inLadder[l.quality] = true
	}
	for _, q := range Qualities {
		if !inLadder[q.Name] {
			p.Items = append(p.Items, Item{Name: q.Name})
		}
	}
	for _, l := range ladder {
		name := s.rungQuality(l.rung)
		if name != l.quality { // a member of a group, such as "WEB 1080p"
			if i := p.itemIndex(name); i >= 0 {
				p.Items[i].Qualities = append(p.Items[i].Qualities, l.quality)
				continue
			}
			p.Items = append(p.Items, Item{Name: name, Qualities: []string{l.quality}, Allowed: slices.Contains(allowed, name)})
			continue
		}
		p.Items = append(p.Items, Item{Name: name, Allowed: slices.Contains(allowed, name)})
	}
	if s.Upgrades {
		p.UpgradeUntil = s.rungQuality(s.UpgradeUntil)
	} else {
		// Unused while upgrades are off, but kept valid for Advanced.
		p.UpgradeUntil = allowed[len(allowed)-1]
	}

	// Sizes: GB per hour → MB per minute, for each quality at that
	// resolution.
	for res, r := range s.Sizes {
		n, _ := strconv.Atoi(res)
		if p.Sizes == nil {
			p.Sizes = map[string]SizeLimit{}
		}
		for _, l := range ladder {
			if l.rung.Resolution == n {
				p.Sizes[l.quality] = SizeLimit{Min: r.Min * 1024 / 60, Max: r.Max * 1024 / 60}
			}
		}
	}

	// Features, in the editor's order so the result is stable.
	add := func(cf CustomFormat, score int) {
		if i := slices.IndexFunc(p.Formats, func(f CustomFormat) bool { return f.Name == cf.Name }); i < 0 {
			p.Formats = append(p.Formats, cf)
		}
		switch {
		case score <= BlockScore || p.Scores[cf.Name] <= BlockScore && p.Scores[cf.Name] != 0:
			p.Scores[cf.Name] = BlockScore
		default:
			p.Scores[cf.Name] += score
		}
	}
	for _, f := range Features {
		c, ok := s.Features[f.ID]
		if !ok {
			continue
		}
		var names []string
		for _, file := range f.Formats {
			cf, trashScore, err := trashFormatScore(file)
			if err != nil {
				return Profile{}, err
			}
			names = append(names, cf.Name)
			weight := f.Weight
			if weight == 0 {
				weight = max(trashScore, -trashScore)
			}
			switch c {
			case Prefer:
				add(cf, weight)
			case Avoid:
				add(cf, -weight)
			case Block:
				add(cf, BlockScore)
			case Require:
				add(cf, 0)
			}
		}
		if c == Require {
			p.Required = append(p.Required, Requirement{Label: f.Label, AnyOf: names})
		}
	}

	// Release groups: one format each, so reasons name the group.
	for _, g := range s.PreferGroups {
		add(groupFormat(g), 100)
	}
	for _, g := range s.BlockGroups {
		add(groupFormat(g), BlockScore)
	}

	// Languages: a requirement, so a rejection says "It doesn't have
	// English audio" rather than naming a negated format.
	switch s.Languages.Mode {
	case "original":
		cf := languageFormat("Original language", LanguageOriginal)
		add(cf, 0)
		p.Required = append(p.Required, Requirement{Label: "the movie's original language", AnyOf: []string{cf.Name}})
	case "list":
		var names []string
		for _, l := range s.Languages.List {
			cf := languageFormat("Language: "+l, LanguageByName(l))
			add(cf, 0)
			names = append(names, cf.Name)
		}
		p.Required = append(p.Required, Requirement{Label: list(s.Languages.List) + " audio", AnyOf: names})
	}

	for name, score := range p.Scores {
		if score == 0 {
			delete(p.Scores, name)
		}
	}
	if err := p.Compile(); err != nil {
		return Profile{}, fmt.Errorf("compiling simple settings: %w", err)
	}
	return p, nil
}

func groupFormat(g string) CustomFormat {
	return CustomFormat{Name: "Group: " + g, Specs: []Spec{{
		Name: g, Kind: KindReleaseGroup, Required: true,
		// Whole name, as TRaSH Guides' group formats do: "FLUX" mustn't
		// match "FLUXX".
		Fields: Fields{"value": "^(" + escapeRegex(g) + ")$"},
	}}}
}

func languageFormat(name string, id int) CustomFormat {
	return CustomFormat{Name: name, Specs: []Spec{{
		Name: name, Kind: KindLanguage, Required: true, Fields: Fields{"value": float64(id)},
	}}}
}

func escapeRegex(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$#`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
