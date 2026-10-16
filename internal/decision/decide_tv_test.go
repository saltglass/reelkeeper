package decision

import (
	"strings"
	"testing"
)

var expanse = Show{Title: "The Expanse", Year: 2015, OriginalLanguage: LanguageEnglish}

func expanseS4(eps []int, pack bool) EpisodeTarget {
	rt := map[int]int{}
	for n := 1; n <= 10; n++ {
		rt[n] = 50
	}
	return EpisodeTarget{Season: 4, Episodes: eps, Runtime: rt, SeasonEpisodes: 10, AllowPack: pack}
}

func TestDecideEpisode(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	cases := []struct {
		name   string
		target EpisodeTarget
		size   int64
		accept bool
		reason string
		covers Covers
	}{
		{"The.Expanse.S04E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, true, "",
			Covers{Season: 4, Episodes: []int{5}}},
		// A multi-episode file holding a wanted episode is fine.
		{"The.Expanse.S04E05E06.1080p.WEB-DL.H.264-NTb", expanseS4([]int{6}, false), 3 * gb, true, "",
			Covers{Season: 4, Episodes: []int{5, 6}}},
		{"The.Expanse.S04E07.1080p.WEB-DL.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, false, "episode 7, which isn't wanted", Covers{}},
		{"The.Expanse.S03E05.1080p.WEB-DL.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, false, "season 3; this search is for season 4", Covers{}},
		{"The.Expanse.S04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", expanseS4([]int{1, 2, 3}, true), 20 * gb, true, "",
			Covers{Season: 4, Pack: true}},
		{"The.Expanse.S04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", expanseS4([]int{3}, false), 20 * gb, false, "whole season; only episode 3 wanted", Covers{}},
		{"Expanse.Unrelated.S04E05.1080p.WEB-DL.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, false, "not The Expanse", Covers{}},
		{"The.Expanse.2015.S04E05.1080p.WEB-DL.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, true, "", Covers{Season: 4, Episodes: []int{5}}},
		{"The.Expanse.2004.S04E05.1080p.WEB-DL.H.264-NTb", expanseS4([]int{5}, false), 2 * gb, false, "started in 2015", Covers{}},
	}
	for _, c := range cases {
		d, cov := DecideEpisode(p, expanse, c.target, Candidate{Name: c.name, Size: c.size, Seeders: 20})
		if d.Accepted != c.accept {
			t.Errorf("%s: accepted = %v, reasons %v", c.name, d.Accepted, d.Rejections)
			continue
		}
		if c.reason != "" && !strings.Contains(strings.Join(d.Rejections, " "), c.reason) {
			t.Errorf("%s: reasons %v, want one mentioning %q", c.name, d.Rejections, c.reason)
		}
		if c.accept && (cov.Season != c.covers.Season || cov.Pack != c.covers.Pack || len(cov.Episodes) != len(c.covers.Episodes)) {
			t.Errorf("%s: covers %+v, want %+v", c.name, cov, c.covers)
		}
	}
}

// WrongContent tells a release for something else (another show, season or
// episode) from the right content that the profile turns down. Backfill's
// options offer only the second kind.
func TestDecideEpisodeWrongContent(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	cases := []struct {
		name  string
		pack  bool
		wrong bool
	}{
		{"The.Expanse.S04E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", false, false}, // accepted
		{"The.Expanse.S04E05.720p.HDTV.x264-KILLERS", false, false},             // right episode, outside the profile
		{"The.Expanse.S04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", false, false},    // a pack holds the episode
		{"Expanse.Unrelated.S04E05.1080p.WEB-DL.H.264-NTb", false, true},        // another show
		{"The.Expanse.S03E05.1080p.WEB-DL.H.264-NTb", false, true},              // another season
		{"The.Expanse.S04E07.1080p.WEB-DL.H.264-NTb", false, true},              // another episode
		{"The.Expanse.S04E05", false, true},                                     // no quality in the name
		{"The.Expanse.1080p.WEB-DL.H.264-NTb", false, true},                     // doesn't say which
		{"The.Expanse.2004.S04E05.1080p.WEB-DL.H.264-NTb", false, true},         // the wrong year: another show
	}
	for _, c := range cases {
		d, _ := DecideEpisode(p, expanse, expanseS4([]int{5}, c.pack), Candidate{Name: c.name, Size: 2 * gb, Seeders: 20})
		if d.WrongContent != c.wrong {
			t.Errorf("%s: WrongContent = %v, want %v (reasons %v)", c.name, d.WrongContent, c.wrong, d.Rejections)
		}
	}
}

// Size limits count every episode a release holds, by their real runtimes.
func TestEpisodeSizeLimits(t *testing.T) {
	s := preset(t, "hd")
	s.Sizes = map[string]SizeRange{"1080": {Max: 2.5}} // GB an hour
	p := mustProfile(t, s)
	// A pack of 10 x 50 minutes may be ~20.8 GB.
	d, _ := DecideEpisode(p, expanse, expanseS4([]int{1}, true), Candidate{Name: "The.Expanse.S04.1080p.BluRay.x264-ROVERS", Size: 40 * gb, Seeders: 20})
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "10 episodes (500 minutes)") {
		t.Errorf("pack: %v", d.Rejections)
	}
	d, _ = DecideEpisode(p, expanse, expanseS4([]int{1}, true), Candidate{Name: "The.Expanse.S04.1080p.BluRay.x264-ROVERS", Size: 18 * gb, Seeders: 20})
	if !d.Accepted {
		t.Errorf("pack within the limit: %v", d.Rejections)
	}
	d, _ = DecideEpisode(p, expanse, expanseS4([]int{5}, false), Candidate{Name: "The.Expanse.S04E05.1080p.BluRay.x264-ROVERS", Size: 4 * gb, Seeders: 20})
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "a 50-minute episode") {
		t.Errorf("episode: %v", d.Rejections)
	}
}

// Daily shows' releases are named by air date.
func TestDecideDailyEpisode(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	show := Show{Title: "The Daily Show", Year: 1996, OriginalLanguage: LanguageEnglish}
	target := EpisodeTarget{Season: 31, Episodes: []int{120}, AirDates: []string{"2026-10-09"}, Runtime: map[int]int{120: 30}}
	d, cov := DecideEpisode(p, show, target, Candidate{Name: "The.Daily.Show.2026.10.09.1080p.WEB.h264-GRP", Size: gb, Seeders: 9})
	if !d.Accepted || cov.AirDate != "2026-10-09" {
		t.Errorf("wanted date: %v %+v", d.Rejections, cov)
	}
	d, _ = DecideEpisode(p, show, target, Candidate{Name: "The.Daily.Show.2026.10.08.1080p.WEB.h264-GRP", Size: gb, Seeders: 9})
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "from 2026-10-08, which isn't wanted") {
		t.Errorf("other date: %v", d.Rejections)
	}
}

func TestEpisodeList(t *testing.T) {
	for in, want := range map[string]string{"3": "episode 3", "3,4": "episodes 3 and 4", "3,4,5": "episodes 3, 4 and 5"} {
		var eps []int
		for _, s := range strings.Split(in, ",") {
			n := 0
			for _, ch := range s {
				n = n*10 + int(ch-'0')
			}
			eps = append(eps, n)
		}
		if got := episodeList(eps); got != want {
			t.Errorf("%v = %q, want %q", eps, got, want)
		}
	}
}
