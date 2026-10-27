package library

import (
	"context"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// BulkEdit changes the versions of many movies or many series at once (the
// Movies and Series pages' "Select").
type BulkEdit struct {
	MediaType string  // Movie or Series
	TitleIDs  []int64 // the chosen titles
	// ProfileID limits the change to the titles' versions with this quality
	// profile (for example only the Remux copies); 0 changes every version.
	ProfileID int64
	Monitored *bool
	Upgrades  *bool  // movies only
	Monitor   string // series only: a monitoring preset; "" leaves the episodes as they are
}

// BulkResult says what a bulk edit did.
type BulkResult struct {
	Titles   int // titles with at least one version changed
	Versions int // versions changed
	// TurnedOn are the versions the edit may have made wanted (monitoring
	// or upgrades turned on, or a preset that monitors episodes), in the
	// order their titles were given, for the caller to search.
	TurnedOn []int64
}

// EditVersions applies a bulk edit. Everything is checked first: ErrNotFound
// when a title doesn't exist, a validation error when a title is the wrong
// kind, the change doesn't fit the kind, or no chosen title has a version
// with the profile. Then every version is changed in one transaction, so a
// bad request or a failure changes nothing.
func (s *Service) EditVersions(ctx context.Context, e BulkEdit) (BulkResult, error) {
	var res BulkResult
	ids := uniqueIDs(e.TitleIDs)
	var problems validation.Errors
	switch {
	case len(ids) == 0:
		problems.Add("titleIds", "Choose at least one title.")
	case e.Monitored == nil && e.Upgrades == nil && e.Monitor == "":
		problems.Add("monitored", "Choose a change to make.")
	}
	if e.MediaType == Series && e.Upgrades != nil {
		problems.Add("upgrades", "Upgrades are always on for series.")
	}
	if e.MediaType == Movie && e.Monitor != "" {
		problems.Add("monitor", "Episode monitoring is for series only.")
	}
	var from *string
	switch e.Monitor {
	case "", MonitorNone:
	case MonitorAll:
		from = ptr(AllEpisodes)
	case MonitorFuture:
		from = ptr(s.today())
	default:
		problems.Add("monitor", fmt.Sprintf("Choose all, future or none (not %q).", e.Monitor))
	}
	if err := problems.Err(); err != nil {
		return res, err
	}

	var titles []Title
	if err := s.db.DB.NewSelect().Model(&titles).Column("id", "media_type").
		Where("id IN (?)", bunList(ids)).Scan(ctx); err != nil {
		return res, err
	}
	if len(titles) != len(ids) {
		return res, ErrNotFound
	}
	for _, t := range titles {
		if t.MediaType != e.MediaType {
			problems.Add("titleIds", fmt.Sprintf("Title %d is a %s, not a %s.", t.ID, t.MediaType, e.MediaType))
		}
	}
	if err := problems.Err(); err != nil {
		return res, err
	}

	var versions []Version
	q := s.db.DB.NewSelect().Model(&versions).Where("title_id IN (?)", bunList(ids))
	if e.ProfileID != 0 {
		q = q.Where("profile_id = ?", e.ProfileID)
	}
	if err := q.Scan(ctx); err != nil {
		return res, err
	}
	if len(versions) == 0 {
		problems.Add("profileId", "None of the chosen titles has a version with that quality profile.")
		return res, problems.Err()
	}
	// In the order the titles were given, so the first searches go to the
	// titles the user saw first.
	order := map[int64]int{}
	for i, id := range ids {
		order[id] = i
	}
	slices.SortStableFunc(versions, func(a, b Version) int { return order[a.TitleID] - order[b.TitleID] })

	vids := make([]int64, len(versions))
	touched := map[int64]bool{}
	for i, v := range versions {
		vids[i] = v.ID
		touched[v.TitleID] = true
		monitored := v.Monitored
		if e.Monitored != nil {
			monitored = *e.Monitored
		}
		on := false
		switch {
		case !monitored:
		case e.Monitored != nil && *e.Monitored && !v.Monitored:
			on = true
		case e.Upgrades != nil && *e.Upgrades && !v.Upgrades:
			on = true
		case from != nil:
			on = true
		}
		if on {
			res.TurnedOn = append(res.TurnedOn, v.ID)
		}
	}

	err := s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if e.Monitored != nil {
			if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("monitored = ?", *e.Monitored).
				Where("id IN (?)", bunList(vids)).Exec(ctx); err != nil {
				return err
			}
		}
		if e.Upgrades != nil {
			if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("upgrades = ?", *e.Upgrades).
				Where("id IN (?)", bunList(vids)).Exec(ctx); err != nil {
				return err
			}
		}
		if e.Monitor != "" {
			// As ApplyMonitorPreset: the preset replaces choices made per
			// season or episode.
			if _, err := tx.NewUpdate().Model((*Version)(nil)).Set("monitor_from = ?", from).
				Where("id IN (?)", bunList(vids)).Exec(ctx); err != nil {
				return err
			}
			if _, err := tx.NewDelete().Model((*EpisodeMonitoring)(nil)).
				Where("version_id IN (?)", bunList(vids)).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return BulkResult{}, err
	}
	s.onChange()
	res.Titles, res.Versions = len(touched), len(versions)
	return res, nil
}
