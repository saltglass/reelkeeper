package builtin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

const torrentsDir = "torrents"

// entry is one torrent the engine knows about: its saved record plus what is
// true of it right now. The engine's mutex guards all of it.
type entry struct {
	rec record
	// info is the torrent's details, once known.
	info *metainfo.Info
	// t is the anacrolix torrent, while the torrent is handed to the client
	// (running); nil while it is queued, stopped or being checked. A torrent
	// that isn't running has no connections and moves no data (one that was paused in
	// place, see parked, still announces to its trackers now and then).
	t       *torrent.Torrent
	started time.Time
	gone    chan struct{} // closed when the torrent stops running; ends its watcher
	// parked is the torrent while it is paused in place (see parkRunning): still
	// in the client, with no connections and no data moving either way. parkedMax
	// is the connection limit to give it back when it runs again. baseDown and
	// baseUp are what the client's counters already held when it last started
	// running (a parked torrent's counters carry on from the run before).
	parked           *torrent.Torrent
	parkedMax        int
	baseDown, baseUp int64

	// have is the bytes verified so far.
	have int64
	// verifying: the engine is checking its files against the pieces; cancel
	// ends the check (the torrent was removed).
	verifying bool
	cancel    chan struct{}
	// busy names something the engine is doing to its files ("moving").
	busy string
	// problem is set when its files can't be read or written (Error) and
	// missing when finished files have gone (MissingFiles). Start or a
	// recheck clears them.
	problem string
	missing bool

	// What this run has moved, on top of what the record holds.
	liveUp, liveDown int64
	prevUp, prevDown int64
	upRate, downRate float64
	lastRecv         time.Time
	lastSent         time.Time
	peers, seeds     int
	nextDHT          time.Time
	dirty            bool
}

func (en *entry) running() bool { return en.t != nil }

func (en *entry) complete() bool { return en.rec.HaveInfo && en.rec.Size > 0 && en.have >= en.rec.Size }

// emptyTorrent is a torrent of no bytes at all (it still finishes).
func (en *entry) hasNoData() bool { return en.rec.HaveInfo && en.rec.Size == 0 }

func (en *entry) done() bool { return en.complete() || en.hasNoData() }

func (e *Engine) torrentPath(hash string) string {
	return filepath.Join(e.dir, torrentsDir, hash+".torrent")
}

// load brings back the torrents in the records. Finished ones are not
// checked again: the piece database says which pieces are good.
func (e *Engine) load() error {
	recs, err := e.st.all()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range recs {
		en := &entry{rec: r}
		if r.HaveInfo {
			if err := e.readInfo(en); err != nil {
				e.logger.Warn("a saved torrent can't be read; it is listed with an error", "hash", r.Hash, "error", err)
				en.problem = "its saved .torrent file can't be read: " + err.Error()
			}
		}
		if en.info != nil {
			en.have = e.verifiedBytes(en)
		}
		if en.rec.Private {
			e.priv.set(r.Hash, true)
		}
		e.ents[r.Hash] = en
	}
	e.checkFilesLocked()
	return nil
}

// readInfo reads the saved .torrent file into en.info.
func (e *Engine) readInfo(en *entry) error {
	b, err := os.ReadFile(e.torrentPath(en.rec.Hash))
	if err != nil {
		return err
	}
	mi, err := metainfo.Load(bytes.NewReader(b))
	if err != nil {
		return err
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return err
	}
	en.info = &info
	return nil
}

// verifiedBytes is how many bytes of a torrent the piece database says are
// good, without reading any file.
func (e *Engine) verifiedBytes(en *entry) int64 {
	var n int64
	ih := metainfo.NewHashFromHex(en.rec.Hash)
	for i := range en.info.NumPieces() {
		c, err := e.pc.Get(metainfo.PieceKey{InfoHash: ih, Index: i})
		if err == nil && c.Ok && c.Complete {
			n += en.info.Piece(i).Length()
		}
	}
	return n
}

// ---- what the torrents look like from outside

// Status is a torrent as the outside world sees it.
type Status struct {
	Hash     string
	Name     string
	Category string
	Tags     []string
	State    rktorrent.State
	// Message says what is wrong, for the Error state.
	Message  string
	Progress float64
	// Size is the torrent's bytes, Have the verified ones, Left the rest.
	Size, Have, Left int64
	// Speed is the download speed in bytes per second.
	Speed       int64
	ETA         time.Duration
	SavePath    string
	ContentPath string
	Ratio       float64
	SeedingTime time.Duration
	Added       time.Time
	Completed   *time.Time
	Seeds       int
	SwarmSeeds  int
	Peers       int
	Uploaded    int64
}

func (e *Engine) statusLocked(en *entry, now time.Time) Status {
	r := en.rec
	s := Status{
		Hash: r.Hash, Name: r.Name, Category: r.Category, Tags: append([]string(nil), r.Tags...),
		Size: r.Size, Have: en.have, SavePath: r.SavePath, Added: r.Added, Completed: r.Completed,
		Uploaded: en.uploaded(), SeedingTime: time.Duration(r.SeedSeconds) * time.Second,
		Peers: en.peers, Seeds: en.seeds, SwarmSeeds: e.trk.swarmSeeds(r.Hash),
		Message: en.problem,
	}
	if s.Name == "" {
		s.Name = r.Hash
	}
	s.ContentPath = filepath.Join(r.SavePath, s.Name)
	if r.Size > 0 {
		s.Left = max(r.Size-en.have, 0)
		s.Progress = min(float64(en.have)/float64(r.Size), 1)
	} else if en.hasNoData() {
		s.Progress = 1
	}
	if en.running() && !en.done() {
		s.Speed = int64(en.downRate)
		if s.Speed > 0 {
			s.ETA = time.Duration(float64(s.Left) / en.downRate * float64(time.Second))
		}
	}
	// Uploaded over what was downloaded, or over its size for a torrent that
	// arrived finished.
	denom := en.downloaded()
	if denom == 0 {
		denom = en.have
	}
	if denom > 0 {
		s.Ratio = float64(s.Uploaded) / float64(denom)
	}
	s.State = e.stateLocked(en, now)
	return s
}

func (en *entry) uploaded() int64   { return en.rec.Uploaded + en.liveUp }
func (en *entry) downloaded() int64 { return en.rec.Downloaded + en.liveDown }

// stateLocked translates what the engine knows into Reelkeeper's own states
// (internal/torrent), with the same meanings the other clients' states have:
//
//   - Metadata: no details yet (a magnet link), and not stopped.
//   - Checking: its files are being checked against the pieces.
//   - Moving: its files are being moved to another folder.
//   - Error and MissingFiles: its files can't be written, or have gone.
//   - Paused / SeedingPaused: someone stopped it, unfinished or finished.
//   - Queued: unfinished and waiting for a download slot (or for the VPN).
//   - Seeding: finished, whether it is uploading or waiting for one of the
//     limited seeding slots (qBittorrent's "queuedUP" counts as seeding too).
//   - SeedingStalled: finished and uploading, but nobody has been served
//     for StalledAfter.
//   - Downloading / Stalled: receiving data, or nothing has arrived for
//     StalledAfter (qBittorrent's "stalledDL").
func (e *Engine) stateLocked(en *entry, now time.Time) rktorrent.State {
	stalled := e.settings().StalledAfter
	// While everything is held (the VPN is down), unfinished torrents wait:
	// they read as Queued, not as stuck without details, so cleanup's
	// "stuck" rules don't count a hold against them.
	blocked := e.blockedLocked() && !en.running()
	switch {
	case en.verifying:
		return rktorrent.Checking
	case en.busy != "":
		return rktorrent.Moving
	case en.missing:
		return rktorrent.MissingFiles
	case en.problem != "":
		return rktorrent.Error
	case en.rec.Stopped:
		if en.done() {
			return rktorrent.SeedingPaused
		}
		return rktorrent.Paused
	case blocked && !en.done():
		return rktorrent.Queued
	case !en.rec.HaveInfo:
		return rktorrent.Metadata
	case en.done():
		if en.running() && now.Sub(latest(en.lastSent, en.started)) >= stalled {
			return rktorrent.SeedingStalled
		}
		return rktorrent.Seeding
	case !en.running():
		return rktorrent.Queued
	case now.Sub(latest(en.lastRecv, en.started)) >= stalled:
		return rktorrent.Stalled
	}
	return rktorrent.Downloading
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ---- files

// fileList is the torrent's files, named as the other clients name them:
// relative to the save path, with the torrent's own name first (the folder of
// a torrent of several files, the file itself for a torrent of one). This is
// also where anacrolix's file storage puts them.
func fileList(info *metainfo.Info) []fileInfo {
	var out []fileInfo
	for _, f := range info.UpvertedFiles() {
		parts := append([]string{info.BestName()}, f.BestPath()...)
		out = append(out, fileInfo{Name: filepath.ToSlash(filepath.Join(parts...)), Size: f.Length})
	}
	return out
}

type fileInfo struct {
	Name string
	Size int64
}

// inside joins a torrent's file name onto its folder and says whether the
// result is still inside that folder (a torrent file with ".." in its names
// must never reach outside it).
func inside(folder, name string) (string, bool) {
	p := filepath.Join(folder, filepath.FromSlash(name))
	rel, err := filepath.Rel(folder, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// filesPresent reports whether every file of a finished torrent is on disk at
// its full size, and names the first that isn't.
func (e *Engine) filesPresent(en *entry) (bool, string) {
	if en.info == nil {
		return true, ""
	}
	for _, f := range fileList(en.info) {
		p, ok := inside(en.rec.SavePath, f.Name)
		if !ok {
			return false, f.Name
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() < f.Size {
			return false, f.Name
		}
	}
	return true, ""
}

// checkFilesLocked looks for finished torrents whose files have gone (or have
// come back).
func (e *Engine) checkFilesLocked() {
	for _, en := range e.ents {
		if en.rec.Completed == nil || en.verifying || en.busy != "" || en.info == nil {
			continue
		}
		ok, name := e.filesPresent(en)
		switch {
		case !ok && !en.missing:
			en.missing = true
			e.logger.Warn("a finished torrent's files have gone", "torrent", en.rec.Name, "file", name)
			e.stopRunning(en)
		case ok && en.missing:
			en.missing = false
		}
	}
}
