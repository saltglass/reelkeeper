package newznab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/core/coretest"
)

const capsXML = `<?xml version="1.0"?><caps><searching>
<search available="yes" supportedParams="q"/>
<tv-search available="yes" supportedParams="q,season,ep,tvdbid"/>
<movie-search available="yes" supportedParams="q,imdbid"/>
</searching><categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/></category><category id="5000" name="TV"/></categories></caps>`

const feedXML = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Heat 1995 1080p BluRay x264-GRP</title><guid>https://t/details/1</guid><comments>https://t/details/1</comments>
<pubDate>Fri, 15 Mar 2024 20:30:00 +0000</pubDate><enclosure url="https://t/dl/1.torrent" length="8000000000" type="application/x-bittorrent"/>
<torznab:attr name="category" value="2040"/><torznab:attr name="seeders" value="12"/><torznab:attr name="peers" value="15"/>
<torznab:attr name="infohash" value="ABCDEF"/><torznab:attr name="downloadvolumefactor" value="0"/><torznab:attr name="imdbid" value="0113277"/>
<torznab:attr name="minimumratio" value="1"/><torznab:attr name="minimumseedtime" value="172800"/></item>
<item><title>No link at all</title></item>
</channel></rss>`

func TestTorznab(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "k" {
			fmt.Fprint(w, `<error code="100" description="Incorrect user credentials"/>`)
			return
		}
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, capsXML)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		fmt.Fprint(w, feedXML)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL + "/torznab/1", APIKey: "k", HTTP: coretest.New()}
	ctx := context.Background()

	res, err := c.Search(ctx, core.Query{Type: "movie-search", Keywords: "Heat", Year: 1995, IMDbID: "tt0113277", Categories: []int{2000}})
	if err != nil {
		t.Fatal(err)
	}
	// IMDb ID used instead of the title; categories passed on.
	if !strings.Contains(queries[0], "imdbid=0113277") || strings.Contains(queries[0], "q=") || !strings.Contains(queries[0], "cat=2000") || !strings.Contains(queries[0], "t=movie") {
		t.Errorf("query = %s", queries[0])
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Title != "Heat 1995 1080p BluRay x264-GRP" || r.Size != 8000000000 || r.Seeders != 12 || r.Leechers != 3 || r.InfoHash != "abcdef" ||
		r.DownloadFactor != 0 || r.IMDbID != 113277 || r.MinimumRatio != 1 || r.MinimumSeedTime != 172800 || r.Categories[0] != 2040 ||
		r.DownloadURL != "https://t/dl/1.torrent" || r.DetailsURL != "https://t/details/1" || r.PublishDate.Year() != 2024 {
		t.Errorf("release = %+v", r)
	}

	// TV by TVDB ID with season and episode.
	if _, err := c.Search(ctx, core.Query{Type: "tv-search", Keywords: "Show", TVDBID: 81189, Season: 2, Episode: "5"}); err != nil {
		t.Fatal(err)
	}
	if q := queries[1]; !strings.Contains(q, "t=tvsearch") || !strings.Contains(q, "tvdbid=81189") || !strings.Contains(q, "season=2") || !strings.Contains(q, "ep=5") {
		t.Errorf("tv query = %s", q)
	}

	if err := c.Test(ctx); err != nil {
		t.Errorf("test: %v", err)
	}
	bad := &Client{URL: srv.URL, APIKey: "wrong", HTTP: coretest.New()}
	if err := bad.Test(ctx); err == nil || !strings.Contains(err.Error(), "Incorrect user credentials") {
		t.Errorf("bad key: %v", err)
	}
}
