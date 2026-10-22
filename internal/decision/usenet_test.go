package decision

import (
	"reflect"
	"strings"
	"testing"
)

// Quality decides first; Usenet only wins between releases that are
// otherwise equally good (same quality, revision and score).
func TestUsenetBreaksTiesOnly(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	torrent := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-GRP", Seeders: 300, Size: 12 << 30}, nil)
	usenet := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-OTHER", Usenet: true, Size: 8 << 30}, nil)
	worseUsenet := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.WEB-DL.DDP5.1.H.264-GRP", Usenet: true}, nil)
	for _, d := range []Decision{torrent, usenet, worseUsenet} {
		if !d.Accepted || d.Usenet != (d.Name != torrent.Name) {
			t.Fatalf("%s: accepted %v %v, usenet %v", d.Name, d.Accepted, d.Rejections, d.Usenet)
		}
	}
	ds := []Decision{worseUsenet, torrent, usenet}
	p.Best(ds)
	got := []string{ds[0].Name, ds[1].Name, ds[2].Name}
	// The Usenet Blu-ray beats the equally good (and bigger, well seeded)
	// torrent; the Usenet WEB-DL is a lower quality, so it comes last.
	want := []string{usenet.Name, torrent.Name, worseUsenet.Name}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
	// Two Usenet releases: the bigger one, as for well-seeded torrents.
	small := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-A", Usenet: true, Size: 6 << 30}, nil)
	big := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-B", Usenet: true, Size: 9 << 30}, nil)
	if p.Compare(big, small) >= 0 {
		t.Error("the bigger Usenet release should win")
	}
}

// Usenet releases have no seeders; the minimum doesn't apply to them.
func TestUsenetIgnoresMinSeeders(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	d := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-GRP", MinSeeders: 3, Usenet: true}, nil)
	if !d.Accepted {
		t.Errorf("rejected: %v", d.Rejections)
	}
	d, _ = DecideEpisode(p, Show{Title: "Harbor Lights", Year: 2024}, EpisodeTarget{Season: 1, Episodes: []int{4}},
		Candidate{Name: "Harbor.Lights.S01E04.1080p.WEB-DL.H.264-GRP", MinSeeders: 3, Usenet: true})
	for _, r := range d.Rejections {
		if strings.Contains(r, "seeder") {
			t.Errorf("TV rejected for seeders: %v", d.Rejections)
		}
	}
}
