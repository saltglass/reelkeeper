// Package cardigann runs tracker definitions in the Cardigann YAML format
// that Jackett and Prowlarr use (Prowlarr's "v11" schema). One definition
// describes how to log in to a tracker, search it, read its results and
// find a release's download link, so hundreds of trackers work without
// code for each.
//
// Definitions are fetched from the community repository at run time (see
// package catalog); none are part of Reelkeeper's own code.
package cardigann

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is one tracker's definition file.
type Definition struct {
	ID             string   `yaml:"id"`
	Replaces       []string `yaml:"replaces"`
	Name           string   `yaml:"name"`
	Description    string   `yaml:"description"`
	Language       string   `yaml:"language"`
	Type           string   `yaml:"type"` // public, semi-private, private
	Encoding       string   `yaml:"encoding"`
	FollowRedirect bool     `yaml:"followredirect"`
	RequestDelay   float64  `yaml:"requestDelay"` // seconds between requests
	Links          []string `yaml:"links"`
	LegacyLinks    []string `yaml:"legacylinks"`

	Caps     Caps      `yaml:"caps"`
	Settings []Setting `yaml:"settings"`
	Login    *Login    `yaml:"login"`
	Search   Search    `yaml:"search"`
	Download *Download `yaml:"download"`
}

// Caps says what a tracker can search for and how its categories map to
// the standard (Newznab) ones.
type Caps struct {
	Categories       map[string]string   `yaml:"categories"` // older style: tracker id → standard name
	CategoryMappings []CategoryMapping   `yaml:"categorymappings"`
	Modes            map[string][]string `yaml:"modes"` // "movie-search": [q, imdbid]...
	AllowRawSearch   bool                `yaml:"allowrawsearch"`
}

// CategoryMapping maps one tracker category to a standard one.
type CategoryMapping struct {
	ID      string `yaml:"id"`
	Cat     string `yaml:"cat"`
	Desc    string `yaml:"desc"`
	Default bool   `yaml:"default"`
}

// Setting is one field the user fills in for the tracker.
type Setting struct {
	Name     string         `yaml:"name"`
	Label    string         `yaml:"label"`
	Type     string         `yaml:"type"` // text, password, checkbox, select, info, info_*
	Default  any            `yaml:"default"`
	Defaults []string       `yaml:"defaults"`
	Options  OrderedStrings `yaml:"options"` // select: value → label
}

// Login describes how to sign in.
type Login struct {
	Method            string           `yaml:"method"` // form, post, cookie, get, oneurl
	Path              string           `yaml:"path"`
	SubmitPath        string           `yaml:"submitpath"`
	Form              string           `yaml:"form"`
	Cookies           []string         `yaml:"cookies"`
	Inputs            OrderedStrings   `yaml:"inputs"`
	SelectorInputs    OrderedSelectors `yaml:"selectorinputs"`
	GetSelectorInputs OrderedSelectors `yaml:"getselectorinputs"`
	Captcha           *struct {
		Type     string `yaml:"type"`
		Selector string `yaml:"selector"`
		Input    string `yaml:"input"`
	} `yaml:"captcha"`
	Error   []ErrorBlock   `yaml:"error"`
	Test    *PageTest      `yaml:"test"`
	Headers OrderedHeaders `yaml:"headers"`
}

// PageTest checks that a page shows we're logged in.
type PageTest struct {
	Path     string `yaml:"path"`
	Selector string `yaml:"selector"`
}

// ErrorBlock describes an error page: if selector matches, the page is an
// error, with message (itself a selector, or the matched text).
type ErrorBlock struct {
	Path     string    `yaml:"path"`
	Selector string    `yaml:"selector"`
	Message  *Selector `yaml:"message"`
}

// Search describes how to search and read the results.
type Search struct {
	Path                 string           `yaml:"path"`
	Paths                []SearchPath     `yaml:"paths"`
	AllowEmptyInputs     bool             `yaml:"allowEmptyInputs"`
	Inputs               OrderedStrings   `yaml:"inputs"`
	Headers              OrderedHeaders   `yaml:"headers"`
	KeywordsFilters      []Filter         `yaml:"keywordsfilters"`
	PreprocessingFilters []Filter         `yaml:"preprocessingfilters"`
	Error                []ErrorBlock     `yaml:"error"`
	Rows                 Rows             `yaml:"rows"`
	Fields               OrderedSelectors `yaml:"fields"`
}

// SearchPath is one request a search makes.
type SearchPath struct {
	Path           string         `yaml:"path"`
	Method         string         `yaml:"method"`
	FollowRedirect bool           `yaml:"followredirect"`
	Categories     []string       `yaml:"categories"` // only for these tracker categories
	Inputs         OrderedStrings `yaml:"inputs"`
	InheritInputs  *bool          `yaml:"inheritinputs"`
	QuerySeparator string         `yaml:"queryseparator"`
	Response       *struct {
		Type             string `yaml:"type"` // json, xml; default html
		NoResultsMessage string `yaml:"noResultsMessage"`
	} `yaml:"response"`
}

// Rows says where each result is.
type Rows struct {
	Selector                        `yaml:",inline"`
	After                           int       `yaml:"after"`       // merge this many following rows into each
	DateHeaders                     *Selector `yaml:"dateheaders"` // rows that hold a date for the rows below
	Count                           *Selector `yaml:"count"`
	Multiple                        bool      `yaml:"multiple"`
	MissingAttributeEqualsNoResults bool      `yaml:"missingAttributeEqualsNoResults"`
}

// Selector picks a value out of a page or JSON document.
type Selector struct {
	Selector  string         `yaml:"selector"`
	Attribute string         `yaml:"attribute"`
	Optional  bool           `yaml:"optional"`
	Default   *string        `yaml:"default"`
	Text      *string        `yaml:"text"`
	Remove    string         `yaml:"remove"`
	Case      OrderedStrings `yaml:"case"`
	Filters   []Filter       `yaml:"filters"`
}

// Filter transforms a value: {name: re_replace, args: ["a", "b"]}.
type Filter struct {
	Name string
	Args []string
}

// UnmarshalYAML accepts args as a single value or a list of any scalars.
func (f *Filter) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		Name string    `yaml:"name"`
		Args yaml.Node `yaml:"args"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	f.Name = raw.Name
	switch raw.Args.Kind {
	case 0:
	case yaml.ScalarNode:
		f.Args = []string{raw.Args.Value}
	case yaml.SequenceNode:
		for _, a := range raw.Args.Content {
			f.Args = append(f.Args, a.Value)
		}
	default:
		return fmt.Errorf("filter %s: args must be a value or a list", raw.Name)
	}
	return nil
}

// Download describes how to find a release's download link.
type Download struct {
	Method string `yaml:"method"`
	Before *struct {
		Path         string         `yaml:"path"`
		PathSelector *Selector      `yaml:"pathselector"`
		Method       string         `yaml:"method"`
		Inputs       OrderedStrings `yaml:"inputs"`
	} `yaml:"before"`
	Selectors []Selector `yaml:"selectors"`
	InfoHash  *struct {
		Hash  Selector `yaml:"hash"`
		Title Selector `yaml:"title"`
	} `yaml:"infohash"`
	Headers OrderedHeaders `yaml:"headers"`
}

// KV is one entry of an ordered mapping.
type KV[T any] struct {
	Key   string
	Value T
}

// OrderedStrings is a YAML mapping of strings, in file order (inputs and
// case blocks depend on it).
type OrderedStrings []KV[string]

// UnmarshalYAML keeps the mapping's order; values may be any scalar.
func (o *OrderedStrings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping, got %v", n.Tag)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		v := n.Content[i+1]
		val := v.Value
		if v.Kind != yaml.ScalarNode {
			val = "" // nested values aren't used by inputs or options
		}
		*o = append(*o, KV[string]{n.Content[i].Value, val})
	}
	return nil
}

// Get returns a key's value.
func (o OrderedStrings) Get(key string) (string, bool) {
	for _, kv := range o {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return "", false
}

// OrderedSelectors is a YAML mapping of selectors, in file order (fields
// may refer to earlier fields).
type OrderedSelectors []KV[Selector]

// UnmarshalYAML keeps the mapping's order. A field may be just a string,
// meaning its selector.
func (o *OrderedSelectors) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping, got %v", n.Tag)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		var sel Selector
		v := n.Content[i+1]
		if v.Kind == yaml.ScalarNode {
			sel.Selector = v.Value
		} else if err := v.Decode(&sel); err != nil {
			return fmt.Errorf("%s: %w", n.Content[i].Value, err)
		}
		*o = append(*o, KV[Selector]{n.Content[i].Value, sel})
	}
	return nil
}

// OrderedHeaders is a mapping of header names to one or more values.
type OrderedHeaders []KV[[]string]

// UnmarshalYAML accepts a value or a list per header.
func (o *OrderedHeaders) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected a mapping, got %v", n.Tag)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		v := n.Content[i+1]
		var vals []string
		switch v.Kind {
		case yaml.ScalarNode:
			vals = []string{v.Value}
		case yaml.SequenceNode:
			for _, x := range v.Content {
				vals = append(vals, x.Value)
			}
		}
		*o = append(*o, KV[[]string]{n.Content[i].Value, vals})
	}
	return nil
}

// Parse reads a definition file.
func Parse(data []byte) (*Definition, error) {
	var d Definition
	if err := yaml.Unmarshal(PrepareYAML(data), &d); err != nil {
		return nil, err
	}
	if d.ID == "" || d.Name == "" {
		return nil, fmt.Errorf("a definition needs an id and a name")
	}
	if len(d.Links) == 0 {
		return nil, fmt.Errorf("%s: no links", d.ID)
	}
	if d.Search.Path == "" && len(d.Search.Paths) == 0 {
		return nil, fmt.Errorf("%s: no search path", d.ID)
	}
	return &d, nil
}

// SearchPaths returns the requests a search makes (a single "path" is a
// list of one).
func (d *Definition) SearchPaths() []SearchPath {
	if len(d.Search.Paths) > 0 {
		return d.Search.Paths
	}
	return []SearchPath{{Path: d.Search.Path}}
}

// SettingDefault is a setting's starting value, as templates see it: text
// as is, a select's default option, a checkbox CheckboxOn or nil.
func (s Setting) SettingDefault() any {
	switch v := s.Default.(type) {
	case nil:
		if s.Type == "select" && len(s.Options) > 0 {
			return s.Options[0].Key
		}
		if s.Type == "checkbox" {
			return nil
		}
		return ""
	case bool:
		return checkbox(v)
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		if s.Type == "checkbox" {
			return checkbox(strings.EqualFold(v, "true"))
		}
		return v
	}
	return fmt.Sprint(s.Default)
}

func checkbox(on bool) any {
	if on {
		return CheckboxOn
	}
	return nil
}

// PrepareYAML readies a definition file for Go's YAML reader; anything
// reading definition files must pass them through it first. It turns
// "\/" into "/": YAML 1.2 allows that escape (it's
// JSON's) and .NET's reader accepts it; Go's YAML library doesn't. Outside
// quotes it only ever appears in regexes, where "\/" and "/" mean the
// same. A backslash that is itself escaped ("\\/") is left alone.
func PrepareYAML(b []byte) []byte {
	out := make([]byte, 0, len(b))
	run := 0 // backslashes in a row
	for _, c := range b {
		if c == '/' && run%2 == 1 {
			out = out[:len(out)-1]
		}
		if c == '\\' {
			run++
		} else {
			run = 0
		}
		out = append(out, c)
	}
	return out
}
