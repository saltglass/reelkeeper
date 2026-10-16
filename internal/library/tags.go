package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// Tags are labels on movie versions, as in Radarr. Seerr tags each request
// with the user who made it, through the Radarr-compatible API; they're
// kept so that information isn't lost at cutover.

// Tag is a label.
type Tag struct {
	bun.BaseModel `bun:"table:tags"`

	ID    int64  `bun:"id,pk,autoincrement" json:"id"`
	Label string `bun:"label" json:"label"`
}

type versionTag struct {
	bun.BaseModel `bun:"table:version_tags"`

	VersionID int64 `bun:"version_id,pk"`
	TagID     int64 `bun:"tag_id,pk"`
}

// Tags lists every tag, by label.
func (s *Service) Tags(ctx context.Context) ([]Tag, error) {
	var out []Tag
	return out, s.db.DB.NewSelect().Model(&out).Order("label").Scan(ctx)
}

// normalizeLabel is how labels are stored: trimmed and lower-case, as
// Radarr does (so "Dan" and "dan" are one tag).
func normalizeLabel(label string) (string, error) {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return "", validation.Errors{"label": "A tag needs a label."}
	}
	if len(l) > 100 {
		return "", validation.Errors{"label": "Keep tags to 100 characters."}
	}
	return l, nil
}

// CreateTag adds a tag, or returns the existing one with that label.
func (s *Service) CreateTag(ctx context.Context, label string) (*Tag, error) {
	l, err := normalizeLabel(label)
	if err != nil {
		return nil, err
	}
	t := &Tag{Label: l}
	if _, err := s.db.DB.NewInsert().Model(t).On("CONFLICT (label) DO NOTHING").Exec(ctx); err != nil {
		return nil, err
	}
	if t.ID == 0 { // it existed
		if err := s.db.DB.NewSelect().Model(t).Where("label = ?", l).Scan(ctx); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// RenameTag changes a tag's label.
func (s *Service) RenameTag(ctx context.Context, id int64, label string) (*Tag, error) {
	l, err := normalizeLabel(label)
	if err != nil {
		return nil, err
	}
	t := new(Tag)
	err = s.db.DB.NewSelect().Model(t).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if other, err := s.db.DB.NewSelect().Model((*Tag)(nil)).Where("label = ? AND id <> ?", l, id).Exists(ctx); err != nil {
		return nil, err
	} else if other {
		return nil, validation.Errors{"label": fmt.Sprintf("There is already a tag called %q.", l)}
	}
	t.Label = l
	_, err = s.db.DB.NewUpdate().Model(t).Column("label").WherePK().Exec(ctx)
	return t, err
}

// VersionTags maps versions to their tag IDs.
func (s *Service) VersionTags(ctx context.Context, versionIDs []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	var rows []versionTag
	if err := s.db.DB.NewSelect().Model(&rows).Where("version_id IN (?)", bunList(versionIDs)).
		Order("version_id", "tag_id").Scan(ctx); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.VersionID] = append(out[r.VersionID], r.TagID)
	}
	return out, nil
}

// SetVersionTags replaces a version's tags. Unknown tag IDs are refused.
func (s *Service) SetVersionTags(ctx context.Context, versionID int64, tagIDs []int64) error {
	if len(tagIDs) > 0 {
		n, err := s.db.DB.NewSelect().Model((*Tag)(nil)).Where("id IN (?)", bunList(tagIDs)).Count(ctx)
		if err != nil {
			return err
		}
		if int(n) != len(unique(tagIDs)) {
			return validation.Errors{"tags": "One of the tags doesn't exist."}
		}
	}
	return s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		if _, err := tx.NewDelete().Model((*versionTag)(nil)).Where("version_id = ?", versionID).Exec(ctx); err != nil {
			return err
		}
		for _, id := range unique(tagIDs) {
			if _, err := tx.NewInsert().Model(&versionTag{VersionID: versionID, TagID: id}).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

func unique(ids []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
