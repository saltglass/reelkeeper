package download

// Tests of the built-in client's adapter and of the host that runs its engine.
// Nothing here reaches beyond 127.0.0.1: every engine gets testNet, which
// refuses any other address, and no test adds a real torrent or magnet.

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// testNet is a builtin.Network that only reaches 127.0.0.1 and counts dials.
type testNet struct {
	dials, refused atomic.Int64
}

func loopbackAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" {
		return net.JoinHostPort("127.0.0.1", port), nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("test network: %s isn't on this computer", addr)
	}
	return addr, nil
}

func (n *testNet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.dials.Add(1)
	a, err := loopbackAddr(addr)
	if err != nil {
		n.refused.Add(1)
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, network, a)
}

func (n *testNet) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	a, err := loopbackAddr(addr)
	if err != nil {
		n.refused.Add(1)
		return nil, err
	}
	return (&net.ListenConfig{}).Listen(ctx, network, a)
}

func (n *testNet) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	a, err := loopbackAddr(addr)
	if err != nil {
		n.refused.Add(1)
		return nil, err
	}
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp4", a)
}

func (n *testNet) LookupHost(context.Context, string) ([]string, error) {
	n.refused.Add(1)
	return nil, errors.New("test network: no name lookups")
}

// bfix is a built-in client with its engine running, in a temporary folder.
type bfix struct {
	t      *testing.T
	host   *BuiltinHost
	nw     *testNet
	folder string
	data   string
	cfg    settings.DownloadClient
	c      *Builtin
}

func newHost(t *testing.T, dataDir string, nw *testNet) *BuiltinHost {
	t.Helper()
	h := NewBuiltinHost(dataDir, slog.New(slog.DiscardHandler))
	h.network = nw
	h.tune = func(c *builtin.Config) {
		c.ListenPort = 0 // any free port; the settings' own are only checked by Version
		c.ListenHost = "127.0.0.1"
		c.DHT, c.PEX = false, false
		c.Tick, c.StalledAfter = 50*time.Millisecond, 3*time.Second
	}
	t.Cleanup(h.Close)
	return h
}

func newBFix(t *testing.T) *bfix {
	t.Helper()
	f := &bfix{t: t, nw: &testNet{}, folder: t.TempDir(), data: t.TempDir()}
	f.host = newHost(t, f.data, f.nw)
	f.cfg = settings.DownloadClient{Type: settings.ClientBuiltin, Folder: f.folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25}
	f.host.Apply(f.cfg, settings.DownloadMove{})
	f.c = &Builtin{cfg: f.cfg, host: f.host}
	return f
}

func hashOf(n int) string { return fmt.Sprintf("%040x", n) }

func magnetOf(hash string) string { return "magnet:?xt=urn:btih:" + hash + "&dn=Example" }

func (f *bfix) torrent(cat, hash string) *Torrent {
	f.t.Helper()
	ts, err := f.c.Torrents(context.Background(), cat)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range ts {
		if ts[i].Hash == hash {
			return &ts[i]
		}
	}
	return nil
}

func TestBuiltinVersionIsTheConnectionTest(t *testing.T) {
	ctx := context.Background()
	f := newBFix(t)
	if v, err := f.c.Version(ctx); err != nil || !strings.HasPrefix(v, "Built-in (anacrolix/torrent v") {
		t.Fatalf("Version = %q, %v", v, err)
	}
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Not running, as when Test is pressed before saving: the port is looked at.
	idle := &Builtin{host: newHost(t, t.TempDir(), &testNet{})}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	for _, tc := range []struct {
		name   string
		folder string
		port   int
		want   string // part of the error; empty means fine
	}{
		{"good", f.folder, freePort, ""},
		{"no folder", "", freePort, "needs a download folder"},
		{"folder missing", filepath.Join(f.folder, "nope"), freePort, "doesn't exist"},
		{"folder is a file", file, freePort, "isn't a folder"},
		{"port taken", f.folder, busyPort, "can't listen on TCP port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Builtin{cfg: settings.DownloadClient{Type: settings.ClientBuiltin, Folder: tc.folder, ListenPort: tc.port}, host: idle.host}
			v, err := c.Version(ctx)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want one with %q (version %q)", err, tc.want, v)
			}
		})
	}
	// A folder that can be seen but not written to.
	if os.Getuid() != 0 {
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
		c := &Builtin{cfg: settings.DownloadClient{Type: settings.ClientBuiltin, Folder: ro, ListenPort: freePort}, host: idle.host}
		if _, err := c.Version(ctx); err == nil || !strings.Contains(err.Error(), "can't write to") {
			t.Errorf("read-only folder: %v", err)
		}
	}
	// Running with the port already: its own port isn't "taken".
	if e, port, _ := f.host.running(); e == nil || port == 0 {
		t.Fatal("the fixture's engine isn't running")
	} else {
		own := &Builtin{cfg: settings.DownloadClient{Type: settings.ClientBuiltin, Folder: f.folder, ListenPort: port}, host: f.host}
		if _, err := own.Version(ctx); err != nil {
			t.Errorf("the engine's own port counted as taken: %v", err)
		}
	}
	// A client made without a host (a bare test of the type) says so.
	if _, err := (&Builtin{cfg: f.cfg}).Version(ctx); !errors.Is(err, ErrUnreachable) {
		t.Errorf("no host: %v", err)
	}
}

func TestBuiltinNeedsARunningEngine(t *testing.T) {
	ctx := context.Background()
	h := newHost(t, t.TempDir(), &testNet{})
	c := &Builtin{cfg: settings.DownloadClient{Type: settings.ClientBuiltin, Folder: t.TempDir()}, host: h}
	h.Apply(settings.DownloadClient{Type: settings.ClientQBittorrent, URL: "http://x"}, settings.DownloadMove{})
	calls := map[string]func() error{
		"Torrents": func() error { _, err := c.Torrents(ctx, settings.MovieCategory); return err },
		"Add": func() error {
			_, err := c.Add(ctx, Add{Magnet: magnetOf(hashOf(1)), Category: settings.MovieCategory})
			return err
		},
		"Remove":          func() error { return c.Remove(ctx, settings.MovieCategory, []string{hashOf(1)}, false) },
		"Stop":            func() error { return c.Stop(ctx, settings.MovieCategory, []string{hashOf(1)}) },
		"Start":           func() error { return c.Start(ctx, settings.MovieCategory, []string{hashOf(1)}) },
		"NoShareLimits":   func() error { return c.NoShareLimits(ctx, settings.MovieCategory, []string{hashOf(1)}) },
		"TopPriority":     func() error { return c.TopPriority(ctx, settings.MovieCategory, []string{hashOf(1)}) },
		"MoveCategory":    func() error { return c.MoveCategory(ctx, "a", "b", []string{hashOf(1)}) },
		"TrackerMessages": func() error { _, err := c.TrackerMessages(ctx, settings.MovieCategory, hashOf(1)); return err },
		"Source":          func() error { _, err := c.Source(ctx, settings.MovieCategory, hashOf(1)); return err },
		"Layout":          func() error { _, err := c.Layout(ctx, settings.MovieCategory, hashOf(1)); return err },
		"Recheck":         func() error { return c.Recheck(ctx, settings.MovieCategory, hashOf(1)) },
		"SetFolder":       func() error { return c.SetFolder(ctx, settings.MovieCategory, hashOf(1), "/x") },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrUnreachable) {
			t.Errorf("%s without an engine: %v, want one that matches ErrUnreachable", name, err)
		}
	}
	// Starting it makes them work.
	h.Apply(settings.DownloadClient{Type: settings.ClientBuiltin, Folder: c.cfg.Folder, ListenPort: 6881, MaxActiveDownloads: 3}, settings.DownloadMove{})
	if _, err := c.Torrents(ctx, settings.MovieCategory); err != nil {
		t.Errorf("with an engine: %v", err)
	}
}

// What every client has to do, asked of the built-in one.
func TestBuiltinBehavesLikeTheOtherClients(t *testing.T) {
	ctx := context.Background()
	f := newBFix(t)
	c := f.c
	mc, tv := settings.MovieCategory, settings.TVCategory
	if c.Name() != "Built-in" || f.cfg.ClientName() != "Built-in" {
		t.Errorf("names: %q / %q", c.Name(), f.cfg.ClientName())
	}
	if err := c.EnsureCategory(ctx, mc, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureCategory(ctx, "", ""); err == nil {
		t.Error("an empty category was accepted")
	}

	// Add a magnet, stopped: the hash comes back, and the torrent lists as
	// paused, with the folder it was given and without the category as a tag.
	h := hashOf(0xabc)
	got, err := c.Add(ctx, Add{Magnet: magnetOf(h), Category: mc, Tags: []string{"reelkeeper", "Tracker"}, Stopped: true})
	if err != nil || got != h {
		t.Fatalf("Add = %q, %v", got, err)
	}
	if again, err := c.Add(ctx, Add{Magnet: magnetOf(h), Category: mc}); err != nil || again != h {
		t.Errorf("adding it again: %q, %v", again, err)
	}
	tr := f.torrent(mc, h)
	if tr == nil {
		t.Fatal("not listed")
	}
	if tr.State != torrent.Paused || tr.Category != mc || tr.SavePath != f.folder || tr.AddedOn.IsZero() || tr.Done() || !tr.Paused() || !tr.Stopped() ||
		!slices.Equal(tr.Tags, []string{"reelkeeper", "Tracker"}) || tr.Name != "Example" || tr.ContentPath != filepath.Join(f.folder, "Example") {
		t.Errorf("torrent = %+v", tr)
	}
	if other, _ := c.Torrents(ctx, tv); len(other) != 0 {
		t.Errorf("the TV category lists %d", len(other))
	}
	if _, err := c.Torrents(ctx, ""); err == nil {
		t.Error("listing without a category")
	}

	// It only touches its own category's torrents.
	for name, err := range map[string]error{
		"Remove":        c.Remove(ctx, tv, []string{h}, true),
		"Stop":          c.Stop(ctx, tv, []string{h}),
		"Start":         c.Start(ctx, tv, []string{h}),
		"NoShareLimits": c.NoShareLimits(ctx, tv, []string{h}),
		"TopPriority":   c.TopPriority(ctx, tv, []string{h}),
		"MoveCategory":  c.MoveCategory(ctx, tv, mc, []string{h}),
		"Recheck":       c.Recheck(ctx, tv, h),
		"SetFolder":     c.SetFolder(ctx, tv, h, f.folder),
	} {
		if err == nil {
			t.Errorf("%s changed a torrent outside its category", name)
		}
	}
	if f.torrent(mc, h) == nil {
		t.Fatal("the torrent is gone")
	}

	// Lifting the limits starts a stopped torrent, as it does in the others;
	// Stop pauses it; Start resumes it; moving to the top is accepted.
	if err := c.NoShareLimits(ctx, mc, []string{h}); err != nil {
		t.Fatal(err)
	}
	if st := f.torrent(mc, h).State; st != torrent.Metadata {
		t.Errorf("after NoShareLimits the torrent is %s, want Metadata (running, no details)", st)
	}
	if err := c.Stop(ctx, mc, []string{h}); err != nil {
		t.Fatal(err)
	}
	if !f.torrent(mc, h).Paused() {
		t.Error("not paused after Stop")
	}
	if err := c.Start(ctx, mc, []string{h}); err != nil {
		t.Fatal(err)
	}
	if f.torrent(mc, h).Paused() {
		t.Error("still paused after Start")
	}
	if err := c.TopPriority(ctx, mc, []string{h}); err != nil {
		t.Fatal(err)
	}

	// No tracker has been asked, so there is nothing to say.
	if msgs, err := c.TrackerMessages(ctx, mc, h); err != nil || len(msgs) != 0 {
		t.Errorf("tracker messages = %q, %v", msgs, err)
	}

	// Categories can change between Reelkeeper's own.
	if err := c.MoveCategory(ctx, mc, tv, []string{h}); err != nil {
		t.Fatal(err)
	}
	if f.torrent(tv, h) == nil || f.torrent(mc, h) != nil {
		t.Error("MoveCategory didn't move it")
	}

	// A .torrent file: the hash is the info hash, the files are listed, and
	// the file comes back as the source.
	data := someData("Movie.mkv", 40000)
	raw := testTorrentFile(t, "Movie.mkv", data)
	want, _ := InfoHash(raw)
	got, err = c.Add(ctx, Add{Torrent: raw, Category: mc, Stopped: true})
	if err != nil || got != want {
		t.Fatalf("Add(torrent) = %q, %v; want %s", got, err, want)
	}
	lay, err := c.Layout(ctx, mc, want)
	if err != nil || lay.Folder != f.folder || !slices.Equal(lay.Look, []string{f.folder}) || len(lay.Files) != 1 || lay.Files[0] != (File{Name: "Movie.mkv", Size: 40000}) {
		t.Errorf("layout = %+v, %v", lay, err)
	}
	if src, err := c.Source(ctx, mc, want); err != nil || string(src.Torrent) != string(raw) || src.Magnet != "" || src.Kind() != SourceTorrent {
		t.Errorf("source = %d bytes, %q, %v", len(src.Torrent), src.Magnet, err)
	}
	if src, err := c.Source(ctx, tv, h); err != nil || src.Magnet == "" || src.Kind() != SourceMagnet {
		t.Errorf("a magnet's source = %+v, %v", src, err)
	}
	if _, err := c.Source(ctx, mc, hashOf(0x999)); err == nil || !errors.Is(err, ErrNoSource) {
		t.Errorf("source of a torrent it doesn't have: %v", err)
	}

	// Its data is in place: a check finds it all, and the torrent is finished.
	if err := os.WriteFile(filepath.Join(f.folder, "Movie.mkv"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Recheck(ctx, mc, want); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the check to finish", func() bool {
		tr := f.torrent(mc, want)
		return tr != nil && tr.State == torrent.SeedingPaused
	})
	tr = f.torrent(mc, want)
	if !tr.Done() || tr.Verified != 40000 || tr.Left != 0 || tr.Size != 40000 || !tr.Stopped() || tr.Paused() {
		t.Errorf("after the check: %+v", tr)
	}

	// SetFolder moves the files; Remove with files deletes only the torrent's.
	dest := filepath.Join(t.TempDir(), "moved")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFolder(ctx, mc, want, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Movie.mkv")); err != nil {
		t.Errorf("the file wasn't moved: %v", err)
	}
	if f.torrent(mc, want).SavePath != dest {
		t.Error("the save path didn't change")
	}
	other := filepath.Join(dest, "other.txt")
	if err := os.WriteFile(other, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, mc, []string{want}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Movie.mkv")); err == nil {
		t.Error("Remove(deleteFiles) kept the file")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Remove(deleteFiles) deleted another file: %v", err)
	}
	if f.torrent(mc, want) != nil {
		t.Error("still listed after Remove")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("gave up waiting for %s", what)
}

// ---- the host

func TestBuiltinHostFollowsTheSettings(t *testing.T) {
	nw := &testNet{}
	h := newHost(t, t.TempDir(), nw)
	folder := t.TempDir()
	builtinCfg := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25}
	qbit := settings.DownloadClient{Type: settings.ClientQBittorrent, URL: "http://qbit:8080", Username: "a"}
	engine := func() *builtin.Engine { e, _, _ := h.running(); return e }

	// Nothing runs for another client, or for a built-in client with no folder.
	h.Apply(qbit, settings.DownloadMove{})
	h.Apply(settings.DownloadClient{Type: settings.ClientBuiltin}, settings.DownloadMove{})
	if engine() != nil {
		t.Fatal("an engine runs when it isn't wanted")
	}
	// It starts when the built-in client is the download client.
	h.Apply(builtinCfg, settings.DownloadMove{})
	first := engine()
	if first == nil {
		t.Fatal("no engine")
	}
	// Limits and queue sizes apply live, to the same engine.
	live := builtinCfg
	live.MaxActiveDownloads, live.MaxActiveSeeds, live.DownloadLimitKBs, live.UploadLimitKBs = 7, 9, 100, 50
	h.Apply(live, settings.DownloadMove{})
	if engine() != first {
		t.Error("a change of limits restarted the engine")
	}
	if c := first.Config(); c.MaxActiveDownloads != 7 || c.MaxActiveSeeds != 9 || c.DownloadLimitKBs != 100 || c.UploadLimitKBs != 50 {
		t.Errorf("engine config = %+v", c)
	}
	// It keeps running while a move away from it is under way, from the old
	// connection; it stops when the move is over.
	h.Apply(qbit, settings.DownloadMove{Active: true, Old: live})
	if engine() != first {
		t.Fatal("the engine stopped during a move from it")
	}
	h.Apply(qbit, settings.DownloadMove{})
	if engine() != nil {
		t.Error("the engine runs after the move ended")
	}
	if _, _, err := h.running(); !errors.Is(err, ErrUnreachable) {
		t.Errorf("running() without an engine: %v", err)
	}
	// A move to the built-in client starts it as soon as it is the client.
	h.Apply(builtinCfg, settings.DownloadMove{Active: true, Old: qbit})
	if engine() == nil {
		t.Error("no engine for a move to it")
	}
	// Close stops it for good.
	h.Close()
	if engine() != nil {
		t.Error("Close left the engine running")
	}
}

func TestBuiltinHostRestartsForANewPortAndReportsAFailure(t *testing.T) {
	h := newHost(t, t.TempDir(), &testNet{})
	// The test tuning ignores the port, so a restart is shown the way the
	// engine would be asked: by what RestartNeeded says of a changed port.
	cfg := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: t.TempDir(), ListenPort: 6881, MaxActiveDownloads: 3}
	h.Apply(cfg, settings.DownloadMove{})
	e, _, _ := h.running()
	if e == nil {
		t.Fatal("no engine")
	}
	c := e.Config()
	c.ListenPort++
	if !e.RestartNeeded(c) {
		t.Error("a new listen port should need a restart")
	}
	c = e.Config()
	c.MaxActiveSeeds = 3
	c.DownloadLimitKBs = 3
	if e.RestartNeeded(c) {
		t.Error("new limits shouldn't need a restart")
	}
	h.Close()

	// A port that can't be opened: the engine isn't running, the host says why.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	h2 := newHost(t, t.TempDir(), &testNet{})
	h2.tune = func(c *builtin.Config) {
		c.ListenPort, c.ListenHost = busy.Addr().(*net.TCPAddr).Port, "127.0.0.1"
		c.DHT, c.PEX = false, false
	}
	h2.Apply(cfg, settings.DownloadMove{})
	_, _, err = h2.running()
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "can't listen on TCP port "+strconv.Itoa(busy.Addr().(*net.TCPAddr).Port)) {
		t.Errorf("a busy port: %v", err)
	}
	// Once the setting is fixed it starts.
	h2.tune = func(c *builtin.Config) { c.ListenPort, c.ListenHost = 0, "127.0.0.1"; c.DHT, c.PEX = false, false }
	h2.Apply(cfg, settings.DownloadMove{})
	if e, _, err := h2.running(); err != nil || e == nil {
		t.Errorf("after fixing the port: %v", err)
	}
}

// The torrents survive the host (the server) restarting.
func TestBuiltinHostKeepsTorrentsAcrossARestart(t *testing.T) {
	ctx := context.Background()
	data, folder := t.TempDir(), t.TempDir()
	cfg := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25}
	h := newHost(t, data, &testNet{})
	h.Apply(cfg, settings.DownloadMove{})
	c := &Builtin{cfg: cfg, host: h}
	hash := hashOf(77)
	if _, err := c.Add(ctx, Add{Magnet: magnetOf(hash), Category: settings.MovieCategory, Stopped: true, Tags: []string{"reelkeeper"}}); err != nil {
		t.Fatal(err)
	}
	h.Close()
	h2 := newHost(t, data, &testNet{})
	h2.Apply(cfg, settings.DownloadMove{})
	c2 := &Builtin{cfg: cfg, host: h2}
	ts, err := c2.Torrents(ctx, settings.MovieCategory)
	if err != nil || len(ts) != 1 || ts[0].Hash != hash || ts[0].State != torrent.Paused || !slices.Equal(ts[0].Tags, []string{"reelkeeper"}) {
		t.Errorf("after a restart: %+v, %v", ts, err)
	}
	if _, err := os.Stat(filepath.Join(data, "builtin", "state.db")); err != nil {
		t.Errorf("the records aren't in the data folder's builtin folder: %v", err)
	}
}

// ---- the service, with the real built-in client

func TestServiceWithTheBuiltinClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		b := newBFix(t)
		f.cfg = b.cfg
		f.svc.SetBuiltin(b.host)
		f.svc.NewClient = func(c settings.DownloadClient) Client { return &Builtin{cfg: c, host: b.host} }
		f.svc.exists = onDisk
		f.grab(t, hashA)
		d := f.download(t, db, hashA)
		if d.Client != settings.ClientBuiltin || d.Status != StatusDownloading {
			t.Fatalf("download = %+v", d)
		}
		// The release is a magnet: the engine holds it, in Reelkeeper's category,
		// in the folder the settings name, with the grab's tags.
		tr := b.torrent(settings.MovieCategory, hashA)
		if tr == nil || tr.SavePath != b.folder || !slices.Equal(tr.Tags, []string{"reelkeeper", "Tracker"}) {
			t.Fatalf("torrent = %+v", tr)
		}
		items, cerr, err := f.svc.Queue(ctx)
		if err != nil || cerr != nil || len(items) != 1 || items[0].Torrent == nil || items[0].Torrent.State != torrent.Metadata {
			t.Fatalf("queue = %+v, %v, %v", items, cerr, err)
		}
		if name := f.svc.ClientName(); name != "Built-in" {
			t.Errorf("the client's name = %q", name)
		}
		// Pause, resume, move to top, through the service.
		if err := f.svc.Pause(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if st := b.torrent(settings.MovieCategory, hashA).State; st != torrent.Paused {
			t.Errorf("after Pause: %s", st)
		}
		if err := f.svc.MoveToTop(ctx, d.ID); err == nil {
			t.Error("moved a paused download to the top")
		}
		if err := f.svc.Resume(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.MoveToTop(ctx, d.ID); err != nil {
			t.Fatal(err)
		}
		if b.torrent(settings.MovieCategory, hashA).Paused() {
			t.Error("still paused after Resume")
		}
		// Removing it takes it out of the engine.
		if _, err := f.svc.Remove(ctx, d.ID, RemoveOptions{}); err != nil {
			t.Fatal(err)
		}
		if b.torrent(settings.MovieCategory, hashA) != nil {
			t.Error("still in the engine after Remove")
		}
		if evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventPaused}}); len(evs) != 1 {
			t.Errorf("%d paused events", len(evs))
		}
	})
}

func TestServiceTestButtonForTheBuiltinClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		h := newHost(t, t.TempDir(), &testNet{})
		f.svc.SetBuiltin(h) // no engine is running: it is tested before it is saved
		f.svc.NewClient = func(c settings.DownloadClient) Client { return &Builtin{cfg: c, host: h} }
		good := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: t.TempDir(), ListenPort: 0}
		// Port 0 would pick any port; Version looks at the real settings' port.
		good.ListenPort = freePort(t)
		if v, err := f.svc.Test(ctx, good); err != nil || !strings.HasPrefix(v, "Built-in") {
			t.Errorf("Test = %q, %v", v, err)
		}
		bad := good
		bad.Folder = filepath.Join(good.Folder, "missing")
		if _, err := f.svc.Test(ctx, bad); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
			t.Errorf("Test with a missing folder: %v", err)
		}
		if _, err := f.svc.Test(ctx, settings.DownloadClient{Type: settings.ClientBuiltin}); !errors.Is(err, ErrNoClient) {
			t.Errorf("Test with no folder: %v", err)
		}
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestNewClientBuildsTheBuiltinClient(t *testing.T) {
	cfg := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: "/downloads"}
	if c, ok := newClient(cfg).(*Builtin); !ok || c.cfg.Folder != "/downloads" || c.Name() != "Built-in" {
		t.Errorf("newClient = %#v", newClient(cfg))
	}
	if !cfg.Configured() || (settings.DownloadClient{Type: settings.ClientBuiltin}).Configured() {
		t.Error("the built-in client is configured exactly when it has a folder")
	}
	h := NewBuiltinHost(t.TempDir(), slog.New(slog.DiscardHandler))
	s := &Service{}
	s.NewClient = func(c settings.DownloadClient) Client { return newClient(c) }
	_ = h
	_ = s
}

// The adapter's port check goes through the same Network as the engine.
func TestBuiltinAdapterDoesNotReachTheNetworkItself(t *testing.T) {
	src, err := os.ReadFile("builtin.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "builtin.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "net" || id.Name == "http") {
			if name := sel.Sel.Name; strings.HasPrefix(name, "Dial") || strings.HasPrefix(name, "Listen") || strings.HasPrefix(name, "Lookup") ||
				strings.HasPrefix(name, "Resolve") || strings.HasPrefix(name, "Default") || name == "Get" || name == "Client" || name == "Dialer" {
				t.Errorf("builtin.go uses %s.%s directly", id.Name, name)
			}
		}
		return true
	})
}
