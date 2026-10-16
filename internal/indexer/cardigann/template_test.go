package cardigann

import "testing"

func vars() Vars {
	return Vars{
		"Config":     map[string]any{"sort": "time", "type": "desc", "disablesort": nil, "freeleech": CheckboxOn, "uploader": ""},
		"Keywords":   "dune part two",
		"Query":      map[string]any{"IMDBID": "tt15239678", "Season": nil, "Ep": nil, "DoubanID": nil},
		"Categories": []string{"42", "76"},
		"Result":     map[string]any{"title_optional": "", "title_default": "Dune Part Two 2024 2160p"},
		"True":       True,
		"False":      nil,
		"Today":      map[string]any{"Year": "2026"},
	}
}

func TestExecute(t *testing.T) {
	cases := []struct{ name, tpl, want string }{
		{"plain", "search/{{ .Keywords }}/", "search/dune part two/"},
		{"no actions", "browse.php", "browse.php"},
		{"if true", "{{ if .Config.freeleech }}fl=1{{ else }}fl=0{{ end }}", "fl=1"},
		{"unticked is false", "{{ if .Config.disablesort }}x{{ else }}y{{ end }}", "y"},
		{"the word False is true, as in Prowlarr", `{{ if "False" }}x{{ else }}y{{ end }}`, "x"},
		{"nil equals .False", "{{ if eq .Query.DoubanID .False }}x{{ else }}y{{ end }}", "x"},
		{"empty is not .False", "{{ if eq .Config.uploader .False }}x{{ else }}y{{ end }}", "y"},
		{"empty is false", "{{ if .Config.uploader }}x{{ else }}y{{ end }}", "y"},
		{"unknown is empty", "a{{ .Config.nope }}b{{ if .Query.Nope }}x{{ end }}", "ab"},
		{"and with parentheses", "{{ if and (.Keywords) (eq .Config.disablesort .False) }}sort-{{ else }}{{ end }}", "sort-"},
		{"and without parentheses", "{{ if and .Query.DoubanID (eq .Query.IMDBID .False) }}d{{ else }}i{{ end }}", "i"},
		{"or", "{{ if or .Query.Season .Query.Ep }}tv{{ else }}all{{ end }}", "all"},
		{"ne", `{{ if ne .Config.sort "time" }}x{{ else }}y{{ end }}`, "y"},
		{"eq with several", `{{ if eq .Config.sort "size" "time" }}x{{ end }}`, "x"},
		{"range", "{{ range .Categories }}cat[{{.}}]=1&{{end}}", "cat[42]=1&cat[76]=1&"},
		{"range with index", "{{ range $i, $c := .Categories }}{{ $i }}:{{ $c }};{{ end }}", "0:42;1:76;"},
		{"range sees variables", "{{ range .Categories }}{{ .Keywords }}{{ end }}", "dune part twodune part two"},
		{"join", `{{ join .Categories "," }}`, "42,76"},
		{"re_replace", `{{ re_replace .Keywords "\\s+" "+" }}`, "dune+part+two"},
		{"re_replace .NET groups", `{{ re_replace .Query.IMDBID "tt(\\d+)" "$1" }}`, "15239678"},
		{"nested if", "{{ if .Keywords }}{{ if .Config.freeleech }}a{{ else }}b{{ end }}{{ else }}c{{ end }}", "a"},
		{"else if", "{{ if .Config.uploader }}u{{ else if .Config.freeleech }}f{{ else }}n{{ end }}", "f"},
		{"result fields", "{{ if .Result.title_optional }}{{ .Result.title_optional }}{{ else }}{{ .Result.title_default }}{{ end }}", "Dune Part Two 2024 2160p"},
		{"trim markers", "a {{- .Today.Year -}} b", "a2026b"},
		{"comment", "a{{/* note */}}b", "ab"},
		{"true and false", "{{ .True }}/{{ .False }}", "True/"},
		// 1337x's real search path, with its stray ")".
		{"stray parenthesis", "{{ if and (.Keywords) (eq .Config.disablesort .False)) }}/{{ .Config.sort }}/{{ .Config.type }}{{ else }}{{ end }}/", "/time/desc/"},
		{"stray end", "a{{ end }}b", "ab"},
	}
	for _, c := range cases {
		got, err := Execute(c.tpl, vars())
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestExecuteErrors(t *testing.T) {
	for _, tpl := range []string{`{{ "unclosed }}`, `{{ frobnicate .Keywords }}`, `{{ re_replace .Keywords "(" "" }}`} {
		if _, err := Execute(tpl, vars()); err == nil {
			t.Errorf("%q: expected an error", tpl)
		}
	}
}

func FuzzExecute(f *testing.F) {
	for _, s := range []string{"{{ if and (.A) (eq .B .C)) }}x{{ else }}y{{ end }}", "{{ range .Categories }}{{.}}{{end}}", "{{", "}}{{ end }}{{ else }}", "{{ ((( }}"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tpl string) {
		_, _ = Execute(tpl, vars()) // must never panic or hang
	})
}
