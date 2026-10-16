package naming

import (
	"strings"
	"testing"
)

var knight = Movie{
	Title: "The Dark Knight", OriginalTitle: "The Dark Knight", Year: 2008, TMDBID: 155, IMDbID: "tt0468569",
	Collection: "The Dark Knight Collection", Certification: "PG-13", Profile: "Remux",
}

var remux = File{
	Quality: "Remux-2160p", Edition: "IMAX", Group: "FraMeSToR", Source: "remux",
	VideoCodec: "H.265", VideoBitDepth: 10, HDR: []string{"DV", "HDR10"},
	AudioCodec: "TrueHD Atmos", AudioChannels: "7.1",
}

func TestFormat(t *testing.T) {
	plain := File{Quality: "WEBDL-1080p", Source: "webdl", VideoCodec: "H.264", AudioCodec: "EAC3", AudioChannels: "5.1"}
	cases := []struct {
		name, tpl string
		m         Movie
		f         File
		want      string
	}{
		{"default folder", Defaults().MovieFolder, knight, remux, "The Dark Knight (2008)"},
		{"default file", Defaults().MovieFile, knight, remux, "The Dark Knight (2008) [IMAX][Remux-2160p][DV HDR10]-FraMeSToR"},
		{"default file, empty tokens vanish", Defaults().MovieFile, Movie{Title: "Amélie", Year: 2001}, File{}, "Amélie (2001)"},
		{"colon replaced", "{Movie Title}", Movie{Title: "Dune: Part Two"}, File{}, "Dune - Part Two"},
		{"clean title", "{Movie CleanTitle}", Movie{Title: "Spider-Man: Across the Spider-Verse"}, File{}, "Spider-Man Across the Spider-Verse"},
		{"clean title drops apostrophes", "{Movie CleanTitle}", Movie{Title: "Ocean's Eleven & Co."}, File{}, "Oceans Eleven and Co"},
		{"title the", "{Movie TitleThe}", knight, File{}, "Dark Knight, The"},
		{"first character skips the article", "{Movie TitleFirstCharacter}/x", knight, File{}, "D-x"},
		{"dot separator", "{Movie.Title}.{Release.Year}", knight, File{}, "The.Dark.Knight.2008"},
		{"upper case", "{MOVIE TITLE}", knight, File{}, "THE DARK KNIGHT"},
		{"lower case", "{movie title}", knight, File{}, "the dark knight"},
		{"plex tags", "{Movie Title} {tmdb-{TmdbId}} {imdb-{ImdbId}} {edition-{Edition Tags}}", knight, remux,
			"The Dark Knight {tmdb-155} {imdb-tt0468569} {edition-IMAX}"},
		{"plex tags vanish when empty", "{Movie Title} {edition-{Edition Tags}} {tmdb-{TmdbId}}", Movie{Title: "X", TMDBID: 7}, File{}, "X {tmdb-7}"},
		{"proper", "{Quality Full}", knight, File{Quality: "Bluray-1080p", Revision: 2}, "Bluray-1080p Proper"},
		{"codec for an encode", "{MediaInfo VideoCodec}", knight, File{VideoCodec: "H.265", Source: "bluray"}, "x265"},
		{"codec for web", "{MediaInfo VideoCodec}", knight, plain, "h264"},
		{"dynamic range", "{MediaInfo VideoDynamicRange} {MediaInfo VideoDynamicRangeType}", knight, remux, "HDR DV HDR10"},
		{"profile for shared folders", "{Movie Title} ({Release Year}) - {Profile Name}", knight, remux, "The Dark Knight (2008) - Remux"},
		{"dangling dash removed", "{Movie Title} - {Edition Tags} - {Release Group}", Movie{Title: "X"}, File{}, "X"},
		{"empty parentheses removed", "{Movie Title} ({Release Year})", Movie{Title: "X"}, File{}, "X"},
		{"text braces kept", "{Movie Title} {not a token", Movie{Title: "X"}, File{}, "X {not a token"},
		{"trailing dots removed", "{Movie Title}", Movie{Title: "Monsters, Inc."}, File{}, "Monsters, Inc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Format(c.tpl, c.m, c.f); got != c.want {
				t.Errorf("Format(%q)\n got %q\nwant %q", c.tpl, got, c.want)
			}
		})
	}
}

// The TRaSH Guides Radarr scheme, pasted in unchanged, must work.
func TestTRaSHScheme(t *testing.T) {
	file := "{Movie CleanTitle} {(Release Year)} {tmdb-{TmdbId}} {edition-{Edition Tags}} {[Custom Formats]}{[Quality Full]}{[MediaInfo 3D]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[Mediainfo VideoCodec]}{-Release Group}"
	folder := "{Movie CleanTitle} ({Release Year}) {tmdb-{TmdbId}}"
	for _, tpl := range []string{file, folder} {
		if p := Check(tpl); p != "" {
			t.Fatalf("Check(%q) = %q", tpl, p)
		}
	}
	want := "The Dark Knight (2008) {tmdb-155} {edition-IMAX} [Remux-2160p][DV HDR10][TrueHD Atmos 7.1][h265]-FraMeSToR"
	if got := Format(file, knight, remux); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if got := Format(folder, knight, remux); got != "The Dark Knight (2008) {tmdb-155}" {
		t.Errorf("folder = %q", got)
	}
}

func TestCheck(t *testing.T) {
	cases := map[string]string{
		"{Movie Title} ({Release Year})": "",
		"{Movie CleanTitleThe}":          "",
		"":                               "can't be empty",
		"{Movie Title}/{Release Year}":   "not a path",
		"{Movie Titel}":                  "doesn't know {Movie Titel}",
		"{Release Year} {Quality Full}":  "Include {Movie Title}",
		"{Movie TitleFirstCharacter}":    "Include {Movie Title}",
	}
	for tpl, want := range cases {
		got := Check(tpl)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("Check(%q) = %q, want it to contain %q", tpl, got, want)
		}
	}
}

func FuzzFormat(f *testing.F) {
	f.Add(Defaults().MovieFile, "The Dark Knight")
	f.Add("{tmdb-{TmdbId}}{[{(", "A: B/C")
	f.Fuzz(func(t *testing.T, tpl, title string) {
		out := Format(tpl, Movie{Title: title, Year: 2000}, remux)
		if strings.ContainsAny(out, `/\:*?"<>|`) {
			t.Errorf("Format(%q, %q) = %q: contains a character not allowed in file names", tpl, title, out)
		}
	})
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Dune: Part Two (2024)": "Dune - Part Two (2024)",
		"What If...? (2021)":    "What If... (2021)",
		"AC/DC: Live":           "AC-DC - Live",
		`Say "Hello"`:           "Say 'Hello'",
		"Ends With Dot.":        "Ends With Dot",
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}
