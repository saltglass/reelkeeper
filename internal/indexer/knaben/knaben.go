// Package knaben searches Knaben (knaben.org), a public search engine that
// collects releases from many public trackers, through its JSON API.
package knaben

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/newznab"
)

// DefaultAPI is Knaben's search endpoint.
const DefaultAPI = "https://api.knaben.org/v1"

// Client searches Knaben.
type Client struct {
	API  string // empty means DefaultAPI
	HTTP core.HTTP
}

// Knaben's categories and the standard ones they mean (its IDs are the
// standard ones times a thousand, roughly, but not exactly).
var categories = map[int]int{
	1000000: newznab.ID("Audio"), 1001000: newznab.ID("Audio/MP3"), 1002000: newznab.ID("Audio/Lossless"),
	1003000: newznab.ID("Audio/Audiobook"), 1004000: newznab.ID("Audio/Video"), 1005000: newznab.ID("Audio/Other"),
	1006000: newznab.ID("Audio/Other"),
	2000000: newznab.ID("TV"), 2001000: newznab.ID("TV/HD"), 2002000: newznab.ID("TV/SD"), 2003000: newznab.ID("TV/UHD"),
	2004000: newznab.ID("TV/Documentary"), 2005000: newznab.ID("TV/Foreign"), 2006000: newznab.ID("TV/Sport"),
	2007000: newznab.ID("TV/Other"), 2008000: newznab.ID("TV/Other"),
	3000000: newznab.ID("Movies"), 3001000: newznab.ID("Movies/HD"), 3002000: newznab.ID("Movies/SD"),
	3003000: newznab.ID("Movies/UHD"), 3004000: newznab.ID("Movies/DVD"), 3005000: newznab.ID("Movies/Foreign"),
	3006000: newznab.ID("Movies/Foreign"), 3007000: newznab.ID("Movies/3D"), 3008000: newznab.ID("Movies/Other"),
	6000000: newznab.ID("TV/Anime"), 6001000: newznab.ID("TV/Anime"), 6002000: newznab.ID("TV/Anime"),
	6003000: newznab.ID("TV/Anime"), 6004000: newznab.ID("TV/Anime"), 6008000: newznab.ID("TV/Anime"),
	10000000: newznab.ID("Other"), 10001000: newznab.ID("Other/Misc"),
}

// knabenCats lists Knaben's categories for the wanted standard ones.
func knabenCats(want []int) []int {
	var out []int
	for k, std := range categories {
		if newznab.Within(std, want) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

type hit struct {
	Title      string `json:"title"`
	CategoryID []int  `json:"categoryId"`
	Hash       string `json:"hash"`
	Details    string `json:"details"`
	Link       string `json:"link"`
	MagnetURL  string `json:"magnetUrl"`
	Bytes      int64  `json:"bytes"`
	Seeders    int    `json:"seeders"`
	Peers      int    `json:"peers"`
	Date       string `json:"date"`
}

// Search runs a search. Knaben searches titles, so an episode search adds
// "S01E02" to the title.
func (c *Client) Search(ctx context.Context, q core.Query) ([]core.Release, error) {
	body := map[string]any{
		"order_by": "date", "order_direction": "desc", "from": 0, "size": 100, "hide_unsafe": true,
	}
	terms := strings.TrimSpace(q.Keywords)
	if terms != "" {
		if q.Type == "movie-search" && q.Year > 0 {
			terms += fmt.Sprintf(" %d", q.Year)
		}
		if q.Season > 0 {
			terms += fmt.Sprintf(" S%02d", q.Season)
			if q.Episode != "" {
				var n int
				if _, err := fmt.Sscanf(q.Episode, "%d", &n); err == nil {
					terms += fmt.Sprintf("E%02d", n)
				}
			}
		}
		body["search_type"], body["search_field"], body["query"] = "100%", "title", terms
	}
	if cats := knabenCats(q.Categories); len(q.Categories) > 0 && len(cats) > 0 {
		body["categories"] = cats
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Knaben API answered HTTP %d", resp.StatusCode)
	}
	return parse(data)
}

var hasZone = regexp.MustCompile(`[+-]\d{2}:\d{2}$|Z$`)

func parse(data []byte) ([]core.Release, error) {
	var out struct {
		Hits []hit `json:"hits"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("the Knaben API's answer couldn't be read: %w", err)
	}
	releases := make([]core.Release, 0, len(out.Hits))
	for _, h := range out.Hits {
		if h.Seeders <= 0 { // dead releases (Prowlarr skips them too)
			continue
		}
		r := core.Release{
			Title: h.Title, GUID: h.Details, DetailsURL: h.Details, DownloadURL: h.Link, MagnetURL: h.MagnetURL,
			InfoHash: strings.ToLower(h.Hash), Size: h.Bytes, Seeders: h.Seeders, Leechers: max(h.Peers, 0),
			DownloadFactor: 0, UploadFactor: 1, Categories: []int{}, // public trackers: nothing counts against a ratio
		}
		if r.GUID == "" {
			r.GUID = firstNonEmpty(r.MagnetURL, r.InfoHash)
		}
		for _, c := range h.CategoryID {
			if std, ok := categories[c]; ok && std != 0 && !slices.Contains(r.Categories, std) {
				r.Categories = append(r.Categories, std)
			}
		}
		// Knaben's dates sometimes lack a time zone; they're Central
		// European (Prowlarr assumes +01:00 too).
		date := h.Date
		if date != "" && !hasZone.MatchString(date) {
			date += "+01:00"
		}
		if t, err := time.Parse(time.RFC3339, date); err == nil {
			r.PublishDate = t.UTC()
		}
		if r.Title != "" && (r.DownloadURL != "" || r.MagnetURL != "") {
			releases = append(releases, r)
		}
	}
	return releases, nil
}

// Test checks Knaben answers with releases.
func (c *Client) Test(ctx context.Context) error {
	res, err := c.Search(ctx, core.Query{Type: "search"})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("the Knaben API answered but listed no releases")
	}
	return nil
}

// DownloadLink returns the link Knaben gave with the release.
func (c *Client) DownloadLink(_ context.Context, r core.Release) (string, error) {
	if l := firstNonEmpty(r.MagnetURL, r.DownloadURL); l != "" {
		return l, nil
	}
	return "", errors.New("the Knaben API gave no download link")
}

var _ core.Client = (*Client)(nil)

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
