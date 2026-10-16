package settings

import (
	"context"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupGrabbing is the settings group for automatic grabbing
// (Settings → Download Client).
const GroupGrabbing = "grabbing"

// Grabbing modes.
const (
	// GrabOff: Reelkeeper doesn't search on its own at all.
	GrabOff = "off"
	// GrabDryRun: Reelkeeper searches and decides, and records what it would
	// grab in History, but sends nothing to the download client.
	GrabDryRun = "dryrun"
	// GrabOn: Reelkeeper grabs.
	GrabOn = "on"
)

// Grabbing controls automatic searching and grabbing.
type Grabbing struct {
	// Mode is GrabOff, GrabDryRun or GrabOn. New installs start in dry run,
	// so nothing downloads until someone has seen what Reelkeeper would do.
	Mode string `json:"mode"`
	// TVMode is the same choice for TV, kept separate so TV can run in dry
	// run while movies grab (2026-10-11). It starts in dry run.
	TVMode string `json:"tvMode"`
	// QueueLimit is the most downloads Activity → Queue may hold, movies and
	// TV together (0 means no limit). Automatic grabbing pauses while the
	// queue is this full and resumes as downloads finish. Seerr requests
	// still go through.
	QueueLimit int `json:"queueLimit"`
	// MinSeeders skips releases with fewer seeders; dead torrents never
	// finish.
	MinSeeders int `json:"minSeeders"`
}

// DefaultGrabbing starts in dry run, with no queue limit and at least one
// seeder.
func DefaultGrabbing() Grabbing { return Grabbing{Mode: GrabDryRun, TVMode: GrabDryRun, MinSeeders: 1} }

func (g *Grabbing) normalize() validation.Errors {
	var errs validation.Errors
	switch g.Mode {
	case GrabOff, GrabDryRun, GrabOn:
	default:
		errs.Add("mode", "Choose Off, Dry run or On.")
	}
	if g.TVMode == "" {
		g.TVMode = GrabDryRun // saved before TV had its own mode
	}
	switch g.TVMode {
	case GrabOff, GrabDryRun, GrabOn:
	default:
		errs.Add("tvMode", "Choose Off, Dry run or On.")
	}
	if g.QueueLimit < 0 || g.QueueLimit > 1000 {
		errs.Add("queueLimit", "Use 0 (no limit) to 1000 downloads.")
	}
	if g.MinSeeders < 0 || g.MinSeeders > 100 {
		errs.Add("minSeeders", "Use 0 to 100 seeders; 1 skips only dead torrents.")
	}
	return errs
}

// loadGrabbing reads the grabbing settings. Before the queue limit, the
// setting was "dailyCap" (grabs a day); a saved value carries over as the
// queue limit unless a queue limit was saved. The old key is never written
// back: saving writes the current struct only.
func (s *Service) loadGrabbing(ctx context.Context) error {
	s.grabbing = DefaultGrabbing()
	found, err := s.load(ctx, GroupGrabbing, &s.grabbing)
	if err != nil || !found || s.grabbing.QueueLimit != 0 {
		return err
	}
	var legacy struct {
		DailyCap int `json:"dailyCap"`
	}
	if _, err := s.load(ctx, GroupGrabbing, &legacy); err != nil {
		return err
	}
	if legacy.DailyCap > 0 && legacy.DailyCap <= 1000 {
		s.grabbing.QueueLimit = legacy.DailyCap
	}
	return nil
}

// Grabbing returns the grabbing settings.
func (s *Service) Grabbing() Grabbing {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.grabbing
}

// SaveGrabbing validates and stores the grabbing settings.
func (s *Service) SaveGrabbing(ctx context.Context, v Grabbing) (Grabbing, error) {
	if errs := v.normalize(); errs != nil {
		return Grabbing{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupGrabbing, v); err != nil {
		s.mu.Unlock()
		return Grabbing{}, err
	}
	s.grabbing = v
	s.mu.Unlock()
	s.notify(GroupGrabbing)
	return v, nil
}
