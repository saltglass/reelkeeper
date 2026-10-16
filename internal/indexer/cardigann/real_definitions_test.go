package cardigann

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealDefinitions checks that every real definition parses and that
// its search templates run. The definitions aren't part of this repo; to
// run it, download them and point REELKEEPER_DEFS at the folder:
//
//	curl -L https://codeload.github.com/Prowlarr/Indexers/tar.gz/refs/heads/master |
//	  tar -xz --strip-components=3 -C /tmp/defs '*/definitions/v11/'
//	REELKEEPER_DEFS=/tmp/defs go test ./internal/indexer/cardigann -run TestRealDefinitions -v
func TestRealDefinitions(t *testing.T) {
	dir := os.Getenv("REELKEEPER_DEFS")
	if dir == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	parsed, tplErr := 0, 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		d, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		parsed++
		v := vars()
		cfg := map[string]any{}
		for _, s := range d.Settings {
			cfg[s.Name] = s.SettingDefault()
		}
		v["Config"] = cfg
		var tpls []string
		for _, p := range d.SearchPaths() {
			tpls = append(tpls, p.Path)
			for _, kv := range p.Inputs {
				tpls = append(tpls, kv.Value)
			}
		}
		for _, kv := range d.Search.Inputs {
			tpls = append(tpls, kv.Value)
		}
		for _, kv := range d.Search.Fields {
			if kv.Value.Text != nil {
				tpls = append(tpls, *kv.Value.Text)
			}
			tpls = append(tpls, kv.Value.Selector)
		}
		tpls = append(tpls, d.Search.Rows.Selector.Selector)
		for _, tpl := range tpls {
			if _, err := Execute(tpl, v); err != nil {
				tplErr++
				t.Errorf("%s: %v in %s", d.ID, err, strings.ReplaceAll(tpl, "\n", " "))
			}
		}
	}
	t.Logf("%d files, %d parsed, %d template errors", len(files), parsed, tplErr)
}
