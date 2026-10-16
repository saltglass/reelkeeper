package decision

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mustProfile(t *testing.T, s Simple) *Profile {
	t.Helper()
	p, err := s.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

func preset(t *testing.T, id string) Simple {
	t.Helper()
	p, ok := PresetByID(id)
	if !ok {
		t.Fatalf("no preset %q", id)
	}
	return p.Settings
}

var heat = Movie{Title: "Heat", Year: 1995, Runtime: 170, OriginalLanguage: LanguageEnglish}

const gb = 1 << 30

func TestBundledFormatsCompile(t *testing.T) {
	files, _ := fs.Glob(trashFS, "trash/*.json")
	if len(files) == 0 {
		t.Fatal("no bundled formats")
	}
	for _, f := range files {
		cf, err := trashFormat(strings.TrimSuffix(strings.TrimPrefix(f, "trash/"), ".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cf.Compile(); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	// Every feature's formats exist.
	for _, f := range Features {
		for _, file := range f.Formats {
			if _, err := trashFormat(file); err != nil {
				t.Errorf("feature %s: %v", f.ID, err)
			}
		}
	}
}

func TestPresetsCompileAndAreStable(t *testing.T) {
	for _, p := range Presets {
		a := mustProfile(t, p.Settings)
		b := mustProfile(t, p.Settings)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if string(ja) != string(jb) {
			t.Errorf("%s: compiling twice gave different profiles", p.ID)
		}
		// A compiled profile survives a JSON round trip (that's how it's
		// stored and shown under Advanced).
		var back Profile
		if err := json.Unmarshal(ja, &back); err != nil {
			t.Fatal(err)
		}
		if err := back.Compile(); err != nil {
			t.Errorf("%s: stored profile doesn't load: %v", p.ID, err)
		}
	}
}

func TestLadder(t *testing.T) {
	hd := mustProfile(t, preset(t, "hd"))
	for name, want := range map[string]bool{
		"WEBDL-1080p": true, "WEBRip-1080p": true, "Bluray-1080p": true,
		"Remux-1080p": false, "Bluray-720p": false, "WEBDL-2160p": false, "BR-DISK": false, "CAM": false,
	} {
		q, _ := QualityByName(name)
		if got := hd.Allowed(q); got != want {
			t.Errorf("HD allows %s = %v, want %v", name, got, want)
		}
	}
	// WEB-DL and WEBRip are one rung when WebEqual is on.
	dl, _ := QualityByName("WEBDL-1080p")
	rip, _ := QualityByName("WEBRip-1080p")
	if hd.rank(dl) != hd.rank(rip) {
		t.Error("WEB-DL and WEBRip should rank equal")
	}
	s := preset(t, "hd")
	s.WebEqual = false
	p := mustProfile(t, s)
	if p.rank(dl) <= p.rank(rip) {
		t.Error("without WebEqual, WEB-DL should rank above WEBRip")
	}
}

func TestDecideReasons(t *testing.T) {
	hd := mustProfile(t, preset(t, "hd"))
	en := preset(t, "hd")
	en.Languages = LanguageChoice{Mode: "list", List: []string{"English"}}
	enOnly := mustProfile(t, en)
	small := preset(t, "hd")
	small.Sizes = map[string]SizeRange{"1080": {Min: 1, Max: 4}}
	sized := mustProfile(t, small)

	tests := []struct {
		name    string
		p       *Profile
		c       Candidate
		want    string // "" = accepted; otherwise a substring of a reason
		accepts bool
	}{
		{"good", hd, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", Seeders: 50}, "", true},
		{"wrong movie", hd, Candidate{Name: "Heat.Wave.2022.1080p.WEB-DL.x264-GRP"}, `"Heat Wave" (2022), not Heat (1995)`, false},
		{"wrong year", hd, Candidate{Name: "Heat.1986.1080p.BluRay.x264-GRP"}, "says 1986, but Heat is from 1995", false},
		{"quality", hd, Candidate{Name: "Heat.1995.720p.BluRay.x264-GRP"}, "Bluray-720p isn't a quality this profile accepts", false},
		{"unknown quality", hd, Candidate{Name: "Heat.1995-GRP"}, "can't tell the quality", false},
		{"blocked group", hd, Candidate{Name: "Heat.1995.1080p.BluRay.x264-YIFY"}, `matches "LQ"`, false},
		{"blocklisted", hd, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", Blocklisted: true}, "on the blocklist", false},
		{"seeders", hd, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", MinSeeders: 1}, "0 seeders; at least 1 needed", false},
		{"too big", sized, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", Size: 20 * gb}, "too big", false},
		{"too small", sized, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", Size: gb}, "too small", false},
		{"size ok", sized, Candidate{Name: "Heat.1995.1080p.BluRay.x264-AMIABLE", Size: 8 * gb}, "", true},
		{"language", enOnly, Candidate{Name: "Heat.1995.FRENCH.1080p.BluRay.x264-GRP"}, "doesn't have English audio", false},
		{"untagged is original (English)", enOnly, Candidate{Name: "Heat.1995.1080p.BluRay.x264-GRP"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.p, heat, tt.c, nil)
			if d.Accepted != tt.accepts {
				t.Fatalf("accepted = %v, reasons %q", d.Accepted, d.Rejections)
			}
			if tt.want != "" && !slices.ContainsFunc(d.Rejections, func(r string) bool { return strings.Contains(r, tt.want) }) {
				t.Errorf("reasons %q don't mention %q", d.Rejections, tt.want)
			}
		})
	}
}

func TestOriginalLanguage(t *testing.T) {
	hd := mustProfile(t, preset(t, "hd")) // original language only
	amelie := Movie{Title: "Amélie", Year: 2001, OriginalLanguage: LanguageByCode("fr"), AltTitles: []string{"Amelie"}}
	if d := Decide(hd, amelie, Candidate{Name: "Amelie.2001.FRENCH.1080p.BluRay.x264-GRP"}, nil); !d.Accepted {
		t.Errorf("French release of a French film rejected: %q", d.Rejections)
	}
	if d := Decide(hd, amelie, Candidate{Name: "Amelie.2001.1080p.BluRay.x264-GRP"}, nil); !d.Accepted {
		t.Errorf("untagged release (assumed original) rejected: %q", d.Rejections)
	}
	d := Decide(hd, amelie, Candidate{Name: "Amelie.2001.GERMAN.1080p.BluRay.x264-GRP"}, nil)
	if d.Accepted || !strings.Contains(strings.Join(d.Rejections, " "), "original language") {
		t.Errorf("German dub of a French film: accepted=%v %q", d.Accepted, d.Rejections)
	}
}

func TestRequireAndPrefer(t *testing.T) {
	s := preset(t, "4k")
	s.Features["hdr"] = Require
	p := mustProfile(t, s)
	sdr := Decide(p, heat, Candidate{Name: "Heat.1995.2160p.UHD.BluRay.x265-GRP"}, nil)
	if sdr.Accepted || !strings.Contains(strings.Join(sdr.Rejections, " "), "doesn't have HDR (any kind)") {
		t.Errorf("SDR 4K: accepted=%v %q", sdr.Accepted, sdr.Rejections)
	}
	dv := Decide(p, heat, Candidate{Name: "Heat.1995.2160p.UHD.BluRay.DV.HDR10.x265-GRP"}, nil)
	hdr := Decide(p, heat, Candidate{Name: "Heat.1995.2160p.UHD.BluRay.HDR10.x265-GRP"}, nil)
	if !dv.Accepted || !hdr.Accepted {
		t.Fatalf("HDR releases rejected: %q %q", dv.Rejections, hdr.Rejections)
	}
	if dv.Score <= hdr.Score {
		t.Errorf("Dolby Vision (score %d) should outscore plain HDR (%d)", dv.Score, hdr.Score)
	}
	ds := []Decision{sdr, hdr, dv}
	p.Best(ds)
	if ds[0].Name != dv.Name || ds[2].Name != sdr.Name {
		t.Errorf("Best order: %s, %s, %s", ds[0].Name, ds[1].Name, ds[2].Name)
	}
}

func TestBestPrefersQualityThenScoreThenSeeders(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	web := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.WEB-DL.DDP5.1.H.264-GRP", Seeders: 500}, nil)
	bd := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-GRP", Seeders: 3}, nil)
	bdMore := Decide(p, heat, Candidate{Name: "Heat.1995.1080p.BluRay.x264-OTHER", Seeders: 300}, nil)
	ds := []Decision{web, bd, bdMore}
	p.Best(ds)
	got := []string{ds[0].Name, ds[1].Name, ds[2].Name}
	want := []string{bdMore.Name, bd.Name, web.Name}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
}

func TestUpgrades(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	q := func(n string) Quality { x, _ := QualityByName(n); return x }
	webFile := &Existing{Name: "Heat.1995.1080p.WEB-DL.H.264-GRP", Quality: q("WEBDL-1080p")}
	bdFile := &Existing{Name: "Heat.1995.1080p.BluRay.x264-GRP", Quality: q("Bluray-1080p")}
	uhdFile := &Existing{Name: "Heat.1995.2160p.WEB-DL.H.265-GRP", Quality: q("WEBDL-2160p")}
	dvdFile := &Existing{Name: "Heat.1995.DVDRip.XviD-GRP", Quality: q("DVD")}

	cases := []struct {
		name string
		p    *Profile
		have *Existing
		c    string
		ok   bool
		why  string
	}{
		{"better quality", p, webFile, "Heat.1995.1080p.BluRay.x264-GRP", true, ""},
		{"same rung (WEB group)", p, webFile, "Heat.1995.1080p.WEBRip.x264-GRP", false, "isn't better"},
		{"reached upgrade-until", p, bdFile, "Heat.1995.1080p.BluRay.x264-OTHER", false, "already reaches"},
		{"proper of same quality", p, webFile, "Heat.1995.PROPER.1080p.WEB-DL.H.264-GRP", true, ""},
		// A file in a quality the profile doesn't list: never "upgraded" to
		// something worse, still replaced when it's worse than the profile.
		{"not backwards from 2160p", p, uhdFile, "Heat.1995.1080p.BluRay.x264-GRP", false, "already better than anything"},
		{"up from an unlisted lower quality", p, dvdFile, "Heat.1995.1080p.WEB-DL.H.264-GRP", true, ""},
	}
	noUp := preset(t, "hd")
	noUp.Upgrades = false
	cases = append(cases, struct {
		name string
		p    *Profile
		have *Existing
		c    string
		ok   bool
		why  string
	}{"upgrades off", mustProfile(t, noUp), webFile, "Heat.1995.1080p.BluRay.x264-GRP", false, "doesn't upgrade"})

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.p, heat, Candidate{Name: tt.c}, tt.have)
			if d.Accepted != tt.ok {
				t.Fatalf("accepted = %v, reasons %q", d.Accepted, d.Rejections)
			}
			if tt.ok != d.Upgrade {
				t.Errorf("upgrade = %v", d.Upgrade)
			}
			if tt.why != "" && !strings.Contains(strings.Join(d.Rejections, " "), tt.why) {
				t.Errorf("reasons %q don't mention %q", d.Rejections, tt.why)
			}
		})
	}
}

// A file better than every quality the profile allows is left alone;
// one the profile doesn't allow but could beat is replaced.
func TestCutoffAboveTheProfile(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	p.UpgradeUntilScore = 1000 // a score no file here reaches
	for _, tt := range []struct {
		quality string
		met     bool
	}{
		{"WEBDL-2160p", true},
		{"Remux-2160p", true},
		{"WEBDL-1080p", false},
		{"DVD", false},
		{"Unknown", false},
		{"BR-DISK", false}, // a disc image isn't a better picture: still replaced
	} {
		have := &Existing{Name: "Heat (1995).mkv", Quality: mustQuality(t, tt.quality)}
		if got := p.CutoffMet(have, heat); got != tt.met {
			t.Errorf("%s: CutoffMet = %v, want %v", tt.quality, got, tt.met)
		}
	}
}

func mustQuality(t *testing.T, name string) Quality {
	t.Helper()
	q, ok := QualityByName(name)
	if !ok {
		t.Fatalf("no quality %q", name)
	}
	return q
}

// Radarr's matching rule: within a kind, required conditions must all hold
// and at least one must hold; every kind must pass.
func TestCustomFormatMatching(t *testing.T) {
	cf := CustomFormat{Name: "x", Specs: []Spec{
		{Name: "a", Kind: KindReleaseTitle, Fields: Fields{"value": `\bfoo\b`}},
		{Name: "b", Kind: KindReleaseTitle, Fields: Fields{"value": `\bbar\b`}},
		{Name: "not baz", Kind: KindReleaseTitle, Negate: true, Required: true, Fields: Fields{"value": `\bbaz\b`}},
		{Name: "bluray", Kind: KindSource, Fields: Fields{"value": float64(SourceBluRay)}},
	}}
	if err := cf.Compile(); err != nil {
		t.Fatal(err)
	}
	bd, _ := QualityByName("Bluray-1080p")
	web, _ := QualityByName("WEBDL-1080p")
	for _, tt := range []struct {
		name string
		q    Quality
		want bool
	}{
		{"FOO", bd, true}, // case-insensitive
		{"bar", bd, true},
		{"foo baz", bd, false}, // required negation fails
		{"qux", bd, true},      // Radarr: the holding negation counts as "one holds"
		{"foo", web, false},    // source group fails
	} {
		if got := cf.Matches(Input{Name: tt.name, Quality: tt.q}); got != tt.want {
			t.Errorf("%q on %s = %v, want %v", tt.name, tt.q.Name, got, tt.want)
		}
	}
}

func TestFormatJSONLayouts(t *testing.T) {
	// Radarr's API layout (fields as a list) and TRaSH's (an object) both
	// load.
	radarr := `{"name":"Lang","specifications":[{"name":"en","implementation":"LanguageSpecification","negate":false,"required":true,
		"fields":[{"name":"value","value":1},{"name":"exceptLanguage","value":false}]}]}`
	var cf CustomFormat
	if err := json.Unmarshal([]byte(radarr), &cf); err != nil {
		t.Fatal(err)
	}
	if err := cf.Compile(); err != nil {
		t.Fatal(err)
	}
	if !cf.Matches(Input{Languages: []int{LanguageEnglish}}) || cf.Matches(Input{Languages: []int{2}}) {
		t.Error("language format from Radarr's layout matches wrongly")
	}
	out, _ := json.Marshal(cf)
	if !strings.Contains(string(out), `"fields":{"exceptLanguage":false,"value":1}`) {
		t.Errorf("not written in TRaSH's layout: %s", out)
	}
}

func TestBadPatternIsRejected(t *testing.T) {
	cf := CustomFormat{Name: "bad", Specs: []Spec{{Name: "x", Kind: KindReleaseTitle, Fields: Fields{"value": "(unclosed"}}}}
	if err := cf.Compile(); err == nil {
		t.Error("an invalid pattern compiled")
	}
}

func TestValidate(t *testing.T) {
	s := preset(t, "hd")
	s.LowestSource, s.HighestSource = "remux", "webdl"
	s.Languages = LanguageChoice{Mode: "list", List: []string{"Klingon"}}
	s.Features = map[string]Choice{"3d": Require}
	s.Sizes = map[string]SizeRange{"1080": {Min: 5, Max: 2}}
	errs := s.Validate()
	for _, k := range []string{"sources", "languages", "features", "sizes"} {
		if errs[k] == "" {
			t.Errorf("no error for %s (got %v)", k, errs)
		}
	}
	if _, err := s.Compile(); err == nil {
		t.Error("invalid settings compiled")
	}
}

func TestLanguageLookups(t *testing.T) {
	if LanguageByCode("fr") != 2 || LanguageByCode("pt-br") != 30 || LanguageByCode("xx") != LanguageUnknown {
		t.Error("ISO code lookup")
	}
	if LanguageByName("portuguese (brazil)") != 30 || LanguageName(37) != "Spanish (Latino)" {
		t.Error("name lookup")
	}
}

func TestMatchKey(t *testing.T) {
	for a, b := range map[string]string{
		"Amélie":                        "Amelie",
		"Schindler's List":              "Schindlers List",
		"Fast & Furious":                "Fast and Furious",
		"Mission: Impossible – Fallout": "Mission Impossible Fallout",
	} {
		if matchKey(a) != matchKey(b) {
			t.Errorf("%q and %q should match (%q vs %q)", a, b, matchKey(a), matchKey(b))
		}
	}
}

func FuzzDecide(f *testing.F) {
	p, err := Presets[1].Settings.Compile()
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{"Heat.1995.2160p.UHD.BluRay.REMUX.DV.HDR.TrueHD.Atmos.7.1-FGT", "", "((", "Heat 1995"} {
		f.Add(s, int64(0))
	}
	f.Fuzz(func(t *testing.T, name string, size int64) {
		d := Decide(&p, heat, Candidate{Name: name, Size: size}, nil) // must not panic
		if d.Accepted != (len(d.Rejections) == 0) {
			t.Error("Accepted disagrees with Rejections")
		}
	})
}

func TestTryName(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	if d := TryName(p, "Some.Movie.2021.1080p.BluRay.x264-GRP", 0, 120, LanguageEnglish); !d.Accepted {
		t.Errorf("rejected: %q", d.Rejections)
	}
	d := TryName(p, "Some.Movie.2021.720p.BluRay.x264-GRP", 0, 120, LanguageEnglish)
	if d.Accepted || len(d.Rejections) != 1 {
		t.Errorf("want exactly the quality reason, got %q", d.Rejections)
	}
}

func TestOriginalLanguageNotKnownYet(t *testing.T) {
	hd := mustProfile(t, preset(t, "hd")) // original language only
	m := Movie{Title: "Heat", Year: 1995} // no original language yet
	if d := Decide(hd, m, Candidate{Name: "Heat.1995.1080p.BluRay.x264-GRP"}, nil); !d.Accepted {
		t.Errorf("untagged release rejected while the original language is unknown: %q", d.Rejections)
	}
	if d := Decide(hd, m, Candidate{Name: "Heat.1995.GERMAN.1080p.BluRay.x264-GRP"}, nil); d.Accepted {
		t.Error("a release that states another language was accepted")
	}
}

// The trial's first grab: a nameless 1.8 GB rip with 145 seeders beat an
// 8.5 GB web release from a known group with 14.
func TestGroupsAndSizeBeatPopularity(t *testing.T) {
	p := mustProfile(t, preset(t, "hd"))
	m := Movie{Title: "101 Dalmatians", Year: 1996, Runtime: 103, OriginalLanguage: LanguageEnglish}
	nameless := Decide(p, m, Candidate{Name: "101 Dalmatians (1996) [WEBRip] [1080p]", Size: 1760936591, Seeders: 145}, nil)
	web := Decide(p, m, Candidate{Name: "101.Dalmatians.1996.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX", Size: 8471822991, Seeders: 14}, nil)
	if !nameless.Accepted || !web.Accepted {
		t.Fatalf("rejections: %q / %q", nameless.Rejections, web.Rejections)
	}
	if nameless.Score >= 0 {
		t.Errorf("a release with no group should score below zero, got %d", nameless.Score)
	}
	ds := []Decision{nameless, web}
	p.Best(ds)
	if ds[0].Name != web.Name {
		t.Errorf("picked %s", ds[0].Name)
	}

	// Without a group difference, size wins once both have enough seeders.
	small := Decide(p, m, Candidate{Name: "101.Dalmatians.1996.1080p.WEB-DL.H.264-AAA", Size: 2 << 30, Seeders: 300}, nil)
	big := Decide(p, m, Candidate{Name: "101.Dalmatians.1996.1080p.WEB-DL.H.264-BBB", Size: 7 << 30, Seeders: 6}, nil)
	ds = []Decision{small, big}
	p.Best(ds)
	if ds[0].Name != big.Name {
		t.Errorf("with enough seeders, the bigger release should win; got %s", ds[0].Name)
	}
	// But a release that may never finish loses to one that will.
	dead := Decide(p, m, Candidate{Name: "101.Dalmatians.1996.1080p.WEB-DL.H.264-CCC", Size: 9 << 30, Seeders: 2}, nil)
	ds = []Decision{dead, small}
	p.Best(ds)
	if ds[0].Name != small.Name {
		t.Errorf("a release with 2 seeders beat one with 300: %s", ds[0].Name)
	}

	// The nameless rip isn't an upgrade over a named x265 encode either.
	q, _ := QualityByName("WEBRip-1080p")
	have := &Existing{Name: "101 Dalmatians (1996) {tmdb-11674} - [WEBRip-1080p][EAC3 2.0][x265]-EDGE2020.mkv", Quality: q, Size: 3148289072}
	if d := Decide(p, m, Candidate{Name: nameless.Name, Size: 1760936591, Seeders: 145}, have); d.Accepted {
		t.Error("a nameless rip counted as an upgrade")
	}
}
