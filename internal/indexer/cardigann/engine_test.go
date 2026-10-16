package cardigann

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testHTTP is the engine's HTTP with a cookie jar, as the indexer layer
// provides it.
type testHTTP struct {
	jar  *cookiejar.Jar
	reqs []string
}

func newTestHTTP() *testHTTP {
	jar, _ := cookiejar.New(nil)
	return &testHTTP{jar: jar}
}

func (h *testHTTP) Do(req *http.Request, follow bool) (*http.Response, error) {
	h.reqs = append(h.reqs, req.Method+" "+req.URL.RequestURI())
	c := &http.Client{Jar: h.jar, Timeout: 5 * time.Second}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c.Do(req)
}
func (h *testHTTP) SetCookies(u *url.URL, c []*http.Cookie) { h.jar.SetCookies(u, c) }
func (h *testHTTP) Cookies(u *url.URL) []*http.Cookie       { return h.jar.Cookies(u) }

func engine(t *testing.T, def string, srv *httptest.Server, settings map[string]string) (*Engine, *testHTTP) {
	t.Helper()
	d, err := Parse([]byte(strings.ReplaceAll(def, "SITE", srv.URL+"/")))
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHTTP()
	return &Engine{Def: d, Settings: settings, HTTP: h, Now: func() time.Time { return now }}, h
}

// A public tracker in the style most HTML definitions describe.
const publicDef = `
id: testpublic
name: Test Public
type: public
links: [SITE]
caps:
  categorymappings:
    - {id: 42, cat: Movies/HD, desc: "Movies/HD", default: true}
    - {id: 41, cat: TV/HD, desc: "TV/HD", default: true}
    - {id: 22, cat: Audio/MP3, desc: "Music"}
  modes:
    search: [q]
    movie-search: [q]
    tv-search: [q, season, ep]
settings:
  - {name: sort, type: select, label: Sort, default: time, options: {time: created, seeders: seeders}}
  - {name: freeleech, type: checkbox, label: Freeleech only, default: false}
download:
  selectors:
    - selector: a.magnet
      attribute: href
search:
  paths:
    - path: "{{ if .Keywords }}search/{{ .Keywords }}/{{ .Config.sort }}/{{ else }}latest/{{ end }}"
  inputs:
    cats: "{{ join .Categories \",\" }}"
    fl: "{{ if .Config.freeleech }}1{{ else }}{{ end }}"
  keywordsfilters:
    - name: re_replace
      args: ["\\s+", "."]
  rows:
    selector: "table.results tr:has(a.title)"
  fields:
    title:
      selector: a.title
      filters:
        - name: replace
          args: ["_", " "]
    details:
      selector: a.title
      attribute: href
    download:
      selector: a.title
      attribute: href
    category:
      selector: td.cat
    size:
      selector: td.size
    seeders:
      selector: td.seeds
    leechers:
      selector: td.leech
    date:
      selector: td.age
      filters:
        - name: timeago
    downloadvolumefactor:
      case:
        span.free: 0
        "*": 1
    uploadvolumefactor:
      text: 1
    imdbid:
      selector: a.imdb
      attribute: href
`

func publicServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/") || strings.HasPrefix(r.URL.Path, "/latest/"):
			fmt.Fprint(w, `<html><body><table class="results">
<tr><th>Name</th></tr>
<tr><td><a class="title" href="/torrent/1/Dune_Part_Two_2024_2160p">Dune_Part_Two_2024_2160p</a><a class="imdb" href="https://imdb.com/title/tt15239678/">i</a></td>
<td class="cat">42</td><td class="size">23.4 GB</td><td class="seeds">1,234</td><td class="leech">56</td><td class="age">2 hours ago</td><td><span class="free">FL</span></td></tr>
<tr><td><a class="title" href="/torrent/2/Some.Show.S01E02.1080p">Some.Show.S01E02.1080p</a></td>
<td class="cat">41</td><td class="size">1.5 GiB</td><td class="seeds">N/A</td><td class="leech">0</td><td class="age">3 days ago</td></tr>
<tr><td><a class="title" href="/torrent/3/broken">Broken row, no size</a></td><td class="cat">42</td></tr>
</table></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/torrent/"):
			fmt.Fprint(w, `<html><a class="magnet" href="magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=x">Magnet</a></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSearchPublicHTML(t *testing.T) {
	srv := publicServer(t)
	defer srv.Close()
	e, h := engine(t, publicDef, srv, map[string]string{"sort": "seeders"})
	res, err := e.Search(context.Background(), Query{Type: "movie-search", Keywords: "Dune Part Two", Year: 2024, Categories: []int{2000}})
	if err != nil {
		t.Fatal(err)
	}
	// The request: keywords filtered, year added, URL-encoded into the path;
	// categories mapped to the tracker's own.
	if got := h.reqs[0]; got != "GET /search/Dune.Part.Two.2024/seeders/?cats=42" {
		t.Errorf("request = %s", got)
	}
	if len(res) != 2 {
		t.Fatalf("results = %d (%+v); the broken row should be skipped", len(res), res)
	}
	d := res[0]
	if d.Title != "Dune Part Two 2024 2160p" || d.Size != 25125558681 || d.Seeders != 1234 || d.Leechers != 56 ||
		d.DownloadFactor != 0 || d.UploadFactor != 1 || d.IMDbID != 15239678 {
		t.Errorf("first = %+v", d)
	}
	if d.DetailsURL != srv.URL+"/torrent/1/Dune_Part_Two_2024_2160p" || len(d.Categories) != 1 || d.Categories[0] != 2040 {
		t.Errorf("first links/categories = %+v", d)
	}
	if !d.PublishDate.Equal(now.Add(-2 * time.Hour)) {
		t.Errorf("date = %v", d.PublishDate)
	}
	s := res[1]
	if s.Seeders != 0 || s.Size != 1610612736 || s.DownloadFactor != 1 || s.Categories[0] != 5040 {
		t.Errorf("second = %+v", s)
	}

	// The real link is on the release page.
	link, err := e.DownloadLink(context.Background(), d)
	if err != nil || !strings.HasPrefix(link, "magnet:?xt=urn:btih:ABCDEF") {
		t.Errorf("download link = %q, %v", link, err)
	}

	// No keywords: the latest page, all default categories.
	h.reqs = nil
	if _, err := e.Search(context.Background(), Query{Type: "search"}); err != nil {
		t.Fatal(err)
	}
	if h.reqs[0] != "GET /latest/?cats=42%2C41" {
		t.Errorf("browse request = %s", h.reqs[0])
	}
}

// A private tracker with a form login (CSRF token in a hidden field) and
// sessions that expire.
const privateDef = `
id: testprivate
name: Test Private
type: private
links: [SITE]
caps:
  categorymappings:
    - {id: 1, cat: Movies}
settings:
  - {name: username, type: text, label: Username}
  - {name: password, type: password, label: Password}
login:
  path: login.php
  method: form
  form: form#login
  inputs:
    user: "{{ .Config.username }}"
    pass: "{{ .Config.password }}"
  error:
    - selector: div.error
  test:
    path: index.php
    selector: a[href="logout.php"]
search:
  paths:
    - path: browse.php
      method: post
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: div.torrent
  fields:
    title:
      selector: h2
    download:
      selector: a.dl
      attribute: href
    size:
      selector: .size
    seeders:
      text: 3
    leechers:
      text: 0
`

func privateServer(t *testing.T, logins *int) *httptest.Server {
	const session = "sess=ok"
	loggedIn := func(r *http.Request) bool {
		c, err := r.Cookie("sess")
		return err == nil && c.Value == "ok"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<form id="login" action="takelogin.php" method="post"><input type="hidden" name="csrf" value="tok123"><input name="user"><input name="pass" type="password"></form>`)
				return
			}
		case "/takelogin.php":
			_ = r.ParseForm()
			if r.Form.Get("csrf") != "tok123" || r.Form.Get("user") != "dan" || r.Form.Get("pass") != "secret" {
				fmt.Fprint(w, `<div class="error">Wrong password</div>`)
				return
			}
			*logins++
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "ok", Path: "/"})
			http.Redirect(w, r, "/index.php", http.StatusFound)
		case "/index.php":
			if loggedIn(r) {
				fmt.Fprint(w, `<a href="logout.php">Log out</a>`)
				return
			}
			http.Redirect(w, r, "/login.php", http.StatusFound)
		case "/browse.php":
			if !loggedIn(r) || r.Header.Get("X-Expire") != "" {
				http.Redirect(w, r, "/login.php", http.StatusFound)
				return
			}
			_ = r.ParseForm()
			fmt.Fprintf(w, `<a href="logout.php">x</a><div class="torrent"><h2>%s 1080p</h2><a class="dl" href="dl.php?id=9">dl</a><span class="size">700 MB</span></div>`, r.Form.Get("q"))
		}
		_ = session
	}))
}

func TestPrivateFormLogin(t *testing.T) {
	logins := 0
	srv := privateServer(t, &logins)
	defer srv.Close()
	e, h := engine(t, privateDef, srv, map[string]string{"username": "dan", "password": "secret"})
	res, err := e.Search(context.Background(), Query{Type: "search", Keywords: "Heat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != "Heat 1080p" || res[0].DownloadURL != srv.URL+"/dl.php?id=9" || res[0].Size != 700<<20 {
		t.Fatalf("results = %+v", res)
	}
	if logins != 1 {
		t.Errorf("logins = %d", logins)
	}
	// Logged in once per engine, not per search.
	if _, err := e.Search(context.Background(), Query{Type: "search", Keywords: "Ran"}); err != nil || logins != 1 {
		t.Errorf("second search: %v, logins %d", err, logins)
	}
	// An expired session (a redirect to the login page) logs in again.
	e.loggedIn = true
	h.jar.SetCookies(mustURL(srv.URL), []*http.Cookie{{Name: "sess", Value: "expired", Path: "/"}})
	if res, err := e.Search(context.Background(), Query{Type: "search", Keywords: "Heat"}); err != nil || len(res) != 1 || logins != 2 {
		t.Errorf("after expiry: %v, %d results, logins %d", err, len(res), logins)
	}

	bad, _ := engine(t, privateDef, srv, map[string]string{"username": "dan", "password": "wrong"})
	_, err = bad.Search(context.Background(), Query{Type: "search", Keywords: "Heat"})
	if !errors.Is(err, ErrLoginFailed) || !strings.Contains(err.Error(), "Wrong password") {
		t.Errorf("wrong password: %v", err)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// A JSON API in the UNIT3D style: rows under data, fields under
// attributes, booleans compared as True/False, rows filtered in the
// selector.
const jsonDef = `
id: testjson
name: Test JSON
type: private
links: [SITE]
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: Movie}
    - {id: 2, cat: TV, desc: TV}
settings:
  - {name: apikey, type: text, label: API key}
  - {name: freeleech, type: checkbox, label: Freeleech only}
search:
  paths:
    - path: api/torrents/filter
      response:
        type: json
  inputs:
    api_token: "{{ .Config.apikey }}"
    name: "{{ .Keywords }}"
    $raw: "{{ range .Categories }}categories[]={{.}}&{{end}}"
  rows:
    selector: "data{{ if .Config.freeleech }}:has(attributes.freeleech:contains(100%)){{ else }}{{ end }}"
    attribute: attributes
  fields:
    category:
      selector: category_id
    title:
      selector: name
    details:
      selector: details_link
    download:
      selector: download_link
    size:
      selector: size
    seeders:
      selector: seeders
    leechers:
      selector: leechers
    date:
      selector: created_at
    tmdbid:
      selector: tmdb_id
    _internal:
      selector: internal
      case:
        False: "{{ .False }}"
        True: "{{ .True }}"
    description:
      text: "{{ if .Result._internal }}Internal{{ else }}{{ end }}"
    downloadvolumefactor:
      selector: freeleech
      case:
        0%: 1
        100%: 0
`

func TestSearchJSON(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"data":[
 {"id":1,"attributes":{"name":"Movie A 2020 1080p","category_id":1,"details_link":"https://x/t/1","download_link":"https://x/d/1","size":1073741824,"seeders":10,"leechers":2,"created_at":"2024-03-15T20:30:00Z","tmdb_id":550,"internal":true,"freeleech":"100%"}},
 {"id":2,"attributes":{"name":"Movie B 2021 720p","category_id":1,"details_link":"https://x/t/2","download_link":"https://x/d/2","size":5000,"seeders":1,"leechers":0,"created_at":"2024-03-16T10:00:00Z","internal":false,"freeleech":"0%"}}
]}`)
	}))
	defer srv.Close()
	e, _ := engine(t, jsonDef, srv, map[string]string{"apikey": "k", "freeleech": "false"})
	res, err := e.Search(context.Background(), Query{Type: "movie-search", Keywords: "Movie", Categories: []int{2000}})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("api_token") != "k" || gotQuery.Get("categories[]") != "1" || gotQuery.Get("name") != "Movie" {
		t.Errorf("query = %v", gotQuery)
	}
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	a := res[0]
	if a.Title != "Movie A 2020 1080p" || a.Size != 1<<30 || a.TMDBID != 550 || a.Description != "Internal" || a.DownloadFactor != 0 ||
		a.PublishDate.Format(time.RFC3339) != "2024-03-15T20:30:00Z" || a.Categories[0] != 2000 {
		t.Errorf("A = %+v", a)
	}
	if res[1].Description != "" || res[1].DownloadFactor != 1 {
		t.Errorf("B = %+v", res[1])
	}
	// The freeleech setting filters rows in the selector.
	e.Settings["freeleech"] = "true"
	res, _ = e.Search(context.Background(), Query{Type: "movie-search", Keywords: "Movie"})
	if len(res) != 1 || res[0].Title != "Movie A 2020 1080p" {
		t.Errorf("freeleech only = %+v", res)
	}
}

// Grouped JSON results: a movie with its torrents; ".." reads the movie.
const groupedDef = `
id: testgrouped
name: Test Grouped
links: [SITE]
caps:
  categorymappings:
    - {id: 1, cat: Movies}
search:
  paths:
    - path: api.php
      response: {type: json}
  rows:
    selector: data.movies
    attribute: torrents
    multiple: true
  fields:
    _movie:
      selector: ..title
    _quality:
      selector: quality
    title:
      text: "{{ .Result._movie }} {{ .Result._quality }}"
    download:
      selector: url
    infohash:
      selector: hash
    size:
      selector: size_bytes
    category:
      text: 1
    seeders:
      selector: seeds
    leechers:
      selector: peers
`

func TestGroupedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"movies":[{"title":"Heat 1995","torrents":[
{"quality":"720p","url":"https://x/1.torrent","hash":"AAAA","size_bytes":100,"seeds":5,"peers":1},
{"quality":"1080p","url":"https://x/2.torrent","hash":"BBBB","size_bytes":200,"seeds":9,"peers":2}]}]}}`)
	}))
	defer srv.Close()
	e, _ := engine(t, groupedDef, srv, nil)
	res, err := e.Search(context.Background(), Query{Type: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Title != "Heat 1995 720p" || res[1].Title != "Heat 1995 1080p" || res[1].InfoHash != "bbbb" ||
		!strings.HasPrefix(res[1].MagnetURL, "magnet:?xt=urn:btih:bbbb") {
		t.Errorf("grouped = %+v", res)
	}
}

// XML answers, "after" rows, date headers, a POSTed search and a $raw
// input.
func TestXMLAndRowLayouts(t *testing.T) {
	xmlDef := `
id: testxml
name: Test XML
links: [SITE]
caps:
  categorymappings:
    - {id: 5, cat: TV}
search:
  paths:
    - path: rss.xml
      response: {type: xml}
  rows:
    selector: item
  fields:
    title: {selector: title}
    details: {selector: link}
    download: {selector: enclosure, attribute: url}
    size: {selector: "torznab\\:attr[name=size]", attribute: value}
    seeders: {selector: "torznab\\:attr[name=seeders]", attribute: value}
    leechers: {text: 0}
    category: {text: 5}
    date: {selector: pubDate}
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Show S01E01 720p</title><link>https://x/details/1</link><pubDate>Fri, 15 Mar 2024 20:30:00 +0000</pubDate>
<enclosure url="https://x/1.torrent" length="10" type="application/x-bittorrent"/>
<torznab:attr name="size" value="734003200"/><torznab:attr name="seeders" value="7"/></item>
</channel></rss>`)
	}))
	defer srv.Close()
	e, _ := engine(t, xmlDef, srv, nil)
	res, err := e.Search(context.Background(), Query{Type: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Title != "Show S01E01 720p" || res[0].DetailsURL != "https://x/details/1" || res[0].Size != 734003200 || res[0].Seeders != 7 {
		t.Errorf("xml = %+v", res)
	}

	rowsDef := `
id: testrows
name: Test Rows
links: [SITE]
caps:
  categorymappings:
    - {id: 1, cat: Movies}
search:
  paths:
    - path: browse
      method: post
  inputs:
    $raw: "search={{ .Keywords }}&order=desc"
  rows:
    selector: tr.r
    after: 1
    dateheaders:
      selector: td.day
      filters:
        - name: dateparse
          args: "yyyy-MM-dd"
  fields:
    title: {selector: a.t}
    download: {selector: a.t, attribute: href}
    size: {selector: td.size}
    seeders: {text: 1}
    leechers: {text: 0}
    category: {text: 1}
`
	var form url.Values
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		fmt.Fprint(w, `<table>
<tr class="h"><td class="day">2024-03-15</td></tr>
<tr class="r"><td><a class="t" href="/d/1">First</a></td></tr><tr class="r"><td class="size">1 GB</td></tr>
<tr class="r"><td><a class="t" href="/d/2">Second</a></td></tr><tr class="r"><td class="size">2 GB</td></tr>
</table>`)
	}))
	defer srv2.Close()
	e2, _ := engine(t, rowsDef, srv2, nil)
	res, err = e2.Search(context.Background(), Query{Type: "search", Keywords: "two words"})
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("search") != "two words" || form.Get("order") != "desc" {
		t.Errorf("posted = %v", form)
	}
	if len(res) != 2 || res[0].Size != 1<<30 || res[1].Size != 2<<30 || res[0].PublishDate.Format("2006-01-02") != "2024-03-15" {
		t.Errorf("rows = %+v", res)
	}
}

func TestNumbers(t *testing.T) {
	sizes := map[string]int64{"1.5 GB": 1610612736, "700 MiB": 734003200, "1,234.5 MB": 1294467072, "12345": 12345, "2 TB": 2 << 40, "1,5 GB": 1610612736}
	for in, want := range sizes {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	counts := map[string]int{"1,234": 1234, "N/A": 0, "-": 0, "": 0, "56": 56, "1.234": 1234}
	for in, want := range counts {
		if got, err := parseCount(in); err != nil || got != want {
			t.Errorf("parseCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
