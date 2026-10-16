package decision

import (
	"fmt"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// TV stops at the first acceptable file (2026-10-11), with one narrow
// exception (2026-10-12): a fix of the very thing you have. A group
// puts out a broken episode and later a REPACK or PROPER of it; that
// replaces the file, but only
//
//   - when the profile prefers propers and repacks (the choice movies use),
//   - for a file Reelkeeper downloaded in the last FixWindow,
//   - at the same quality, with a higher revision than the download,
//   - for a REPACK from the same group, for a PROPER from any group,
//   - and for a season pack, only when the files for the season all came
//     from the one pack.
//
// CheckFix is that rule; everything else about the release (blocklist,
// size, language...) is DecideEpisode's job.

// FixWindow is how long after a download is imported a fix may still
// replace it. Fixes come out within days; this keeps Reelkeeper from
// touching the rest of the library.
const FixWindow = 14 * 24 * time.Hour // 2026-10-12

// Current is the download an episode's present file came from.
type Current struct {
	DownloadID int64
	// Release is the download's release name.
	Release string
	// ImportedAt is when the download was imported.
	ImportedAt time.Time
}

// Fix says what a release is a fix of.
type Fix struct {
	// Kind is "PROPER" or "REPACK" (also said for RERIP, REAL and "v2",
	// which are held to the REPACK rule).
	Kind string
	// Group is the fixing release's group; OldGroup the one it fixes.
	Group, OldGroup string
	// Pack is true for a whole-season fix.
	Pack bool
}

// Describe says what was grabbed, for History: "a REPACK of S02E05 from the
// same group (FLUX), to fix the file you have." what names the episodes
// ("S02E05", "season 2").
func (f Fix) Describe(what string) string {
	if f.Kind == "PROPER" {
		old := f.OldGroup
		if old == "" {
			old = "earlier"
		}
		return fmt.Sprintf("a PROPER of %s (%s) to fix the %s release you have", what, groupText(f.Group), old)
	}
	return fmt.Sprintf("a REPACK of %s from the same group (%s), to fix the file you have", what, groupText(f.Group))
}

func groupText(g string) string {
	if g == "" {
		return "no group"
	}
	return g
}

// CheckFix decides whether a release may replace the files of the episodes
// it holds. have is the download behind each of those episodes' present
// files, one entry per episode that has a file (so a season pack lists one
// per episode of the season that has a file, all the same download when
// they came from the pack). It returns what the release is a fix of, or
// why it isn't one, in plain words.
func (p *Profile) CheckFix(have []Current, release string, now time.Time) (Fix, string) {
	r := parser.Parse(release)
	switch {
	case !p.PreferPropers:
		return Fix{}, "This profile doesn't prefer PROPER and REPACK releases, so it doesn't replace TV files."
	case len(have) == 0:
		return Fix{}, "None of the episodes has a file Reelkeeper downloaded, so there's nothing to fix."
	case r.Revision < 2:
		return Fix{}, "It isn't a PROPER or REPACK."
	case r.Episode == nil:
		return Fix{}, "The name doesn't say which season and episode it is."
	}
	fix := Fix{Kind: "REPACK", Group: r.Group, Pack: r.Episode.AirDate == "" && len(r.Episode.Episodes) == 0}
	if r.Proper {
		fix.Kind = "PROPER"
	}
	q := QualityOf(r)

	for _, h := range have {
		if age := now.Sub(h.ImportedAt); age > FixWindow {
			return Fix{}, fmt.Sprintf("The file you have was downloaded %d days ago; fixes are only taken for %d days.",
				int(age/(24*time.Hour)), int(FixWindow/(24*time.Hour)))
		}
	}
	if fix.Pack {
		// Only a season whose files all came from one pack.
		first := have[0]
		old := parser.Parse(first.Release)
		oldIsPack := old.Episode != nil && old.Episode.AirDate == "" && len(old.Episode.Episodes) == 0
		for _, h := range have {
			if h.DownloadID == 0 || h.DownloadID != first.DownloadID {
				return Fix{}, "The files you have for this season didn't all come from one season pack, so a pack wouldn't fix them."
			}
		}
		if !oldIsPack {
			return Fix{}, "The files you have for this season didn't come from a season pack, so a pack wouldn't fix them."
		}
	}
	for _, h := range have {
		old := parser.Parse(h.Release)
		oq := QualityOf(old)
		switch {
		case oq.ID == 0 || oq.ID != q.ID:
			return Fix{}, fmt.Sprintf("It's %s, but the file you have is %s; a fix has to be the same quality.", q.Name, oq.Name)
		case r.Revision <= max(old.Revision, 1):
			return Fix{}, "It isn't a newer revision than the file you have."
		case !r.Proper && (r.Group == "" || !strings.EqualFold(r.Group, old.Group)):
			return Fix{}, fmt.Sprintf("It's a REPACK from %s, and REPACKs are only taken from the group that made the file you have (%s).",
				groupText(r.Group), groupText(old.Group))
		}
		fix.OldGroup = old.Group
	}
	return fix, ""
}
