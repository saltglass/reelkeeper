package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

// record is everything the engine remembers about one torrent between runs.
// It is kept in builtin/state.db in the data folder (not in Reelkeeper's
// database), so, like an external client's torrent list, it isn't part of
// Reelkeeper's backups.
type record struct {
	// Hash is the torrent's v1 info hash in lower-case hex.
	Hash     string `json:"hash"`
	Category string `json:"category"`
	// Tags are the extra labels Reelkeeper gave it (the indexer's name,
	// "requested").
	Tags []string `json:"tags,omitempty"`
	Name string   `json:"name"`
	// SavePath is the folder its files are in (or go in).
	SavePath string `json:"savePath"`
	// Magnet is kept until the torrent's details are known and its .torrent
	// file is saved.
	Magnet string `json:"magnet,omitempty"`
	// Trackers are the tracker addresses to use, in tiers.
	Trackers [][]string `json:"trackers,omitempty"`
	// HaveInfo says the .torrent file is saved (torrents/<hash>.torrent).
	HaveInfo bool `json:"haveInfo,omitempty"`
	Private  bool `json:"private,omitempty"`
	// Size is the torrent's size in bytes, known once it has its details.
	Size int64 `json:"size,omitempty"`

	Added     time.Time  `json:"added"`
	Completed *time.Time `json:"completed,omitempty"`
	// Position is its place in the queue: lower goes first. "Move to top"
	// gives a torrent a lower one than any other.
	Position int64 `json:"position"`
	// Stopped means someone paused it. Only they (or a move) resume it.
	Stopped bool `json:"stopped,omitempty"`

	// Uploaded and Downloaded are the payload bytes moved in earlier runs;
	// SeedSeconds how long it has been uploading, also from earlier runs.
	Uploaded    int64 `json:"uploaded,omitempty"`
	Downloaded  int64 `json:"downloaded,omitempty"`
	SeedSeconds int64 `json:"seedSeconds,omitempty"`
}

// store is the state database.
type store struct {
	db *bbolt.DB
}

var torrentsBucket = []byte("torrents")

func openStore(dir string) (*store, error) {
	db, err := bbolt.Open(filepath.Join(dir, "state.db"), 0o600, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if errors.Is(err, bbolt.ErrTimeout) {
			return nil, errors.New("the built-in client's records (builtin/state.db) are in use by another Reelkeeper")
		}
		return nil, fmt.Errorf("can't open the built-in client's records: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(torrentsBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) all() ([]record, error) {
	var out []record
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(torrentsBucket).ForEach(func(k, v []byte) error {
			var r record
			if err := json.Unmarshal(v, &r); err != nil {
				return fmt.Errorf("a stored torrent record (%s) is damaged: %w", k, err)
			}
			out = append(out, r)
			return nil
		})
	})
	return out, err
}

// save writes these records in one transaction.
func (s *store) save(rs ...record) error {
	if len(rs) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(torrentsBucket)
		for _, r := range rs {
			v, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(r.Hash), v); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *store) remove(hash string) error {
	return s.db.Update(func(tx *bbolt.Tx) error { return tx.Bucket(torrentsBucket).Delete([]byte(hash)) })
}

func (s *store) close() error { return s.db.Close() }
