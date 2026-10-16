package download

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/secrets"
)

// A download client can only check files it already has against a torrent it
// has, so moving a torrent to another client means giving that client the
// torrent. Reelkeeper keeps what it sent at grab time: the .torrent file or
// the magnet link. Both hold the tracker's passkey, so they are stored
// encrypted with the key that protects indexer passwords. A copy is deleted
// as soon as its torrent leaves the client (finish), so only active torrents
// are kept.

// downloadSource is a row of download_sources.
type downloadSource struct {
	bun.BaseModel `bun:"table:download_sources"`

	DownloadID int64     `bun:"download_id,pk"`
	Kind       string    `bun:"kind"`
	Sealed     string    `bun:"sealed"`
	CreatedAt  time.Time `bun:"created_at"`
}

// SetSecrets gives the service the key it encrypts kept torrents with. Without
// one, nothing is kept (and moves fall back to asking the client).
func (s *Service) SetSecrets(box *secrets.Box) { s.box = box }

// keepSource stores what was sent for the download with this hash, replacing
// an older copy.
func (s *Service) keepSource(ctx context.Context, hash string, src Source) error {
	if s.box == nil || src.Empty() {
		return nil
	}
	var id int64
	if err := s.db.DB.NewSelect().Model((*Download)(nil)).Column("id").Where("hash = ?", hash).Scan(ctx, &id); err != nil {
		return err
	}
	return s.storeSource(ctx, id, src)
}

// storeSource seals and saves src for a download.
func (s *Service) storeSource(ctx context.Context, downloadID int64, src Source) error {
	if s.box == nil || src.Empty() {
		return nil
	}
	plain := src.Magnet
	if len(src.Torrent) > 0 {
		plain = string(src.Torrent)
	}
	sealed, err := s.box.Seal(plain)
	if err != nil {
		return err
	}
	_, err = s.db.DB.NewInsert().Model(&downloadSource{DownloadID: downloadID, Kind: src.Kind(), Sealed: sealed, CreatedAt: s.now().UTC()}).
		On("CONFLICT (download_id) DO UPDATE").
		Set("kind = EXCLUDED.kind").Set("sealed = EXCLUDED.sealed").Set("created_at = EXCLUDED.created_at").
		Exec(ctx)
	return err
}

// storedSource returns what was kept for a download; ok is false when
// nothing was (a download from before 1.3, or one grabbed without a key).
func (s *Service) storedSource(ctx context.Context, downloadID int64) (src Source, ok bool, err error) {
	if s.box == nil {
		return Source{}, false, nil
	}
	var row downloadSource
	if err := s.db.DB.NewSelect().Model(&row).Where("download_id = ?", downloadID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Source{}, false, nil
		}
		return Source{}, false, err
	}
	plain, err := s.box.Open(row.Sealed)
	if err != nil {
		// A changed secret key: as good as not kept; the client may still
		// hand the torrent over.
		s.logger.Warn("can't read a kept torrent; asking the download client for it instead", "download", downloadID, "error", err)
		return Source{}, false, nil
	}
	if row.Kind == SourceTorrent {
		return Source{Torrent: []byte(plain)}, true, nil
	}
	return Source{Magnet: plain}, true, nil
}

// dropSource deletes a download's kept torrent.
func (s *Service) dropSource(ctx context.Context, downloadID int64) {
	if _, err := s.db.DB.NewDelete().Model((*downloadSource)(nil)).Where("download_id = ?", downloadID).Exec(ctx); err != nil {
		s.logger.Warn("couldn't delete a kept torrent", "download", downloadID, "error", err)
	}
}
