package download

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// torrentFetcher hands out a .torrent file instead of a magnet link.
type torrentFetcher struct {
	fakeFetcher
	file []byte
}

func (f torrentFetcher) Fetch(context.Context, indexer.Release) (indexer.Fetched, error) {
	return indexer.Fetched{Torrent: f.file}, nil
}

// grabRelease grabs the fixture's movie, as f.grab does, but answers the error.
func (f *dfix) grabRelease(hash string) error {
	return f.svc.Grab(context.Background(), grab.Grabbed{
		Release: indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-GRP", InfoHash: hash, Size: 8 << 30},
			IndexerID: f.ixID, Indexer: "Tracker"},
		Version: f.mv, Quality: "Bluray-1080p", Score: 10,
	})
}

func TestTorrentFiles(t *testing.T) {
	single := testTorrentFile(t, "Ronin.1998.1080p.mkv", []byte("video"))
	files, err := TorrentFiles(single)
	if err != nil || len(files) != 1 || files[0].Name != "Ronin.1998.1080p.mkv" || files[0].Size != 5 {
		t.Fatalf("single file = %+v, %v", files, err)
	}
	multi := testMultiTorrentFile(t, "Ronin", []tfile{
		{"Ronin/Ronin.mkv", []byte("video")}, {"Ronin/Subs/en.srt", []byte("subs!!")},
	})
	files, err = TorrentFiles(multi)
	want := []File{{"Ronin/Ronin.mkv", 5}, {"Ronin/Subs/en.srt", 6}}
	if err != nil || !slices.Equal(files, want) {
		t.Fatalf("several files = %+v, %v; want %+v", files, err, want)
	}
	if _, err := TorrentFiles([]byte("<html>not a torrent</html>")); err == nil {
		t.Error("a page that isn't a torrent was read as one")
	}
}

// A .torrent whose files are a program is refused before the download
// client sees it, and blocklisted for good with the reason.
func TestGrabRefusesATorrentWithAProgram(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		fake := testTorrentFile(t, "Ronin.1998.1080p.BluRay.x264-GRP.mkv.exe", []byte("MZ not a movie"))
		f.svc.idx = torrentFetcher{fakeFetcher: fakeFetcher{ratio: 1}, file: fake}

		err := f.grabRelease("")
		if !errors.Is(err, grab.ErrBadFiles) || !strings.Contains(err.Error(), "a Windows program") {
			t.Fatalf("Grab = %v, want ErrBadFiles naming the program", err)
		}
		if len(f.mem.torrents) != 0 || f.mem.added != nil {
			t.Error("the torrent reached the download client")
		}
		if n, _ := db.DB.NewSelect().Model((*Download)(nil)).Count(ctx); n != 0 {
			t.Errorf("%d downloads recorded, want none", n)
		}
		var b BlockedRelease
		if err := db.DB.NewSelect().Model(&b).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		hash, _ := InfoHash(fake)
		if b.Rule != BlockBadFiles || b.Hash != hash || b.ExpiresAt != nil || b.TitleID != f.mv.Title.ID ||
			!strings.Contains(b.Reason, "Ronin.1998.1080p.BluRay.x264-GRP.mkv.exe") {
			t.Errorf("blocklist entry = %+v", b)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked("Ronin.1998.1080p.BluRay.x264-GRP", "") {
			t.Error("the release isn't blocked for its title")
		}
	})
}

// A .torrent with only an archive and a "password" file has nothing the
// import could take, so it's refused too.
func TestGrabRefusesATorrentWithNoVideo(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newDFix(t, db)
		f.svc.idx = torrentFetcher{file: testMultiTorrentFile(t, "Ronin", []tfile{
			{"Ronin/Ronin.1998.1080p.zip", []byte("PK")}, {"Ronin/Password.txt", []byte("visit")},
		})}
		if err := f.grabRelease(""); !errors.Is(err, grab.ErrBadFiles) || !strings.Contains(err.Error(), "no movie or episode file") {
			t.Fatalf("Grab = %v, want ErrBadFiles for no video", err)
		}
		if len(f.mem.torrents) != 0 {
			t.Error("the torrent reached the download client")
		}
	})
}

// A good .torrent goes through, and its files count as checked: "Check
// downloads" doesn't ask the client for them again.
func TestGrabSendsAGoodTorrentAndChecksItOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		good := testMultiTorrentFile(t, "Ronin", []tfile{
			{"Ronin/Ronin.1998.1080p.mkv", []byte("video")}, {"Ronin/Ronin.nfo", []byte("nfo")},
		})
		f.svc.idx = torrentFetcher{fakeFetcher: fakeFetcher{ratio: 1}, file: good}
		if err := f.grabRelease(""); err != nil {
			t.Fatal(err)
		}
		hash, _ := InfoHash(good)
		if f.mem.torrents[hash] == nil {
			t.Fatal("the torrent wasn't sent")
		}
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if f.mem.layouts != 0 {
			t.Errorf("Check listed the files of a torrent already checked (%d calls)", f.mem.layouts)
		}
	})
}

// A magnet link's files are checked by "Check downloads" once the client
// has them: a bad one is removed with its data, blocklisted for good, and
// searched for again.
func TestCheckRemovesAMagnetWithBadFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		var searched []int64
		f.svc.searchAgain = func(_ context.Context, versionID int64) error {
			searched = append(searched, versionID)
			return nil
		}
		f.grab(t, hashA)

		// Still fetching its details: nothing to check, nothing asked.
		f.mem.setState(hashA, torrent.Metadata)
		if msg, _, err := f.svc.Check(ctx); err != nil || strings.Contains(msg, "bad files") || f.mem.layouts != 0 {
			t.Fatalf("Check while fetching details = %q, %v (%d layouts)", msg, err, f.mem.layouts)
		}
		// Details arrived but the client lists no files yet: tried again later.
		f.mem.setState(hashA, torrent.Downloading)
		if msg, _, _ := f.svc.Check(ctx); strings.Contains(msg, "bad files") || f.mem.layouts != 1 {
			t.Fatalf("Check with no files yet = %q (%d layouts)", msg, f.mem.layouts)
		}
		f.mem.files = map[string][]File{hashA: {
			{"Ronin.1998.1080p/Ronin.1998.1080p.mkv", 2 << 30}, {"Ronin.1998.1080p/codec/Setup.exe", 3 << 20},
		}}
		msg, quiet, err := f.svc.Check(ctx)
		if err != nil || quiet || !strings.Contains(msg, "1 removed for bad files") {
			t.Fatalf("Check = %q, quiet %v, %v", msg, quiet, err)
		}
		if !slices.Contains(f.mem.removed, hashA) || f.mem.torrents[hashA] != nil {
			t.Error("the torrent is still in the client")
		}
		var d Download
		if err := db.DB.NewSelect().Model(&d).Where("hash = ?", hashA).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if d.Status != StatusRemoved || !strings.Contains(d.Message, "Setup.exe, a Windows program") || !strings.Contains(d.Message, "Searching for another") {
			t.Errorf("download = %s: %q", d.Status, d.Message)
		}
		var b BlockedRelease
		if err := db.DB.NewSelect().Model(&b).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if b.Rule != BlockBadFiles || b.Hash != hashA || b.ExpiresAt != nil {
			t.Errorf("blocklist entry = %+v", b)
		}
		if !slices.Equal(searched, []int64{f.mv.Version.ID}) {
			t.Errorf("searched again for %v, want the version", searched)
		}
	})
}

// A magnet whose files are fine is left alone, and its files are listed only
// once.
func TestCheckLeavesAGoodMagnetAlone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		f.mem.files = map[string][]File{hashA: {{"Ronin.1998.1080p.mkv", 2 << 30}}}
		for range 3 {
			if _, _, err := f.svc.Check(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if f.mem.torrents[hashA] == nil || len(f.mem.removed) != 0 {
			t.Error("a good download was removed")
		}
		if f.mem.layouts != 1 {
			t.Errorf("files listed %d times, want once", f.mem.layouts)
		}
	})
}
