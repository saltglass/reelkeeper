package settings

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// General holds Settings → General.
type General struct {
	// InstanceName appears in the browser tab and sidebar.
	InstanceName string `json:"instanceName"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `json:"logLevel"`
}

// DefaultGeneral returns the settings used until the user saves any.
func DefaultGeneral() General {
	return General{InstanceName: "Reelkeeper", LogLevel: "info"}
}

// SlogLevel converts LogLevel for log/slog. Unknown values mean info.
func (g General) SlogLevel() slog.Level {
	switch g.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (g *General) normalize() validation.Errors {
	var errs validation.Errors
	g.InstanceName = strings.TrimSpace(g.InstanceName)
	switch {
	case g.InstanceName == "":
		errs.Add("instanceName", "Enter a name.")
	case utf8.RuneCountInString(g.InstanceName) > 50:
		errs.Add("instanceName", "Use 50 characters or fewer.")
	}
	switch g.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs.Add("logLevel", "Choose debug, info, warn or error.")
	}
	return errs
}

// General returns the current general settings.
func (s *Service) General() General {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.general
}

// SaveGeneral validates and stores g, returning what was stored. Invalid
// input returns validation.Errors and changes nothing.
func (s *Service) SaveGeneral(ctx context.Context, g General) (General, error) {
	if errs := g.normalize(); errs != nil {
		return General{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupGeneral, g); err != nil {
		s.mu.Unlock()
		return General{}, err
	}
	s.general = g
	s.mu.Unlock()
	s.notify(GroupGeneral)
	return g, nil
}
