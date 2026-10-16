package cardigann

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)

func TestFilters(t *testing.T) {
	v := Vars{"Config": map[string]any{"multilanguage": "MULTi", "flag": True}, "Result": map[string]any{"_bg": ""}}
	cases := []struct {
		name  string
		value string
		f     []Filter
		want  string
	}{
		{"querystring", "/download.php?id=42&name=x", []Filter{{"querystring", []string{"id"}}}, "42"},
		{"regexp group", "Size: 1.4 GB", []Filter{{"regexp", []string{`(\d+\.\d+ GB)`}}}, "1.4 GB"},
		{"regexp no match", "nothing", []Filter{{"regexp", []string{`(\d+)`}}}, ""},
		{"re_replace .NET lookahead", "MULTI FRENCH", []Filter{{"re_replace", []string{`(?i)\b(MULTI(?!.*(?:FRENCH|ENGLISH)))\b`, "X"}}}, "MULTI FRENCH"},
		{"re_replace templated", "Movie MULTI 1080p", []Filter{{"re_replace", []string{`(?i)\bMULTI\b`, "{{ .Config.multilanguage }}"}}}, "Movie MULTi 1080p"},
		{"split", "a/b/c", []Filter{{"split", []string{"/", "1"}}}, "b"},
		{"split negative", "a/b/c", []Filter{{"split", []string{"/", "-1"}}}, "c"},
		{"split out of range", "a/b", []Filter{{"split", []string{"/", "5"}}}, ""},
		{"replace chain", " x-y ", []Filter{{"replace", []string{"-", " "}}, {"trim", nil}}, "x y"},
		{"trim cutset", "[x]", []Filter{{"trim", []string{"[]"}}}, "x"},
		{"append templated", "Title", []Filter{{"append", []string{"{{ if .Result._bg }} (BG){{ else }}{{ end }}"}}}, "Title"},
		{"prepend", "x", []Filter{{"prepend", []string{"https://site/"}}}, "https://site/x"},
		{"case", "AbC", []Filter{{"tolower", nil}}, "abc"},
		{"url decode", "a%20b", []Filter{{"urldecode", nil}}, "a b"},
		{"html decode", "a &amp; b", []Filter{{"htmldecode", nil}}, "a & b"},
		{"diacritics", "Amélie", []Filter{{"diacritics", []string{"replace"}}}, "Amelie"},
		{"valid filename", `a:b?c`, []Filter{{"validfilename", nil}}, "abc"},
		{"validate", "Drama, Spy, Thriller", []Filter{{"validate", []string{"Action, Drama, Thriller"}}}, "Drama, Thriller"},
		{"json join", `{"tags":["a","b"]}`, []Filter{{"jsonjoinarray", []string{"tags", ", "}}}, "a, b"},
		// Dates in .NET formats, as Prowlarr's definitions write them.
		{"dateparse with zone", "2024-03-15 20:30:00 +01:00", []Filter{{"dateparse", []string{"yyyy-MM-dd HH:mm:ss zzz"}}}, "2024-03-15T20:30:00+01:00"},
		{"dateparse US", "03/15/2024 08:30:00 -05:00", []Filter{{"dateparse", []string{"MM/dd/yyyy HH:mm:ss zzz"}}}, "2024-03-15T08:30:00-05:00"},
		{"dateparse day name", "Fri, 15 Mar 2024 20:30:00 +00:00", []Filter{{"dateparse", []string{"ddd, dd MMM yyyy HH:mm:ss zzz"}}}, "2024-03-15T20:30:00Z"},
		{"dateparse 12h", "Mar 5 2024 08:30 PM", []Filter{{"dateparse", []string{"MMM d yyyy hh:mm tt"}}}, "2024-03-05T20:30:00Z"},
		{"dateparse no year", "15/03", []Filter{{"dateparse", []string{"dd/MM"}}}, "2026-03-15T00:00:00Z"},
		{"timeago", "2 hours ago", []Filter{{"timeago", nil}}, "2026-10-08T16:00:00Z"},
		{"timeago compound", "1 day, 3 hours ago", []Filter{{"timeago", nil}}, "2026-10-07T15:00:00Z"},
		{"fuzzy today", "Today 12:30", []Filter{{"fuzzytime", nil}}, "2026-10-08T12:30:00Z"},
		{"fuzzy yesterday", "Yesterday 11:00 pm", []Filter{{"fuzzytime", nil}}, "2026-10-07T23:00:00Z"},
		{"fuzzy unix", "1710534600", []Filter{{"fuzzytime", nil}}, "2024-03-15T20:30:00Z"},
		{"fuzzy date", "2024-03-15 20:30", []Filter{{"fuzzytime", nil}}, "2024-03-15T20:30:00Z"},
	}
	for _, c := range cases {
		got, err := applyFilters(c.value, c.f, v, now)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFilterErrors(t *testing.T) {
	for _, f := range []Filter{{"frobnicate", nil}, {"split", []string{"/", "x"}}, {"dateparse", []string{"yyyy-MM-dd"}}} {
		if _, err := applyFilters("not a date", []Filter{f}, Vars{}, now); err == nil {
			t.Errorf("%s: expected an error", f.Name)
		}
	}
}
