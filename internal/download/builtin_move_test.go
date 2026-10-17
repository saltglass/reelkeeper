package download

// Moving torrents between the built-in client and another client, with the
// real built-in engine and real files. The other client is diskClient, a stand-in
// that keeps its files in a real folder and checks them against the torrent's
// pieces the way a real client does. (The real Transmission is used by
// integration_builtin_test.go, which needs the dev stack.)

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // BitTorrent v1 pieces are hashed with SHA-1 by definition
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

type diskTorrent struct {
	Torrent
	info    metainfo.Info
	raw     []byte
	filesIn string
	paused  bool
}

// diskClient is an external client over a real folder: single-file torrents,
// plain file names, checks by hashing.
type diskClient struct {
	t       *testing.T
	tors    map[string]*diskTorrent
	deleted []string
	calls   []string
}

func newDiskClient(t *testing.T) *diskClient {
	c := &diskClient{t: t, tors: map[string]*diskTorrent{}}
	t.Cleanup(func() {
		if len(c.deleted) > 0 {
			t.Errorf("the other client was asked to delete the data of %v", c.deleted)
		}
	})
	return c
}

func (c *diskClient) Name() string                                         { return "Transmission" }
func (c *diskClient) Version(context.Context) (string, error)              { return "disk", nil }
func (c *diskClient) EnsureCategory(context.Context, string, string) error { return nil }
func (c *diskClient) MoveCategory(context.Context, string, string, []string) error {
	return errors.New("not used by a move")
}
func (c *diskClient) TrackerMessages(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (c *diskClient) TopPriority(_ context.Context, _ string, hashes []string) error {
	c.calls = append(c.calls, "top")
	return nil
}

// settle puts a torrent in its resting state from what is on disk.
func (c *diskClient) settle(t *diskTorrent) {
	switch {
	case t.paused && t.Progress >= 1:
		t.State = torrent.SeedingPaused
	case t.paused:
		t.State = torrent.Paused
	case t.Progress >= 1:
		t.State = torrent.Seeding
	default:
		t.State = torrent.Downloading
	}
}

func (c *diskClient) Add(_ context.Context, a Add) (string, error) {
	h, err := InfoHash(a.Torrent)
	if err != nil {
		return "", err
	}
	mi, err := metainfo.Load(bytes.NewReader(a.Torrent))
	if err != nil {
		return "", err
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return "", err
	}
	in := a.SavePath
	if a.FilesIn != "" {
		in = a.FilesIn
	}
	t := &diskTorrent{info: info, raw: a.Torrent, filesIn: in, paused: a.Stopped}
	t.Hash, t.Category, t.SavePath, t.Tags, t.Size, t.Left = h, a.Category, in, a.Tags, info.TotalLength(), info.TotalLength()
	c.settle(t)
	c.tors[h] = t
	return h, nil
}

func (c *diskClient) Torrents(_ context.Context, cat string) ([]Torrent, error) {
	var out []Torrent
	for _, t := range c.tors {
		if t.Category == cat {
			out = append(out, t.Torrent)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out, nil
}

func (c *diskClient) Remove(_ context.Context, _ string, hashes []string, deleteFiles bool) error {
	for _, h := range hashes {
		if deleteFiles {
			c.deleted = append(c.deleted, h)
		}
		delete(c.tors, h)
	}
	return nil
}

func (c *diskClient) NoShareLimits(_ context.Context, _ string, hashes []string) error {
	for _, h := range hashes {
		if t := c.tors[h]; t != nil {
			t.paused = false
			c.settle(t)
		}
	}
	return nil
}

func (c *diskClient) Stop(_ context.Context, _ string, hashes []string) error {
	for _, h := range hashes {
		if t := c.tors[h]; t != nil {
			t.paused = true
			c.settle(t)
		}
	}
	return nil
}

func (c *diskClient) Start(ctx context.Context, cat string, hashes []string) error {
	return c.NoShareLimits(ctx, cat, hashes)
}

func (c *diskClient) Source(_ context.Context, _, hash string) (Source, error) {
	return Source{Torrent: c.tors[hash].raw}, nil
}

func (c *diskClient) Layout(_ context.Context, _, hash string) (Layout, error) {
	t := c.tors[hash]
	return Layout{Files: []File{{Name: t.info.Name, Size: t.info.Length}}, Folder: t.SavePath, Look: []string{t.SavePath}}, nil
}

// Recheck hashes the file against the pieces, as a real client's check does.
func (c *diskClient) Recheck(_ context.Context, _, hash string) error {
	t := c.tors[hash]
	if !t.Stopped() {
		c.t.Errorf("asked to check %s while it was running", hash[:4])
	}
	b, _ := os.ReadFile(filepath.Join(t.filesIn, t.info.Name))
	var good int64
	for i := range t.info.NumPieces() {
		p := t.info.Piece(i)
		if int64(len(b)) < p.Offset()+p.Length() {
			continue
		}
		sum := sha1.Sum(b[p.Offset() : p.Offset()+p.Length()]) //nolint:gosec // see import
		if want := p.V1Hash().Value; sum == want {
			good += p.Length()
		}
	}
	t.Verified, t.Left = good, t.Size-good
	t.Progress = float64(good) / float64(t.Size)
	c.settle(t)
	return nil
}

func (c *diskClient) SetFolder(_ context.Context, _, hash, folder string) error {
	t := c.tors[hash]
	if err := os.Rename(filepath.Join(t.filesIn, t.info.Name), filepath.Join(folder, t.info.Name)); err != nil {
		return err
	}
	t.filesIn, t.SavePath = folder, folder
	return nil
}

// ---- a half-finished download on disk

const mvPieces = 13 // 200,000 bytes in 16 KiB pieces

type halfDone struct {
	raw    []byte
	hash   string
	data   []byte
	folder string
	file   string
	name   string
	// haveBytes is what the first havePieces pieces add up to.
	haveBytes int64
}

// newHalfDone writes a file of the torrent's size with only its first
// havePieces pieces right: what a client leaves behind when it is stopped
// part-way.
func newHalfDone(t *testing.T, name string, havePieces int) *halfDone {
	t.Helper()
	return newHalfDoneIn(t, t.TempDir(), name, havePieces)
}

// newHalfDoneIn is newHalfDone in a folder of the caller's choosing.
func newHalfDoneIn(t *testing.T, folder, name string, havePieces int) *halfDone {
	t.Helper()
	h := &halfDone{name: name, folder: folder}
	h.data = someData(name, 200_000)
	h.raw = testTorrentFile(t, name, h.data)
	var err error
	if h.hash, err = InfoHash(h.raw); err != nil {
		t.Fatal(err)
	}
	h.haveBytes = min(int64(havePieces)*16384, int64(len(h.data)))
	onDisk := make([]byte, len(h.data))
	copy(onDisk, h.data[:h.haveBytes])
	h.file = filepath.Join(h.folder, name)
	if err := os.WriteFile(h.file, onDisk, 0o644); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *halfDone) bytesOnDisk(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(h.file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bworld is the service with the built-in client on one side and diskClient on
// the other.
type bworld struct {
	t    *testing.T
	f    *dfix
	db   *store.Store
	host *BuiltinHost
	nw   *testNet
	disk *diskClient
	// ext is the other client: disk, unless a test swaps in a real one.
	ext   Client
	moves *memMoves
	bcfg  settings.DownloadClient
	xcfg  settings.DownloadClient
}

func newBWorld(t *testing.T, db *store.Store, folder string) *bworld {
	t.Helper()
	w := &bworld{t: t, db: db, nw: &testNet{}, disk: newDiskClient(t), moves: &memMoves{}}
	w.ext = w.disk
	w.host = newHost(t, t.TempDir(), w.nw)
	w.bcfg = settings.DownloadClient{Type: settings.ClientBuiltin, Folder: folder, ListenPort: freePort(t), MaxActiveDownloads: 3, MaxActiveSeeds: 25}
	w.xcfg = settings.DownloadClient{Type: settings.ClientTransmission, URL: "http://old", Username: "u"}
	w.f = newDFix(t, db)
	s := w.f.svc
	s.SetBuiltin(w.host)
	s.NewClient = func(c settings.DownloadClient) Client {
		if c.ClientType() == settings.ClientBuiltin {
			return &Builtin{cfg: c, host: w.host}
		}
		return w.ext
	}
	s.exists, s.rename = onDisk, os.Rename
	s.moveTiming = moveTiming{poll: time.Millisecond, begin: 20 * time.Millisecond, stop: 2 * time.Second, check: 10 * time.Second,
		metadata: time.Second, files: time.Second, giveUp: moveGiveUp}
	box, err := secrets.New("a-test-key-of-sixteen-or-more")
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecrets(box)
	s.SetMoves(w.moves)
	s.freeSpace = func(string) int64 { return -1 }
	return w
}

// download records a download in the database, as a grab would, with the
// torrent kept for a move.
func (w *bworld) download(h *halfDone, status, client string) {
	w.t.Helper()
	d := &Download{
		CreatedAt: time.Now().UTC(), Hash: h.hash, Category: settings.MovieCategory, ReleaseTitle: "Release " + h.name, Indexer: "Tracker",
		Quality: "WEBDL-1080p", Size: int64(len(h.data)), Status: status, CleanupTrack: "{}", Client: client,
		TitleID: &w.f.mv.Title.ID, VersionID: &w.f.mv.Version.ID,
	}
	if _, err := w.db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	if err := w.f.svc.storeSource(context.Background(), d.ID, Source{Torrent: h.raw}); err != nil {
		w.t.Fatal(err)
	}
}

// switchClient does what saving a different download client does (see
// mover.switchClient and the API handler): plan, begin, save the settings
// (which starts or stops the engine), and let the move start.
func (w *bworld) switchClient(from, to settings.DownloadClient, leaveSeeding bool) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.f.svc.PlanMove(ctx, from, to, leaveSeeding); err != nil {
		w.t.Fatalf("PlanMove: %v", err)
	}
	if _, err := w.f.svc.BeginMove(ctx, from, to, leaveSeeding); err != nil {
		w.t.Fatalf("BeginMove: %v", err)
	}
	w.f.cfg = to
	w.host.Apply(to, w.moves.m)
	if _, err := w.f.svc.ClientChanged(ctx, from.ClientName(), to.ClientName()); err != nil {
		w.t.Fatalf("ClientChanged: %v", err)
	}
}

func (w *bworld) run() {
	w.t.Helper()
	if err := w.f.svc.RunMove(context.Background(), &runReport{}); err != nil {
		w.t.Fatalf("RunMove: %v", err)
	}
	// The settings service tells the host when the move ends.
	w.host.Apply(w.f.cfg, w.moves.m)
}

func (w *bworld) builtinTorrent(hash string) *Torrent {
	w.t.Helper()
	c := &Builtin{cfg: w.bcfg, host: w.host}
	ts, err := c.Torrents(context.Background(), settings.MovieCategory)
	if err != nil {
		w.t.Fatal(err)
	}
	for i := range ts {
		if ts[i].Hash == hash {
			return &ts[i]
		}
	}
	return nil
}

func moveFailures(t *testing.T, f *dfix) []grab.Event {
	t.Helper()
	evs, err := f.hist.List(context.Background(), grab.ListOptions{Events: []string{grab.EventMoveFailed}})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// A half-finished download moves from another client to the built-in one:
// the files are checked where they are, the pieces it had are still there,
// nothing is downloaded again and nothing is deleted.
func TestMoveToTheBuiltinClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		pieces int
		paused bool
	}{
		{"half downloaded", StatusDownloading, 8, false},
		{"half downloaded and paused", StatusDownloading, 8, true},
		{"finished and seeding", StatusImported, mvPieces, false},
		{"nothing yet", StatusDownloading, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				h := newHalfDone(t, "Movie.mkv", tc.pieces)
				w := newBWorld(t, db, h.folder)
				before := h.bytesOnDisk(t)
				w.download(h, tc.status, settings.ClientTransmission)

				// The other client has it, stopped or running.
				if _, err := w.disk.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: h.folder, Tags: []string{"reelkeeper", "Tracker"}, Stopped: true}); err != nil {
					t.Fatal(err)
				}
				if err := w.disk.Recheck(ctx, "", h.hash); err != nil {
					t.Fatal(err)
				}
				if !tc.paused {
					_ = w.disk.Start(ctx, "", []string{h.hash})
				}
				w.f.cfg = w.xcfg
				w.switchClient(w.xcfg, w.bcfg, false)
				w.run()

				d := w.f.download(t, db, h.hash)
				if d.MoveState != "" || d.Client != settings.ClientBuiltin || d.Status != tc.status {
					t.Fatalf("download after the move = %+v", d)
				}
				if evs := moveFailures(t, w.f); len(evs) != 0 {
					t.Fatalf("the move failed: %s", evs[0].Message)
				}
				if w.disk.tors[h.hash] != nil {
					t.Error("the other client still has it")
				}
				tr := w.builtinTorrent(h.hash)
				if tr == nil {
					t.Fatal("the built-in client doesn't have it")
				}
				if tr.Verified != h.haveBytes || tr.Size != int64(len(h.data)) || tr.SavePath != h.folder || tr.Category != settings.MovieCategory {
					t.Errorf("built-in torrent = verified %d of %d at %s", tr.Verified, tr.Size, tr.SavePath)
				}
				wantState := torrent.Downloading
				switch {
				case tc.paused:
					wantState = torrent.Paused
				case tc.pieces == mvPieces:
					wantState = torrent.Seeding
				}
				if tr.State != wantState && (wantState != torrent.Downloading || tr.State != torrent.Stalled) {
					t.Errorf("state = %s, want %s", tr.State, wantState)
				}
				if tr.Done() != (tc.pieces == mvPieces) {
					t.Errorf("done = %v at %d pieces", tr.Done(), tc.pieces)
				}
				if !bytes.Equal(h.bytesOnDisk(t), before) {
					t.Error("the file on disk changed")
				}
				if w.nw.dials.Load() != 0 {
					t.Errorf("the engine opened %d connections, with nothing to download from", w.nw.dials.Load())
				}
				if tr.Uploaded != 0 {
					t.Errorf("uploaded = %d", tr.Uploaded)
				}
				// The tags came along, and the other client was never asked to delete.
				if !sliceEq(tr.Tags, []string{"reelkeeper", "Tracker"}) {
					t.Errorf("tags = %v", tr.Tags)
				}
			})
		})
	}
}

// The other way: the built-in client's torrent, running and part-way, goes to
// another client, which checks the files and finds what the built-in one had.
func TestMoveFromTheBuiltinClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		pieces int
	}{
		{"half downloaded", StatusDownloading, 8},
		{"finished and seeding", StatusImported, mvPieces},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				h := newHalfDone(t, "Movie.mkv", tc.pieces)
				w := newBWorld(t, db, h.folder)
				before := h.bytesOnDisk(t)
				w.f.cfg = w.bcfg
				w.host.Apply(w.bcfg, settings.DownloadMove{})
				b := &Builtin{cfg: w.bcfg, host: w.host}
				if _, err := b.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: h.folder, Tags: []string{"reelkeeper", "Tracker"}}); err != nil {
					t.Fatal(err)
				}
				w.download(h, tc.status, settings.ClientBuiltin)
				// It runs, and has found the pieces on disk.
				eventually(t, "the built-in client to find what is on disk", func() bool {
					tr := w.builtinTorrent(h.hash)
					return tr != nil && tr.Verified == h.haveBytes
				})

				w.switchClient(w.bcfg, w.xcfg, false)
				w.run()

				d := w.f.download(t, db, h.hash)
				if d.MoveState != "" || d.Client != settings.ClientTransmission || d.Status != tc.status {
					t.Fatalf("download after the move = %+v", d)
				}
				if evs := moveFailures(t, w.f); len(evs) != 0 {
					t.Fatalf("the move failed: %s", evs[0].Message)
				}
				x := w.disk.tors[h.hash]
				if x == nil || x.Verified != h.haveBytes {
					t.Fatalf("the other client's torrent = %+v", x)
				}
				if x.Done() != (tc.pieces == mvPieces) || x.Paused() {
					t.Errorf("the other client has it done=%v paused=%v (state %s)", x.Done(), x.Paused(), x.State)
				}
				// The engine let go of it, and, the move being over, stopped.
				if e, _, _ := w.host.running(); e != nil {
					t.Error("the built-in engine is still running after the move")
				}
				if !bytes.Equal(h.bytesOnDisk(t), before) {
					t.Error("the file on disk changed")
				}
				// The built-in client's records of it are gone too.
				w.host.Apply(w.bcfg, settings.DownloadMove{})
				if tr := w.builtinTorrent(h.hash); tr != nil {
					t.Errorf("the built-in client still lists it: %+v", tr)
				}
			})
		})
	}
}

// A paused torrent stays paused across a move, in both directions.
func TestMoveKeepsAPausedTorrentPaused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		h := newHalfDone(t, "Movie.mkv", 8)
		w := newBWorld(t, db, h.folder)
		w.f.cfg = w.bcfg
		w.host.Apply(w.bcfg, settings.DownloadMove{})
		b := &Builtin{cfg: w.bcfg, host: w.host}
		if _, err := b.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: h.folder, Stopped: true}); err != nil {
			t.Fatal(err)
		}
		if err := b.Recheck(ctx, settings.MovieCategory, h.hash); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the check", func() bool {
			tr := w.builtinTorrent(h.hash)
			return tr != nil && tr.State == torrent.Paused && tr.Verified == h.haveBytes
		})
		w.download(h, StatusDownloading, settings.ClientBuiltin)
		w.switchClient(w.bcfg, w.xcfg, false)
		w.run()
		if x := w.disk.tors[h.hash]; x == nil || !x.Paused() {
			t.Errorf("the other client's torrent should be paused: %+v", x)
		}
		// And back.
		w.switchClient(w.xcfg, w.bcfg, false)
		w.run()
		if tr := w.builtinTorrent(h.hash); tr == nil || tr.State != torrent.Paused || tr.Verified != h.haveBytes {
			t.Errorf("back in the built-in client: %+v", tr)
		}
		if evs := moveFailures(t, w.f); len(evs) != 0 {
			t.Fatalf("a move failed: %s", evs[0].Message)
		}
	})
}

// A magnet link nobody shares can't be turned into a torrent, so the move
// fails: the built-in client lets go of it, the files are left alone, and the
// other client gets it back.
func TestMoveToTheBuiltinClientRollsBackWhenItGetsNoDetails(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		h := newHalfDone(t, "Movie.mkv", 8)
		w := newBWorld(t, db, h.folder)
		w.f.svc.moveTiming.metadata = 300 * time.Millisecond
		before := h.bytesOnDisk(t)
		w.download(h, StatusDownloading, settings.ClientTransmission)
		// Only a magnet link was kept; the other client keeps the real thing.
		if _, err := w.db.DB.NewDelete().Model((*downloadSource)(nil)).Where("1=1").Exec(ctx); err != nil {
			t.Fatal(err)
		}
		d := w.f.download(t, db, h.hash)
		if err := w.f.svc.storeSource(ctx, d.ID, Source{Magnet: magnetOf(h.hash)}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.disk.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: h.folder, Stopped: true}); err != nil {
			t.Fatal(err)
		}
		_ = w.disk.Recheck(ctx, "", h.hash)
		_ = w.disk.Start(ctx, "", []string{h.hash})

		w.f.cfg = w.xcfg
		w.switchClient(w.xcfg, w.bcfg, false)
		w.run()

		d = w.f.download(t, db, h.hash)
		if d.MoveState != "" || d.Status != StatusRemoved || d.Client != settings.ClientTransmission {
			t.Fatalf("download after the failed move = %+v", d)
		}
		if evs := moveFailures(t, w.f); len(evs) != 1 {
			t.Fatalf("%d move failures", len(evs))
		}
		if tr := w.builtinTorrent(h.hash); tr != nil {
			t.Errorf("the built-in client still has it: %+v", tr)
		}
		if x := w.disk.tors[h.hash]; x == nil || x.Verified != h.haveBytes {
			t.Errorf("the other client lost it: %+v", x)
		}
		if !bytes.Equal(h.bytesOnDisk(t), before) {
			t.Error("the file on disk changed")
		}
	})
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
