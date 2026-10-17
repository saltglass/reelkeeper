package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// The built-in client (internal/download/builtin) as a download client.
//
// BuiltinHost owns the one engine for the whole server. Unlike the external
// clients, which are cheap objects made for each call, the engine is a running
// BitTorrent client, so the server starts it when the built-in client is the
// one in use (or is the one a move is leaving), changes its limits as settings
// change, and stops it when it is no longer needed. Builtin is the thin
// Client that talks to whatever engine the host is running.

// engineCloseTimeout is how long shutting the engine down may take.
const engineCloseTimeout = 10 * time.Second

// BuiltinHost starts, changes and stops the built-in engine to match the
// settings.
type BuiltinHost struct {
	dataDir string
	network builtin.Network
	logger  *slog.Logger
	// tune lets tests change the engine's configuration (a loopback address,
	// no DHT, faster timing).
	tune func(*builtin.Config)

	mu  sync.Mutex
	eng *builtin.Engine
	// cfg is what the engine was started with.
	cfg settings.DownloadClient
	// err is why the engine isn't running when it should be.
	err error
}

// NewBuiltinHost makes a host that keeps the engine's records in dataDir/builtin.
// Nothing runs until Apply says so.
func NewBuiltinHost(dataDir string, logger *slog.Logger) *BuiltinHost {
	return &BuiltinHost{dataDir: dataDir, network: builtin.OSNetwork{}, logger: logger}
}

// config turns the settings into the engine's configuration. The DHT and peer
// exchange are on, for public torrents (the engine keeps private ones off both).
func (h *BuiltinHost) config(c settings.DownloadClient) builtin.Config {
	cfg := builtin.Config{
		DataDir: h.dataDir, Folder: c.Folder, ListenPort: c.ListenPort,
		MaxActiveDownloads: c.MaxActiveDownloads, MaxActiveSeeds: c.MaxActiveSeeds,
		DownloadLimitKBs: c.DownloadLimitKBs, UploadLimitKBs: c.UploadLimitKBs,
		DHT: true, PEX: true, Network: h.network, Logger: h.logger,
	}
	if h.tune != nil {
		h.tune(&cfg)
	}
	return cfg
}

// Apply makes the engine match the settings: running when the built-in client
// is the download client, or the one a move is leaving; stopped otherwise.
func (h *BuiltinHost) Apply(cur settings.DownloadClient, mv settings.DownloadMove) {
	var want *settings.DownloadClient
	switch {
	case cur.ClientType() == settings.ClientBuiltin && cur.Configured():
		want = &cur
	case mv.Active && mv.Old.ClientType() == settings.ClientBuiltin && mv.Old.Configured():
		want = &mv.Old
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if want == nil {
		h.err = nil
		h.stopLocked()
		return
	}
	cfg := h.config(*want)
	if h.eng != nil {
		if !h.eng.RestartNeeded(cfg) {
			h.eng.Configure(cfg)
			h.cfg = *want
			return
		}
		h.logger.Info("restarting the built-in client", "port", want.ListenPort)
		h.stopLocked()
	}
	eng, err := builtin.Open(cfg)
	if err != nil {
		h.err = err
		h.logger.Error("the built-in download client can't start", "error", err)
		return
	}
	h.eng, h.cfg, h.err = eng, *want, nil
	h.logger.Info("the built-in download client is running", "port", eng.Port(), "folder", want.Folder)
}

func (h *BuiltinHost) stopLocked() {
	if h.eng == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineCloseTimeout)
	defer cancel()
	if err := h.eng.Close(ctx); err != nil {
		h.logger.Warn("the built-in download client didn't close cleanly", "error", err)
	}
	h.eng = nil
}

// Close stops the engine, saving what it knows (the server's shutdown).
func (h *BuiltinHost) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = nil
	h.stopLocked()
}

// running is the engine, or why there isn't one.
func (h *BuiltinHost) running() (*builtin.Engine, int, error) {
	if h == nil {
		return nil, 0, &unreachableError{msg: "the built-in download client isn't available here"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.eng != nil {
		return h.eng, h.eng.Port(), nil
	}
	msg := "the built-in download client isn't running"
	if h.err != nil {
		msg += ": " + h.err.Error()
	}
	return nil, 0, &unreachableError{msg: msg, err: h.err}
}

// canListen reports whether the built-in client could open its port, using the
// engine's own network (so the check goes where the engine's traffic goes).
func (h *BuiltinHost) canListen(port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr := net.JoinHostPort("", strconv.Itoa(port))
	l, err := h.network.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("the built-in client can't listen on TCP port %d (is something else using it?): %w", port, err)
	}
	_ = l.Close()
	pc, err := h.network.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("the built-in client can't listen on UDP port %d (is something else using it?): %w", port, err)
	}
	_ = pc.Close()
	return nil
}

// Builtin is the built-in torrent client as a download client.
type Builtin struct {
	cfg  settings.DownloadClient
	host *BuiltinHost
}

// Name is what messages call this client.
func (b *Builtin) Name() string { return "Built-in" }

// engine is the running engine, or an error that says it isn't.
func (b *Builtin) engine() (*builtin.Engine, error) {
	e, _, err := b.host.running()
	return e, err
}

// Version is also the connection test: there is nothing to connect to, so it
// checks what the client needs from outside, that the download folder can be
// written to and the port can be opened, and returns the engine's version.
func (b *Builtin) Version(_ context.Context) (string, error) {
	if b.cfg.Folder == "" {
		return "", errors.New("the built-in client needs a download folder")
	}
	if err := settings.CheckWritableFolder(b.cfg.Folder); err != nil {
		return "", err
	}
	if b.host == nil {
		return "", &unreachableError{msg: "the built-in download client isn't available here"}
	}
	// Our own engine already holds the port when it is running with it.
	if e, port, _ := b.host.running(); e == nil || port != b.cfg.ListenPort {
		if err := b.host.canListen(b.cfg.ListenPort); err != nil {
			return "", err
		}
	}
	return builtin.Version(), nil
}

// EnsureCategory needs nothing: the engine keeps categories as labels.
func (b *Builtin) EnsureCategory(_ context.Context, name, _ string) error {
	if name == "" {
		return errors.New("a category is required")
	}
	return nil
}

// MoveCategory gives torrents another of Reelkeeper's categories.
func (b *Builtin) MoveCategory(_ context.Context, from, to string, hashes []string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.MoveCategory(from, to, hashes)
}

// Add adds a torrent and returns its info hash. For a torrent moving here
// (FilesIn), the files are checked where they are now, and SetFolder moves them
// to the folder they belong in afterwards.
func (b *Builtin) Add(_ context.Context, a Add) (string, error) {
	e, err := b.engine()
	if err != nil {
		return "", err
	}
	folder := a.SavePath
	if a.FilesIn != "" {
		folder = a.FilesIn
	}
	return e.Add(builtin.AddRequest{
		Magnet: a.Magnet, Torrent: a.Torrent, Category: a.Category, Tags: a.Tags, SavePath: folder, Stopped: a.Stopped,
	})
}

// Torrents lists the torrents in a category.
func (b *Builtin) Torrents(_ context.Context, category string) ([]Torrent, error) {
	e, err := b.engine()
	if err != nil {
		return nil, err
	}
	sts, err := e.List(category)
	if err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(sts))
	for _, s := range sts {
		out = append(out, Torrent{
			Hash: s.Hash, Name: s.Name, Category: s.Category, State: s.State, Progress: s.Progress,
			Size: s.Size, Left: s.Left, Speed: s.Speed, ETA: s.ETA, SavePath: s.SavePath, ContentPath: s.ContentPath,
			Ratio: s.Ratio, SeedingTime: s.SeedingTime, AddedOn: s.Added, CompletedOn: s.Completed,
			Seeds: s.Seeds, SwarmSeeds: s.SwarmSeeds, Peers: s.Peers, Verified: s.Have, Uploaded: s.Uploaded,
			Tags: s.Tags, Message: s.Message,
		})
	}
	return out, nil
}

// Remove removes torrents, and their files (inside their own folder) when
// deleteFiles is set.
func (b *Builtin) Remove(_ context.Context, category string, hashes []string, deleteFiles bool) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.Remove(category, hashes, deleteFiles)
}

// NoShareLimits does what the others do apart from changing limits: the built-in
// client has no seeding limits of its own (only Reelkeeper's cleanup, and the
// cap on how many seed at once, stop a torrent), but the call also starts the
// torrents, as it does in the other clients, and a move relies on that.
func (b *Builtin) NoShareLimits(ctx context.Context, category string, hashes []string) error {
	return b.Start(ctx, category, hashes)
}

// TopPriority moves torrents to the front of the queue.
func (b *Builtin) TopPriority(_ context.Context, category string, hashes []string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.Top(category, hashes)
}

// Stop pauses torrents.
func (b *Builtin) Stop(_ context.Context, category string, hashes []string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.Stop(category, hashes)
}

// Start resumes torrents.
func (b *Builtin) Start(_ context.Context, category string, hashes []string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.Start(category, hashes)
}

// TrackerMessages are what the trackers last said that was a failure.
func (b *Builtin) TrackerMessages(_ context.Context, category, hash string) ([]string, error) {
	e, err := b.engine()
	if err != nil {
		return nil, err
	}
	return e.TrackerMessages(category, hash)
}

// Source gives the torrent back: its saved .torrent file, or the magnet link
// while it has no details yet.
func (b *Builtin) Source(_ context.Context, category, hash string) (Source, error) {
	e, err := b.engine()
	if err != nil {
		return Source{}, err
	}
	s, err := e.Source(category, hash)
	if err != nil {
		return Source{}, &NoSourceError{Reason: capitalizeFirst(err.Error()) + "."}
	}
	return Source{Torrent: s.Torrent, Magnet: s.Magnet}, nil
}

// Layout says where the torrent's files are.
func (b *Builtin) Layout(_ context.Context, category, hash string) (Layout, error) {
	e, err := b.engine()
	if err != nil {
		return Layout{}, err
	}
	l, err := e.Layout(category, hash)
	if err != nil {
		return Layout{}, err
	}
	out := Layout{Folder: l.Folder, Look: []string{l.Folder}}
	for _, f := range l.Files {
		out.Files = append(out.Files, File{Name: f.Name, Size: f.Size})
	}
	return out, nil
}

// Recheck checks the torrent's files against its pieces.
func (b *Builtin) Recheck(_ context.Context, category, hash string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.Recheck(category, hash)
}

// SetFolder moves the torrent's files to the folder it belongs in.
func (b *Builtin) SetFolder(_ context.Context, category, hash, folder string) error {
	e, err := b.engine()
	if err != nil {
		return err
	}
	return e.SetFolder(category, hash, folder)
}

var _ Client = (*Builtin)(nil)
