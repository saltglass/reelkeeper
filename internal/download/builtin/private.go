package builtin

import (
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Private torrents (BEP 27) may only find peers through their own tracker: no
// DHT, no peer exchange. anacrolix/torrent doesn't know about the flag (it
// announces every torrent to the DHT, and offers peer exchange to every peer),
// so the engine keeps those two away from private torrents itself:
//
//   - DHT: anacrolix's own announcing is off (Config.PeriodicallyAnnounceTorrentsToDht
//     is false); the engine announces public torrents only (announceDHTs).
//   - Peer exchange: for a private torrent the engine takes ut_pex out of the
//     extended handshake it reads (so it never learns the peer's id for it and
//     can't send any) and out of the one it sends (privatePeerAdded).
//   - Connections that came from the DHT or from peer exchange are closed
//     (a stray DHT node can announce any info hash to us).
//
// A magnet link's torrent can't be known as private until its details arrive,
// so until then it may use the DHT; a private torrent's peers don't hand out
// the details anyway.

// privateSet is the info hashes of private torrents. It has its own lock,
// because anacrolix calls the hooks below with its own lock held, and they
// must never wait for the engine's.
type privateSet struct {
	mu sync.RWMutex
	m  map[metainfo.Hash]struct{}
}

func (s *privateSet) set(hexHash string, private bool) {
	h := metainfo.NewHashFromHex(hexHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !private {
		delete(s.m, h)
		return
	}
	if s.m == nil {
		s.m = map[metainfo.Hash]struct{}{}
	}
	s.m[h] = struct{}{}
}

func (s *privateSet) has(h metainfo.Hash) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.m[h]
	return ok
}

// privateHandshake runs when a peer's extended handshake arrives, before
// anacrolix reads it.
func (e *Engine) privateHandshake(pc *torrent.PeerConn, m *pp.ExtendedHandshakeMessage) {
	if e.priv.has(pc.Torrent().InfoHash()) {
		delete(m.M, pp.ExtensionNamePex)
	}
}

// privatePeerAdded runs for each new peer connection, before our own extended
// handshake goes out.
func (e *Engine) privatePeerAdded(pc *torrent.PeerConn) {
	if !e.priv.has(pc.Torrent().InfoHash()) {
		return
	}
	// Speak only the extension that carries the torrent's details.
	pc.LocalLtepProtocolMap = &torrent.LocalLtepProtocolMap{Index: []pp.ExtensionName{pp.ExtensionNameMetadata}, NumBuiltin: 1}
	switch pc.Discovery {
	case torrent.PeerSourceDhtGetPeers, torrent.PeerSourceDhtAnnouncePeer, torrent.PeerSourcePex:
		_ = pc.Close()
	}
}

// dhtEvery is how often a torrent is announced to the DHT.
const dhtEvery = 15 * time.Minute

// announceDHTs announces public, running torrents to the DHT when it is time.
func (e *Engine) announceDHTs(now time.Time) {
	if !e.settings().DHT {
		return
	}
	for _, en := range e.ents {
		if !en.running() || en.rec.Private || now.Before(en.nextDHT) {
			continue
		}
		en.nextDHT = now.Add(dhtEvery)
		go e.announceDHT(en.t)
	}
}

// defaultAnnounceDHT asks the DHT for peers for a torrent and tells it about us.
func (e *Engine) defaultAnnounceDHT(t *torrent.Torrent) {
	if e.dht == nil {
		return
	}
	_, stop, err := t.AnnounceToDht(torrent.AnacrolixDhtServerWrapper{Server: e.dht})
	if err != nil {
		e.logger.Debug("a DHT announce failed", "error", err)
		return
	}
	// It finishes by itself; this only keeps one from running for ever.
	time.AfterFunc(5*time.Minute, stop)
}
