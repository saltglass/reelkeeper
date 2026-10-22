package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// linkClient is an indexer client whose releases all download from one
// link, through a plain session.
type linkClient struct {
	link string
	sess *session
}

func (c linkClient) Search(context.Context, core.Query) ([]core.Release, error) { return nil, nil }
func (c linkClient) Test(context.Context) error                                 { return nil }
func (c linkClient) DownloadLink(context.Context, core.Release) (string, error) { return c.link, nil }
func (c linkClient) Session() core.HTTP                                         { return c.sess }

const testNZB = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="x" date="1" subject="&quot;a.mkv&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="10" number="1">abc@test</segment></segments>
  </file>
</nzb>`

func TestFetchNZB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nzb":
			_, _ = w.Write([]byte(testNZB))
		case "/limit": // a Newznab error instead of the file
			_, _ = w.Write([]byte(`<?xml version="1.0"?><error code="429" description="Request limit reached"/>`))
		case "/torrent":
			_, _ = w.Write([]byte("d4:infod4:name4:testee"))
		case "/magnet":
			http.Redirect(w, r, "magnet:?xt=urn:btih:abc", http.StatusFound)
		}
	}))
	defer srv.Close()
	get := func(path string) (Fetched, error) {
		return fetch(context.Background(), linkClient{link: srv.URL + path, sess: newSession(0, "test", nil)}, core.Release{}, true)
	}
	if f, err := get("/nzb"); err != nil || string(f.NZB) != testNZB || f.Torrent != nil || f.Magnet != "" {
		t.Fatalf("nzb = %+v, %v", f, err)
	}
	if _, err := get("/limit"); err == nil || !strings.Contains(err.Error(), "Request limit reached") {
		t.Errorf("Newznab error: %v", err)
	}
	if _, err := get("/torrent"); err == nil || !strings.Contains(err.Error(), "something other than an .nzb") {
		t.Errorf("a torrent from a Usenet indexer: %v", err)
	}
	if _, err := get("/magnet"); err == nil {
		t.Error("a Usenet indexer redirecting to a magnet link was accepted")
	}
	// A torrent indexer's fetch is unchanged.
	f, err := fetch(context.Background(), linkClient{link: srv.URL + "/torrent", sess: newSession(0, "test", nil)}, core.Release{}, false)
	if err != nil || string(f.Torrent) != "d4:infod4:name4:testee" || f.NZB != nil {
		t.Errorf("torrent = %+v, %v", f, err)
	}
}

func TestIsNZB(t *testing.T) {
	for _, c := range []struct {
		data string
		want bool
	}{
		{testNZB, true},
		{"<nzb>\n<file/></nzb>", true},
		{"<!-- made by x -->\n<nzb xmlns='y'></nzb>", true},
		{"<html><body>Please log in. Use an <nzb> file.</body></html>", false},
		{"<nzbfoo/>", false},
		{"d4:infod4:name4:testee", false},
		{"", false},
	} {
		if got := IsNZB([]byte(c.data)); got != c.want {
			t.Errorf("IsNZB(%.40q) = %v", c.data, got)
		}
	}
}

func TestProtocol(t *testing.T) {
	for kind, want := range map[string]string{KindNewznab: ProtocolUsenet, KindTorznab: ProtocolTorrent, KindCardigann: ProtocolTorrent, KindKnaben: ProtocolTorrent} {
		if got := (&Indexer{Kind: kind}).Protocol(); got != want {
			t.Errorf("%s: %s", kind, got)
		}
	}
	if (Release{}).Usenet() || !(Release{Protocol: ProtocolUsenet}).Usenet() {
		t.Error("Release.Usenet")
	}
}

// Usenet indexers are searched only while a Usenet client is set up, and
// their releases say they're Usenet.
func TestSearchSkipsUsenetWithoutAClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		var asked atomic.Int32
		feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked.Add(1)
			if r.URL.Query().Get("t") == "caps" {
				_, _ = w.Write([]byte(`<caps><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q"/></searching>` +
					`<categories><category id="2000" name="Movies"/></categories></caps>`))
				return
			}
			_, _ = w.Write([]byte(`<rss><channel><item><title>Heat.1995.1080p.WEB-DL-GRP</title><guid>g1</guid>` +
				`<enclosure url="http://x/get/1" length="100" type="application/x-nzb"/></item></channel></rss>`))
		}))
		defer feed.Close()
		if _, err := f.svc.Create(ctx, Input{Name: "Usenet", Kind: KindNewznab, BaseURL: feed.URL, Enabled: true,
			Settings: map[string]string{"apiKey": "k"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Create(ctx, f.input("Tracker")); err != nil {
			t.Fatal(err)
		}
		q := core.Query{Type: "movie-search", Keywords: "Heat", Categories: []int{2000}}

		on := false
		f.svc.SetSearchable(func(p string) bool { return p == ProtocolTorrent || on })
		res, _, err := f.svc.Search(ctx, q, SearchOptions{})
		if err != nil || len(res) != 1 || res[0].Indexer != "Tracker" || res[0].Usenet() || asked.Load() != 0 {
			t.Fatalf("without a Usenet client: %+v, %v (feed asked %d times)", res, err, asked.Load())
		}
		if f.svc.Searchable(ProtocolUsenet) || !f.svc.Searchable(ProtocolTorrent) {
			t.Error("Searchable")
		}

		on = true
		res, _, err = f.svc.Search(ctx, q, SearchOptions{})
		if err != nil || len(res) != 2 {
			t.Fatalf("with a Usenet client: %+v, %v", res, err)
		}
		for _, r := range res {
			if r.Usenet() != (r.Indexer == "Usenet") {
				t.Errorf("%s: protocol %q", r.Indexer, r.Protocol)
			}
		}
	})
}
