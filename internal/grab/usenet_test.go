package grab

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func usenetRel(name string) indexer.Release {
	r := rel(name, 0)
	r.InfoHash, r.Indexer, r.IndexerID, r.Protocol = "", "NZBIndexer", 2, indexer.ProtocolUsenet
	return r
}

// A Usenet release equally good as the best torrent is chosen, and the
// search says why; Usenet releases aren't turned down for having no
// seeders, and a better torrent still wins.
func TestUsenetWinsTiesOnly(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		ronin := f.lib.versions[1].Version.ID
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 400),
			usenetRel("Ronin.1998.1080p.BluRay.x264-OTHER"),
		}
		msg, err := f.svc.SearchVersion(ctx, ronin, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "2 acceptable") || !strings.Contains(msg, "Would grab Ronin.1998.1080p.BluRay.x264-OTHER") ||
			!strings.Contains(msg, "Usenet was chosen over an equally good torrent (Ronin.1998.1080p.BluRay.x264-AMIABLE)") {
			t.Errorf("message = %q", msg)
		}
		var data EventData
		_ = json.Unmarshal([]byte(f.events(t, EventSearched)[0].Data), &data)
		if !data.Releases[0].Usenet || data.Releases[1].Usenet {
			t.Errorf("recorded releases = %+v", data.Releases)
		}

		// A better torrent beats a lesser Usenet release, without a note.
		f.idx.releases = []indexer.Release{
			usenetRel("Ronin.1998.1080p.WEB-DL.H.264-GRP"),
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 400),
		}
		msg, err = f.svc.SearchVersion(ctx, ronin, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "Would grab Ronin.1998.1080p.BluRay.x264-AMIABLE") || strings.Contains(msg, "Usenet was chosen") {
			t.Errorf("message = %q", msg)
		}
	})
}

// The same name on Usenet and on a torrent site is two downloads.
func TestDedupeKeepsBothProtocols(t *testing.T) {
	a := rel("Ronin.1998.1080p.BluRay.x264-GRP", 10)
	a.InfoHash = ""
	b := usenetRel("Ronin.1998.1080p.BluRay.x264-GRP")
	if got := dedupe([]indexer.Release{a, b, b}); len(got) != 2 {
		t.Errorf("dedupe kept %d", len(got))
	}
}
