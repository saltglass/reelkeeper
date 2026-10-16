package download

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// TemporaryBlockLife is how long a temporary blocklist entry lasts. Releases
// blocked because a download got stuck (stalled, too slow, no metadata,
// missing files, a failed import) are tried again afterwards, because a dead
// swarm can come back to life. Entries the user made, and releases a tracker
// deleted, never expire. One fixed number, no setting (2026-10-12).
const TemporaryBlockLife = 30 * 24 * time.Hour

// Why a release was blocked (stored in blocklist.rule). The cleanup rules
// keep their own names (stalled, slow, metadata, unregistered, missing,
// import-failed; see internal/cleanup).
const (
	// BlockRemoved: the user removed the download from the Queue.
	BlockRemoved = "removed"
	// BlockRadarr and BlockSonarr: brought over from Radarr or Sonarr.
	BlockRadarr = "radarr"
	BlockSonarr = "sonarr"
	// BlockCleanup: cleanup, but the rule isn't known (old rows only).
	BlockCleanup = "cleanup"
)

// tempRules are the cleanup rules whose blocklist entries expire: the
// release may be fine once its swarm is back. A release the tracker deleted
// ("unregistered") is gone for good, so it stays blocked. A rule not listed
// here is permanent too, the safe way round.
var tempRules = map[string]bool{
	"stalled": true, "slow": true, "metadata": true, "missing": true, "import-failed": true,
}

// BlockedRelease is a release Reelkeeper skips for a title: forever, or
// until ExpiresAt.
type BlockedRelease struct {
	bun.BaseModel `bun:"table:blocklist"`

	ID           int64     `bun:"id,pk,autoincrement"`
	CreatedAt    time.Time `bun:"created_at"`
	TitleID      int64     `bun:"title_id"`
	ReleaseTitle string    `bun:"release_title"`
	Hash         string    `bun:"hash"`
	Indexer      string    `bun:"indexer"`
	Quality      string    `bun:"quality"`
	Reason       string    `bun:"reason"`
	// Rule is what blocked it (the Block* constants or a cleanup rule).
	Rule string `bun:"rule"`
	// ExpiresAt is when a temporary entry stops blocking; nil means
	// permanent.
	ExpiresAt *time.Time `bun:"expires_at"`

	// Temporary asks AddToBlocklist to make the entry expire after
	// TemporaryBlockLife (not stored).
	Temporary bool `bun:"-"`
}

// expired reports whether a temporary entry has run out.
func (b BlockedRelease) expired(now time.Time) bool {
	return b.ExpiresAt != nil && !b.ExpiresAt.After(now)
}

// SetWantedTitles says how to find the titles that still want something
// (grab.Service.WantedTitleIDs). Without it no entry counts as dormant.
func (s *Service) SetWantedTitles(fn func(ctx context.Context) (map[int64]bool, error)) {
	s.wantedTitles = fn
}

// AddToBlocklist blocklists a release for a title.
func (s *Service) AddToBlocklist(ctx context.Context, b BlockedRelease) error {
	b.CreatedAt = s.now().UTC()
	if b.Temporary && b.ExpiresAt == nil {
		t := b.CreatedAt.Add(TemporaryBlockLife)
		b.ExpiresAt = &t
	}
	_, err := s.db.DB.NewInsert().Model(&b).Exec(ctx)
	return err
}

// Blocklisted returns a test for a title's blocklist: by torrent hash when
// both have one, otherwise by release name. Expired entries don't count,
// even before the daily task has deleted them.
func (s *Service) Blocklisted(ctx context.Context, titleID int64) (func(name, hash string) bool, error) {
	rows, err := s.active(ctx, titleID)
	if err != nil {
		return nil, err
	}
	return func(name, hash string) bool {
		for _, b := range rows {
			if (hash != "" && b.Hash != "" && strings.EqualFold(hash, b.Hash)) || strings.EqualFold(name, b.ReleaseTitle) {
				return true
			}
		}
		return false
	}, nil
}

// BlockedEntry is a blocklist entry with what the lists show beside it.
type BlockedEntry struct {
	BlockedRelease
	// Episodes names the episodes of a TV release ("S02E05", "season 2"),
	// from the download it came from; empty for movies and when unknown.
	Episodes string
	// MediaType is "movie" or "series".
	MediaType string
	// Dormant: the title has nothing wanted right now. Dormant entries
	// still block; the list just hides them by default.
	Dormant bool
}

// Permanent reports whether the entry never expires.
func (b BlockedEntry) Permanent() bool { return b.ExpiresAt == nil }

// BlocklistQuery says which entries to list.
type BlocklistQuery struct {
	// TitleID limits the list to one title (0: all).
	TitleID int64
	// IncludeDormant also lists entries of titles with nothing wanted.
	IncludeDormant bool
}

// BlocklistPage is the list and the counts around it. The counts cover every
// entry that still blocks (for the title asked about, if any), listed or not.
type BlocklistPage struct {
	Items []BlockedEntry
	// Total is every entry that still blocks, Dormant how many of those
	// are dormant, and Temporary how many will expire on their own.
	Total, Dormant, Temporary int
}

// active returns the entries that still block, newest first.
func (s *Service) active(ctx context.Context, titleID int64) ([]BlockedRelease, error) {
	var rows []BlockedRelease
	q := s.db.DB.NewSelect().Model(&rows).Order("id DESC")
	if titleID != 0 {
		q = q.Where("title_id = ?", titleID)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, err
	}
	now := s.now()
	return slices.DeleteFunc(rows, func(b BlockedRelease) bool { return b.expired(now) }), nil
}

// Blocklist lists the entries that still block, newest first.
func (s *Service) Blocklist(ctx context.Context, q BlocklistQuery) (*BlocklistPage, error) {
	rows, err := s.active(ctx, q.TitleID)
	if err != nil {
		return nil, err
	}
	page := &BlocklistPage{Items: []BlockedEntry{}}
	if len(rows) == 0 {
		return page, nil
	}

	var wanted map[int64]bool
	if s.wantedTitles != nil {
		if wanted, err = s.wantedTitles(ctx); err != nil {
			return nil, err
		}
	}
	episodes, err := s.episodesOf(ctx, rows)
	if err != nil {
		return nil, err
	}
	kinds, err := s.mediaTypes(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, b := range rows {
		e := BlockedEntry{BlockedRelease: b, MediaType: kinds[b.TitleID], Episodes: episodes[episodeKey{b.TitleID, strings.ToLower(b.Hash)}],
			Dormant: s.wantedTitles != nil && !wanted[b.TitleID]}
		page.Total++
		if e.Dormant {
			page.Dormant++
		}
		if !e.Permanent() {
			page.Temporary++
		}
		if e.Dormant && !q.IncludeDormant {
			continue
		}
		page.Items = append(page.Items, e)
	}
	return page, nil
}

// mediaTypes says whether each entry's title is a movie or a series.
func (s *Service) mediaTypes(ctx context.Context, rows []BlockedRelease) (map[int64]string, error) {
	seen := map[int64]bool{}
	var ids []int64
	for _, b := range rows {
		if !seen[b.TitleID] {
			seen[b.TitleID] = true
			ids = append(ids, b.TitleID)
		}
	}
	out := map[int64]string{}
	for chunk := range slices.Chunk(ids, 500) {
		var ts []struct {
			ID        int64  `bun:"id"`
			MediaType string `bun:"media_type"`
		}
		if err := s.db.DB.NewSelect().Table("titles").Column("id", "media_type").Where("id IN (?)", bun.List(chunk)).Scan(ctx, &ts); err != nil {
			return nil, err
		}
		for _, t := range ts {
			out[t.ID] = t.MediaType
		}
	}
	return out, nil
}

type episodeKey struct {
	titleID int64
	hash    string
}

// episodesOf finds which episodes each entry was for, from the download the
// entry came from (same title and torrent hash). Entries without a download
// (brought over from Sonarr, say) have none.
func (s *Service) episodesOf(ctx context.Context, rows []BlockedRelease) (map[episodeKey]string, error) {
	var hashes []string
	for _, b := range rows {
		if b.Hash != "" {
			hashes = append(hashes, strings.ToLower(b.Hash))
		}
	}
	out := map[episodeKey]string{}
	// In batches, to stay under the databases' limits on parameters.
	for chunk := range slices.Chunk(hashes, 500) {
		var ds []struct {
			TitleID       sql.NullInt64 `bun:"title_id"`
			Hash          string        `bun:"hash"`
			EpisodesLabel string        `bun:"episodes_label"`
		}
		if err := s.db.DB.NewSelect().Model((*Download)(nil)).Column("title_id", "hash", "episodes_label").
			Where("episodes_label <> ''").Where("LOWER(hash) IN (?)", bun.List(chunk)).Scan(ctx, &ds); err != nil {
			return nil, err
		}
		for _, d := range ds {
			if d.TitleID.Valid {
				out[episodeKey{d.TitleID.Int64, strings.ToLower(d.Hash)}] = d.EpisodesLabel
			}
		}
	}
	return out, nil
}

// Unblock removes a release from the blocklist.
func (s *Service) Unblock(ctx context.Context, id int64) error {
	res, err := s.db.DB.NewDelete().Model((*BlockedRelease)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnblockTemporary removes every temporary entry that still blocks, for one
// title or (titleID 0) all of them, and returns how many. Permanent entries
// stay.
func (s *Service) UnblockTemporary(ctx context.Context, titleID int64) (int, error) {
	rows, err := s.active(ctx, titleID)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for _, b := range rows {
		if b.ExpiresAt != nil {
			ids = append(ids, b.ID)
		}
	}
	return s.deleteBlocks(ctx, ids)
}

// ExpireBlocklist deletes the entries that have run out and returns how many.
// They already stopped blocking (Blocklisted ignores them); this just keeps
// the table small. History still records why each was blocked.
func (s *Service) ExpireBlocklist(ctx context.Context) (int, error) {
	var rows []BlockedRelease
	if err := s.db.DB.NewSelect().Model(&rows).Where("expires_at IS NOT NULL").Scan(ctx); err != nil {
		return 0, err
	}
	now := s.now()
	var ids []int64
	for _, b := range rows {
		if b.expired(now) {
			ids = append(ids, b.ID)
		}
	}
	return s.deleteBlocks(ctx, ids)
}

func (s *Service) deleteBlocks(ctx context.Context, ids []int64) (int, error) {
	// In batches, to stay under the databases' limits on parameters.
	for chunk := range slices.Chunk(ids, 500) {
		if _, err := s.db.DB.NewDelete().Model((*BlockedRelease)(nil)).Where("id IN (?)", bun.List(chunk)).Exec(ctx); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
