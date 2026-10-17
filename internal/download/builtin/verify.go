package builtin

import (
	"context"
	"crypto/sha1" //nolint:gosec // BitTorrent v1 pieces are hashed with SHA-1 by definition
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// saveAllLocked writes every record. saveDirtyLocked writes the ones that
// changed since the last write.
func (e *Engine) saveAllLocked() error {
	rs := make([]record, 0, len(e.ents))
	for _, en := range e.ents {
		rs = append(rs, e.recordNow(en))
		en.dirty = false
	}
	return e.st.save(rs...)
}

func (e *Engine) saveDirtyLocked() error {
	var rs []record
	for _, en := range e.ents {
		if en.dirty {
			rs = append(rs, e.recordNow(en))
			en.dirty = false
		}
	}
	return e.st.save(rs...)
}

// recordNow is the torrent's record with what the current run has moved added,
// so a crash loses little.
func (e *Engine) recordNow(en *entry) record {
	r := en.rec
	r.Uploaded += en.liveUp
	r.Downloaded += en.liveDown
	return r
}

// forgetPieces marks every piece of a torrent as not good.
func (e *Engine) forgetPieces(en *entry) {
	if en.info == nil {
		return
	}
	ih := metainfo.NewHashFromHex(en.rec.Hash)
	for i := range en.info.NumPieces() {
		_ = e.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, false)
	}
	en.have = 0
}

// Recheck checks a torrent's files against its pieces, from the files
// themselves; it starts at once, and the torrent shows as Checking until it is
// done. The torrent isn't on the network meanwhile.
func (e *Engine) Recheck(category, hash string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ens, err := e.ours(category, []string{hash})
	if err != nil {
		return err
	}
	en := ens[0]
	if en.verifying || en.busy != "" {
		return nil
	}
	if !en.rec.HaveInfo || en.info == nil {
		return nil // nothing to check against yet
	}
	e.stopRunning(en)
	en.verifying, en.problem, en.missing = true, "", false
	cancel := make(chan struct{})
	en.cancel = cancel
	go e.verify(en, cancel)
	return nil
}

// verify reads every piece of the torrent from disk and records which are
// good. Nothing on disk is changed.
func (e *Engine) verify(en *entry, cancel <-chan struct{}) {
	e.mu.Lock()
	info, folder, hash := en.info, en.rec.SavePath, en.rec.Hash
	e.mu.Unlock()
	good, err := e.hashPieces(info, folder, hash, cancel)
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-cancel:
		return // the torrent was removed meanwhile
	default:
	}
	en.cancel = nil
	en.verifying = false
	if err != nil {
		en.problem = "its files couldn't be checked: " + err.Error()
	} else {
		en.have = good
		// A torrent that is whole again is no longer missing anything.
		if en.complete() && en.rec.Completed == nil {
			t := time.Now().UTC()
			en.rec.Completed = &t
		}
	}
	en.dirty = true
	e.poke()
}

// hashPieces checks a torrent's pieces against the files in folder and records
// the answer in the piece database. It returns the bytes that were good.
func (e *Engine) hashPieces(info *metainfo.Info, folder, hash string, cancel <-chan struct{}) (int64, error) {
	ih := metainfo.NewHashFromHex(hash)
	impl, err := e.storageFor(folder).OpenTorrent(context.Background(), info, ih)
	if err != nil {
		return 0, err
	}
	defer func() { _ = impl.Close() }()
	var good int64
	for i := range info.NumPieces() {
		select {
		case <-e.stop:
			return good, errors.New("the built-in client is shutting down")
		case <-cancel:
			return good, errors.New("cancelled")
		default:
		}
		p := info.Piece(i)
		ok := false
		h := sha1.New() //nolint:gosec // see import
		if _, err := io.Copy(h, io.NewSectionReader(impl.Piece(p), 0, p.Length())); err == nil {
			want := p.V1Hash().Value
			ok = string(h.Sum(nil)) == string(want[:])
		}
		if err := e.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, ok); err != nil {
			return good, err
		}
		if ok {
			good += p.Length()
		}
	}
	return good, nil
}

// SetFolder moves a torrent's files to another folder (renaming them, or
// copying and then deleting the originals when the folders are on different
// filesystems) and points the torrent at it. Files already in the new folder
// are never overwritten.
func (e *Engine) SetFolder(category, hash, folder string) error {
	if !filepath.IsAbs(folder) {
		return fmt.Errorf("%q isn't a full path", folder)
	}
	folder = filepath.Clean(folder)
	e.mu.Lock()
	ens, err := e.ours(category, []string{hash})
	if err != nil {
		e.mu.Unlock()
		return err
	}
	en := ens[0]
	if en.rec.SavePath == folder {
		e.mu.Unlock()
		return nil
	}
	if en.busy != "" || en.verifying {
		e.mu.Unlock()
		return errors.New("the built-in client is busy with this torrent; try again in a moment")
	}
	e.stopRunning(en)
	en.busy = "moving"
	from, info := en.rec.SavePath, en.info
	e.mu.Unlock()

	err = moveFiles(info, from, folder)

	e.mu.Lock()
	defer e.mu.Unlock()
	en.busy = ""
	if err != nil {
		return err
	}
	en.rec.SavePath = folder
	en.dirty = true
	e.poke()
	return e.saveDirtyLocked()
}

// moveFiles moves the files a torrent lists from one folder to another, and
// then the folders it leaves empty. A file that isn't there is skipped (the
// torrent may not have it yet); one that is in the way aborts before anything
// is overwritten.
func moveFiles(info *metainfo.Info, from, to string) error {
	if info == nil {
		return os.MkdirAll(to, 0o755) //nolint:gosec // see Add
	}
	type step struct{ src, dst string }
	var steps []step
	for _, f := range fileList(info) {
		src, ok1 := inside(from, f.Name)
		dst, ok2 := inside(to, f.Name)
		if !ok1 || !ok2 {
			return fmt.Errorf("%s would be outside the torrent's folder", f.Name)
		}
		if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("%s is already in %s, and the built-in client won't overwrite it", f.Name, to)
		}
		steps = append(steps, step{src, dst})
	}
	for _, s := range steps {
		if err := os.MkdirAll(filepath.Dir(s.dst), 0o755); err != nil { //nolint:gosec // see Add
			return err
		}
		if err := renameOrCopy(s.src, s.dst); err != nil {
			return fmt.Errorf("couldn't move %s: %w", filepath.Base(s.src), err)
		}
	}
	for _, s := range steps {
		for d := filepath.Dir(s.src); d != from && len(d) > len(from); d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	return nil
}

// renameOrCopy renames a file, or, when the folders are on different
// filesystems, copies it and deletes the original once the copy is whole.
func renameOrCopy(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) || !errors.Is(le.Err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src) //nolint:gosec // paths come from the torrent's own file list, checked to be inside its folder
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	tmp := dst + ".reelkeeper-copy"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // see above
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if got, serr := os.Stat(tmp); serr != nil || got.Size() != st.Size() {
			err = errors.New("the copy isn't the size of the original")
		}
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}
