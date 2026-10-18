// Package builtin is Reelkeeper's own BitTorrent client, for people who don't
// want to run qBittorrent, Transmission or Deluge next to it. It is built on
// anacrolix/torrent and is deliberately small: it downloads, seeds, queues and
// remembers its torrents, and gives Reelkeeper the same facts the external
// clients do. internal/download adapts it to the Client interface.
//
// Three rules shape everything here:
//
//   - Every socket comes from one Network (network.go). The engine never
//     dials, listens or looks up a name any other way, so a VPN can later be
//     slipped underneath without touching anything else.
//   - Nothing opens a port on the user's router. UPnP and NAT-PMP are off for
//     good (a test pins it), and so are WebTorrent, web seeds and
//     any default or public tracker list.
//   - A private torrent never touches the DHT or peer exchange. anacrolix
//     doesn't honour the private flag by itself, so the engine does (see
//     private.go).
//
// The engine keeps its own records in <data folder>/builtin (state.db for its
// torrents, a piece-completion database, and a copy of each .torrent file).
package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/utp"
	"go.etcd.io/bbolt"
	"golang.org/x/time/rate"
)

// Config is how the engine is set up. Everything but Network, ListenPort,
// ListenHost and the DHT and PEX switches can change while it runs
// (Engine.Configure).
type Config struct {
	// DataDir is Reelkeeper's data folder; the engine keeps its records in
	// DataDir/builtin.
	DataDir string
	// Folder is where downloads go when a torrent isn't given a folder.
	Folder string
	// ListenPort is the port peers connect to (TCP and UDP). 0 picks any free
	// one, which only tests do.
	ListenPort int
	// ListenHost is the address to listen on; empty means all addresses.
	ListenHost string
	// MaxActiveDownloads is how many unfinished torrents download at once;
	// the others wait as Queued, in queue order.
	MaxActiveDownloads int
	// MaxActiveSeeds is how many finished torrents upload at once; 0 means
	// no limit.
	MaxActiveSeeds int
	// DownloadLimitKBs and UploadLimitKBs are speed limits in KB/s; 0 means
	// no limit.
	DownloadLimitKBs int
	UploadLimitKBs   int
	// DHT and PEX let a torrent find peers through the DHT and through other
	// peers. Both apply to public torrents only.
	DHT bool
	PEX bool
	// Network is where every socket comes from; empty means the computer's
	// own (OSNetwork). A Network that is also a Gate (the VPN) can stop the
	// engine's torrents by itself.
	Network Network
	// Hold starts the engine with all its torrents held (see SetHold).
	Hold   bool
	Logger *slog.Logger

	// StalledAfter is how long a torrent may receive (or send) nothing before
	// it is reported stalled. Tick is how often the engine looks at its
	// torrents. Tests shorten both.
	StalledAfter time.Duration
	Tick         time.Duration
}

const (
	defaultStalledAfter = 30 * time.Second
	defaultTick         = time.Second
	// flushEvery is how often counters are written to disk (they are also
	// written whenever anything else changes, and on shutdown).
	flushEvery = 30 * time.Second
	// sweepEvery is how often finished torrents' files are checked to still be
	// there.
	sweepEvery = time.Minute
)

// Engine is the running client. Use Open to make one.
type Engine struct {
	netw Network
	// gate is the Network again, when it can say it is closed (the VPN).
	gate   Gate
	logger *slog.Logger
	dir    string

	st *store
	pc storage.PieceCompletion
	cl *torrent.Client
	// The limiters are handed to anacrolix once; changing them changes the
	// speed limits live.
	up, down *rate.Limiter

	// The client and its sockets. They exist while the engine may use the
	// network and are closed while it is blocked (held, or the VPN is down):
	// a new client is made when it may again, which also leaves behind
	// everything the old one remembered about its peers and trackers.
	tcp  net.Listener
	utp  *utp.Socket
	port atomic.Int64
	// netMu guards dht, which announcing goroutines read.
	netMu sync.RWMutex
	dht   *dht.Server
	// resumeTried is when starting the client again was last tried.
	resumeTried time.Time
	// stale holds the hashes of torrents that were dropped from the client (not
	// just paused in place). The client can't track such a torrent again once it
	// is added a second time (see stopRunning), so before one is started again
	// the client is made anew.
	stale map[string]bool
	// deleting holds the hashes of removed torrents whose files are still being
	// deleted (Remove does that after letting go of mu); deletions counts them,
	// so that Close waits for them before closing the piece records.
	deleting  map[string]bool
	deletions sync.WaitGroup

	trk  *trackerLog
	priv privateSet

	cfgMu sync.RWMutex
	cfg   Config

	mu sync.Mutex
	// held: every torrent is kept stopped, whatever its own state (SetHold).
	held  bool
	ents  map[string]*entry
	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}
	shut  bool
	last  time.Time // of the previous tick
	flush time.Time
	sweep time.Time

	// announceDHT does a DHT announce for a torrent (tests replace it).
	announceDHT func(t *torrent.Torrent)
	// beforeDelete runs as a removed torrent's files start being deleted
	// (tests use it to hold the deleting up).
	beforeDelete func(hash string)
}

// Open starts the engine: it opens its records, starts listening and brings
// back the torrents it had, without re-checking finished ones.
func Open(cfg Config) (*Engine, error) {
	if cfg.Network == nil {
		cfg.Network = OSNetwork{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.StalledAfter <= 0 {
		cfg.StalledAfter = defaultStalledAfter
	}
	if cfg.Tick <= 0 {
		cfg.Tick = defaultTick
	}
	if cfg.DataDir == "" {
		return nil, errors.New("the built-in client has no data folder")
	}
	if cfg.ListenPort < 0 || cfg.ListenPort > 65535 {
		return nil, fmt.Errorf("%d isn't a port", cfg.ListenPort)
	}
	e := &Engine{
		netw: cfg.Network, logger: cfg.Logger.With("component", "builtin"), cfg: cfg,
		dir:  filepath.Join(cfg.DataDir, "builtin"),
		ents: map[string]*entry{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		trk: newTrackerLog(), held: cfg.Hold, stale: map[string]bool{}, deleting: map[string]bool{},
		up:   rate.NewLimiter(limitOf(cfg.UploadLimitKBs), burstOf(cfg.UploadLimitKBs)),
		down: rate.NewLimiter(limitOf(cfg.DownloadLimitKBs), burstOf(cfg.DownloadLimitKBs)),
	}
	if g, ok := cfg.Network.(Gate); ok {
		e.gate = g
		g.OnChange(e.poke)
	}
	if err := os.MkdirAll(filepath.Join(e.dir, torrentsDir), 0o750); err != nil {
		return nil, fmt.Errorf("can't create the built-in client's folder %s: %w", e.dir, err)
	}
	var err error
	if e.st, err = openStore(e.dir); err != nil {
		return nil, err
	}
	if e.pc, err = openPieceRecords(e.dir, e.logger); err != nil {
		_ = e.st.close()
		return nil, fmt.Errorf("can't open the built-in client's piece records: %w", err)
	}
	// While blocked at the start (a VPN that hasn't connected yet), the engine
	// makes no sockets at all; the first beat after the block ends makes them.
	if !e.blockedLocked() {
		if err := e.startClient(); err != nil {
			e.closeStorage()
			return nil, err
		}
	}
	if err := e.load(); err != nil {
		e.closeNetwork()
		e.closeStorage()
		return nil, err
	}
	go e.run()
	e.poke()
	return e, nil
}

// openPieceRecords opens the database that says which pieces of each torrent are
// good. It is only a cache of what hashing the files would find out, so if it
// is damaged (it is not flushed on every write, so a crash can do that) it is
// put aside and started again: every torrent is then checked once more.
func openPieceRecords(dir string, logger *slog.Logger) (storage.PieceCompletion, error) {
	pc, err := storage.NewBoltPieceCompletion(dir)
	if err == nil {
		return pc, nil
	}
	if errors.Is(err, bbolt.ErrTimeout) {
		return nil, errors.New("another Reelkeeper has it open")
	}
	damaged := filepath.Join(dir, fmt.Sprintf(".torrent.bolt.db.damaged-%d", time.Now().Unix()))
	if rerr := os.Rename(filepath.Join(dir, ".torrent.bolt.db"), damaged); rerr != nil {
		return nil, err
	}
	logger.Warn("the built-in client's piece records were damaged; they were put aside and its torrents will be checked again", "error", err, "moved to", damaged)
	return storage.NewBoltPieceCompletion(dir)
}

// clientConfig is the anacrolix configuration. It is the one place that says
// what the engine will and won't do on the network.
func (e *Engine) clientConfig() *torrent.ClientConfig {
	cfg := torrent.NewDefaultClientConfig()
	e.cfgMu.RLock()
	c := e.cfg
	e.cfgMu.RUnlock()

	// anacrolix would open its own sockets, with the operating system's
	// network. They are all switched off; the engine opens its own through
	// Network and hands them over (startClient).
	cfg.DisableTCP, cfg.DisableUTP, cfg.NoDHT = true, true, true
	// Never ask the router to open a port, and never use the extras that
	// reach outside the trackers and peers a torrent names.
	cfg.NoDefaultPortForwarding = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DisablePEX = !c.PEX
	// anacrolix starts a DHT announce for every torrent by itself, private
	// ones too. The engine does the announcing (dht.go), for public ones.
	cfg.PeriodicallyAnnounceTorrentsToDht = false
	cfg.Seed = true // upload to anyone who asks; Reelkeeper's cleanup decides when to stop
	// Every file goes in the folder its torrent was given. There is no default
	// storage, so nothing is ever written anywhere else.
	cfg.DefaultStorage = noDefaultStorage{}

	cfg.UploadRateLimiter, cfg.DownloadRateLimiter = e.up, e.down

	cfg.HTTPProxy = nil // never a proxy picked up from the environment
	cfg.HTTPDialContext = e.dialHost
	cfg.TrackerDialContext = e.dialHost
	cfg.TrackerListenPacket = func(network, addr string) (net.PacketConn, error) {
		return e.netw.ListenPacket(context.Background(), network, addr)
	}
	cfg.LookupTrackerIp = e.lookupTracker
	cfg.DhtStartingNodes = e.dhtStartingNodes

	cfg.Slogger = slog.New(&capture{trk: e.trk, out: e.logger})
	cfg.Callbacks.ReadExtendedHandshake = e.privateHandshake
	cfg.Callbacks.PeerConnAdded = append(cfg.Callbacks.PeerConnAdded, e.privatePeerAdded)
	return cfg
}

// startClient creates the anacrolix client and gives it its sockets.
func (e *Engine) startClient() error {
	cl, err := torrent.NewClient(e.clientConfig())
	if err != nil {
		return fmt.Errorf("can't start the built-in client: %w", err)
	}
	e.cl = cl
	e.port.Store(0)
	e.cfgMu.RLock()
	c := e.cfg
	e.cfgMu.RUnlock()
	host := c.ListenHost
	ctx := context.Background()

	// TCP and UDP share a port number. When any port will do (only tests ask
	// for that), another program may have taken the UDP one the TCP port
	// chose, so another is tried.
	var pc net.PacketConn
	for attempt := 0; ; attempt++ {
		e.tcp, err = e.netw.Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(c.ListenPort)))
		if err != nil {
			e.closeNetwork()
			return portError(c.ListenPort, "TCP", err)
		}
		e.port.Store(int64(e.tcp.Addr().(*net.TCPAddr).Port))
		if pc, err = e.netw.ListenPacket(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(e.Port()))); err == nil {
			break
		}
		_ = e.tcp.Close()
		e.tcp = nil
		if c.ListenPort != 0 || attempt >= 9 {
			e.closeNetwork()
			return portError(e.Port(), "UDP", err)
		}
	}
	if e.utp, err = utp.NewSocketFromPacketConn(pc); err != nil {
		_ = pc.Close()
		e.closeNetwork()
		return fmt.Errorf("can't start uTP: %w", err)
	}
	cl.AddListener(e.tcp)
	cl.AddDialer(torrent.NetworkDialer{Network: "tcp", Dialer: dialerOf(e.dialHost)})
	cl.AddListener(e.utp)
	cl.AddDialer(torrent.NetworkDialer{Network: "udp", Dialer: e.utp})
	if c.DHT {
		d, err := cl.NewAnacrolixDhtServer(e.utp)
		if err != nil {
			e.closeNetwork()
			return fmt.Errorf("can't start the DHT: %w", err)
		}
		e.netMu.Lock()
		e.dht = d
		e.netMu.Unlock()
		cl.AddDhtServer(torrent.AnacrolixDhtServerWrapper{Server: d})
	}
	e.announceDHT = e.defaultAnnounceDHT
	return nil
}

func portError(port int, kind string, err error) error {
	return fmt.Errorf("the built-in client can't listen on %s port %d (is something else using it?): %w", kind, port, err)
}

func limitOf(kbs int) rate.Limit {
	if kbs <= 0 {
		return rate.Inf
	}
	return rate.Limit(kbs * 1024)
}

// burstOf is the most a limiter lets through at once. It must hold a whole
// chunk (16 KiB), and is about a second's worth otherwise, so a small limit
// still means a small speed.
func burstOf(kbs int) int {
	return max(kbs*1024, 64<<10)
}

// SetHold keeps every torrent stopped (true) or lets the queue run them again
// (false). It is not the same as pausing: nobody's own paused torrents are
// changed, so when the hold ends exactly what was running starts again. The
// built-in client holds its torrents while a VPN is removed and the person
// hasn't yet agreed to download without one.
func (e *Engine) SetHold(held bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.held == held {
		return
	}
	e.held = held
	e.reconcile(time.Now())
}

// Held reports whether SetHold is on.
func (e *Engine) Held() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.held
}

// blockedLocked is true while nothing may run: held, or the network's gate
// (the VPN) is closed.
func (e *Engine) blockedLocked() bool {
	return e.held || (e.gate != nil && !e.gate.Usable())
}

// Port is the port the engine listens on (useful when it was asked for 0).
func (e *Engine) Port() int { return int(e.port.Load()) }

// Configure applies the settings that can change while the engine runs:
// the queue sizes, the speed limits and the default folder. A new listen
// port, network or DHT/PEX switch needs a new engine (RestartNeeded).
func (e *Engine) Configure(c Config) {
	e.cfgMu.Lock()
	e.cfg.Folder = c.Folder
	e.cfg.MaxActiveDownloads = c.MaxActiveDownloads
	e.cfg.MaxActiveSeeds = c.MaxActiveSeeds
	e.cfg.DownloadLimitKBs = c.DownloadLimitKBs
	e.cfg.UploadLimitKBs = c.UploadLimitKBs
	e.cfgMu.Unlock()
	e.up.SetLimit(limitOf(c.UploadLimitKBs))
	e.up.SetBurst(burstOf(c.UploadLimitKBs))
	e.down.SetLimit(limitOf(c.DownloadLimitKBs))
	e.down.SetBurst(burstOf(c.DownloadLimitKBs))
	e.poke()
}

// RestartNeeded reports whether going from the running configuration to c
// needs a new engine (its sockets would change).
func (e *Engine) RestartNeeded(c Config) bool {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return c.ListenPort != e.cfg.ListenPort || c.ListenHost != e.cfg.ListenHost || c.DHT != e.cfg.DHT || c.PEX != e.cfg.PEX
}

// Config is the running configuration.
func (e *Engine) Config() Config {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg
}

func (e *Engine) settings() Config { return e.Config() }

// Version says which engine this is, such as "Built-in (anacrolix/torrent v1.61.0)".
func Version() string {
	v := "unknown version"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/anacrolix/torrent" {
				v = d.Version
				if d.Replace != nil {
					v = d.Replace.Version
				}
			}
		}
	}
	return "Built-in (anacrolix/torrent " + v + ")"
}

// Close stops the engine: it saves what it knows, lets go of every torrent
// and closes its sockets and files. It gives up waiting when ctx ends.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.shut {
		e.mu.Unlock()
		return nil
	}
	e.shut = true
	e.mu.Unlock()
	close(e.stop)
	select {
	case <-e.done:
	case <-ctx.Done():
	}
	e.mu.Lock()
	e.settleAll()
	if err := e.saveAllLocked(); err != nil {
		e.logger.Warn("couldn't save the built-in client's records on shutdown", "error", err)
	}
	e.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		e.closeNetwork()
		e.deletions.Wait() // they still write to the piece records
		e.closeStorage()
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return errors.New("the built-in client didn't close in time")
	}
}

// closeNetwork closes the client and every socket it was given. The client
// is made again by startClient.
func (e *Engine) closeNetwork() {
	if e.cl != nil {
		e.cl.Close()
	}
	e.netMu.Lock()
	d := e.dht
	e.dht = nil
	e.netMu.Unlock()
	if d != nil {
		d.Close()
	}
	if e.utp != nil {
		_ = e.utp.Close()
	}
	if e.tcp != nil {
		_ = e.tcp.Close()
	}
	e.cl, e.utp, e.tcp = nil, nil, nil
	e.port.Store(0)
	// A new client remembers nothing about any torrent.
	e.stale = map[string]bool{}
}

func (e *Engine) closeStorage() {
	_ = e.pc.Close()
	_ = e.st.close()
}

// noDefaultStorage refuses to open storage: every torrent is added with a
// storage of its own, in its own folder.
type noDefaultStorage struct{}

func (noDefaultStorage) OpenTorrent(context.Context, *metainfo.Info, metainfo.Hash) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{}, errors.New("no folder was given for this torrent")
}
