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
		read, wrote := st.BytesReadUsefulData.Int64()-en.baseDown, st.BytesWrittenData.Int64()-en.baseUp
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
//     don't run. A torrent that doesn't run has no connections and moves no data
//     (a paused one that was running is parked, see parkRunning).
//   - While the engine is held, or its network's gate is closed (the VPN is
//     down), none run.
func (e *Engine) reconcile(now time.Time) {
	if e.blockedLocked() {
		// Held, or the VPN is down: nothing runs, and what was running stops.
		// Nobody's paused flags are touched, so when this ends the queue rules
		// below start exactly what ran before.
		e.suspend()
		return
	}
	if e.cl == nil && !e.resume(now) {
		return
	}
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
	// A torrent the client has dropped and is now to run again needs a client
	// that has never seen it (see stopRunning).
	for _, en := range e.ents {
		if want[en] && !en.running() && en.parked == nil && e.stale[en.rec.Hash] {
			e.suspend()
			if !e.resume(now) {
				return
			}
			break
		}
	}
	for _, en := range e.ents {
		switch {
		case want[en] && !en.running():
			e.startRunning(en, now)
		case !want[en] && en.running():
			if en.verifying || en.busy != "" || en.problem != "" || en.missing {
				e.stopRunning(en) // its files are being touched or are wrong
			} else {
				e.parkRunning(en) // waiting its turn, or paused: no need to start over
			}
		}
	}
}

// suspend stops every torrent and closes the client and all its sockets, so
// that while the engine is blocked it holds no connection and no open socket
// of any kind.
func (e *Engine) suspend() {
	for _, en := range e.ents {
		e.stopRunning(en)
	}
	if e.cl != nil {
		e.logger.Info("the built-in client has stopped everything and closed its sockets")
		e.closeNetwork()
	}
}

// resumeRetry is how long the engine waits before trying again to make its
// sockets after that failed.
const resumeRetry = 5 * time.Second

// resume makes the client and its sockets again after a block. It reports
// whether the engine can now run torrents.
func (e *Engine) resume(now time.Time) bool {
	if now.Sub(e.resumeTried) < resumeRetry {
		return false
	}
	e.resumeTried = now
	if err := e.startClient(); err != nil {
		e.logger.Warn("the built-in client can't start its network yet; it will try again", "error", err)
		return false
	}
	e.logger.Info("the built-in client is running again")
	return true
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
	if en.parked != nil {
		e.unparkRunning(en, now)
		return
	}
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
	en.baseDown, en.baseUp = 0, 0
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

// stopRunning takes a torrent out of the client for good (or until it is
// added again): its connections close, it stops announcing (the client says
// "stopped" to its trackers), and what it moved this run is added to its
// record. Used when its files are about to be touched, it is removed, or the
// client closes. Pausing and queueing use parkRunning instead.
//
// A torrent dropped this way is "stale": anacrolix/torrent 1.61 keeps its
// tracker bookkeeping per (tracker, info hash) for the life of the client and
// never forgets it, and hands every later announce result to the torrent that
// was first added, which is gone. A torrent added again to the same client
// would then never be told about the peers its trackers know, and tracker-only
// (private) torrents would never get peers. So a stale torrent is only ever
// started in a new client (reconcile).
func (e *Engine) stopRunning(en *entry) {
	if en.running() {
		e.accountRun(en)
		close(en.gone)
		t := en.t
		en.t, en.gone = nil, nil
		en.dirty = true
		t.Drop()
		e.stale[en.rec.Hash] = true
	}
	if en.parked != nil {
		en.parked.Drop()
		en.parked = nil
		e.stale[en.rec.Hash] = true
	}
}

// accountRun notes what a running torrent has done, before it stops.
func (e *Engine) accountRun(en *entry) {
	st := en.t.Stats()
	if en.info != nil {
		en.have = haveOf(en.t, en.info, st.PiecesComplete)
	}
	en.rec.Uploaded += st.BytesWrittenData.Int64() - en.baseUp
	en.rec.Downloaded += st.BytesReadUsefulData.Int64() - en.baseDown
	en.liveUp, en.liveDown, en.prevUp, en.prevDown = 0, 0, 0, 0
	en.downRate, en.upRate, en.peers, en.seeds = 0, 0, 0, 0
}

// parkRunning pauses a torrent where it is: it stays in the client, but its
// connections are closed, it takes no new ones and moves no data either way. A
// torrent that is paused, pushed back in the queue or rotated out of the
// seeding slots is parked, not dropped, so that when it runs again its trackers
// still reach it (see stopRunning). The one thing a parked torrent still does is
// announce to its trackers now and then, as anacrolix gives no way to stop that
// without dropping it.
func (e *Engine) parkRunning(en *entry) {
	if !en.running() {
		return
	}
	t := en.t
	e.accountRun(en)
	close(en.gone)
	en.t, en.gone = nil, nil
	en.dirty = true
	t.DisallowDataDownload()
	t.DisallowDataUpload()
	en.parkedMax = t.SetMaxEstablishedConns(0) // closes what is connected
	en.parked = t
}

// unparkRunning runs a parked torrent again.
func (e *Engine) unparkRunning(en *entry, now time.Time) {
	t := en.parked
	en.parked = nil
	// Giving the connection limit back also dials the peers it already knew, at
	// once (its trackers may not be asked for new ones for a while).
	t.SetMaxEstablishedConns(en.parkedMax)
	t.AllowDataDownload()
	t.AllowDataUpload()
	st := t.Stats()
	en.baseDown, en.baseUp = st.BytesReadUsefulData.Int64(), st.BytesWrittenData.Int64()
	en.t = t
	en.started = now
	en.gone = make(chan struct{})
	en.prevDown, en.prevUp, en.liveDown, en.liveUp = 0, 0, 0, 0
	en.downRate, en.upRate = 0, 0
	en.lastRecv, en.lastSent = time.Time{}, time.Time{}
	go e.watch(en, t, en.gone, nil)
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
