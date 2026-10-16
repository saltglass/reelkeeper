package library

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// A profile's settings are kept in one of two forms (see internal/decision):
//
//   - simple: the editor's plain choices, compiled whenever the profile is
//     used, so simple profiles pick up improvements to Reelkeeper's built-in
//     formats on upgrade;
//   - advanced: the compiled form itself (qualities, custom formats and
//     scores), edited directly and never recompiled.
//
// Switching to advanced stores exactly what the simple settings compile
// to at that moment, so the switch never changes what the profile does.
// Switching back replaces the advanced form with simple settings; the
// editor warns before that.

// Profile modes.
const (
	ModeSimple   = "simple"
	ModeAdvanced = "advanced"
)

// ProfileSettings is a profile's settings in both forms. Compiled is what
// the profile does now: the advanced form, or the simple settings
// compiled.
type ProfileSettings struct {
	Mode     string
	Simple   decision.Simple
	Compiled decision.Profile
}

// defaultPreset is the preset a profile starts from before its settings are
// first saved: profiles created before M4 (HD and Remux) and new ones.
func defaultPreset(slug string) decision.Simple {
	id := "hd"
	if slug == "remux" {
		id = "remux"
	}
	p, _ := decision.PresetByID(id)
	return p.Settings
}

// ProfileSettings loads one profile's settings.
func (s *Service) ProfileSettings(ctx context.Context, id int64) (ProfileSettings, error) {
	p, err := s.profile(ctx, id)
	if err != nil {
		return ProfileSettings{}, err
	}
	return settingsOf(p)
}

func settingsOf(p *Profile) (ProfileSettings, error) {
	out := ProfileSettings{Mode: p.Mode, Simple: defaultPreset(p.Slug)}
	if strings.TrimSpace(p.Simple) != "" && strings.TrimSpace(p.Simple) != "{}" {
		var simple decision.Simple
		if err := json.Unmarshal([]byte(p.Simple), &simple); err != nil {
			return out, fmt.Errorf("profile %s: reading simple settings: %w", p.Name, err)
		}
		out.Simple = simple
	}
	if p.Mode == ModeAdvanced {
		if err := json.Unmarshal([]byte(p.Advanced), &out.Compiled); err != nil {
			return out, fmt.Errorf("profile %s: reading advanced settings: %w", p.Name, err)
		}
		if err := out.Compiled.Compile(); err != nil {
			return out, fmt.Errorf("profile %s: %w", p.Name, err)
		}
		return out, nil
	}
	out.Mode = ModeSimple
	compiled, err := out.Simple.Compile()
	if err != nil {
		return out, fmt.Errorf("profile %s: %w", p.Name, err)
	}
	out.Compiled = compiled
	return out, nil
}

// CompiledProfile is what the decision engine uses for a profile.
func (s *Service) CompiledProfile(ctx context.Context, id int64) (*decision.Profile, error) {
	ps, err := s.ProfileSettings(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ps.Compiled, nil
}

// SaveProfileSimple stores simple settings and puts the profile in simple
// mode (replacing any advanced form). Problems come back as
// validation.Errors keyed "simple.<field>".
func (s *Service) SaveProfileSimple(ctx context.Context, id int64, simple decision.Simple) (ProfileSettings, error) {
	p, err := s.profile(ctx, id)
	if err != nil {
		return ProfileSettings{}, err
	}
	if errs := simple.Validate(); len(errs) > 0 {
		var v validation.Errors
		for field, msg := range errs {
			v.Add("simple."+field, msg)
		}
		return ProfileSettings{}, v
	}
	b, err := json.Marshal(simple)
	if err != nil {
		return ProfileSettings{}, err
	}
	p.Mode, p.Simple, p.Advanced = ModeSimple, string(b), "{}"
	if err := s.saveProfileSettings(ctx, p); err != nil {
		return ProfileSettings{}, err
	}
	s.logger.Info("quality profile settings saved", "name", p.Name, "mode", ModeSimple)
	return settingsOf(p)
}

// SaveProfileAdvanced stores an edited advanced form. Problems come back as
// validation.Errors under "advanced".
func (s *Service) SaveProfileAdvanced(ctx context.Context, id int64, adv decision.Profile) (ProfileSettings, error) {
	p, err := s.profile(ctx, id)
	if err != nil {
		return ProfileSettings{}, err
	}
	if err := adv.Compile(); err != nil {
		return ProfileSettings{}, validation.Errors{"advanced": err.Error()}
	}
	b, err := json.Marshal(adv)
	if err != nil {
		return ProfileSettings{}, err
	}
	p.Mode, p.Advanced = ModeAdvanced, string(b)
	if err := s.saveProfileSettings(ctx, p); err != nil {
		return ProfileSettings{}, err
	}
	s.logger.Info("quality profile settings saved", "name", p.Name, "mode", ModeAdvanced)
	return settingsOf(p)
}

// SwitchProfileToAdvanced copies what the simple settings compile to into
// the advanced form. The simple settings are kept, so the editor can show
// what the profile started from.
func (s *Service) SwitchProfileToAdvanced(ctx context.Context, id int64) (ProfileSettings, error) {
	ps, err := s.ProfileSettings(ctx, id)
	if err != nil {
		return ProfileSettings{}, err
	}
	if ps.Mode == ModeAdvanced {
		return ps, nil
	}
	return s.SaveProfileAdvanced(ctx, id, ps.Compiled)
}

func (s *Service) saveProfileSettings(ctx context.Context, p *Profile) error {
	_, err := s.db.DB.NewUpdate().Model(p).Column("mode", "simple", "advanced").WherePK().Exec(ctx)
	return err
}
