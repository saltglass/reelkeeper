package download

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// The parts of the qBittorrent client that moving a torrent to or from
// another client uses.

const qbHash = "abcdef0123456789abcdef0123456789abcdef01"

func qbClientWith(t *testing.T, f *fakeQbit) *QBittorrent {
	t.Helper()
	return &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
}

func TestQbitReadsWhatAMoveNeeds(t *testing.T) {
	f := newFakeQbit(t)
	f.torrents = append(f.torrents, map[string]any{
		"hash": qbHash, "name": "Film", "category": "reelkeeper-movies", "state": "stoppedDL", "progress": 0.5, "size": 1000,
		"amount_left": 500, "completed": 480, "uploaded": 123, "tags": "reelkeeper, Tracker",
		"save_path": "/data/final", "content_path": "/data/temp/Film", "download_path": "/data/temp",
	})
	f.fileLists = map[string][]map[string]any{qbHash: {{"name": "Film/a.mkv", "size": 600}, {"name": "Film/b.srt", "size": 400}}}
	q := qbClientWith(t, f)
	ctx := context.Background()
	ts, err := q.Torrents(ctx, "reelkeeper-movies")
	if err != nil || len(ts) != 1 {
		t.Fatalf("%v %v", ts, err)
	}
	if tr := ts[0]; tr.Verified != 480 || tr.Uploaded != 123 || !slices.Equal(tr.Tags, []string{"reelkeeper", "Tracker"}) || !tr.Stopped() || tr.State != torrent.Paused {
		t.Errorf("torrent = %+v", tr)
	}
	lay, err := q.Layout(ctx, "reelkeeper-movies", qbHash)
	if err != nil {
		t.Fatal(err)
	}
	want := Layout{Folder: "/data/final", Look: []string{"/data/temp", "/data/final"}, Files: []File{{"Film/a.mkv", 600}, {"Film/b.srt", 400}}}
	if !reflect.DeepEqual(lay, want) {
		t.Errorf("layout:\n got %+v\nwant %+v", lay, want)
	}
	if err := q.Recheck(ctx, "reelkeeper-movies", qbHash); err != nil || !slices.Equal(f.rechecked, []string{qbHash}) {
		t.Errorf("recheck: %v %v", err, f.rechecked)
	}
	if err := q.SetFolder(ctx, "reelkeeper-movies", qbHash, "/x"); err != nil {
		t.Errorf("SetFolder: %v", err)
	}
	// Another app's torrent is never read for a move.
	f.torrents = append(f.torrents, map[string]any{"hash": "1111", "name": "x", "category": "radarr"})
	for name, call := range map[string]func() error{
		"Source":  func() error { _, err := q.Source(ctx, "reelkeeper-movies", "1111"); return err },
		"Layout":  func() error { _, err := q.Layout(ctx, "reelkeeper-movies", "1111"); return err },
		"Recheck": func() error { return q.Recheck(ctx, "reelkeeper-movies", "1111") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "won't change it") {
			t.Errorf("%s on someone else's torrent: %v", name, err)
		}
	}
	if len(f.rechecked) != 1 {
		t.Errorf("rechecked another app's torrent: %v", f.rechecked)
	}
}

func TestQbitSource(t *testing.T) {
	ctx := context.Background()
	file := testTorrentFile(t, "a.mkv", someData("a", 40000))
	hash, _ := InfoHash(file)
	f := newFakeQbit(t)
	f.torrents = append(f.torrents,
		map[string]any{"hash": hash, "name": "a.mkv", "category": "reelkeeper-movies"},
		map[string]any{"hash": qbHash, "name": "Fetching", "category": "reelkeeper-movies", "magnet_uri": "magnet:?xt=urn:btih:" + qbHash + "&tr=http%3A%2F%2F127.0.0.1%3A9%2Fannounce%3Fpasskey%3Dsecret"},
	)
	f.exports = map[string][]byte{hash: file}
	q := qbClientWith(t, f)

	// A torrent with its details: the file qBittorrent exports.
	src, err := q.Source(ctx, "reelkeeper-movies", hash)
	if err != nil || !reflect.DeepEqual(src.Torrent, file) || src.Magnet != "" || src.Kind() != SourceTorrent {
		t.Errorf("export: %+v %v", src, err)
	}
	// One still fetching its details answers 409: its magnet link, trackers and all.
	src, err = q.Source(ctx, "reelkeeper-movies", qbHash)
	if err != nil || !strings.Contains(src.Magnet, "passkey%3Dsecret") || len(src.Torrent) != 0 || src.Kind() != SourceMagnet {
		t.Errorf("409: %+v %v", src, err)
	}
	// Without a magnet_uri it still gives the hash.
	delete(f.torrents[2], "magnet_uri")
	if src, _ = q.Source(ctx, "reelkeeper-movies", qbHash); !strings.HasPrefix(src.Magnet, "magnet:?xt=urn:btih:"+qbHash) {
		t.Errorf("bare magnet: %+v", src)
	}
	// A qBittorrent older than 4.5 can't export: the magnet link again.
	f.noExport = true
	if src, err = q.Source(ctx, "reelkeeper-movies", hash); err != nil || !strings.HasPrefix(src.Magnet, "magnet:?xt=urn:btih:"+hash) {
		t.Errorf("no export: %+v %v", src, err)
	}
	// A file that isn't this torrent's is refused.
	f.noExport = false
	f.exports[hash] = testTorrentFile(t, "other.mkv", someData("other", 40000))
	if _, err = q.Source(ctx, "reelkeeper-movies", hash); err == nil {
		t.Error("took another torrent's file")
	}
	var ns *NoSourceError
	if !errors.As(err, &ns) || !errors.Is(err, ErrNoSource) {
		t.Errorf("error = %v", err)
	}
}

// A torrent moving here is added with explicit folders; one added any other
// time isn't.
func TestQbitAddWithFilesIn(t *testing.T) {
	ctx := context.Background()
	f := newFakeQbit(t)
	q := qbClientWith(t, f)
	m1 := "magnet:?xt=urn:btih:" + qbHash
	if _, err := q.Add(ctx, Add{Magnet: m1, Category: "reelkeeper-movies", Stopped: true, SavePath: "/data/final", FilesIn: "/data/temp"}); err != nil {
		t.Fatal(err)
	}
	a := f.added[0]
	if a["savepath"] != "/data/final" || a["downloadPath"] != "/data/temp" || a["useDownloadPath"] != "true" || a["autoTMM"] != "false" || a["stopped"] != "true" {
		t.Errorf("files elsewhere: %v", a)
	}
	m2 := "magnet:?xt=urn:btih:" + strings.Repeat("12", 20)
	if _, err := q.Add(ctx, Add{Magnet: m2, Category: "reelkeeper-movies", Stopped: true, SavePath: "/data/final", FilesIn: "/data/final"}); err != nil {
		t.Fatal(err)
	}
	a = f.added[1]
	if a["savepath"] != "/data/final" || a["useDownloadPath"] != "false" || a["autoTMM"] != "false" {
		t.Errorf("files in the final folder: %v", a)
	}
	if _, ok := a["downloadPath"]; ok {
		t.Errorf("a download path was sent: %v", a)
	}
	m3 := "magnet:?xt=urn:btih:" + strings.Repeat("34", 20)
	if _, err := q.Add(ctx, Add{Magnet: m3, Category: "reelkeeper-movies", SavePath: "/data/final"}); err != nil {
		t.Fatal(err)
	}
	if a = f.added[2]; a["useDownloadPath"] != "" || a["autoTMM"] != "" {
		t.Errorf("an ordinary grab got move-only fields: %v", a)
	}
}

// ---- Transmission

func tmTorrent(hash string, labels ...string) map[string]any {
	return map[string]any{"hashString": hash, "name": "Film", "labels": labels, "status": 0, "percentDone": 0.5, "downloadDir": "/data/final",
		"haveValid": 480, "uploadedEver": 77, "sizeWhenDone": 1000, "leftUntilDone": 500, "metadataPercentComplete": 1.0}
}

func TestTransmissionReadsWhatAMoveNeeds(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	row := tmTorrent(tmA, "reelkeeper-movies", "reelkeeper", "Tracker")
	row["files"] = []map[string]any{{"name": "Film/a.mkv", "length": 600}, {"name": "Film/b.srt", "length": 400}}
	row["isPrivate"] = true
	row["magnetLink"] = "magnet:?xt=urn:btih:" + tmA
	f.torrents = append(f.torrents, row)
	f.sessionExtra = map[string]any{"incomplete-dir-enabled": true, "incomplete-dir": "/data/inc", "download-dir": "/data/final"}
	tr := f.client()

	ts, err := tr.Torrents(ctx, "reelkeeper-movies")
	if err != nil || len(ts) != 1 || ts[0].Verified != 480 || ts[0].Uploaded != 77 || !ts[0].Stopped() {
		t.Fatalf("torrents = %+v, %v", ts, err)
	}
	lay, err := tr.Layout(ctx, "reelkeeper-movies", tmA)
	want := Layout{Folder: "/data/final", Look: []string{"/data/final", "/data/inc"}, Files: []File{{"Film/a.mkv", 600}, {"Film/b.srt", 400}}}
	if err != nil || !reflect.DeepEqual(lay, want) {
		t.Errorf("layout:\n got %+v (%v)\nwant %+v", lay, err, want)
	}
	// The incomplete folder only counts when it's used.
	f.sessionExtra["incomplete-dir-enabled"] = false
	if lay, _ = tr.Layout(ctx, "reelkeeper-movies", tmA); !slices.Equal(lay.Look, []string{"/data/final"}) {
		t.Errorf("look = %v", lay.Look)
	}

	// A private torrent can't be handed over; a public one gives its magnet link.
	_, err = tr.Source(ctx, "reelkeeper-movies", tmA)
	var ns *NoSourceError
	if !errors.As(err, &ns) || !strings.Contains(ns.Reason, "private torrent") || !strings.Contains(ns.Reason, "before Reelkeeper 1.3") {
		t.Errorf("private: %v", err)
	}
	row["isPrivate"] = false
	src, err := tr.Source(ctx, "reelkeeper-movies", tmA)
	if err != nil || src.Magnet != "magnet:?xt=urn:btih:"+tmA {
		t.Errorf("public: %+v %v", src, err)
	}

	// Check, and put the files in their folder (only when they aren't there).
	if err := tr.Recheck(ctx, "reelkeeper-movies", tmA); err != nil {
		t.Fatal(err)
	}
	if got := f.calledWith("torrent-verify"); len(got) != 1 || !reflect.DeepEqual(got[0]["ids"], []any{tmA}) {
		t.Errorf("verify calls = %v", got)
	}
	if err := tr.SetFolder(ctx, "reelkeeper-movies", tmA, "/data/final"); err != nil || len(f.calledWith("torrent-set-location")) != 0 {
		t.Errorf("already there: %v %v", err, f.calledWith("torrent-set-location"))
	}
	if err := tr.SetFolder(ctx, "reelkeeper-movies", tmA, "/data/other"); err != nil {
		t.Fatal(err)
	}
	loc := f.calledWith("torrent-set-location")
	if len(loc) != 1 || loc[0]["location"] != "/data/other" || loc[0]["move"] != true {
		t.Errorf("set-location = %v", loc)
	}
	// Another app's torrent is never touched.
	for name, call := range map[string]func() error{
		"Source":    func() error { _, err := tr.Source(ctx, "reelkeeper-movies", strings.Repeat("f", 40)); return err },
		"Layout":    func() error { _, err := tr.Layout(ctx, "reelkeeper-movies", strings.Repeat("f", 40)); return err },
		"Recheck":   func() error { return tr.Recheck(ctx, "reelkeeper-movies", strings.Repeat("f", 40)) },
		"SetFolder": func() error { return tr.SetFolder(ctx, "reelkeeper-movies", strings.Repeat("f", 40), "/x") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "won't change it") {
			t.Errorf("%s on someone else's torrent: %v", name, err)
		}
	}
	if len(f.calledWith("torrent-verify")) != 1 || len(f.calledWith("torrent-set-location")) != 1 {
		t.Error("another app's torrent was checked or moved")
	}
}

// A torrent moving here is added to the folder its files are in now.
func TestTransmissionAddWithFilesIn(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	tr := f.client()
	if _, err := tr.Add(ctx, Add{Magnet: "magnet:?xt=urn:btih:" + tmA, Category: "reelkeeper-movies", Stopped: true, SavePath: "/data/final", FilesIn: "/data/temp"}); err != nil {
		t.Fatal(err)
	}
	add := f.calledWith("torrent-add")[0]
	if add["download-dir"] != "/data/temp" || add["paused"] != true {
		t.Errorf("add = %v", add)
	}
}

func TestUnreachableClientsMatchErrUnreachable(t *testing.T) {
	ctx := context.Background()
	u := "http://127.0.0.1:1"
	if _, err := (&QBittorrent{URL: u, Username: "a", Password: "b"}).Version(ctx); !errors.Is(err, ErrUnreachable) || !strings.HasPrefix(err.Error(), "can't reach qBittorrent: ") {
		t.Errorf("qBittorrent: %v", err)
	}
	if _, err := (&Transmission{URL: u}).Version(ctx); !errors.Is(err, ErrUnreachable) || !strings.HasPrefix(err.Error(), "can't reach Transmission: ") {
		t.Errorf("Transmission: %v", err)
	}
	if !unreachable(ErrLogin) || unreachable(errors.New("refused")) {
		t.Error("unreachable() is wrong")
	}
}
