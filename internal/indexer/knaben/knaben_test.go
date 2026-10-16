package knaben

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/core/coretest"
)

func TestKnaben(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"hits":[
{"title":"Some Show S01E02 1080p WEB h264-GRP","categoryId":[2001000],"hash":"ABC123","details":"https://k/1","link":null,"magnetUrl":"magnet:?xt=urn:btih:ABC123","bytes":1500000000,"seeders":40,"peers":5,"date":"2024-03-15T20:30:00"},
{"title":"Dead release","categoryId":[2000000],"hash":"DEF","details":"https://k/2","magnetUrl":"magnet:?xt=urn:btih:DEF","bytes":1,"seeders":0,"peers":0,"date":"2024-03-15T20:30:00+00:00"}
]}`)
	}))
	defer srv.Close()
	c := &Client{API: srv.URL, HTTP: coretest.New()}
	res, err := c.Search(context.Background(), core.Query{Type: "tv-search", Keywords: "Some Show", Season: 1, Episode: "2", Categories: []int{5000}})
	if err != nil {
		t.Fatal(err)
	}
	if body["query"] != "Some Show S01E02" || body["search_field"] != "title" {
		t.Errorf("request = %v", body)
	}
	if cats, _ := body["categories"].([]any); len(cats) == 0 || cats[0] != float64(2000000) {
		t.Errorf("categories = %v", body["categories"])
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v (dead releases are skipped)", res)
	}
	r := res[0]
	if r.InfoHash != "abc123" || r.Seeders != 40 || r.Size != 1500000000 || r.Categories[0] != 5040 || r.DownloadFactor != 0 ||
		r.PublishDate.Format("2006-01-02T15:04") != "2024-03-15T19:30" {
		t.Errorf("release = %+v", r)
	}
}
