package cardigann

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// madeUp1337xLike is a small definition written for these tests, aimed at
// the same kind of page the community "1337x" definition reads: a table of
// results with "coll-N" cells, dates like "Apr. 6th '24", and a details
// page that holds the magnet link.
const madeUp1337xLike = `
id: madeup
name: Made-Up Torrents
type: public
links: [SITE]
caps:
  categorymappings:
    - {id: 42, cat: Movies/HD, desc: "Movies/HD"}
    - {id: 54, cat: Movies/HD, desc: "Movies/h.264/x264"}
    - {id: 70, cat: Movies/UHD, desc: "Movies/UHD"}
    - {id: 76, cat: Movies/UHD, desc: "Movies/4K"}
    - {id: 4, cat: Movies/SD, desc: "Movies/DivX"}
    - {id: 40, cat: Other/Misc, desc: "Other"}
  modes:
    search: [q]
    movie-search: [q]
download:
  selectors:
    - selector: ul li a[href^="magnet:"]
      attribute: href
search:
  paths:
    - path: "search/{{ .Keywords }}/1/"
  rows:
    selector: 'tr:has(a[href^="/torrent/"])'
  fields:
    title:
      selector: td[class^="coll-1"] a[href^="/torrent/"]
    category_optional:
      optional: true
      selector: td[class^="coll-1"] a[href^="/sub/"]
      attribute: href
      filters:
        - name: split
          args: ["/", 2]
    category:
      text: "{{ if .Result.category_optional }}{{ .Result.category_optional }}{{ else }}40{{ end }}"
    details:
      selector: td[class^="coll-1"] a[href^="/torrent/"]
      attribute: href
    download:
      selector: td[class^="coll-1"] a[href^="/torrent/"]
      attribute: href
    date:
      selector: td[class^="coll-date"]
      filters:
        - name: replace
          args: ["'", ""]
        - name: re_replace
          args: ["st|nd|rd|th", ""]
        - name: dateparse
          args: "MMM. d yy"
    size:
      selector: td[class^="coll-4"]
    seeders:
      selector: td[class^="coll-2"]
    leechers:
      selector: td[class^="coll-3"]
`

// The recorded pages are made up (invented release names, uploaders and
// hashes), in the shape of a real search page and release page.
func readRecorded1337x(t *testing.T) (search, details []byte) {
	t.Helper()
	search, err := os.ReadFile("../../../testdata/indexers/1337x-search-made-up.html")
	if err != nil {
		t.Fatal(err)
	}
	details, err = os.ReadFile("../../../testdata/indexers/1337x-details.html")
	if err != nil {
		t.Fatal(err)
	}
	return search, details
}

func serveRecorded1337x(search, details []byte, paths *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/torrent/") {
			_, _ = w.Write(details)
			return
		}
		_, _ = w.Write(search)
	}))
}

// checkRecorded1337x holds what both tests below expect from the pages.
func checkRecorded1337x(t *testing.T, e *Engine, paths *[]string) {
	t.Helper()
	res, err := e.Search(context.Background(), Query{Type: "movie-search", Keywords: "Zendral Quorn Part Two", Year: 2024, Categories: []int{2000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(*paths) == 0 || (*paths)[0] != "/search/Zendral Quorn Part Two 2024/1/" {
		t.Errorf("requested %v", *paths)
	}
	if len(res) < 15 {
		t.Fatalf("only %d results", len(res))
	}
	for _, r := range res {
		if r.Title == "" || r.Size < 100<<20 || r.DetailsURL == "" || r.PublishDate.IsZero() || len(r.Categories) == 0 {
			t.Errorf("incomplete: %+v", r)
		}
	}
	if !strings.Contains(strings.ReplaceAll(res[0].Title, ".", " "), "Zendral Quorn Part Two") {
		t.Errorf("first title = %q", res[0].Title)
	}
	link, err := e.DownloadLink(context.Background(), res[0])
	if err != nil || !strings.HasPrefix(link, "magnet:?xt=urn:btih:") {
		t.Errorf("download link = %q, %v", link, err)
	}
	t.Logf("%d results; first %q, %d bytes, %d seeders", len(res), res[0].Title, res[0].Size, res[0].Seeders)
}

// TestRecordedPages runs a made-up definition against the recorded search
// page and release page.
func TestRecordedPages(t *testing.T) {
	search, details := readRecorded1337x(t)
	var paths []string
	srv := serveRecorded1337x(search, details, &paths)
	defer srv.Close()
	e, _ := engine(t, madeUp1337xLike, srv, nil)
	checkRecorded1337x(t, e, &paths)
}

// TestRecorded1337x runs the community "1337x" definition against the same
// pages. The definitions aren't part of this repo; see TestRealDefinitions
// for how to point REELKEEPER_DEFS at them.
func TestRecorded1337x(t *testing.T) {
	defs := os.Getenv("REELKEEPER_DEFS")
	if defs == "" {
		t.Skip("needs REELKEEPER_DEFS (the community definitions)")
	}
	search, details := readRecorded1337x(t)
	raw, err := os.ReadFile(filepath.Join(defs, "1337x.yml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	srv := serveRecorded1337x(search, details, &paths)
	defer srv.Close()
	// Settings: sorting off, magnet links first.
	e := &Engine{Def: d, BaseURL: srv.URL + "/", HTTP: newTestHTTP(), Now: func() time.Time { return now },
		Settings: map[string]string{"disablesort": "true", "primarydownloadlink": "magnet:", "fallbackdownloadlink": "//itorrents."}}
	checkRecorded1337x(t, e, &paths)
}
