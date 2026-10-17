package builtin

// The tests below never touch the internet. Every network the engine sees is
// loopbackNet, which refuses anything that isn't 127.0.0.1, so a mistake
// (a tracker, a DHT node, a name lookup) fails loudly here instead of reaching
// out. Peers are in-process anacrolix clients on 127.0.0.1, and trackers are
// httptest servers.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	httpTracker "github.com/anacrolix/torrent/tracker/http"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// loopbackNet is a Network that only reaches 127.0.0.1, and counts what it is
// asked to do.
type loopbackNet struct {
	dials, listens, packets, lookups atomic.Int64
	refused                          atomic.Int64
	mu                               sync.Mutex
	dialed                           []string
}

func (n *loopbackNet) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.dials.Add(1)
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		n.refused.Add(1)
		return nil, fmt.Errorf("test network: refusing to dial %s", addr)
	}
	n.mu.Lock()
	n.dialed = append(n.dialed, addr)
	n.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (n *loopbackNet) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	n.listens.Add(1)
	if err := onlyLoopback(addr); err != nil {
		n.refused.Add(1)
		return nil, err
	}
	return (&net.ListenConfig{}).Listen(ctx, network, addr)
}

func (n *loopbackNet) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	n.packets.Add(1)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host == "" || host == "0.0.0.0" {
		// A UDP tracker client asks for ":0"; in a test that means loopback.
		host = "127.0.0.1"
	}
	if err := onlyLoopback(net.JoinHostPort(host, port)); err != nil {
		n.refused.Add(1)
		return nil, err
	}
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp4", net.JoinHostPort(host, port))
}

func (n *loopbackNet) LookupHost(_ context.Context, host string) ([]string, error) {
	n.lookups.Add(1)
	if host == "localhost" {
		return []string{"127.0.0.1"}, nil
	}
	n.refused.Add(1)
	return nil, fmt.Errorf("test network: refusing to look up %s", host)
}

func onlyLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("test network: refusing to listen on %q", addr)
	}
	return nil
}

// content makes a deterministic multi-file download in a new folder and returns
// the torrent for it (the .torrent bytes) and the folder it sits in. Each file
// is size bytes of made-up data.
type content struct {
	root     string // the torrent's own folder, <parent>/<name>
	parent   string
	name     string
	torrent  []byte
	hash     string
	mi       *metainfo.MetaInfo
	files    map[string][]byte // by name relative to root
	totalLen int64
}

type contentOpts struct {
	private  bool
	announce string
	// sizes of the three files
	big, small int
}

func makeContent(t *testing.T, name string, o contentOpts) *content {
	t.Helper()
	if o.big == 0 {
		o.big = 600 << 10
	}
	if o.small == 0 {
		o.small = 20 << 10
	}
	parent := t.TempDir()
	c := &content{parent: parent, name: name, root: filepath.Join(parent, name), files: map[string][]byte{}}
	for rel, size := range map[string]int{"movie.mkv": o.big, "Subs/en.srt": o.small, "notes.txt": 1234} {
		data := someData(name+"/"+rel, size)
		c.files[rel] = data
		p := filepath.Join(c.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		c.totalLen += int64(size)
	}
	info := metainfo.Info{PieceLength: 32 << 10}
	if o.private {
		priv := true
		info.Private = &priv
	}
	if err := info.BuildFromFilePath(c.root); err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info), Announce: o.announce}
	b, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	c.mi, c.torrent, c.hash = mi, b, mi.HashInfoBytes().HexString()
	return c
}

// someData is deterministic made-up content.
func someData(seed string, size int) []byte {
	out := make([]byte, 0, size)
	for i := 0; len(out) < size; i++ {
		sum := sha256.Sum256(fmt.Appendf(nil, "%s-%d", seed, i))
		out = append(out, sum[:]...)
	}
	return out[:size]
}

func (c *content) magnet() string {
	return "magnet:?xt=urn:btih:" + c.hash + "&dn=" + c.name
}

// seeder is a plain anacrolix client on 127.0.0.1 that has the content.
type seeder struct {
	cl   *torrent.Client
	port int
	mu   sync.Mutex
	// ext is what each peer said it speaks, by info hash (hex).
	ext map[string]map[pp.ExtensionName]pp.ExtensionNumber
}

// extensions is what the peer of a torrent offered in its extended handshake.
func (s *seeder) extensions(hash string) map[pp.ExtensionName]pp.ExtensionNumber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ext[hash]
}

type seederOpts struct {
	pex bool
}

func startSeeder(t *testing.T, o seederOpts, cs ...*content) *seeder {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = t.TempDir()
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisablePEX = !o.pex
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DisableTrackers = true
	cfg.Seed = true
	cfg.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &seeder{ext: map[string]map[pp.ExtensionName]pp.ExtensionNumber{}}
	cfg.Callbacks.ReadExtendedHandshake = func(pc *torrent.PeerConn, m *pp.ExtendedHandshakeMessage) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.ext[pc.Torrent().InfoHash().HexString()] = maps.Clone(m.M)
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	s.cl, s.port = cl, cl.LocalPort()
	for _, c := range cs {
		s.add(t, c)
	}
	return s
}

func (s *seeder) add(t *testing.T, c *content) *torrent.Torrent {
	t.Helper()
	// The seeder's storage is the content's own folder.
	spec := torrent.TorrentSpecFromMetaInfo(c.mi)
	spec.Storage = fileStorageAt(c.parent)
	tt, _, err := s.cl.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tt.Complete().On():
	case <-time.After(20 * time.Second):
		t.Fatal("the seeder never finished checking its own files")
	}
	return tt
}

func (s *seeder) addr() string { return fmt.Sprintf("127.0.0.1:%d", s.port) }

// peerInfo is the seeder as a peer to hand a torrent.
func (s *seeder) peerInfo() torrent.PeerInfo {
	return torrent.PeerInfo{Addr: torrent.StringAddr(s.addr()), Trusted: true}
}

// tracker is a stub HTTP tracker on 127.0.0.1.
type stubTracker struct {
	srv *httptest.Server
	mu  sync.Mutex
	// answers by info hash (hex): a failure reason, or peers.
	failure map[string]string
	peers   map[string][]string
	calls   map[string]int
}

func startTracker(t *testing.T) *stubTracker {
	t.Helper()
	tr := &stubTracker{failure: map[string]string{}, peers: map[string][]string{}, calls: map[string]int{}}
	tr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ih := fmt.Sprintf("%x", r.URL.Query().Get("info_hash"))
		tr.mu.Lock()
		tr.calls[ih]++
		fail, peers := tr.failure[ih], tr.peers[ih]
		tr.mu.Unlock()
		resp := httpTracker.HttpResponse{Interval: 1}
		if fail != "" {
			resp = httpTracker.HttpResponse{FailureReason: fail}
		} else {
			resp.Complete = 7
			resp.Peers.Compact = true
			for _, p := range peers {
				host, port, _ := net.SplitHostPort(p)
				var n int
				_, _ = fmt.Sscan(port, &n)
				resp.Peers.List = append(resp.Peers.List, httpTracker.Peer{IP: net.ParseIP(host).To4(), Port: n})
			}
		}
		b, _ := bencode.Marshal(resp)
		_, _ = w.Write(b)
	}))
	t.Cleanup(tr.srv.Close)
	return tr
}

func (tr *stubTracker) url() string { return tr.srv.URL + "/announce" }

func (tr *stubTracker) set(hash, failure string, peers ...string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.failure[hash], tr.peers[hash] = failure, peers
}

func (tr *stubTracker) called(hash string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.calls[hash]
}

// ---- the engine under test

type engineOpts struct {
	cfg  func(*Config)
	dir  string // data folder; empty makes a new one
	down string // download folder; empty makes a new one
}

func testConfig(t *testing.T, nw Network, o engineOpts) Config {
	t.Helper()
	cfg := Config{
		DataDir: o.dir, Folder: o.down, ListenPort: 0, ListenHost: "127.0.0.1",
		MaxActiveDownloads: 3, MaxActiveSeeds: 25, Network: nw,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		StalledAfter: 3 * time.Second, Tick: 100 * time.Millisecond,
	}
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	if cfg.Folder == "" {
		cfg.Folder = t.TempDir()
	}
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	return cfg
}

func openEngine(t *testing.T, nw Network, o engineOpts) (*Engine, Config) {
	t.Helper()
	cfg := testConfig(t, nw, o)
	e, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeEngine(t, e) })
	return e, cfg
}

func closeEngine(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Close(ctx); err != nil {
		t.Errorf("closing the engine: %v", err)
	}
}

const cat = "reelkeeper-movies"

// status finds one torrent's status.
func status(t *testing.T, e *Engine, hash string) Status {
	t.Helper()
	all, err := e.List(cat)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Hash == hash {
			return s
		}
	}
	t.Fatalf("torrent %s isn't listed", hash)
	return Status{}
}

func tryStatus(e *Engine, hash string) (Status, bool) {
	all, _ := e.List(cat)
	for _, s := range all {
		if s.Hash == hash {
			return s, true
		}
	}
	return Status{}, false
}

// waitFor polls until ok holds for the torrent.
func waitFor(t *testing.T, e *Engine, hash, what string, timeout time.Duration, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last Status
	for time.Now().Before(deadline) {
		if s, found := tryStatus(e, hash); found {
			last = s
			if ok(s) {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: gave up; the torrent is %s at %.0f%%", what, last.State, last.Progress*100)
	return last
}

// unstick watches a torrent for the length of a test. Plain anacrolix clients
// on loopback now and then stall for good with a request lost (about one run in
// sixty under the race detector, with no engine involved at all), so when a
// torrent that is not finished makes no progress for a few seconds, its
// connections are closed and, if there is a seeder, it is handed to the torrent
// again.
func unstick(t *testing.T, get func() *torrent.Torrent, s *seeder) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		var last int64 = -1
		still := 0
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			tt := get()
			if tt == nil || tt.Info() == nil {
				continue
			}
			if tt.BytesMissing() == 0 {
				last, still = -1, 0
				continue
			}
			if n := tt.BytesCompleted(); n != last {
				last, still = n, 0
				continue
			}
			if still++; still >= 4 {
				still = 0
				for _, pc := range tt.PeerConns() {
					_ = pc.Close()
				}
				if s != nil {
					tt.AddPeers([]torrent.PeerInfo{s.peerInfo()})
				}
			}
		}
	}()
}

// engineTorrent is a function that finds a torrent as the engine has it now.
func engineTorrent(e *Engine, hash string) func() *torrent.Torrent {
	return func() *torrent.Torrent {
		e.mu.Lock()
		defer e.mu.Unlock()
		if en := e.ents[hash]; en != nil {
			return en.t
		}
		return nil
	}
}

// feed hands a running torrent a peer, waiting until the torrent runs, and keeps
// an eye on it (see unstick).
func feed(t *testing.T, e *Engine, hash string, s *seeder) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if tt := engineTorrent(e, hash)(); tt != nil {
			tt.AddPeers([]torrent.PeerInfo{s.peerInfo()})
			unstick(t, engineTorrent(e, hash), s)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the torrent never started running")
}

// readTree reads every file under dir, by slash-separated relative name.
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out
}

func sameFiles(t *testing.T, dir string, c *content) {
	t.Helper()
	got := readTree(t, filepath.Join(dir, c.name))
	if len(got) != len(c.files) {
		t.Fatalf("%s has %d files, want %d", dir, len(got), len(c.files))
	}
	for rel, want := range c.files {
		if string(got[rel]) != string(want) {
			t.Errorf("%s differs", rel)
		}
	}
}

func fileStorageAt(dir string) storage.ClientImpl {
	return storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: dir, PieceCompletion: storage.NewMapPieceCompletion(), UsePartFiles: g.Some(false),
	})
}

// leech is a plain anacrolix client that downloads c from the given port; the
// engine seeding to it is how the tests make the engine upload.
func leech(t *testing.T, c *content, port int) *torrent.Torrent {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = t.TempDir()
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisablePEX = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DisableTrackers = true
	cfg.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	tt, err := cl.AddTorrent(c.mi)
	if err != nil {
		t.Fatal(err)
	}
	peer := torrent.PeerInfo{Addr: torrent.StringAddr(fmt.Sprintf("127.0.0.1:%d", port)), Trusted: true}
	tt.AddPeers([]torrent.PeerInfo{peer})
	tt.DownloadAll()
	unstick(t, func() *torrent.Torrent { return tt }, &seeder{port: port})
	return tt
}

// running is how many torrents the engine has handed to the network.
func running(e *Engine) (n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, en := range e.ents {
		if en.running() {
			n++
		}
	}
	return n
}

func isRunning(e *Engine, hash string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ents[hash].running()
}

// eventually polls cond for a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(30 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("gave up waiting for %s", what)
}

// sharing: a finished torrent that is up (uploading, or with nobody yet to
// upload to for a few seconds, which reads as seeding-stalled).
func sharing(s rktorrent.State) bool {
	return s == rktorrent.Seeding || s == rktorrent.SeedingStalled
}
