package settings

import (
	"context"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupCleanup is the settings group for the cleanup engine
// (Settings → Cleanup).
const GroupCleanup = "cleanup"

// Cleanup modes.
const (
	CleanupOff    = "off"
	CleanupDryRun = "dryrun"
	CleanupOn     = "on"
)

// Cleanup controls what Reelkeeper removes on its own. Every number has a
// "turn this rule off" value of 0.
type Cleanup struct {
	// Mode is CleanupOff, CleanupDryRun (record what it would do) or
	// CleanupOn. New installs start in dry run.
	Mode string `json:"mode"`

	// The seeding goal, on top of each tracker's own minimum: a torrent
	// is removed once it reaches SeedRatio or has seeded SeedHours,
	// whichever comes first. Both 0: as soon as the tracker's minimum is
	// met.
	SeedRatio float64 `json:"seedRatio"`
	SeedHours int     `json:"seedHours"`

	// Stuck downloads: removed, blocklisted and replaced.
	StalledHours     int  `json:"stalledHours"`     // no progress at all
	SlowKBps         int  `json:"slowKBps"`         // below this speed...
	SlowHours        int  `json:"slowHours"`        // ...for this long
	MetadataHours    int  `json:"metadataHours"`    // stuck fetching torrent details
	ImportFailedDays int  `json:"importFailedDays"` // a failed import nobody dealt with
	Unregistered     bool `json:"unregistered"`     // the tracker deleted the torrent
	MissingFiles     bool `json:"missingFiles"`     // qBittorrent lost the files

	// KeepFreeGB keeps this much space free on the download disk (0: off).
	// Grabs that wouldn't fit are skipped, and when the disk runs low,
	// imported torrents whose tracker minimum is met are removed early,
	// longest finished first (2026-10-11: 100 GB).
	KeepFreeGB int `json:"keepFreeGB"`

	// EmptyReplacedDays empties .reelkeeper-replaced folders (files replaced by
	// upgrades) older than this; 0 keeps them.
	EmptyReplacedDays int `json:"emptyReplacedDays"`
}

// DefaultCleanup is cautious: dry run, and generous thresholds.
func DefaultCleanup() Cleanup {
	return Cleanup{
		Mode: CleanupDryRun, SeedRatio: 1.0, SeedHours: 24,
		StalledHours: 6, SlowKBps: 50, SlowHours: 12, MetadataHours: 2, ImportFailedDays: 3,
		Unregistered: true, MissingFiles: true, EmptyReplacedDays: 30, KeepFreeGB: 100,
	}
}

func (c *Cleanup) normalize() validation.Errors {
	var errs validation.Errors
	switch c.Mode {
	case CleanupOff, CleanupDryRun, CleanupOn:
	default:
		errs.Add("mode", "Choose Off, Dry run or On.")
	}
	if c.SeedRatio < 0 || c.SeedRatio > 100 {
		errs.Add("seedRatio", "Use 0 (no ratio goal) to 100.")
	}
	for field, v := range map[string]struct{ n, max int }{
		"seedHours": {c.SeedHours, 24 * 365}, "stalledHours": {c.StalledHours, 24 * 30}, "slowKBps": {c.SlowKBps, 100000},
		"slowHours": {c.SlowHours, 24 * 30}, "metadataHours": {c.MetadataHours, 24 * 30},
		"importFailedDays": {c.ImportFailedDays, 365}, "emptyReplacedDays": {c.EmptyReplacedDays, 3650},
		"keepFreeGB": {c.KeepFreeGB, 100000},
	} {
		if v.n < 0 || v.n > v.max {
			errs.Add(field, "Use 0 (off) or a positive number.")
		}
	}
	if (c.SlowKBps == 0) != (c.SlowHours == 0) {
		errs.Add("slowHours", "Set both the speed and how long, or neither.")
	}
	return errs
}

// Cleanup returns the cleanup settings.
func (s *Service) Cleanup() Cleanup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cleanup
}

// SaveCleanup validates and stores the cleanup settings.
func (s *Service) SaveCleanup(ctx context.Context, v Cleanup) (Cleanup, error) {
	if errs := v.normalize(); errs != nil {
		return Cleanup{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupCleanup, v); err != nil {
		s.mu.Unlock()
		return Cleanup{}, err
	}
	s.cleanup = v
	s.mu.Unlock()
	s.notify(GroupCleanup)
	return v, nil
}
