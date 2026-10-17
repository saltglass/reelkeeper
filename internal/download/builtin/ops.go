package builtin

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// AddRequest is a torrent to add.
type AddRequest struct {
	// Magnet or Torrent (the .torrent file's bytes) says which torrent.
	Magnet  string
	Torrent []byte
	// Category is Reelkeeper's category for it; it is how the engine tells
	// Reelkeeper's torrents from any others (there are none, but every call
	// checks).
	Category string
	// Tags are extra labels, such as the indexer's name.
	Tags []string
	// SavePath is the folder for its files; empty means the engine's folder.
	SavePath string
	// Stopped adds it without starting it.
	Stopped bool
}

// Add adds a torrent and returns its info hash in lower-case hex. A torrent
// the engine already has in the same category counts as added.
func (e *Engine) Add(req AddRequest) (string, error) {
	if req.Category == "" {
		return "", errors.New("a category is required")
	}
	r := record{Category: req.Category, Tags: slices.Clone(req.Tags), Added: time.Now().UTC(), Stopped: req.Stopped}
	var mi *metainfo.MetaInfo
	var info *metainfo.Info
	switch {
	case len(req.Torrent) > 0:
		var err error
		if mi, err = metainfo.Load(bytes.NewReader(req.Torrent)); err != nil {
			return "", fmt.Errorf("that isn't a torrent file: %w", err)
		}
		i, err := mi.UnmarshalInfo()
		if err != nil {
			return "", fmt.Errorf("the torrent file is damaged: %w", err)
		}
		if !i.HasV1() {
			return "", errors.New("the built-in client can't use a BitTorrent v2-only torrent")
		}
		info = &i
		r.Hash = mi.HashInfoBytes().HexString()
		r.HaveInfo, r.Name, r.Size = true, i.BestName(), i.TotalLength()
		r.Private = i.Private != nil && *i.Private
		r.Trackers = mi.UpvertedAnnounceList()
	case req.Magnet != "":
		m, err := metainfo.ParseMagnetV2Uri(req.Magnet)
		if err != nil || !m.InfoHash.Ok {
			return "", errors.New("the magnet link has no BitTorrent info hash")
		}
		r.Hash, r.Name, r.Magnet = m.InfoHash.Value.HexString(), m.DisplayName, req.Magnet
		if len(m.Trackers) > 0 {
			r.Trackers = [][]string{m.Trackers}
		}
	default:
		return "", errors.New("nothing to add: no magnet link or torrent file")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shut {
		return "", errClosed
	}
	if have, ok := e.ents[r.Hash]; ok {
		if have.rec.Category != req.Category {
			return "", fmt.Errorf("the built-in client already has this torrent, outside the category %q, so Reelkeeper leaves it alone", req.Category)
		}
		return r.Hash, nil
	}
	r.SavePath = req.SavePath
	if r.SavePath == "" {
		r.SavePath = e.settings().Folder
	}
	if !filepath.IsAbs(r.SavePath) {
		return "", fmt.Errorf("%q isn't a full path", r.SavePath)
	}
	if err := os.MkdirAll(r.SavePath, 0o755); err != nil { //nolint:gosec // a download folder other apps (Emby, a NAS user) may read
		return "", fmt.Errorf("can't create the download folder %s: %w", r.SavePath, err)
	}
	r.Position = e.lastPosition() + 1
	if mi != nil {
		if err := writeFileAtomic(e.torrentPath(r.Hash), req.Torrent); err != nil {
			return "", fmt.Errorf("can't keep a copy of the torrent: %w", err)
		}
	}
	en := &entry{rec: r, info: info}
	if info != nil {
		en.have = e.verifiedBytes(en)
	}
	if err := e.st.save(r); err != nil {
		return "", fmt.Errorf("can't save the torrent: %w", err)
	}
	if r.Private {
		e.priv.set(r.Hash, true)
	}
	e.ents[r.Hash] = en
	e.poke()
	return r.Hash, nil
}

var errClosed = errors.New("the built-in client is shutting down")

// lastPosition is the biggest queue position in use (0 when the queue is empty).
func (e *Engine) lastPosition() int64 {
	var m int64
	for _, en := range e.ents {
		m = max(m, en.rec.Position)
	}
	return m
}

func (e *Engine) firstPosition() int64 {
	m := int64(1)
	for _, en := range e.ents {
		m = min(m, en.rec.Position)
	}
	return m
}

// writeFileAtomic writes a file so that it is either whole or not there.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// infoBytes is the info section of a torrent's saved .torrent file.
func (e *Engine) infoBytes(en *entry) ([]byte, error) {
	b, err := os.ReadFile(e.torrentPath(en.rec.Hash))
	if err != nil {
		return nil, err
	}
	mi, err := metainfo.Load(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return mi.InfoBytes, nil
}

// learnInfo saves the details a magnet link's torrent has just received, as a
// .torrent file, and forgets the magnet link.
func (e *Engine) learnInfo(en *entry, t *torrent.Torrent) error {
	info := t.Info()
	if info == nil {
		return errors.New("no details yet")
	}
	mi := t.Metainfo()
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return err
	}
	if err := writeFileAtomic(e.torrentPath(en.rec.Hash), buf.Bytes()); err != nil {
		return err
	}
	en.info = info
	en.rec.HaveInfo, en.rec.Magnet = true, ""
	en.rec.Name, en.rec.Size = info.BestName(), info.TotalLength()
	en.rec.Private = info.Private != nil && *info.Private
	if tiers := mi.UpvertedAnnounceList(); len(tiers) > 0 {
		en.rec.Trackers = tiers
	}
	en.have = haveOf(t, info, t.Stats().PiecesComplete)
	if en.rec.Private {
		e.priv.set(en.rec.Hash, true)
	}
	return e.st.save(en.rec)
}

// ours finds the torrents with these hashes, which must all be in the category.
// Nothing else is ever touched.
func (e *Engine) ours(category string, hashes []string) ([]*entry, error) {
	out := make([]*entry, 0, len(hashes))
	for _, h := range hashes {
		en, ok := e.ents[strings.ToLower(h)]
		if !ok || en.rec.Category != category {
			return nil, fmt.Errorf("torrent %s isn't one of Reelkeeper's in %s, so Reelkeeper won't change it", h, category)
		}
		out = append(out, en)
	}
	return out, nil
}

// List gives the torrents in a category, oldest first. The category must not be
// empty: the engine never lists other torrents.
func (e *Engine) List(category string) ([]Status, error) {
	if category == "" {
		return nil, errors.New("a category is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	var out []Status
	for _, en := range e.ents {
		if en.rec.Category == category {
			out = append(out, e.statusLocked(en, now))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := e.ents[out[i].Hash].rec, e.ents[out[j].Hash].rec
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		return a.Hash < b.Hash
	})
	return out, nil
}

// Stop pauses torrents; Start resumes them. Starting also forgets an error,
// so a torrent that couldn't write its files tries again.
func (e *Engine) Stop(category string, hashes []string) error {
	return e.setStopped(category, hashes, true)
}

// Start resumes torrents (see Stop).
func (e *Engine) Start(category string, hashes []string) error {
	return e.setStopped(category, hashes, false)
}

func (e *Engine) setStopped(category string, hashes []string, stopped bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, hashes)
	if err != nil {
		return err
	}
	for _, en := range ens {
		en.rec.Stopped = stopped
		if !stopped {
			en.problem = ""
		}
		en.dirty = true
	}
	e.reconcile(time.Now())
	return e.saveDirtyLocked()
}

// Top moves torrents to the front of the queue, keeping their order among
// themselves.
func (e *Engine) Top(category string, hashes []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, hashes)
	if err != nil {
		return err
	}
	first := e.firstPosition()
	for i, en := range ens {
		en.rec.Position = first - int64(len(ens)) + int64(i)
		en.dirty = true
	}
	e.reconcile(time.Now())
	return e.saveDirtyLocked()
}

// MoveCategory changes the category of torrents that are in from.
func (e *Engine) MoveCategory(from, to string, hashes []string) error {
	if to == "" {
		return errors.New("a category is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(from, hashes)
	if err != nil {
		return err
	}
	for _, en := range ens {
		en.rec.Category = to
		en.dirty = true
	}
	return e.saveDirtyLocked()
}

// Remove drops torrents from the engine, and deletes their files too when
// deleteFiles is set. Files are only ever deleted inside the torrent's own
// folder, and only the ones the torrent lists.
func (e *Engine) Remove(category string, hashes []string, deleteFiles bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, hashes)
	if err != nil {
		return err
	}
	for _, en := range ens {
		if en.busy != "" {
			return fmt.Errorf("the built-in client is busy with %s; try again in a moment", en.rec.Name)
		}
	}
	for _, en := range ens {
		e.stopRunning(en)
		if en.cancel != nil {
			close(en.cancel) // a check in progress stops
			en.cancel = nil
		}
		// Not listed any more from this moment, whatever happens next.
		delete(e.ents, en.rec.Hash)
		if err := e.st.remove(en.rec.Hash); err != nil {
			return err
		}
		e.priv.set(en.rec.Hash, false)
		e.trk.forget(en.rec.Hash)
		if deleteFiles {
			e.deleteFiles(en)
		}
		_ = os.Remove(e.torrentPath(en.rec.Hash))
	}
	return nil
}

// deleteFiles deletes the files a torrent lists from its folder, then the
// folders that are left empty (up to, never including, the folder itself).
func (e *Engine) deleteFiles(en *entry) {
	if en.info == nil || !filepath.IsAbs(en.rec.SavePath) || filepath.Dir(en.rec.SavePath) == en.rec.SavePath {
		return
	}
	var dirs []string
	for _, f := range fileList(en.info) {
		p, ok := inside(en.rec.SavePath, f.Name)
		if !ok {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.logger.Warn("couldn't delete a file of a removed torrent", "file", p, "error", err)
		}
		for d := filepath.Dir(p); d != en.rec.SavePath && strings.HasPrefix(d, en.rec.SavePath); d = filepath.Dir(d) {
			dirs = append(dirs, d)
		}
	}
	// Deepest first; a folder with anything else in it stays.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	// Forget the pieces, so the same torrent added again starts from nothing.
	e.forgetPieces(en)
}

// TrackerMessages are what the torrent's trackers last said that was a
// failure, such as "Unregistered torrent".
func (e *Engine) TrackerMessages(category, hash string) ([]string, error) {
	e.mu.Lock()
	_, err := e.ours(category, []string{hash})
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return e.trk.messages(strings.ToLower(hash)), nil
}

// Source is what another client needs to be given this torrent: its .torrent
// file, or the magnet link while the details aren't known yet.
type Source struct {
	Torrent []byte
	Magnet  string
}

// Source gives the torrent back.
func (e *Engine) Source(category, hash string) (Source, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, []string{hash})
	if err != nil {
		return Source{}, err
	}
	en := ens[0]
	if en.rec.HaveInfo {
		b, err := os.ReadFile(e.torrentPath(en.rec.Hash))
		if err != nil {
			return Source{}, fmt.Errorf("the built-in client's copy of the .torrent file can't be read: %w", err)
		}
		return Source{Torrent: b}, nil
	}
	if en.rec.Magnet == "" {
		return Source{}, errors.New("the built-in client has neither the .torrent file nor the magnet link")
	}
	return Source{Magnet: en.rec.Magnet}, nil
}

// Layout says where a torrent's files are.
type Layout struct {
	// Files are named relative to Folder: a torrent of several files has its
	// own folder first. Empty until the torrent's details are known.
	Files []FileEntry
	// Folder is the folder the torrent is saved in.
	Folder string
}

// FileEntry is one file of a torrent.
type FileEntry struct {
	Name string
	Size int64
}

// Layout gives the torrent's files and folder.
func (e *Engine) Layout(category, hash string) (Layout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, []string{hash})
	if err != nil {
		return Layout{}, err
	}
	l := Layout{Folder: ens[0].rec.SavePath}
	if ens[0].info != nil {
		for _, f := range fileList(ens[0].info) {
			l.Files = append(l.Files, FileEntry(f))
		}
	}
	return l, nil
}
