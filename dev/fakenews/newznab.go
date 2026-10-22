package main

import (
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// indexer is a Newznab indexer listing the made-up releases. It searches by
// words only (its caps say so, so Reelkeeper sends no IDs): a release
// matches when every word of the query is in its name.
type indexer struct {
	data string
	// base is the address clients fetch .nzb files from (e.g.
	// http://fakenews:8080), which is the address SABnzbd reaches it at.
	base string
	news *newsServer
	log  *slog.Logger
}

const capsXML = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server title="fakenews" />
  <limits max="100" default="100" />
  <searching>
    <search available="yes" supportedParams="q" />
    <tv-search available="yes" supportedParams="q,season,ep" />
    <movie-search available="yes" supportedParams="q" />
  </searching>
  <categories>
    <category id="2000" name="Movies"><subcat id="2040" name="HD" /></category>
    <category id="5000" name="TV"><subcat id="5040" name="HD" /></category>
  </categories>
</caps>`

func (ix *indexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api":
		ix.api(w, r)
	case "/control":
		// POST /control?down=1 takes the news server away; down=0 brings it
		// back.
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		ix.news.down.Store(r.URL.Query().Get("down") == "1")
		ix.log.Info("news server switched", "down", ix.news.down.Load())
		fmt.Fprintf(w, "down=%v\n", ix.news.down.Load())
	case "/":
		fmt.Fprintln(w, "fakenews: a made-up Usenet for Reelkeeper's dev stack")
	default:
		http.NotFound(w, r)
	}
}

func (ix *indexer) api(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch q.Get("t") {
	case "caps":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(capsXML))
	case "get":
		name := q.Get("id")
		if strings.ContainsAny(name, `/\`) || name == "" {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		b, err := os.ReadFile(filepath.Join(releaseDir(ix.data, name), name+".nzb"))
		if err != nil {
			newznabError(w, 300, "no such item")
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.nzb"`, name))
		_, _ = w.Write(b)
	case "search", "movie", "tvsearch":
		ix.search(w, r)
	default:
		newznabError(w, 202, "no such function")
	}
}

func newznabError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><error code="%d" description="%s"/>`, code, msg)
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func words(s string) []string {
	return strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

func (ix *indexer) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	terms := words(q.Get("q"))
	if s := q.Get("season"); s != "" {
		n, _ := strconv.Atoi(s)
		ep := fmt.Sprintf("s%02d", n)
		if e := q.Get("ep"); e != "" {
			m, _ := strconv.Atoi(e)
			ep += fmt.Sprintf("e%02d", m)
		}
		terms = append(terms, ep)
	}
	var cats []int
	for c := range strings.SplitSeq(q.Get("cat"), ",") {
		if n, err := strconv.Atoi(c); err == nil {
			cats = append(cats, n)
		}
	}
	all, err := loadReleases(ix.data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var items []rssItem
	for _, rel := range all {
		if !inCategories(rel.Category, cats) || !matches(rel.Name, terms) {
			continue
		}
		link := fmt.Sprintf("%s/api?t=get&id=%s&apikey=%s", ix.base, rel.Name, q.Get("apikey"))
		items = append(items, rssItem{
			Title: rel.Name, GUID: rssGUID{IsPermaLink: false, ID: "fakenews-" + rel.Name}, Link: link,
			PubDate: rel.Posted.Format(time.RFC1123Z), Category: strconv.Itoa(rel.Category),
			Enclosure: rssEnclosure{URL: link, Length: rel.Size, Type: "application/x-nzb"},
			Attrs: []rssAttr{{"category", strconv.Itoa(rel.Category)}, {"size", strconv.FormatInt(rel.Size, 10)},
				{"files", strconv.Itoa(len(rel.Files))}, {"grabs", "1"}},
		})
	}
	ix.log.Info("search", "t", q.Get("t"), "terms", terms, "cats", cats, "results", len(items))
	feed := rss{Version: "2.0", NS: "http://www.newznab.com/DTD/2010/feeds/attributes/",
		Channel: rssChannel{Title: "fakenews", Response: rssResponse{Offset: 0, Total: len(items)}, Items: items}}
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(feed)
}

func inCategories(c int, want []int) bool {
	if len(want) == 0 {
		return true
	}
	for _, x := range want {
		// 2000 covers 2000-2999; 2040 is just 2040.
		if x == c || (x%1000 == 0 && c/1000 == x/1000) || (c%1000 == 0 && x/1000 == c/1000) {
			return true
		}
	}
	return false
}

func matches(name string, terms []string) bool {
	have := map[string]bool{}
	for _, w := range words(name) {
		have[w] = true
	}
	for _, t := range terms {
		if !have[t] {
			return false
		}
	}
	return true
}

type rss struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	NS      string     `xml:"xmlns:newznab,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title    string      `xml:"title"`
	Response rssResponse `xml:"newznab:response"`
	Items    []rssItem   `xml:"item"`
}

type rssResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type rssItem struct {
	Title     string       `xml:"title"`
	GUID      rssGUID      `xml:"guid"`
	Link      string       `xml:"link"`
	PubDate   string       `xml:"pubDate"`
	Category  string       `xml:"category"`
	Enclosure rssEnclosure `xml:"enclosure"`
	Attrs     []rssAttr    `xml:"newznab:attr"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	ID          string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type rssAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}
