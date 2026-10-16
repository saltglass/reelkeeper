package decision

import (
	"fmt"
	"slices"
	"strings"
)

// Profile is a quality profile in its compiled form: Radarr's model of
// qualities, custom formats and scores. Simple settings compile into it;
// "Advanced" shows and edits it directly.
type Profile struct {
	// Items are the qualities, worst first; a later item is better. An item
	// is either one quality or a group of qualities that count as equal
	// (such as "WEB 1080p" for WEBDL-1080p and WEBRip-1080p). Every
	// quality appears exactly once. Only allowed items are ever grabbed.
	Items []Item `json:"items"`

	// UpgradesAllowed lets Reelkeeper replace a file with a better release.
	// When false, the first acceptable release is final.
	UpgradesAllowed bool `json:"upgradesAllowed"`
	// UpgradeUntil names the item at which quality upgrades stop.
	UpgradeUntil string `json:"upgradeUntil"`
	// UpgradeUntilScore: once the quality is reached, upgrades for a better
	// custom-format score stop at this score.
	UpgradeUntilScore int `json:"upgradeUntilScore"`
	// MinUpgradeStep is how much a release must raise the score to count
	// as an upgrade on score alone (Radarr's minimum upgrade format score).
	MinUpgradeStep int `json:"minUpgradeStep"`

	// Formats are the custom formats this profile scores, and Scores their
	// scores by name. A format without a score scores 0.
	Formats []CustomFormat `json:"formats"`
	Scores  map[string]int `json:"scores"`
	// MinScore rejects releases whose total score is lower.
	MinScore int `json:"minScore"`
	// Required lists what a release must have, such as "HDR" or "English
	// audio". Each is met when the release matches any of its formats.
	// (Radarr has no such list; it fakes it with negated formats scoring
	// -10000. A list gives clearer reasons, and formats stay importable.)
	Required []Requirement `json:"required,omitempty"`

	// Sizes limits release size per quality name, in MB per minute of
	// runtime (as Radarr's quality definitions do). A missing quality or a
	// zero bound means no limit.
	Sizes map[string]SizeLimit `json:"sizes,omitempty"`

	// PreferPropers makes a PROPER or REPACK of the same quality an
	// upgrade, and preferred over the original, as Radarr's "prefer and
	// upgrade" does.
	PreferPropers bool `json:"preferPropers"`
}

// Requirement is something a release must have.
type Requirement struct {
	// Label names it in reasons: "It doesn't have <Label>, which this
	// profile requires."
	Label string `json:"label"`
	// AnyOf are custom format names; matching one is enough.
	AnyOf []string `json:"anyOf"`
}

// Item is one rung of a profile's quality ladder.
type Item struct {
	// Name is a quality's name, or a group's own name.
	Name string `json:"name"`
	// Qualities are the members of a group; empty for a single quality.
	Qualities []string `json:"qualities,omitempty"`
	Allowed   bool     `json:"allowed"`
}

// SizeLimit bounds release size in MB per minute of runtime. Zero means no
// bound.
type SizeLimit struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// BlockScore is the score the simple editor gives formats it blocks (and
// that TRaSH Guides uses for unwanted ones). Any format scoring this low or
// lower is reported as "blocked" rather than as a low total.
const BlockScore = -10000

// members are the quality names an item covers.
func (it Item) members() []string {
	if len(it.Qualities) > 0 {
		return it.Qualities
	}
	return []string{it.Name}
}

// Compile checks the profile and prepares its custom formats. Call it once
// after loading a profile and before using it.
func (p *Profile) Compile() error {
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, it := range p.Items {
		if strings.TrimSpace(it.Name) == "" {
			return fmt.Errorf("a quality item has no name")
		}
		if names[strings.ToLower(it.Name)] {
			return fmt.Errorf("two quality items are called %q", it.Name)
		}
		names[strings.ToLower(it.Name)] = true
		for _, m := range it.members() {
			q, ok := QualityByName(m)
			if !ok {
				return fmt.Errorf("unknown quality %q", m)
			}
			if seen[q.Name] {
				return fmt.Errorf("quality %s appears twice", q.Name)
			}
			seen[q.Name] = true
		}
	}
	if len(seen) != len(Qualities) {
		var missing []string
		for _, q := range Qualities {
			if !seen[q.Name] {
				missing = append(missing, q.Name)
			}
		}
		return fmt.Errorf("qualities missing from the profile: %s", strings.Join(missing, ", "))
	}
	if !slices.ContainsFunc(p.Items, func(it Item) bool { return it.Allowed }) {
		return fmt.Errorf("the profile doesn't allow any quality")
	}
	if p.UpgradesAllowed {
		i := p.itemIndex(p.UpgradeUntil)
		if i < 0 {
			return fmt.Errorf("\"upgrade until\" names %q, which isn't in the profile", p.UpgradeUntil)
		}
		if !p.Items[i].Allowed {
			return fmt.Errorf("\"upgrade until\" is %s, which the profile doesn't allow", p.UpgradeUntil)
		}
	}
	fnames := map[string]bool{} // exact names, as Scores uses them
	lower := map[string]bool{}
	for i := range p.Formats {
		if err := p.Formats[i].Compile(); err != nil {
			return err
		}
		n := p.Formats[i].Name
		if lower[strings.ToLower(n)] {
			return fmt.Errorf("two custom formats are called %q", n)
		}
		fnames[n], lower[strings.ToLower(n)] = true, true
	}
	for _, r := range p.Required {
		if strings.TrimSpace(r.Label) == "" || len(r.AnyOf) == 0 {
			return fmt.Errorf("a requirement needs a label and at least one custom format")
		}
		for _, name := range r.AnyOf {
			if !fnames[name] {
				return fmt.Errorf("requirement %q names %q, but there's no custom format with that name", r.Label, name)
			}
		}
	}
	for name := range p.Scores {
		if !fnames[name] {
			return fmt.Errorf("a score is set for %q, but there's no custom format with that name", name)
		}
	}
	return nil
}

func (p *Profile) itemIndex(name string) int {
	return slices.IndexFunc(p.Items, func(it Item) bool { return strings.EqualFold(it.Name, name) })
}

// Rank ranks a file the version already has (higher is better), for
// picking its best file: the position of the item holding q, with qualities
// the profile doesn't list placed as haveRank explains.
func (p *Profile) Rank(q Quality) int { return p.haveRank(q) }

func (p *Profile) rank(q Quality) int {
	return slices.IndexFunc(p.Items, func(it Item) bool {
		return slices.ContainsFunc(it.members(), func(m string) bool { return strings.EqualFold(m, q.Name) })
	})
}

// haveRank ranks a file the version already has. A file better than every
// quality the profile allows (a 2160p WEB-DL under an HD profile, say)
// ranks above every item: it is left alone, never "upgraded" to something
// worse. Otherwise it's the item holding its quality, or -1 when the profile
// doesn't list it, so any allowed release replaces it.
func (p *Profile) haveRank(q Quality) int {
	if p.aboveAllowed(q) {
		return len(p.Items)
	}
	return p.rank(q)
}

// aboveAllowed reports whether q is not allowed but better than every
// quality the profile allows: a higher resolution, or the same resolution
// and higher in Radarr's quality order. BR-DISK (a disc image) and Raw-HD
// end Radarr's list but aren't a better picture, so they never count.
func (p *Profile) aboveAllowed(q Quality) bool {
	if q.Resolution == 0 || q.Modifier == ModifierBRDisk || q.Modifier == ModifierRawHD || p.Allowed(q) {
		return false
	}
	found := false
	for _, it := range p.Items {
		if !it.Allowed {
			continue
		}
		for _, name := range it.members() {
			a, ok := QualityByName(name)
			if !ok {
				continue
			}
			found = true
			if q.Resolution < a.Resolution || q.Resolution == a.Resolution && qualityOrder(q) <= qualityOrder(a) {
				return false
			}
		}
	}
	return found
}

// qualityOrder is q's place in Qualities (Radarr's order, worst first).
func qualityOrder(q Quality) int {
	return slices.IndexFunc(Qualities, func(o Quality) bool { return o.ID == q.ID })
}

// Allowed reports whether the profile may grab quality q.
func (p *Profile) Allowed(q Quality) bool {
	r := p.rank(q)
	return r >= 0 && p.Items[r].Allowed
}

// FormatMatch is a custom format a release matched, with its score.
type FormatMatch struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Score adds up the scores of the formats the release matches. Matches are
// returned in the profile's order.
func (p *Profile) Score(in Input) (int, []FormatMatch) {
	total := 0
	var matched []FormatMatch
	for i := range p.Formats {
		cf := &p.Formats[i]
		if !cf.Matches(in) {
			continue
		}
		s := p.Scores[cf.Name]
		total += s
		matched = append(matched, FormatMatch{cf.Name, s})
	}
	return total, matched
}
