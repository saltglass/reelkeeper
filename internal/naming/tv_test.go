package naming

import (
	"strings"
	"testing"
)

var quest = Series{Title: "Mythic Quest", Year: 2020, TMDBID: 94951, TVDBID: 362829, IMDbID: "tt8879940"}

var webdl = File{Quality: "WEBDL-1080p", Source: "webdl", VideoCodec: "H.264", AudioCodec: "EAC3 Atmos", AudioChannels: "5.1", Group: "AMCON"}

func TestFormatTV(t *testing.T) {
	d := Defaults()
	pilot := []Episode{{Season: 1, Number: 1, Title: "Pilot"}}
	cases := []struct {
		name, got, want string
	}{
		// The defaults reproduce the names Sonarr gave with TRaSH's scheme.
		{"default series folder", FormatSeries(d.SeriesFolder, quest), "Mythic Quest (2020) {tvdb-362829}"},
		{"default season folder", FormatSeason(d.SeasonFolder, quest, 1), "Season 1"},
		{"specials folder", FormatSeason(d.SeasonFolder, quest, 0), "Specials"},
		{"padded season folder", FormatSeason("Season {season:00}", quest, 3), "Season 03"},
		{"default episode", FormatEpisode(d.EpisodeFile, quest, pilot, webdl, d.MultiEpisodeStyle),
			"Mythic Quest (2020) - S01E01 - Pilot [WEBDL-1080p][EAC3 Atmos 5.1][h264]-AMCON"},
		{"empty media info vanishes", FormatEpisode(d.EpisodeFile, quest, pilot, File{Quality: "HDTV-720p"}, ""),
			"Mythic Quest (2020) - S01E01 - Pilot [HDTV-720p]"},
		{"clean title", FormatEpisode("{Episode CleanTitle}", quest, []Episode{{Season: 2, Number: 17, Title: "You've Been: Disconnected"}}, File{}, ""),
			"Youve Been Disconnected"},
		{"title shortened", FormatEpisode("{Episode Title:10}", quest, []Episode{{Season: 1, Number: 1, Title: "A Very Long Episode Title"}}, File{}, ""),
			"A Very Lon"},
		{"specials number", FormatEpisode("S{season:00}E{episode:00}", quest, []Episode{{Season: 0, Number: 3}}, File{}, ""), "S00E03"},
		{"older style", FormatEpisode("{season}x{episode:00}", quest, []Episode{{Season: 1, Number: 2}}, File{}, ""), "1x02"},
		{"dots", FormatEpisode("{Series.Title}.S{season:00}E{episode:00}", quest, pilot, File{}, ""), "Mythic.Quest.S01E01"},
		{"title already has its year", FormatSeries("{Series TitleYear}", Series{Title: "Doctor Who (2005)", Year: 2005}), "Doctor Who (2005)"},
		{"title without year", FormatSeries("{Series TitleWithoutYear}", Series{Title: "Doctor Who (2005)", Year: 2005}), "Doctor Who"},
		{"daily", FormatEpisode(d.DailyEpisodeFile, Series{Title: "The Daily Thing", Year: 2010},
			[]Episode{{Season: 2024, Number: 51, Title: "Guest Name", AirDate: "2024-03-15"}}, File{Quality: "WEBDL-720p"}, ""),
			"The Daily Thing (2010) - 2024-03-15 - Guest Name [WEBDL-720p]"},
		{"air date with dots", FormatEpisode("{Air.Date}", quest, []Episode{{AirDate: "2024-03-15"}}, File{}, ""), "2024.03.15"},
		{"ids", FormatSeries("{Series Title} {tmdb-{TmdbId}} {imdb-{ImdbId}}", quest), "Mythic Quest {tmdb-94951} {imdb-tt8879940}"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

func TestMultiEpisodeStyles(t *testing.T) {
	eps := []Episode{{Season: 1, Number: 1, Title: "A"}, {Season: 1, Number: 2, Title: "B"}, {Season: 1, Number: 3, Title: "C"}}
	tpl := "Show - S{season:00}E{episode:00}"
	for _, st := range MultiEpisodeStyles {
		if got := FormatEpisode(tpl, Series{}, eps, File{}, st.ID); got != "Show - "+st.Example {
			t.Errorf("%s = %q, want %q", st.ID, got, "Show - "+st.Example)
		}
	}
	// Titles: two parts of one story become one title; others are joined.
	parts := []Episode{{Season: 5, Number: 1, Title: "A Little Kiss (1)"}, {Season: 5, Number: 2, Title: "A Little Kiss (2)"}}
	if got := FormatEpisode(Defaults().EpisodeFile, Series{Title: "Mad Men", Year: 2007}, parts, File{Quality: "Bluray-1080p"}, "prefixed-range"); got != "Mad Men (2007) - S05E01-E02 - A Little Kiss [Bluray-1080p]" {
		t.Errorf("two parts = %q", got)
	}
	two := []Episode{{Season: 1, Number: 9, Title: "Critical Mass"}, {Season: 1, Number: 10, Title: "Leviathan Wakes"}}
	if got := FormatEpisode("{Episode Title}", Series{}, two, File{}, ""); got != "Critical Mass + Leviathan Wakes" {
		t.Errorf("two titles = %q", got)
	}
}

func TestCheckKinds(t *testing.T) {
	d := Defaults()
	for k, tpl := range map[Kind]string{
		SeriesFolder: d.SeriesFolder, SeasonFolder: d.SeasonFolder, EpisodeFile: d.EpisodeFile, DailyEpisodeFile: d.DailyEpisodeFile,
		MovieFolder: d.MovieFolder, MovieFile: d.MovieFile,
	} {
		if p := CheckKind(k, tpl); p != "" {
			t.Errorf("default %d: %s", k, p)
		}
	}
	bad := []struct {
		k    Kind
		tpl  string
		want string
	}{
		{EpisodeFile, "{Series Title} - {Episode Title}", "Include {season} and {episode}"},
		{SeasonFolder, "Season", "Include {season}"},
		{SeriesFolder, "{tvdb-{TvdbId}}", "Include {Series Title}"},
		{SeriesFolder, "{Movie Title}", "doesn't know {Movie Title}"},
		{SeriesFolder, "{Series Title} {episode}", "doesn't know {episode}"},
		{MovieFile, "{Movie Title} {Episode Title}", "doesn't know {Episode Title}"},
		{DailyEpisodeFile, "{Series Title} {Episode Title}", "Include {Air-Date}"},
	}
	for _, c := range bad {
		if p := CheckKind(c.k, c.tpl); !strings.Contains(p, c.want) {
			t.Errorf("CheckKind(%d, %q) = %q, want it to mention %q", c.k, c.tpl, p, c.want)
		}
	}
}
