package decision

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// A CustomFormat is a named test on a release, such as "x265" or "DV
// (w/o HDR fallback)". A profile gives each format a score; a release's
// score is the sum of the scores of the formats it matches.
//
// The JSON is Radarr's (and TRaSH Guides'), so formats can be pasted in
// from either and exported back. Matching follows Radarr's rules exactly
// (see Matches), so an imported format means the same thing here.
type CustomFormat struct {
	Name string `json:"name"`
	// IncludeWhenRenaming makes the format part of {Custom Formats} in file
	// names. Kept for compatibility with Radarr's JSON.
	IncludeWhenRenaming bool   `json:"includeCustomFormatWhenRenaming"`
	Specs               []Spec `json:"specifications"`
}

// Spec is one condition of a custom format.
type Spec struct {
	Name string `json:"name"`
	// Kind is Radarr's implementation name, such as
	// "ReleaseTitleSpecification". Kinds are listed in specKinds.
	Kind string `json:"implementation"`
	// Negate turns the condition around: "the title does NOT contain".
	Negate bool `json:"negate"`
	// Required conditions must all hold. Of the conditions of one kind that
	// aren't required, at least one must hold.
	Required bool `json:"required"`
	// Fields hold the kind's settings, such as "value" (a pattern, a number)
	// or "min"/"max".
	Fields Fields `json:"fields"`

	re *regexp2.Regexp // compiled "value" for the pattern kinds
}

// Fields are a spec's settings. Radarr's API writes them as a list of
// {"name", "value"} objects; TRaSH Guides writes a plain object. Both are
// read; the plain object is written.
type Fields map[string]any

// UnmarshalJSON accepts both layouts.
func (f *Fields) UnmarshalJSON(b []byte) error {
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err == nil {
		*f = obj
		return nil
	}
	var list []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("fields: want an object or a list of {name, value}: %w", err)
	}
	*f = Fields{}
	for _, kv := range list {
		(*f)[kv.Name] = kv.Value
	}
	return nil
}

func (f Fields) str(k string) string {
	s, _ := f[k].(string)
	return s
}

func (f Fields) num(k string) float64 {
	switch v := f[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}

func (f Fields) boolean(k string) bool {
	b, _ := f[k].(bool)
	return b
}

// The spec kinds Reelkeeper understands: all of Radarr's.
const (
	KindReleaseTitle  = "ReleaseTitleSpecification"
	KindReleaseGroup  = "ReleaseGroupSpecification"
	KindEdition       = "EditionSpecification"
	KindSource        = "SourceSpecification"
	KindResolution    = "ResolutionSpecification"
	KindModifier      = "QualityModifierSpecification"
	KindLanguage      = "LanguageSpecification"
	KindIndexerFlag   = "IndexerFlagSpecification"
	KindSize          = "SizeSpecification"
	KindYear          = "YearSpecification"
	patternMatchLimit = 100 * time.Millisecond
)

var specKinds = []string{
	KindReleaseTitle, KindReleaseGroup, KindEdition, KindSource, KindResolution,
	KindModifier, KindLanguage, KindIndexerFlag, KindSize, KindYear,
}

func isPatternKind(k string) bool {
	return k == KindReleaseTitle || k == KindReleaseGroup || k == KindEdition
}

// Compile checks the format and prepares its patterns. It must be called
// before Matches. Patterns are .NET regular expressions (as in Radarr), so
// lookarounds work; matching is case-insensitive.
func (cf *CustomFormat) Compile() error {
	if strings.TrimSpace(cf.Name) == "" {
		return fmt.Errorf("a custom format needs a name")
	}
	if len(cf.Specs) == 0 {
		return fmt.Errorf("custom format %q has no conditions", cf.Name)
	}
	for i := range cf.Specs {
		s := &cf.Specs[i]
		if !slices.Contains(specKinds, s.Kind) {
			return fmt.Errorf("custom format %q: unknown condition type %q", cf.Name, s.Kind)
		}
		if s.Fields == nil {
			s.Fields = Fields{}
		}
		if isPatternKind(s.Kind) {
			v := s.Fields.str("value")
			if v == "" {
				return fmt.Errorf("custom format %q, condition %q: the pattern is empty", cf.Name, s.Name)
			}
			re, err := regexp2.Compile(v, regexp2.IgnoreCase)
			if err != nil {
				return fmt.Errorf("custom format %q, condition %q: bad pattern: %w", cf.Name, s.Name, err)
			}
			// Formats can be pasted in from anywhere; a pathological
			// pattern must not stall a search.
			re.MatchTimeout = patternMatchLimit
			s.re = re
		}
	}
	return nil
}

// Input is what custom formats are tested against.
type Input struct {
	// Name is the release name (for a file: the original release name if
	// known, else the file name).
	Name string
	// FileName is the file's own name, for files already in the library;
	// title patterns are tried against it too, as Radarr does.
	FileName string
	Release  parser.Release
	Quality  Quality
	// Languages are the release's audio languages, as Radarr language IDs.
	Languages []int
	// OriginalLanguage is the movie's original language (a Radarr language
	// ID, 0 if unknown), which the "Original" language condition means.
	OriginalLanguage int
	Size             int64 // bytes, 0 if unknown
	IndexerFlags     int
	// Year is the movie's year, used when the name doesn't state one.
	Year int
}

// Matches reports whether the release matches the format, using Radarr's
// rule: conditions are grouped by kind; within each group every required
// condition must hold and at least one condition must hold. Every group
// must pass.
func (cf *CustomFormat) Matches(in Input) bool {
	groups := map[string][]bool{}
	required := map[string]bool{} // a required condition failed in this group
	for i := range cf.Specs {
		s := &cf.Specs[i]
		ok := s.holds(in)
		groups[s.Kind] = append(groups[s.Kind], ok)
		if s.Required && !ok {
			required[s.Kind] = true
		}
	}
	for kind, results := range groups {
		if required[kind] || !slices.Contains(results, true) {
			return false
		}
	}
	return true
}

// holds tests one condition, Negate included.
func (s *Spec) holds(in Input) bool {
	if s.Kind == KindLanguage {
		return s.languageHolds(in)
	}
	ok := s.holdsIgnoringNegate(in)
	if s.Negate {
		return !ok
	}
	return ok
}

func (s *Spec) holdsIgnoringNegate(in Input) bool {
	switch s.Kind {
	case KindReleaseTitle:
		return s.match(in.Name) || s.match(in.FileName)
	case KindReleaseGroup:
		return s.match(in.Release.Group)
	case KindEdition:
		return s.match(in.Release.Edition)
	case KindSource:
		return int(in.Quality.Source) == int(s.Fields.num("value"))
	case KindResolution:
		return in.Quality.Resolution == int(s.Fields.num("value"))
	case KindModifier:
		return int(in.Quality.Modifier) == int(s.Fields.num("value"))
	case KindIndexerFlag:
		flag := int(s.Fields.num("value"))
		return flag != 0 && in.IndexerFlags&flag == flag
	case KindSize:
		// Radarr: above min and at most max, in GB (1024^3 bytes).
		gb := float64(in.Size) / (1 << 30)
		return gb > s.Fields.num("min") && gb <= s.Fields.num("max")
	case KindYear:
		year := in.Release.Year
		if year == 0 {
			year = in.Year
		}
		return year >= int(s.Fields.num("min")) && year <= int(s.Fields.num("max"))
	}
	return false
}

// languageHolds follows Radarr's LanguageSpecification, including its
// handling of Negate with "except language": negated, a release with no
// known languages never holds.
func (s *Spec) languageHolds(in Input) bool {
	want := int(s.Fields.num("value"))
	if want == LanguageOriginal {
		if in.OriginalLanguage != LanguageUnknown {
			want = in.OriginalLanguage
		} else {
			// The movie's original language isn't known yet (its details
			// haven't been refreshed): a name that states no language
			// gets the benefit of the doubt. (Radarr rejects it.)
			want = LanguageUnknown
		}
	}
	var ok bool
	if s.Fields.boolean("exceptLanguage") {
		ok = slices.ContainsFunc(in.Languages, func(l int) bool { return l != want })
	} else {
		ok = slices.Contains(in.Languages, want)
	}
	if s.Negate {
		if in.Languages == nil {
			return false
		}
		return !ok
	}
	return ok
}

func (s *Spec) match(text string) bool {
	if text == "" || s.re == nil {
		return false
	}
	ok, err := s.re.MatchString(text)
	// A pattern that times out counts as not matching; the format is
	// still usable for other releases.
	return err == nil && ok
}
