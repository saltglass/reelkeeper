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

// expanseSeasons is a search for several whole seasons of a six-season
// show whose seasons have 10 episodes of 50 minutes.
func expanseSeasons(ended bool, wanted ...int) EpisodeTarget {
	seasons := map[int]SeasonInfo{}
	for n := 1; n <= 6; n++ {
		seasons[n] = SeasonInfo{Episodes: 10, Minutes: 500}
	}
	return EpisodeTarget{WantedSeasons: wanted, Seasons: seasons, Ended: ended}
}

// Packs of several seasons (2026-11-28: a show's only well-seeded copy
// held all five seasons, and searching only knew one season at a time).
func TestDecideMultiSeasonPacks(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	pack := func(name string) Candidate { return Candidate{Name: name, Size: 60 * gb, Seeders: 86} }
	cases := []struct {
		name   string
		target EpisodeTarget
		accept bool
		wrong  bool
		reason string
		covers Covers
	}{
		{"The.Expanse.S01-S06.COMPLETE.SERIES.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2, 3, 4, 5, 6), true, false, "",
			Covers{Season: 1, LastSeason: 6, Pack: true}},
		// Holding seasons the version already has is fine; importing skips them.
		{"The.Expanse.S01-S06.1080p.BluRay.x265-GRP", expanseSeasons(true, 5, 6), true, false, "",
			Covers{Season: 1, LastSeason: 6, Pack: true}},
		{"The.Expanse.Complete.Series.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2, 3), true, false, "",
			Covers{AllSeasons: true, Pack: true}},
		// A single season's pack in a search for several is fine too.
		{"The.Expanse.S02.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2, 3), true, false, "", Covers{Season: 2, Pack: true}},
		{"The.Expanse.S04.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2, 3), false, false, "season 4, which isn't wanted as a whole", Covers{}},
		{"The.Expanse.S02E03.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2, 3), false, false, "this search is for whole seasons", Covers{}},
		{"The.Expanse.S01-S03.1080p.BluRay.x265-GRP", expanseSeasons(true, 3, 4), false, false, "holds only one of the seasons wanted", Covers{}},
		{"The.Expanse.S01-S02.1080p.BluRay.x265-GRP", expanseSeasons(true, 4, 5), false, false, "holds none of the seasons wanted", Covers{}},
		// A range naming a season the show doesn't have is another show
		// (or a mislabelled one).
		{"The.Expanse.S01-S08.1080p.BluRay.x265-GRP", expanseSeasons(true, 1, 2), false, true, "has no season 7", Covers{}},
		// A running show's "complete series" is older than its last season.
		{"The.Expanse.Complete.Series.1080p.BluRay.x265-GRP", expanseSeasons(false, 1, 2), false, false, "still running", Covers{}},
		// A search for one season turns a pack of several down; it isn't the
		// wrong content when it holds that season.
		{"The.Expanse.S01-S06.1080p.BluRay.x265-GRP", expanseS4([]int{1, 2, 3}, true), false, false, "only season 4 is wanted as a whole", Covers{}},
		{"The.Expanse.S01-S03.1080p.BluRay.x265-GRP", expanseS4([]int{1, 2, 3}, true), false, true, "this search is for season 4", Covers{}},
	}
	for _, c := range cases {
		d, cov := DecideEpisode(p, expanse, c.target, pack(c.name))
		if d.Accepted != c.accept || d.WrongContent != c.wrong {
			t.Errorf("%s: accepted = %v, wrong = %v, reasons %v", c.name, d.Accepted, d.WrongContent, d.Rejections)
			continue
		}
		if c.reason != "" && !strings.Contains(strings.Join(d.Rejections, " "), c.reason) {
			t.Errorf("%s: reasons %v, want one mentioning %q", c.name, d.Rejections, c.reason)
		}
		if c.accept && (cov.Season != c.covers.Season || cov.LastSeason != c.covers.LastSeason || cov.AllSeasons != c.covers.AllSeasons || cov.Pack != c.covers.Pack) {
			t.Errorf("%s: covers %+v, want %+v", c.name, cov, c.covers)
		}
	}
}

// A pack of several seasons is held to the size limit of everything in it.
func TestMultiSeasonSizeLimit(t *testing.T) {
	s := preset(t, "hd")
	s.Sizes = map[string]SizeRange{"1080": {Max: 2.5}} // GB an hour: 6 x 500 minutes may be ~125 GB
	p := mustProfile(t, s)
	target := expanseSeasons(true, 1, 2, 3, 4, 5, 6)
	d, _ := DecideEpisode(p, expanse, target, Candidate{Name: "The.Expanse.S01-S06.1080p.BluRay.x264-GRP", Size: 200 * gb, Seeders: 20})
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "6 seasons, 60 episodes (3000 minutes)") {
		t.Errorf("too big: %v", d.Rejections)
	}
	d, _ = DecideEpisode(p, expanse, target, Candidate{Name: "The.Expanse.S01-S06.1080p.BluRay.x264-GRP", Size: 100 * gb, Seeders: 20})
	if !d.Accepted {
		t.Errorf("within the limit: %v", d.Rejections)
	}
	// A season's pack in the same search is held to that season alone.
	d, _ = DecideEpisode(p, expanse, target, Candidate{Name: "The.Expanse.S02.1080p.BluRay.x264-GRP", Size: 30 * gb, Seeders: 20})
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "1 season, 10 episodes (500 minutes)") {
		t.Errorf("one season: %v", d.Rejections)
	}
}

func TestBreadth(t *testing.T) {
	several := expanseSeasons(true, 2, 3, 4)
	one := expanseS4(nil, true)
	for _, c := range []struct {
		cov          Covers
		several, one int
	}{
		{Covers{Season: 4, Episodes: []int{1}}, 0, 0},
		{Covers{Season: 4, Pack: true}, 1, 1},
		{Covers{Season: 1, LastSeason: 3, Pack: true}, 2, 0},
		{Covers{Season: 1, LastSeason: 6, Pack: true}, 3, 1},
		{Covers{AllSeasons: true, Pack: true}, 3, 1},
	} {
		if got := several.Breadth(c.cov); got != c.several {
			t.Errorf("%+v in a search for seasons 2-4: %d, want %d", c.cov, got, c.several)
		}
		if got := one.Breadth(c.cov); got != c.one {
			t.Errorf("%+v in a search for season 4: %d, want %d", c.cov, got, c.one)
		}
	}
}
