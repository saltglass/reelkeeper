package download

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// testNZB is an .nzb in the shape posting tools write: one <file> per file,
// its name quoted in the subject, its size in its segments. A name of ""
// stands for an obfuscated post, whose subject quotes nothing.
func testNZB(names ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
 <head><meta type="name">Ronin.1998.1080p.BluRay.x264-NZB</meta></head>
`)
	for i, n := range names {
		subject := "d41d8cd98f00b204e9800998ecf8427e (1/2)"
		if n != "" {
			subject = fmt.Sprintf(`Ronin.1998.1080p.BluRay.x264-NZB [%d/%d] - &quot;%s&quot; yEnc (1/2)`, i+1, len(names), n)
		}
		fmt.Fprintf(&b, ` <file poster="poster@example.invalid" date="1700000000" subject="%s">
  <groups><group>alt.binaries.test</group></groups>
  <segments><segment bytes="700000" number="1">p%da@example.invalid</segment><segment bytes="300000" number="2">p%db@example.invalid</segment></segments>
 </file>
`, subject, i, i)
	}
	b.WriteString("</nzb>\n")
	return []byte(b.String())
}

func TestNZBFiles(t *testing.T) {
	files, err := NZBFiles(testNZB("Ronin.part01.rar", "", "Ronin.par2"))
	want := []File{{"Ronin.part01.rar", 1_000_000}, {"Ronin.par2", 1_000_000}}
	if err != nil || !slices.Equal(files, want) {
		t.Fatalf("files = %+v, %v; want %+v (the obfuscated one left out)", files, err, want)
	}
	if _, err := NZBFiles([]byte("<html><body>Not found</body>")); err == nil {
		t.Error("a page that isn't an .nzb was read as one")
	}
}

// fixedNZB hands out the same .nzb for every Usenet release.
type fixedNZB struct {
	fakeFetcher
	file []byte
}

func (f fixedNZB) Fetch(context.Context, indexer.Release) (indexer.Fetched, error) {
	return indexer.Fetched{NZB: f.file}, nil
}

func (f *ufix) grabNZB(file []byte) error {
	f.svc.idx = fixedNZB{fakeFetcher: fakeFetcher{ratio: 1.5}, file: file}
	return f.svc.Grab(context.Background(), grab.Grabbed{
		Release: indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-NZB", Size: 8 << 30},
			IndexerID: f.ixID, Indexer: "NZBIndexer", Protocol: indexer.ProtocolUsenet},
		Version: f.mv, Quality: "Bluray-1080p", Score: 10,
	})
}

// An .nzb that names a program is refused before the Usenet client sees it,
// and blocklisted for good by name (a Usenet release has no info hash).
func TestGrabRefusesAnNZBWithAProgram(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		err := f.grabNZB(testNZB("Ronin.1998.1080p.part01.rar", "Ronin.1998.1080p.par2", "Codec.Setup.exe"))
		if !errors.Is(err, grab.ErrBadFiles) || !strings.Contains(err.Error(), "Codec.Setup.exe, a Windows program") {
			t.Fatalf("Grab = %v, want ErrBadFiles naming the program", err)
		}
		if len(f.news.added) != 0 {
			t.Error("the .nzb reached the Usenet client")
		}
		var b BlockedRelease
		if err := db.DB.NewSelect().Model(&b).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if b.Rule != BlockBadFiles || b.Hash != "" || b.ExpiresAt != nil || b.TitleID != f.mv.Title.ID {
			t.Errorf("blocklist entry = %+v", b)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked("Ronin.1998.1080p.BluRay.x264-NZB", "") {
			t.Error("the release isn't blocked for its title")
		}
	})
}

// Archives, par2 files and obfuscated posts are what Usenet releases look
// like: they go through, even with no video named anywhere.
func TestGrabSendsAnNZBOfArchives(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newUFix(t, db)
		if err := f.grabNZB(testNZB("a8f3c1d07e.part01.rar", "a8f3c1d07e.part02.rar", "", "a8f3c1d07e.vol00+01.par2")); err != nil {
			t.Fatal(err)
		}
		if len(f.news.added) != 1 {
			t.Errorf("%d sent to the Usenet client, want 1", len(f.news.added))
		}
	})
}
