package settings

import (
	"context"
	"slices"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupLabels is the settings group for the quality labels shown under each
// movie on the Movies page (Settings → General → Labels).
const GroupLabels = "labels"

// The parts a label can show, in the order they appear in it.
var labelParts = []string{
	"profile",      // the version's quality profile: "Remux"
	"quality",      // source and resolution: "Bluray-2160p"
	"hdr",          // "DV", "HDR10"
	"videoCodec",   // "H.265"
	"audio",        // the first audio track: "TrueHD 7.1"
	"releaseGroup", // "FraMeSToR"
	"edition",      // "Director's Cut"
	"size",         // "58 GB"
}

// Labels holds what the labels on the Movies page show.
type Labels struct {
	Parts []string `json:"parts"`
}

// DefaultLabels shows what the labels showed before they could be chosen.
func DefaultLabels() Labels {
	return Labels{Parts: []string{"profile", "quality", "hdr"}}
}

// normalize drops duplicates and puts the parts in display order. An empty
// list is allowed: labels then only appear for warnings (missing files).
func (l *Labels) normalize() validation.Errors {
	var errs validation.Errors
	var out []string
	for _, p := range labelParts {
		if slices.Contains(l.Parts, p) {
			out = append(out, p)
		}
	}
	for _, p := range l.Parts {
		if !slices.Contains(labelParts, p) {
			errs.Add("parts", "Unknown label part: "+p)
			break
		}
	}
	if out == nil {
		out = []string{}
	}
	l.Parts = out
	return errs
}

// Labels returns what the labels show.
func (s *Service) Labels() Labels {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Labels{Parts: slices.Clone(s.labels.Parts)}
}

// SaveLabels validates and stores what the labels show.
func (s *Service) SaveLabels(ctx context.Context, l Labels) (Labels, error) {
	if errs := l.normalize(); errs != nil {
		return Labels{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupLabels, l); err != nil {
		s.mu.Unlock()
		return Labels{}, err
	}
	s.labels = l
	s.mu.Unlock()
	s.notify(GroupLabels)
	return Labels{Parts: slices.Clone(l.Parts)}, nil
}
