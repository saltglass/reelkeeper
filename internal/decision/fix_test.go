package decision

import (
	"strings"
	"testing"
	"time"
)

var fixNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// have is a present file's download, imported d days before fixNow.
func have(id int64, release string, days int) Current {
	return Current{DownloadID: id, Release: release, ImportedAt: fixNow.Add(-time.Duration(days) * 24 * time.Hour)}
}

const (
	webNTb = "The.Expanse.S04E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
	packNT = "The.Expanse.S04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
)

func TestCheckFix(t *testing.T) {
	on := mustProfile(t, preset(t, "hd"))
	off := mustProfile(t, preset(t, "hd"))
	off.PreferPropers = false

	cases := []struct {
		name    string
		profile *Profile
		have    []Current
		release string
		kind    string // "" when it must be refused
		why     string // part of the reason when refused
	}{
		{"REPACK from the same group", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "REPACK", ""},
		{"same group, any capitalisation", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-ntb", "REPACK", ""},
		{"RERIP counts as a REPACK", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.RERIP.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "REPACK", ""},
		{"REPACK from another group", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "", "only taken from the group"},
		{"REPACK without a group", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264", "", "only taken from the group"},
		{"REPACK when the file has no group", on, []Current{have(1, "The.Expanse.S04E05.1080p.AMZN.WEB-DL.DDP5.1.H.264", 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264", "", "only taken from the group"},
		{"PROPER from another group", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.PROPER.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "PROPER", ""},
		{"PROPER from the same group", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.PROPER.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "PROPER", ""},
		{"profile doesn't prefer propers", off, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "doesn't prefer"},
		{"exactly 14 days old", on, []Current{have(1, webNTb, 14)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "REPACK", ""},
		{"15 days old", on, []Current{have(1, webNTb, 15)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "15 days ago"},
		{"no download record", on, nil,
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "nothing to fix"},
		{"different source", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.REPACK.1080p.WEBRip.x264-NTb", "", "same quality"},
		{"different resolution", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.PROPER.720p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "same quality"},
		{"better quality isn't a fix", on, []Current{have(1, "The.Expanse.S04E05.720p.HDTV.x264-NTb", 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "same quality"},
		{"a plain release", on, []Current{have(1, webNTb, 2)},
			"The.Expanse.S04E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "", "isn't a PROPER or REPACK"},
		{"equal revision", on, []Current{have(1, "The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2)},
			"The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "newer revision"},
		{"lower revision", on, []Current{have(1, "The.Expanse.S04E05.REPACK2.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2)},
			"The.Expanse.S04E05.PROPER.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "", "newer revision"},
		{"a second REPACK of a REPACK", on, []Current{have(1, "The.Expanse.S04E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 2)},
			"The.Expanse.S04E05.REPACK2.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "REPACK", ""},
		{"a multi-episode REPACK where one file is too old", on, []Current{have(1, webNTb, 2), have(2, "The.Expanse.S04E06.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 20)},
			"The.Expanse.S04E05E06.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "20 days ago"},

		// Season packs.
		{"pack REPACK, every file from the same pack", on,
			[]Current{have(7, packNT, 3), have(7, packNT, 3), have(7, packNT, 3)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "REPACK", ""},
		{"pack PROPER from another group", on,
			[]Current{have(7, packNT, 3), have(7, packNT, 3)},
			"The.Expanse.S04.PROPER.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "PROPER", ""},
		{"pack, files from mixed sources", on,
			[]Current{have(7, packNT, 3), have(8, packNT, 3)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "didn't all come from one season pack"},
		{"pack, files from single-episode downloads", on,
			[]Current{have(1, webNTb, 3), have(2, "The.Expanse.S04E06.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", 3)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "didn't all come from one season pack"},
		{"pack, one episode from a single-episode download", on,
			[]Current{have(1, webNTb, 3)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "didn't come from a season pack"},
		{"pack REPACK from another group", on,
			[]Current{have(7, packNT, 3), have(7, packNT, 3)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "", "only taken from the group"},
		{"pack imported 20 days ago", on,
			[]Current{have(7, packNT, 20), have(7, packNT, 20)},
			"The.Expanse.S04.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "20 days ago"},
		{"pack at another quality", on,
			[]Current{have(7, packNT, 3), have(7, packNT, 3)},
			"The.Expanse.S04.REPACK.720p.AMZN.WEB-DL.DDP5.1.H.264-NTb", "", "same quality"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fix, why := c.profile.CheckFix(c.have, c.release, fixNow)
			switch {
			case c.kind != "" && why != "":
				t.Fatalf("refused: %s", why)
			case c.kind != "" && fix.Kind != c.kind:
				t.Errorf("kind = %q, want %q", fix.Kind, c.kind)
			case c.kind == "" && why == "":
				t.Fatalf("accepted as %+v, want a refusal mentioning %q", fix, c.why)
			case c.kind == "" && !strings.Contains(why, c.why):
				t.Errorf("reason = %q, want it to mention %q", why, c.why)
			}
		})
	}
}

func TestFixDescribe(t *testing.T) {
	for _, c := range []struct {
		fix  Fix
		what string
		want string
	}{
		{Fix{Kind: "REPACK", Group: "NTb"}, "S02E05", "a REPACK of S02E05 from the same group (NTb), to fix the file you have"},
		{Fix{Kind: "PROPER", Group: "FLUX", OldGroup: "NTb"}, "S02E05", "a PROPER of S02E05 (FLUX) to fix the NTb release you have"},
		{Fix{Kind: "REPACK", Group: "NTb", Pack: true}, "season 2", "a REPACK of season 2 from the same group (NTb), to fix the file you have"},
	} {
		if got := c.fix.Describe(c.what); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
