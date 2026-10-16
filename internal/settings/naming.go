package settings

import (
	"context"
	"strings"

	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupNaming is the settings group for folder and file name templates
// (Settings → Media Folders → Naming).
const GroupNaming = "naming"

// Naming returns the current name templates.
func (s *Service) Naming() naming.Templates {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.naming
}

// CheckNaming validates templates without saving them.
func CheckNaming(t naming.Templates) error {
	var errs validation.Errors
	for field, c := range map[string]struct {
		kind naming.Kind
		tpl  string
	}{
		"movieFolder":      {naming.MovieFolder, t.MovieFolder},
		"movieFile":        {naming.MovieFile, t.MovieFile},
		"seriesFolder":     {naming.SeriesFolder, t.SeriesFolder},
		"seasonFolder":     {naming.SeasonFolder, t.SeasonFolder},
		"episodeFile":      {naming.EpisodeFile, t.EpisodeFile},
		"dailyEpisodeFile": {naming.DailyEpisodeFile, t.DailyEpisodeFile},
	} {
		if p := naming.CheckKind(c.kind, c.tpl); p != "" {
			errs.Add(field, p)
		}
	}
	known := false
	for _, st := range naming.MultiEpisodeStyles {
		known = known || st.ID == t.MultiEpisodeStyle
	}
	if !known {
		errs.Add("multiEpisodeStyle", "Choose how files with several episodes are named.")
	}
	if errs != nil {
		return errs
	}
	return nil
}

// SaveNaming validates and stores the name templates.
func (s *Service) SaveNaming(ctx context.Context, t naming.Templates) (naming.Templates, error) {
	for _, f := range []*string{&t.MovieFolder, &t.MovieFile, &t.SeriesFolder, &t.SeasonFolder, &t.EpisodeFile, &t.DailyEpisodeFile} {
		*f = strings.TrimSpace(*f)
	}
	if err := CheckNaming(t); err != nil {
		return naming.Templates{}, err
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupNaming, t); err != nil {
		s.mu.Unlock()
		return naming.Templates{}, err
	}
	s.naming = t
	s.mu.Unlock()
	s.notify(GroupNaming)
	return t, nil
}
