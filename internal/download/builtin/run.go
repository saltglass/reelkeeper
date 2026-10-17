package builtin

import (
	"cmp"
	"slices"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// poke asks the loop to look at the queue again soon.
func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// run is the engine's heartbeat. Each beat it notes what every running
// torrent has done, then decides which torrents should be running.
func (e *Engine) run() {
	defer close(e.done)
	tick := time.NewTicker(e.settings().Tick)
	defer tick.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-tick.C:
		case <-e.wake:
		}
		e.mu.Lock()
		e.step(time.Now())
		e.mu.Unlock()
	}
}

func (e *Engine) step(now time.Time) {
	if e.last.IsZero() {
		e.last = now
	}
	if dt := now.Sub(e.last); dt >= e.settings().Tick/2 {
		e.sample(now, dt)
		e.last = now
	}
	if now.Sub(e.sweep) >= sweepEvery {
		e.sweep = now
		e.checkFilesLocked()
	}
	e.reconcile(now)
	e.announceDHTs(now)
	if now.Sub(e.flush) >= flushEvery {
		e.flush = now
		if err := e.saveAllLocked(); err != nil {
			e.logger.Warn("couldn't save the built-in client's records", "error", err)
		}
	}
}

// sample notes what the running torrents have done since the last beat: speeds,
// bytes moved, bytes verified, a torrent finishing.
func (e *Engine) sample(now time.Time, dt time.Duration) {
	secs := dt.Seconds()
	for _, en := range e.ents {
		if !en.running() {
			continue
		}
		st := en.t.Stats()
		read, wrote := st.BytesReadUsefulData.Int64(), st.BytesWrittenData.Int64()
		dr, dw := read-en.prevDown, wrote-en.prevUp
		en.prevDown, en.prevUp = read, wrote
		en.liveDown, en.liveUp = read, wrote
		// A smoothed speed: a quarter of the newest second.
		en.downRate += (float64(dr)/secs - en.downRate) * 0.5
		en.upRate += (float64(dw)/secs - en.upRate) * 0.5
		if dr > 0 {
			en.lastRecv = now
		}
		if dw > 0 {
			en.lastSent = now
		}
		en.peers, en.seeds = st.ActivePeers, st.ConnectedSeeders
		if en.info != nil {
			en.have = haveOf(en.t, en.info, st.PiecesComplete)
		}
		if en.done() {
			en.rec.SeedSeconds += int64(secs + 0.5)
			en.dirty = true
		}
	}
	for _, en := range e.ents {
		if en.done() && en.rec.Completed == nil && !en.verifying {
			t := now.UTC()
			en.rec.Completed = &t
			en.dirty = true
			e.logger.Info("a torrent is finished", "torrent", en.rec.Name)
		}
	}
}

// haveOf is the bytes of a running torrent whose pieces are verified.
func haveOf(t *torrent.Torrent, info *metainfo.Info, complete int) int64 {
	n := int64(complete) * info.PieceLength
	if last := info.NumPieces() - 1; last >= 0 && t.PieceState(last).Complete {
		n -= info.PieceLength - info.Piece(last).Length()
	}
	return max(n, 0)
}

// reconcile starts and stops torrents so that the right ones run:
//
//   - A torrent without its details (a magnet link) runs whenever it isn't
//     stopped. It is cheap, and it needs peers to learn what it is.
//   - Of the unfinished ones, the first MaxActiveDownloads in queue order run.
//   - Of the finished ones, the first MaxActiveSeeds in queue order run (all of
//     them when the limit is 0).
//   - Stopped torrents, ones being checked or moved, and ones with a problem
//     don't run. A torrent that doesn't run uses no network at all.
func (e *Engine) reconcile(now time.Time) {
	cfg := e.settings()
	var want = map[*entry]bool{}
	var downloads, seeds []*entry
	for _, en := range e.ents {
		if en.rec.Stopped || en.verifying || en.busy != "" || en.problem != "" || en.missing {
			continue
		}
		switch {
		case !en.rec.HaveInfo:
			want[en] = true
		case en.done():
			seeds = append(seeds, en)
		default:
			downloads = append(downloads, en)
		}
	}
	byPosition := func(a, b *entry) int { return cmp.Compare(a.rec.Position, b.rec.Position) }
	slices.SortFunc(downloads, byPosition)
	slices.SortFunc(seeds, byPosition)
	for i, en := range downloads {
		want[en] = i < max(cfg.MaxActiveDownloads, 1)
	}
	for i, en := range seeds {
		want[en] = cfg.MaxActiveSeeds <= 0 || i < cfg.MaxActiveSeeds
	}
	for _, en := range e.ents {
		switch {
		case want[en] && !en.running():
			e.startRunning(en, now)
		case !want[en] && en.running():
			e.stopRunning(en)
		}
	}
}

// storageFor is where a torrent's files go: plain files, under their own
// names, in the torrent's folder. Which pieces are good is remembered in the
// piece database, so a restart doesn't check everything again.
func (e *Engine) storageFor(folder string) storage.ClientImpl {
	return storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   folder,
		PieceCompletion: keepOpen{e.pc},
		UsePartFiles:    g.Some(false),
		Logger:          e.logger,
	})
}

// keepOpen stops a torrent's storage from closing the shared piece database.
type keepOpen struct{ storage.PieceCompletion }

func (keepOpen) Close() error { return nil }

// startRunning hands a torrent to the client.
func (e *Engine) startRunning(en *entry, now time.Time) {
	spec := &torrent.TorrentSpec{
		AddTorrentOpts: torrent.AddTorrentOpts{
			InfoHash: metainfo.NewHashFromHex(en.rec.Hash),
			Storage:  e.storageFor(en.rec.SavePath),
		},
		DisplayName: en.rec.Name,
	}
	if en.rec.HaveInfo {
		if b, err := e.infoBytes(en); err == nil {
			spec.InfoBytes = b
		} else {
			en.problem = "its saved .torrent file can't be read: " + err.Error()
			return
		}
	}
	// Trackers with an address already; names are resolved through the
	// Network next (resolveTrackers), never by anacrolix itself.
	known, names := splitTrackers(en.rec.Trackers)
	spec.Trackers = known
	t, _, err := e.cl.AddTorrentSpec(spec)
	if err != nil {
		en.problem = "the built-in client couldn't start it: " + err.Error()
		return
	}
	en.t = t
	en.started = now
	en.gone = make(chan struct{})
	en.prevDown, en.prevUp, en.liveDown, en.liveUp = 0, 0, 0, 0
	en.downRate, en.upRate = 0, 0
	en.lastRecv, en.lastSent = time.Time{}, time.Time{}
	if en.rec.Private {
		e.priv.set(en.rec.Hash, true)
	}
	// The client's lock is held when this is called, and stopping a torrent
	// needs it, so the work is done on its own goroutine.
	t.SetOnWriteChunkError(func(err error) { go e.storageFailed(en, t, err) })
	go e.watch(en, t, en.gone, names)
}

// stopRunning takes a torrent away from the client: its connections close,
// it stops announcing, and what it moved this run is added to its record.
func (e *Engine) stopRunning(en *entry) {
	if !en.running() {
		return
	}
	t := en.t
	st := t.Stats()
	if en.info != nil {
		en.have = haveOf(t, en.info, st.PiecesComplete)
	}
	en.rec.Uploaded += st.BytesWrittenData.Int64()
	en.rec.Downloaded += st.BytesReadUsefulData.Int64()
	en.liveUp, en.liveDown, en.prevUp, en.prevDown = 0, 0, 0, 0
	en.downRate, en.upRate, en.peers, en.seeds = 0, 0, 0, 0
	close(en.gone)
	en.t, en.gone = nil, nil
	en.dirty = true
	t.Drop()
}

// settleAll stops every running torrent, for shutdown.
func (e *Engine) settleAll() {
	for _, en := range e.ents {
		e.stopRunning(en)
	}
}

// storageFailed notes that a torrent's files can't be written.
func (e *Engine) storageFailed(en *entry, t *torrent.Torrent, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if en.t != t || en.problem != "" {
		return
	}
	en.problem = "its files can't be written: " + err.Error()
	e.logger.Warn("a torrent's files can't be written", "torrent", en.rec.Name, "error", err)
	e.stopRunning(en)
}

// watch waits for a running torrent's details to arrive (a magnet link has
// none), saves them, and tells the client to download everything.
func (e *Engine) watch(en *entry, t *torrent.Torrent, gone chan struct{}, names []string) {
	if len(names) > 0 {
		go e.resolveTrackers(t, gone, names)
	}
	select {
	case <-gone:
		return
	case <-t.GotInfo():
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if en.t != t {
		return
	}
	if !en.rec.HaveInfo {
		if err := e.learnInfo(en, t); err != nil {
			en.problem = "the torrent's details can't be saved: " + err.Error()
			e.stopRunning(en)
			return
		}
	}
	t.DownloadAll()
	e.poke()
}
