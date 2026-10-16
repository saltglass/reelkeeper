package library

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func profileBySlug(t *testing.T, svc *Service, slug string) Profile {
	t.Helper()
	all, err := svc.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.Slug == slug {
			return p
		}
	}
	t.Fatalf("no profile %q", slug)
	return Profile{}
}

func TestProfileSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newFixture(t, db).svc
		hd, remux := profileBySlug(t, svc, "hd"), profileBySlug(t, svc, "remux")

		// Profiles from before M4 start on the matching preset.
		ps, err := svc.ProfileSettings(ctx, remux.ID)
		if err != nil || ps.Mode != ModeSimple || ps.Simple.LowestSource != "remux" {
			t.Fatalf("remux settings = %+v, %v", ps.Simple, err)
		}
		ps, err = svc.ProfileSettings(ctx, hd.ID)
		if err != nil || ps.Simple.HighestSource != "bluray" {
			t.Fatalf("hd settings = %+v, %v", ps.Simple, err)
		}

		// Saving simple settings, and refusing bad ones field by field.
		s := ps.Simple
		s.Resolutions = []int{1080, 2160}
		if _, err := svc.SaveProfileSimple(ctx, hd.ID, s); err != nil {
			t.Fatal(err)
		}
		bad := s
		bad.Resolutions = nil
		fieldError(t, mustSettingsErr(svc.SaveProfileSimple(ctx, hd.ID, bad)), "simple.resolutions")

		// Switching to advanced keeps exactly what the profile does.
		before, _ := svc.CompiledProfile(ctx, hd.ID)
		ps, err = svc.SwitchProfileToAdvanced(ctx, hd.ID)
		if err != nil || ps.Mode != ModeAdvanced {
			t.Fatalf("switch = %v, %v", ps.Mode, err)
		}
		after, _ := svc.CompiledProfile(ctx, hd.ID)
		jb, _ := json.Marshal(before)
		ja, _ := json.Marshal(after)
		if string(jb) != string(ja) {
			t.Error("switching to advanced changed the profile")
		}

		// Advanced edits are checked before saving.
		broken := *after
		broken.Items = broken.Items[1:]
		fieldError(t, mustSettingsErr(svc.SaveProfileAdvanced(ctx, hd.ID, broken)), "advanced")
		edited := *after
		edited.MinScore = 5
		if ps, err = svc.SaveProfileAdvanced(ctx, hd.ID, edited); err != nil || ps.Compiled.MinScore != 5 {
			t.Fatalf("advanced save = %v, %v", ps.Compiled.MinScore, err)
		}

		// Back to simple replaces the advanced form.
		if ps, err = svc.SaveProfileSimple(ctx, hd.ID, s); err != nil || ps.Mode != ModeSimple ||
			ps.Compiled.MinScore != decision.BlockScore+1 {
			t.Fatalf("back to simple = %v %v, %v", ps.Mode, ps.Compiled.MinScore, err)
		}

		// New profiles start on the HD preset.
		p, err := svc.CreateProfile(ctx, "Kids", "movie")
		if err != nil {
			t.Fatal(err)
		}
		if ps, err = svc.ProfileSettings(ctx, p.ID); err != nil || ps.Simple.HighestSource != "bluray" {
			t.Fatalf("new profile settings = %+v, %v", ps.Simple, err)
		}
	})
}

func mustSettingsErr(_ ProfileSettings, err error) error { return err }
