package parser

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"path"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Hand-written cases: made-up names in every style seen in real libraries,
// plus one case per bug fixed. Safe to publish.
func TestParse(t *testing.T) {
	cases := []struct {
		name string
		want Release
	}{
		// Plain "Title (Year)".
		{"Some Movie (2010).mkv", Release{Title: "Some Movie", Year: 2010}},
		{"Some Movie (2010)", Release{Title: "Some Movie", Year: 2010}},
		// Radarr-style quality suffix.
		{"Some Movie (2013) Bluray-2160p.mkv", Release{Title: "Some Movie", Year: 2013, Resolution: 2160, Source: SourceBluRay}},
		{"Some Movie (2021) WEBDL-2160p Proper.mkv", Release{Title: "Some Movie", Year: 2021, Resolution: 2160, Source: SourceWebDL, Revision: 2, Proper: true}},
		{"A Movie - Part Two (2024) Remux-2160p.mkv", Release{Title: "A Movie - Part Two", Year: 2024, Resolution: 2160, Source: SourceRemux}},
		// Numbers in titles: a title that is a year, a year inside a title.
		{"1917 (2019) Remux-2160p.mkv", Release{Title: "1917", Year: 2019, Resolution: 2160, Source: SourceRemux}},
		{"Runner 2049 (2017) Bluray-2160p.mkv", Release{Title: "Runner 2049", Year: 2017, Resolution: 2160, Source: SourceBluRay}},
		{"300 Rise Of An Empire (2014).mkv", Release{Title: "300 Rise Of An Empire", Year: 2014}},
		{"Sequel 2 (2018).mkv", Release{Title: "Sequel 2", Year: 2018}},
		// Scene names with dots, including a year followed by a resolution.
		{"The.Sea.Thing.2022.1080p.WEBRip.x264-GRP.mp4", Release{Title: "The Sea Thing", Year: 2022, Resolution: 1080, Source: SourceWebRip, VideoCodec: "H.264", Group: "GRP"}},
		{"The.Thin.Red.Wire.1998.Criterion.Collection.BDRip.2160p.mkv", Release{Title: "The Thin Red Wire", Year: 1998, Resolution: 2160, Source: SourceBluRay, Edition: "Criterion"}},
		{"Some.Film.2025.2160p.iT.WEB-DL.DDP5.1.Atmos.DV.HDR.H.265-GRP.mkv", Release{
			Title: "Some Film", Year: 2025, Resolution: 2160, Source: SourceWebDL, Service: "iT",
			VideoCodec: "H.265", HDR: []string{"DV", "HDR"}, Audio: []string{"DD+", "Atmos"}, Channels: "5.1", Group: "GRP",
		}},
		{"Some.Film.2025.2160p.HDR10Plus.DV.WEBRip.6CH.x265.HEVC-GRP.mkv", Release{
			Title: "Some Film", Year: 2025, Resolution: 2160, Source: SourceWebRip, VideoCodec: "H.265",
			HDR: []string{"HDR10+", "DV"}, Channels: "5.1", Group: "GRP",
		}},
		{"Old.Film.1986.SE.PROPER.1080p.BluRay.H264.AAC-GRP[TGx].mp4", Release{
			Title: "Old Film", Year: 1986, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264", Audio: []string{"AAC"}, Revision: 2, Proper: true, Group: "GRP",
		}},
		{"Some.Film.2017.REPACK.1080p.BluRay.x264-GRP[EtHD].mkv", Release{Title: "Some Film", Year: 2017, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264", Revision: 2, Group: "GRP"}},
		// PROPER is another group's fix; REPACK, RERIP and "v2" are not (TV fixes
		// take a REPACK only from the group that made the original).
		{"Some.Film.2017.RERIP.1080p.BluRay.x264-GRP.mkv", Release{Title: "Some Film", Year: 2017, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264", Revision: 2, Group: "GRP"}},
		{"Some.Film.2017.REPACK2.1080p.BluRay.x264-GRP.mkv", Release{Title: "Some Film", Year: 2017, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264", Revision: 3, Group: "GRP"}},
		{"Some.Film.2017.REAL.PROPER.1080p.BluRay.x264-GRP.mkv", Release{Title: "Some Film", Year: 2017, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264", Revision: 3, Proper: true, Group: "GRP"}},
		// IDs in names.
		{"Some Movie (2024) {tmdb-519182} - [WEBRip-2160p][AAC 5.1][DV HDR10Plus][HEVC]-GRP.mkv", Release{
			Title: "Some Movie", Year: 2024, TMDBID: 519182, Resolution: 2160, Source: SourceWebRip, VideoCodec: "H.265",
			HDR: []string{"DV", "HDR10+"}, Audio: []string{"AAC"}, Channels: "5.1", Group: "GRP",
		}},
		{"Some Movie (2005) {tmdb-953} - [Bluray-1080p][AC3 5.1][x264]-GRP.mkv", Release{
			Title: "Some Movie", Year: 2005, TMDBID: 953, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264",
			Audio: []string{"DD"}, Channels: "5.1", Group: "GRP",
		}},
		{"Some Show (2026) {tvdb-433631}", Release{Title: "Some Show", Year: 2026, TVDBID: 433631}},
		{"Some Movie (2002) [imdbid-tt0289043]", Release{Title: "Some Movie", Year: 2002, IMDbID: "tt0289043"}},
		// Bracket lists (YTS style).
		{"Some Movie (2012) [2160p] [4K] [BluRay] [5.1] [YTS.MX].mkv", Release{Title: "Some Movie", Year: 2012, Resolution: 2160, Source: SourceBluRay, Channels: "5.1", Group: "YTS.MX"}},
		{"Some Movie (2023) [2160p] [4K] [WEB] [5.1] [YTS.MX].mkv", Release{Title: "Some Movie", Year: 2023, Resolution: 2160, Source: SourceWebDL, Channels: "5.1", Group: "YTS.MX"}},
		// Parenthesised spec with the group as its last word.
		{"Some Movie (2020) REPACK (1080p BluRay x265 HEVC 10bit AAC 7.1 Grp) [QxR].mkv", Release{
			Title: "Some Movie", Year: 2020, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.265", BitDepth: 10,
			Audio: []string{"AAC"}, Channels: "7.1", Revision: 2, Group: "QxR", // Radarr: the site tag after the spec wins
		}},
		{"Some Movie (2025) (2160p UHD BluRay x265 10bit DV HDR10+ TrueHD Atmos 7.1 grp).mkv", Release{
			Title: "Some Movie", Year: 2025, Resolution: 2160, Source: SourceBluRay, VideoCodec: "H.265", BitDepth: 10,
			HDR: []string{"DV", "HDR10+"}, Audio: []string{"TrueHD", "Atmos"}, Channels: "7.1", Group: "grp",
		}},
		// Titles with dashes, ampersands and apostrophes stay intact; a dash
		// inside a title is not a group.
		{"Hero-Man - Far From Home (2019) Bluray-1080p.mkv", Release{Title: "Hero-Man - Far From Home", Year: 2019, Resolution: 1080, Source: SourceBluRay}},
		{"Chip 'n Pal - Rescue Team (2022) WEBRip-2160p.mkv", Release{Title: "Chip 'n Pal - Rescue Team", Year: 2022, Resolution: 2160, Source: SourceWebRip}},
		{"Cats & Dogs (2001).mkv", Release{Title: "Cats & Dogs", Year: 2001}},
		// Junk prefixes from download sites.
		{"www.SomeSite.org    -    The.Show.S01E05.1080p.WEB.h264-GRP.mkv", Release{Title: "The Show", Resolution: 1080, Source: SourceWebDL, VideoCodec: "H.264", Group: "GRP", Episode: &Episode{Season: 1, Episodes: []int{5}}}},
		// A resolution stuck to the title by a dash.
		{"Some Movie Alive-1080p WEB-DL.mkv", Release{Title: "Some Movie Alive", Resolution: 1080, Source: SourceWebDL}},
		{"[ www.Example.com ] - Some.Movie.2020.1080p.WEB-DL.mkv", Release{Title: "Some Movie", Year: 2020, Resolution: 1080, Source: SourceWebDL}},
		// Streaming-service codes are only codes in the right place.
		{"It Was Red (2019) Bluray-2160p.mkv", Release{Title: "It Was Red", Year: 2019, Resolution: 2160, Source: SourceBluRay}},
		{"Some Film 2011 1080p DSNP WEB-DL H265 SDR DDP Atmos 5 1 English --GRP.mkv", Release{
			Title: "Some Film", Year: 2011, Resolution: 1080, Source: SourceWebDL, Service: "DSNP", VideoCodec: "H.265",
			Audio: []string{"DD+", "Atmos"}, Languages: []string{"English"}, Group: "GRP",
		}},
		// Editions and low-quality sources.
		{"Some Movie (2012) Extended (2160p UHD BluRay x265 HDR DD+ 7.1 GRP).mkv", Release{
			Title: "Some Movie", Year: 2012, Resolution: 2160, Source: SourceBluRay, VideoCodec: "H.265", HDR: []string{"HDR"},
			Audio: []string{"DD+"}, Channels: "7.1", Edition: "Extended", Group: "GRP",
		}},
		{"Some Movie (1982) Director's Cut Bluray-1080p.mkv", Release{Title: "Some Movie", Year: 1982, Resolution: 1080, Source: SourceBluRay, Edition: "Director's Cut"}},
		{"Some Movie (2025) TELESYNC.mkv", Release{Title: "Some Movie", Year: 2025, Source: SourceTeleSync}},
		{"Some Movie (2010) (REMUX).mkv", Release{Title: "Some Movie", Year: 2010, Source: SourceRemux}},
		// Lossless audio formats.
		{"Some.Movie.2009.1080p.BluRay.DTS-HD.MA.5.1.x264-GRP", Release{
			Title: "Some Movie", Year: 2009, Resolution: 1080, Source: SourceBluRay, VideoCodec: "H.264",
			Audio: []string{"DTS-HD MA"}, Channels: "5.1", Group: "GRP",
		}},
		{"Some Movie (2013) {tmdb-138832} - {edition-Extended} [Bluray-1080p]-GRP.mkv", Release{
			Title: "Some Movie", Year: 2013, TMDBID: 138832, Edition: "Extended", Resolution: 1080, Source: SourceBluRay, Group: "GRP",
		}},
		{"Some.Movie.1979.Redux.2160p.UHD.BluRay.x265-GRP", Release{
			Title: "Some Movie", Year: 1979, Edition: "Redux", Resolution: 2160, Source: SourceBluRay, VideoCodec: "H.265", Group: "GRP",
		}},
		{"Some.Movie.2022.REPACK.2160p.MA.WEBRip.x265-GRP", Release{
			Title: "Some Movie", Year: 2022, Revision: 2, Resolution: 2160, Service: "MA", Source: SourceWebRip, VideoCodec: "H.265", Group: "GRP",
		}},
		// Nothing but a title.
		{"Some Movie", Release{Title: "Some Movie"}},
		{"", Release{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.name)
			if !reflect.DeepEqual(got, c.want) {
				gb, _ := json.Marshal(got)
				wb, _ := json.Marshal(c.want)
				t.Errorf("\n got %s\nwant %s", gb, wb)
			}
		})
	}
}

func TestQuality(t *testing.T) {
	cases := map[string]string{
		"Some Movie (2013) Bluray-2160p.mkv": "Bluray-2160p",
		"Some.Movie.2022.1080p.WEBRip.x264":  "WEBRip-1080p",
		"Some Movie (2010) (REMUX).mkv":      "Remux-1080p", // no resolution: Radarr assumes 1080p
		"Some Movie (2023) [2160p].mkv":      "HDTV-2160p",  // Radarr reads a bare resolution as TV
		"Some Movie (2010).mkv":              "Unknown",
		"Some Movie (2021) WEBDL-720p.mkv":   "WEBDL-720p",
		"Some Movie (2025) TELESYNC.mkv":     "TELESYNC",
		// An .iso is a disc image, whatever the name claims.
		"Some Movie (2011) [Bluray-1080p]-R-US.iso": "BR-DISK",
		"Some.Movie.2011.1080p.BluRay.x264.ISO":     "BR-DISK",
	}
	for name, want := range cases {
		if got := Parse(name).Quality(); got != want {
			t.Errorf("%s: Quality() = %q, want %q", name, got, want)
		}
	}
}

func TestIsVideo(t *testing.T) {
	for name, want := range map[string]bool{
		"a.mkv": true, "A.MP4": true, "a.m2ts": true, "a.avi": true,
		"a-thumb.jpg": false, "a.srt": false, "a.nfo": false, "mkv": false, "a.mkv.part": false,
	} {
		if got := IsVideo(name); got != want {
			t.Errorf("IsVideo(%q) = %v", name, got)
		}
	}
}

var update = flag.Bool("update", false, "rewrite the library golden files with current results")

// TestLibraryNames checks every movie and TV file name in
// testdata/parser/library-*-paths.txt against a reviewed snapshot, so any
// change in how these names parse is noticed. The paths are a made-up media
// library (invented titles) in the folder layouts and release styles found
// in real libraries. Run with -update to accept changes, then review the
// diff.
func TestLibraryNames(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			checkLibrary(t, kind)
		})
	}
}

func checkLibrary(t *testing.T, kind string) {
	pathsFile := "../../testdata/parser/library-" + kind + "-paths.txt"
	goldenFile := "../../testdata/parser/library-" + kind + "-parses.golden.jsonl"
	f, err := os.Open(pathsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name := path.Base(sc.Text())
		r := Parse(name)
		b, _ := json.Marshal(struct {
			Name string  `json:"name"`
			Got  Release `json:"got"`
		}{name, r})
		lines = append(lines, string(b))

		// Every movie name has a title. (Episode names may not:
		// "S00E02 - Special.mkv" leaves the series to its folder.)
		if kind == "movie" && r.Title == "" {
			t.Errorf("%s: no title", name)
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	if *update {
		if err := os.WriteFile(goldenFile, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range min(len(gl), len(wl)) {
			if gl[i] != wl[i] {
				t.Errorf("first difference at line %d:\n got %s\nwant %s", i+1, gl[i], wl[i])
				break
			}
		}
		t.Error("parses differ from the reviewed snapshot; if the change is right, run with -update and review the diff")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"Some.Movie.2022.1080p.WEBRip.x264-GRP.mkv",
		"Some Movie (2024) {tmdb-1} - [WEBRip-2160p][AAC 5.1]-GRP.mkv",
		"www.site.org - x", "((((", "-", "..5.1..", "{tmdb-}", "tt", "H.2", "2160p-",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := Parse(s) // must not panic
		if !utf8.ValidString(s) {
			return
		}
		if r.Title != strings.TrimSpace(r.Title) {
			t.Errorf("title has outer spaces: %q", r.Title)
		}
		if r.Year != 0 && (r.Year < 1900 || r.Year > 2099) {
			t.Errorf("implausible year %d", r.Year)
		}
	})
}
