package parser

import (
	"reflect"
	"testing"
)

// Made-up TV names in every style seen in real libraries. Safe to publish.
func TestParseEpisodes(t *testing.T) {
	type want struct {
		title string
		year  int
		ep    *Episode
		group string
	}
	cases := []struct {
		name string
		want want
	}{
		// Sonarr's own naming, with the episode title.
		{"Some Show (2020) - S01E01 - Pilot [WEBDL-1080p][EAC3 Atmos 5.1][h264]-GRP.mkv",
			want{"Some Show", 2020, &Episode{Season: 1, Episodes: []int{1}, Title: "Pilot"}, "GRP"}},
		{"Some Show (2020) - S02E03 - #Hashtag Day [WEBDL-720p][EAC3 Atmos 5.1][h264]-GRP.mkv",
			want{"Some Show", 2020, &Episode{Season: 2, Episodes: []int{3}, Title: "#Hashtag Day"}, "GRP"}},
		{"Some Show (2020) - S01E01 - Pilot (1080p ATVP WEB-DL x265 grp).mkv",
			want{"Some Show", 2020, &Episode{Season: 1, Episodes: []int{1}, Title: "Pilot"}, "grp"}},
		{"Some Show - S01E06 - The Thing WEBRip-1080p Proper.mkv",
			want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{6}, Title: "The Thing"}, ""}},
		{"Some Show (2005) - S04E08 - A Library 1 [Bluray-720p][AAC 2.0][x264].mkv",
			want{"Some Show", 2005, &Episode{Season: 4, Episodes: []int{8}, Title: "A Library 1"}, ""}},
		// Multi-episode files.
		{"Some Show (2007) - S05E01-E02 - A Long Kiss [Bluray-1080p][DTS 5.1][x264]-grp.mkv",
			want{"Some Show", 2007, &Episode{Season: 5, Episodes: []int{1, 2}, Title: "A Long Kiss"}, "grp"}},
		{"Some.Show.S01E01E02.720p.HDTV.x264-GRP.mkv", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{1, 2}}, "GRP"}},
		{"Some.Show.S01E01-03.720p.HDTV.x264-GRP.mkv", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{1, 2, 3}}, "GRP"}},
		// Scene names; the year inside belongs to the series.
		{"Some.Show.2011.S01E08.1080p.BluRay.AV1-GRP.mkv", want{"Some Show", 2011, &Episode{Season: 1, Episodes: []int{8}}, "GRP"}},
		{"some.show.s01e08.1080p.bluray.x264-grp.mkv", want{"some show", 0, &Episode{Season: 1, Episodes: []int{8}}, "grp"}},
		{"Some.Show.S04E07.PROPER.1080p.WEB.H264-GRP[TGx].mkv", want{"Some Show", 0, &Episode{Season: 4, Episodes: []int{7}}, "GRP"}},
		{"The.Some.Show.S03E08.Through.the.Mists.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP.mkv",
			want{"The Some Show", 0, &Episode{Season: 3, Episodes: []int{8}, Title: "Through the Mists"}, "GRP"}},
		{"Some Show S08E04 The Last of Them.1080p.BluRay.x265.10bit.TrueHD.7.1-GRP[UTR].mkv",
			want{"Some Show", 0, &Episode{Season: 8, Episodes: []int{4}, Title: "The Last of Them"}, "GRP"}},
		{"Some Show S01E10 I Felt a Funeral 1080p ATVP WEB-DL H 264 DDP5.mkv",
			want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{10}, Title: "I Felt a Funeral"}, ""}},
		// A resolution stuck to the episode title.
		{"Some Show S03E10 This was a Poet-1080p ATVP WEB-DL H 264 DDP5 1 Atmos-GRP.mkv",
			want{"Some Show", 0, &Episode{Season: 3, Episodes: []int{10}, Title: "This was a Poet"}, "GRP"}},
		{"Some Show S01E05.mp4", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{5}}, ""}},
		// Season and episode apart.
		{"Some.Show.S01.Ep01.1080p.BluRay.DTS.x264-GRP.mkv", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{1}}, "GRP"}},
		{"Some Show S01 E02 720p.mkv", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{2}}, ""}},
		{"Some Show Season 2 Episode 5.mkv", want{"Some Show", 0, &Episode{Season: 2, Episodes: []int{5}}, ""}},
		// The older 1x02 style.
		{"Some.Show.1x02.720p.mkv", want{"Some Show", 0, &Episode{Season: 1, Episodes: []int{2}}, ""}},
		{"Some Show 3x10-3x11 Finale.mkv", want{"Some Show", 0, &Episode{Season: 3, Episodes: []int{10, 11}, Title: "Finale"}, ""}},
		// Specials and episodes without a season.
		{"S00E03 - Some Special (1080p HDTV x265 grp).mkv", want{"", 0, &Episode{Season: 0, Episodes: []int{3}, Title: "Some Special"}, "grp"}},
		{"Some Show E05 720p.mkv", want{"Some Show", 0, &Episode{Season: -1, Episodes: []int{5}}, ""}},
		// Daily shows.
		{"Some.Talk.Show.2024.03.15.Guest.Name.720p.WEB.h264-GRP.mkv",
			want{"Some Talk Show", 0, &Episode{Season: -1, AirDate: "2024-03-15", Title: "Guest Name"}, "GRP"}},
		{"Some Talk Show - 2024-03-15 - Guest Name [WEBDL-720p].mkv",
			want{"Some Talk Show", 0, &Episode{Season: -1, AirDate: "2024-03-15", Title: "Guest Name"}, ""}},
		// Season packs.
		{"Some.Show.S02.1080p.BluRay.x264-GRP", want{"Some Show", 0, &Episode{Season: 2}, "GRP"}},
		// Not episodes: movies keep their titles.
		{"Season of the Witch (2011).mkv", want{"Season of the Witch", 2011, nil, ""}},
		{"Some Movie 2024 03 Remastered.mkv", want{"Some Movie", 2024, nil, ""}},
		{"S1m0ne (2002).mkv", want{"S1m0ne", 2002, nil, ""}},
		// A title that is a date-like year stays a title.
		{"2012 (2009) Bluray-1080p.mkv", want{"2012", 2009, nil, ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Parse(c.name)
			got := want{r.Title, r.Year, r.Episode, r.Group}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("\n got %+v ep=%+v\nwant %+v ep=%+v", got, r.Episode, c.want, c.want.ep)
			}
		})
	}
}

func TestSeasonFolder(t *testing.T) {
	cases := map[string]int{
		"Season 1": 1, "Season 01": 1, "season 12": 12, "S03": 3, "Specials": 0, "Series 2": 2,
		"Season 1 (2019)": 1, "Staffel 4": 4,
	}
	for name, want := range cases {
		if got, ok := SeasonFolder(name); !ok || got != want {
			t.Errorf("SeasonFolder(%q) = %d, %v; want %d", name, got, ok, want)
		}
	}
	for _, name := range []string{"Featurettes", "Some Show (2020)", "Seasonal", "Extras"} {
		if _, ok := SeasonFolder(name); ok {
			t.Errorf("SeasonFolder(%q) should be false", name)
		}
	}
}
