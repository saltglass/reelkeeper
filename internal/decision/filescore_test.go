package decision

import (
	"testing"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// A file already in the library is scored by its name plus what its headers
// say (library.MediaFile.ScoringName). This checks the bundled TRaSH formats
// match that text the way they match a release name: a renamed Dolby Vision
// remux with TrueHD must not score as if it had none of it.
func TestBundledFormatsMatchScoringNames(t *testing.T) {
	q, _ := QualityByName("Remux-2160p")
	for _, tt := range []struct {
		name string
		want map[string]bool
	}{
		{"Tron Ares (2025) (2160p) REMUX DV HDR10 H.265 10bit TrueHD 7.1",
			map[string]bool{"hdr": true, "truehd": true, "truehd-atmos": false, "x265": false}},
		{"Tron Ares (2025) (2160p) REMUX DV HDR10 H.265 10bit TrueHD Atmos 7.1-FraMeSToR",
			map[string]bool{"hdr": true, "truehd-atmos": true, "remux-tier-01": true}},
		{"Tron Ares (2025) (2160p) REMUX",
			map[string]bool{"hdr": false, "truehd": false}},
	} {
		for file, want := range tt.want {
			cf, err := trashFormat(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := cf.Compile(); err != nil {
				t.Fatal(err)
			}
			in := Input{Name: tt.name, Release: parser.Parse(tt.name), Quality: q}
			if got := cf.Matches(in); got != want {
				t.Errorf("%s on %q = %v, want %v", cf.Name, tt.name, got, want)
			}
		}
	}
}
