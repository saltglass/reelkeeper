package newznab

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
)

// Client searches a Torznab (torrents) or Newznab (Usenet) indexer: a
// standard API that Jackett, other Prowlarrs and many trackers offer.
//
//	GET <url>/api?t=caps                          what it can search
//	GET <url>/api?t=movie&imdbid=…&cat=2000&apikey=…  a search, answered in RSS
type Client struct {
	URL    string // the indexer's address, e.g. https://tracker/api or …/torznab/1
	APIKey string
	HTTP   core.HTTP

	mu   sync.Mutex
	caps *Caps
}

// Caps is what an indexer says it can search by.
type Caps struct {
	Search      []string // supported params: q, imdbid, tmdbid, tvdbid, season, ep
	TVSearch    []string
	MovieSearch []string
	Categories  []int
}

func (c *Caps) supports(t, param string) bool {
	var list []string
	switch t {
	case "tv-search":
		list = c.TVSearch
	case "movie-search":
		list = c.MovieSearch
	default:
		list = c.Search
	}
	for _, p := range list {
		if p == param {
			return true
		}
	}
	return false
}

// apiURL builds a request URL for function t.
func (c *Client) apiURL(params url.Values) string {
	base := strings.TrimRight(c.URL, "/")
	if !strings.HasSuffix(base, "/api") && !strings.Contains(base, "/api?") {
		base += "/api"
	}
	if c.APIKey != "" {
		params.Set("apikey", c.APIKey)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + params.Encode()
}

func (c *Client) get(ctx context.Context, params url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(params), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if msg := apiError(body); msg != "" {
		return nil, fmt.Errorf("the indexer said: %s", msg)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the indexer answered HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// apiError reads <error code="100" description="Incorrect user credentials"/>.
func apiError(body []byte) string {
	var e struct {
		XMLName     xml.Name `xml:"error"`
		Code        string   `xml:"code,attr"`
		Description string   `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &e) == nil && e.XMLName.Local == "error" {
		if e.Description != "" {
			return e.Description
		}
		return "error " + e.Code
	}
	return ""
}

// Capabilities asks the indexer what it can search (and caches it).
func (c *Client) Capabilities(ctx context.Context) (*Caps, error) {
	c.mu.Lock()
	cached := c.caps
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	body, err := c.get(ctx, url.Values{"t": {"caps"}})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Searching struct {
			Search      capMode `xml:"search"`
			TVSearch    capMode `xml:"tv-search"`
			MovieSearch capMode `xml:"movie-search"`
		} `xml:"searching"`
		Categories struct {
			Category []struct {
				ID     int `xml:"id,attr"`
				Subcat []struct {
					ID int `xml:"id,attr"`
				} `xml:"subcat"`
			} `xml:"category"`
		} `xml:"categories"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("the indexer's capabilities couldn't be read: %w", err)
	}
	caps := &Caps{
		Search: raw.Searching.Search.params(), TVSearch: raw.Searching.TVSearch.params(),
		MovieSearch: raw.Searching.MovieSearch.params(),
	}
	for _, cat := range raw.Categories.Category {
		caps.Categories = append(caps.Categories, cat.ID)
		for _, s := range cat.Subcat {
			caps.Categories = append(caps.Categories, s.ID)
		}
	}
	c.mu.Lock()
	c.caps = caps
	c.mu.Unlock()
	return caps, nil
}

type capMode struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

func (m capMode) params() []string {
	if m.Available != "yes" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(m.SupportedParams, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Search runs a search. IDs are used where the indexer supports them
// (they're exact); otherwise the title is searched.
func (c *Client) Search(ctx context.Context, q core.Query) ([]core.Release, error) {
	caps, err := c.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	t := q.Type
	if t == "" || (t != "search" && len(map[string][]string{"tv-search": caps.TVSearch, "movie-search": caps.MovieSearch}[t]) == 0) {
		t = "search" // the indexer can't do this kind of search
	}
	params := url.Values{"t": {map[string]string{"search": "search", "tv-search": "tvsearch", "movie-search": "movie"}[t]}, "extended": {"1"}}
	usedID := false
	if q.IMDbID != "" && caps.supports(t, "imdbid") {
		params.Set("imdbid", strings.TrimPrefix(q.IMDbID, "tt"))
		usedID = true
	}
	if q.TMDBID != 0 && caps.supports(t, "tmdbid") {
		params.Set("tmdbid", strconv.Itoa(q.TMDBID))
		usedID = true
	}
	if q.TVDBID != 0 && caps.supports(t, "tvdbid") {
		params.Set("tvdbid", strconv.Itoa(q.TVDBID))
		usedID = true
	}
	keywords := q.Keywords
	if t == "search" && q.Year > 0 && keywords != "" {
		keywords += " " + strconv.Itoa(q.Year)
	}
	if !usedID && keywords != "" {
		params.Set("q", keywords)
	}
	if q.Season > 0 {
		if t == "tv-search" {
			params.Set("season", strconv.Itoa(q.Season))
			if q.Episode != "" {
				params.Set("ep", q.Episode)
			}
		} else if params.Get("q") != "" {
			ep := fmt.Sprintf("S%02d", q.Season)
			if n, err := strconv.Atoi(q.Episode); err == nil {
				ep += fmt.Sprintf("E%02d", n)
			}
			params.Set("q", params.Get("q")+" "+ep)
		}
	}
	if len(q.Categories) > 0 {
		params.Set("cat", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(q.Categories)), ","), "[]"))
	}
	body, err := c.get(ctx, params)
	if err != nil {
		return nil, err
	}
	return ParseFeed(body)
}

// Test checks the API key works and the indexer returns releases.
func (c *Client) Test(ctx context.Context) error {
	c.mu.Lock()
	c.caps = nil
	c.mu.Unlock()
	res, err := c.Search(ctx, core.Query{Type: "search"})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("the indexer answered but listed no releases")
	}
	return nil
}

// DownloadLink returns the link the indexer gave with the release.
func (c *Client) DownloadLink(_ context.Context, r core.Release) (string, error) {
	if l := firstNonEmpty(r.DownloadURL, r.MagnetURL); l != "" {
		return l, nil
	}
	return "", errors.New("the indexer gave no download link")
}

var _ core.Client = (*Client)(nil)

// ParseFeed reads a Torznab/Newznab RSS answer.
func ParseFeed(body []byte) ([]core.Release, error) {
	var feed struct {
		Items []struct {
			Title     string `xml:"title"`
			GUID      string `xml:"guid"`
			Link      string `xml:"link"`
			Comments  string `xml:"comments"`
			PubDate   string `xml:"pubDate"`
			Size      int64  `xml:"size"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
			} `xml:"enclosure"`
			Attrs []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"attr"` // torznab:attr and newznab:attr
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("the indexer's answer couldn't be read: %w", err)
	}
	out := make([]core.Release, 0, len(feed.Items))
	for _, it := range feed.Items {
		r := core.Release{
			Title: strings.TrimSpace(it.Title), GUID: it.GUID, DetailsURL: it.Comments, Size: it.Size,
			DownloadFactor: 1, UploadFactor: 1, Categories: []int{},
		}
		dl := firstNonEmpty(it.Enclosure.URL, it.Link)
		if strings.HasPrefix(dl, "magnet:") {
			r.MagnetURL = dl
		} else {
			r.DownloadURL = dl
		}
		if r.Size == 0 {
			r.Size = it.Enclosure.Length
		}
		if t, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
			r.PublishDate = t
		} else if t, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
			r.PublishDate = t
		}
		peers := -1
		for _, a := range it.Attrs {
			v := a.Value
			n, _ := strconv.Atoi(v)
			f, _ := strconv.ParseFloat(v, 64)
			switch strings.ToLower(a.Name) {
			case "size":
				if s, err := strconv.ParseInt(v, 10, 64); err == nil {
					r.Size = s
				}
			case "seeders":
				r.Seeders = n
			case "peers":
				peers = n
			case "leechers":
				r.Leechers = n
			case "infohash":
				r.InfoHash = strings.ToLower(v)
			case "magneturl":
				r.MagnetURL = v
			case "category":
				if n > 0 {
					r.Categories = append(r.Categories, n)
				}
			case "files":
				r.Files = n
			case "grabs":
				r.Grabs = n
			case "imdb", "imdbid":
				r.IMDbID, _ = strconv.Atoi(strings.TrimPrefix(v, "tt"))
			case "tmdbid":
				r.TMDBID = n
			case "tvdbid":
				r.TVDBID = n
			case "downloadvolumefactor":
				r.DownloadFactor = f
			case "uploadvolumefactor":
				r.UploadFactor = f
			case "minimumratio":
				r.MinimumRatio = f
			case "minimumseedtime":
				r.MinimumSeedTime = int64(n)
			case "year":
				r.Year = n
			}
		}
		if peers >= 0 && r.Leechers == 0 {
			r.Leechers = max(peers-r.Seeders, 0)
		}
		if r.GUID == "" {
			r.GUID = firstNonEmpty(r.DetailsURL, r.DownloadURL, r.MagnetURL)
		}
		if r.Title != "" && (r.DownloadURL != "" || r.MagnetURL != "") {
			out = append(out, r)
		}
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Session is the connection the indexer uses (cookies, request spacing,
// solver), so a release's .torrent can be fetched the same way.
func (c *Client) Session() core.HTTP { return c.HTTP }
