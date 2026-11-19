package library

import (
	"strings"
	"testing"
)

// The torrents here are made up, in the shapes real releases and real fakes
// take on public trackers.
func TestUnfitRelease(t *testing.T) {
	const gb = 1 << 30
	tests := []struct {
		name  string
		files []ReleaseFile
		want  string // a part of the reason; "" means fine
	}{
		{"single movie file", []ReleaseFile{{"Heat.1995.1080p.BluRay.x264-GRP.mkv", 9 * gb}}, ""},
		{"movie with nfo, subs and a sample", []ReleaseFile{
			{"Heat.1995.1080p.BluRay.x264-GRP/Heat.1995.1080p.BluRay.x264-GRP.mkv", 9 * gb},
			{"Heat.1995.1080p.BluRay.x264-GRP/Heat.1995.1080p.BluRay.x264-GRP.nfo", 4000},
			{"Heat.1995.1080p.BluRay.x264-GRP/Subs/English.srt", 90000},
			{"Heat.1995.1080p.BluRay.x264-GRP/Sample/heat-sample.mkv", 50 << 20},
			{"Heat.1995.1080p.BluRay.x264-GRP/RARBG.txt", 30},
		}, ""},
		{"season pack", []ReleaseFile{
			{"Show.S01.1080p.WEB-DL/Show.S01E01.1080p.WEB-DL.mkv", gb},
			{"Show.S01.1080p.WEB-DL/Show.S01E02.1080p.WEB-DL.mkv", gb},
		}, ""},
		{"hybrid torrent padding is ignored", []ReleaseFile{
			{"Heat/Heat.mkv", 9 * gb}, {"Heat/.pad/12345", 12345},
		}, ""},
		{"Blu-ray image", []ReleaseFile{{"Heat.1995.COMPLETE.BLURAY.iso", 45 * gb}}, ""},
		{"a big file called sample is still the movie", []ReleaseFile{{"Sample.2024.1080p.WEB-DL.mkv", 4 * gb}}, ""},
		{"magnet without details yet", nil, ""},

		{"a program pretending to be the movie", []ReleaseFile{{"Heat.1995.1080p.BluRay.x264.mkv.exe", 2 << 20}},
			"it contains Heat.1995.1080p.BluRay.x264.mkv.exe, a Windows program, not a video"},
		{"an installer next to a real-looking video", []ReleaseFile{
			{"Heat.1995.1080p/Heat.1995.1080p.mkv", 2 * gb},
			{"Heat.1995.1080p/Codec/Setup.EXE", 3 << 20},
		}, "it contains Setup.EXE, a Windows program"},
		{"a shortcut", []ReleaseFile{{"Heat/Heat.mkv", 2 * gb}, {"Heat/Play Movie.lnk", 2000}}, "a Windows shortcut"},
		{"a script", []ReleaseFile{{"Heat/Heat.mkv", 2 * gb}, {"Heat/run.vbs", 2000}}, "a Windows script"},
		{"a hidden program still counts", []ReleaseFile{{"Heat/Heat.mkv", 2 * gb}, {"Heat/.x/svc.scr", 2000}}, "svc.scr"},
		{"several programs", []ReleaseFile{{"Heat/a.exe", 1}, {"Heat/b.bat", 1}}, "it contains 2 programs or scripts (a.exe first"},

		{"a password-locked archive", []ReleaseFile{
			{"Heat.1995.1080p/Heat.1995.1080p.zip", 2 * gb},
			{"Heat.1995.1080p/Password.txt", 100},
		}, "it has no movie or episode file in it, only Heat.1995.1080p.zip and Password.txt"},
		{"only a sample", []ReleaseFile{
			{"Heat/Heat.rar", 2 * gb}, {"Heat/Sample/sample.mkv", 30 << 20},
		}, "no movie or episode file"},
		{"only extras", []ReleaseFile{{"Heat/Heat-trailer.mkv", 90 << 20}}, "no movie or episode file"},
		{"many other files are summed up", []ReleaseFile{
			{"x/a.rar", 1}, {"x/a.r00", 1}, {"x/a.r01", 1}, {"x/a.r02", 1}, {"x/a.r03", 1},
		}, "only a.rar, a.r00, a.r01 and 2 more files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnfitRelease(tt.files)
			switch {
			case tt.want == "" && got != "":
				t.Fatalf("UnfitRelease = %q, want fine", got)
			case tt.want != "" && !strings.Contains(got, tt.want):
				t.Fatalf("UnfitRelease = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// Usenet releases are judged by programs alone: a set of archives with
// scrambled names is normal there, and only a named program is refused.
func TestProgramsIn(t *testing.T) {
	archives := []ReleaseFile{{"a8f3c1d07e.part01.rar", 1 << 30}, {"a8f3c1d07e.part02.rar", 1 << 30}, {"a8f3c1d07e.par2", 1 << 10}}
	if got := ProgramsIn(archives); got != "" {
		t.Errorf("archives = %q, want fine", got)
	}
	if got := ProgramsIn(nil); got != "" {
		t.Errorf("no names = %q, want fine", got)
	}
	withSetup := append(archives, ReleaseFile{"Setup.exe", 3 << 20})
	if got := ProgramsIn(withSetup); !strings.Contains(got, "Setup.exe, a Windows program") {
		t.Errorf("with Setup.exe = %q", got)
	}
}
