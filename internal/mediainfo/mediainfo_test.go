package mediainfo

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

func read(t *testing.T, b []byte) *Info {
	t.Helper()
	info, err := ReadFrom(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestMKVBasics(t *testing.T) {
	f := mkvFile(
		el(idInfo, el(idTimecodeSc, euint(1_000_000)), el(idDuration, efloat(7_260_000)), el(idTitle, estr("Some Movie"))),
		el(idTracks,
			el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_MPEG4/ISO/AVC")),
				el(idCodecPriv, []byte{1, 100, 0, 40}),
				el(idVideo, el(idPixelWidth, euint(1920)), el(idPixelHeight, euint(800)))),
			el(idTrackEntry, el(idTrackType, euint(trackAudio)), el(idCodecID, estr("A_EAC3")),
				el(idAudio, el(idChannels, euint(6)))), // no Language element: Matroska's default is English
			el(idTrackEntry, el(idTrackType, euint(trackAudio)), el(idCodecID, estr("A_AC3")),
				el(idLanguage, estr("spa")), el(idFlagDefault, euint(0)), el(idName, estr("Spanish")),
				el(idAudio, el(idChannels, euint(2)))),
			el(idTrackEntry, el(idTrackType, euint(trackSubtitle)), el(idCodecID, estr("S_HDMV/PGS")),
				el(idLanguageIET, estr("en-US")), el(idFlagForced, euint(1))),
			el(idTrackEntry, el(idTrackType, euint(trackSubtitle)), el(idCodecID, estr("S_TEXT/UTF8")),
				el(idLanguage, estr("und")), el(idName, estr("Forced"))),
		),
		el(idCluster, make([]byte, 64)),
	)
	got := read(t, f)
	want := &Info{
		Container: "mkv",
		Duration:  2*time.Hour + time.Minute,
		Title:     "Some Movie",
		Video:     &Video{Codec: "H.264", Width: 1920, Height: 800, BitDepth: 8},
		Audio: []Audio{
			{Codec: "DD+", Channels: 6, Language: "eng", Default: true},
			{Codec: "DD", Channels: 2, Language: "spa", Title: "Spanish"},
		},
		Subtitles: []Subtitle{
			{Codec: "PGS", Language: "eng", Forced: true},
			{Codec: "SRT", Title: "Forced", Forced: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %+v\nwant %+v", got, want)
	}
	if r := got.Video.Resolution(); r != 1080 {
		t.Errorf("1920x800 should count as 1080p, got %d", r)
	}
}

func TestMKVHDRAndDolbyVision(t *testing.T) {
	sps := buildSPS(spsOptions{transfer: 16, maxSubLayers: 1, scalingList: true, refPicSets: 2, longTerm: true, pcm: true})
	hdr10 := hvcc(10, map[byte][]byte{33: sps, 39: seiMastering()})

	t.Run("HDR10 from the stream's own parameters", func(t *testing.T) {
		f := mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)),
			el(idCodecID, estr("V_MPEGH/ISO/HEVC")), el(idCodecPriv, hdr10),
			el(idVideo, el(idPixelWidth, euint(3840)), el(idPixelHeight, euint(1600))))))
		v := read(t, f).Video
		if v.BitDepth != 10 || !reflect.DeepEqual(v.HDR, []string{"HDR10"}) || v.Resolution() != 2160 {
			t.Errorf("video = %+v", v)
		}
	})

	t.Run("HLG from the Colour element", func(t *testing.T) {
		f := mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)),
			el(idCodecID, estr("V_MPEGH/ISO/HEVC")),
			el(idVideo, el(idPixelWidth, euint(3840)), el(idPixelHeight, euint(2160)),
				el(idColour, el(idBitsPerChan, euint(10)), el(idTransfer, euint(18)))))))
		v := read(t, f).Video
		if !reflect.DeepEqual(v.HDR, []string{"HLG"}) || v.BitDepth != 10 {
			t.Errorf("video = %+v", v)
		}
	})

	t.Run("Dolby Vision profile 8 from BlockAdditionMapping", func(t *testing.T) {
		dvcc := []byte{1, 0, 8 << 1, 0x10} // version 1.0, profile 8
		f := mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)),
			el(idCodecID, estr("V_MPEGH/ISO/HEVC")), el(idCodecPriv, hdr10),
			el(idVideo, el(idPixelWidth, euint(3840)), el(idPixelHeight, euint(2160))),
			el(idBlockAddMap, el(idBlockAddTyp, euint(0x64766343)), el(idBlockAddExt, dvcc)))))
		v := read(t, f).Video
		if v.DVProfile != 8 || !reflect.DeepEqual(v.HDR, []string{"DV", "HDR10"}) {
			t.Errorf("video = %+v", v)
		}
	})

	t.Run("dual-track Dolby Vision (profile 7)", func(t *testing.T) {
		f := mkvFile(el(idTracks,
			el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_MPEGH/ISO/HEVC")),
				el(idCodecPriv, hdr10), el(idVideo, el(idPixelWidth, euint(3840)), el(idPixelHeight, euint(2160)))),
			el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_MPEGH/ISO/HEVC")),
				el(idVideo, el(idPixelWidth, euint(1920)), el(idPixelHeight, euint(1080))))))
		v := read(t, f).Video
		if v.DVProfile != 7 || !reflect.DeepEqual(v.HDR, []string{"DV", "HDR10"}) || v.Width != 3840 {
			t.Errorf("video = %+v", v)
		}
	})

	t.Run("SDR stream says so", func(t *testing.T) {
		sdr := hvcc(10, map[byte][]byte{33: buildSPS(spsOptions{transfer: 1})})
		f := mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)),
			el(idCodecID, estr("V_MPEGH/ISO/HEVC")), el(idCodecPriv, sdr),
			el(idVideo, el(idPixelWidth, euint(1920)), el(idPixelHeight, euint(1080))))))
		if v := read(t, f).Video; len(v.HDR) != 0 {
			t.Errorf("SDR file reported %v", v.HDR)
		}
	})
}

func TestHEVCParameterSets(t *testing.T) {
	cases := []spsOptions{
		{transfer: 16},
		{transfer: 18, maxSubLayers: 2},
		{transfer: 16, scalingList: true},
		{transfer: 16, refPicSets: 3},
		{transfer: 16, longTerm: true, pcm: true},
		{transfer: 16, maxSubLayers: 6, scalingList: true, refPicSets: 4, longTerm: true, pcm: true},
	}
	for _, c := range cases {
		got, err := spsTransfer(unescape(buildSPS(c)))
		if err != nil || got != c.transfer {
			t.Errorf("%+v: transfer = %d, %v", c, got, err)
		}
	}
	// No VUI: nothing to say, no error.
	if got, err := spsTransfer(unescape(buildSPS(spsOptions{}))); err != nil || got != 0 {
		t.Errorf("no VUI: %d, %v", got, err)
	}
	// Mastering metadata alone still means HDR10 when the SPS is unreadable.
	cfg := parseHVCC(hvcc(10, map[byte][]byte{33: {0x42, 0x01, 0xFF}, 39: seiMastering()}))
	if !cfg.masteringSEI || cfg.hdr() != "HDR10" {
		t.Errorf("mastering fallback: %+v", cfg)
	}
}

func TestMKVTracksAfterClusters(t *testing.T) {
	// Some muxers write Tracks at the end; the SeekHead points to it.
	tracks := el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_AV1")),
		el(idCodecPriv, []byte{0x81, 0x08, 0x4C, 0}),
		el(idVideo, el(idPixelWidth, euint(1280)), el(idPixelHeight, euint(720)))))
	cluster := el(idCluster, make([]byte, 128))
	// Positions are relative to the Segment's data; compute with a dummy
	// SeekHead of fixed size first.
	seekHead := func(pos int) []byte {
		return el(idSeekHead, el(idSeek, el(idSeekID, euint(idTracks)), el(idSeekPos, euint(uint64(pos)))))
	}
	dummy := seekHead(0)
	f := mkvFile(seekHead(len(dummy)+len(cluster)), cluster, tracks)
	v := read(t, f).Video
	if v == nil || v.Codec != "AV1" || v.BitDepth != 10 || v.Resolution() != 720 {
		t.Errorf("video = %+v", v)
	}
}

func TestMKVUnknownSizeSegment(t *testing.T) {
	f := mkvFileUnknownSize(
		el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackAudio)), el(idCodecID, estr("A_OPUS")),
			el(idAudio, el(idChannels, euint(2))))),
		el(idCluster, make([]byte, 16)),
	)
	if a := read(t, f).Audio; len(a) != 1 || a[0].Codec != "Opus" {
		t.Errorf("audio = %+v", a)
	}
}

func TestMP4(t *testing.T) {
	hevc := hvcc(10, map[byte][]byte{33: buildSPS(spsOptions{transfer: 16})})
	moov := box("moov",
		mvhd(1000, 5_400_000),
		trak("vide", "und", "VideoHandler", visualEntry("hvc1", 3840, 1608,
			box("hvcC", hevc), box("dvcC", []byte{1, 0, 5 << 1, 0}))),
		trak("soun", "eng", "SoundHandler", audioEntry("ec-3", 2)),
		trak("soun", "fre", "Commentary", audioEntry("ac-3", 2, box("dac3", []byte{0x10, 0x3C, 0x00}))),
		trak("sbtl", "spa", "", box("tx3g", make([]byte, 40))),
		box("udta", box("meta", []byte{0, 0, 0, 0}, box("ilst", box("\xa9nam", box("data", make([]byte, 8), []byte("Some Movie")))))),
	)
	// mdat before moov, as in files that weren't "fast-started".
	f := bytes.Join([][]byte{box("ftyp", []byte("isom"), u32(0)), box("mdat", make([]byte, 4096)), moov}, nil)
	got := read(t, f)
	want := &Info{
		Container: "mp4",
		Duration:  90 * time.Minute,
		Title:     "Some Movie",
		Video:     &Video{Codec: "H.265", Width: 3840, Height: 1608, BitDepth: 10, HDR: []string{"DV", "HDR10"}, DVProfile: 5},
		Audio: []Audio{
			{Codec: "DD+", Channels: 2, Language: "eng", Default: true},
			{Codec: "DD", Channels: 6, Language: "fre", Title: "Commentary", Default: true},
		},
		Subtitles: []Subtitle{{Codec: "TX3G", Language: "spa"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %+v %+v\nwant %+v %+v", got, got.Video, want, want.Video)
	}
}

func TestMP4ColourBox(t *testing.T) {
	colr := box("colr", []byte("nclx"), u16(9), u16(18), u16(9), []byte{0})
	moov := box("moov", mvhd(600, 600),
		trak("vide", "eng", "", visualEntry("avc1", 1920, 1080, box("avcC", []byte{1, 110, 0, 40}), colr)))
	f := append(box("ftyp", []byte("mp42"), u32(0)), moov...)
	v := read(t, f).Video
	if v.Codec != "H.264" || v.BitDepth != 10 || !reflect.DeepEqual(v.HDR, []string{"HLG"}) {
		t.Errorf("video = %+v", v)
	}
}

func TestUnsupportedAndDamaged(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":       {},
		"text file":   []byte("hello, this is not a video at all"),
		"webm-ish":    append([]byte{0x1A, 0x45, 0xDF, 0xA3}, make([]byte, 20)...),
		"mp4 no moov": append(box("ftyp", []byte("isom"), u32(0)), box("mdat", make([]byte, 64))...),
	} {
		if _, err := ReadFrom(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A file cut short in the middle of its headers must fail cleanly.
	full := mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_AV1")))))
	for cut := 1; cut < len(full); cut += 7 {
		_, _ = ReadFrom(bytes.NewReader(full[:cut]), int64(cut)) // must not panic
	}
	if _, err := ReadFrom(bytes.NewReader([]byte("plain")), 5); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}

func TestResolutionAndChannels(t *testing.T) {
	for _, c := range []struct{ w, h, want int }{
		{3840, 2160, 2160}, {3840, 1600, 2160}, {3832, 1596, 2160}, {1920, 1080, 1080}, {1920, 800, 1080},
		{1912, 1040, 1080}, {1280, 720, 720}, {1280, 536, 720}, {720, 576, 576}, {720, 480, 480}, {640, 360, 480},
	} {
		if got := (Video{Width: c.w, Height: c.h}).Resolution(); got != c.want {
			t.Errorf("%dx%d: %d, want %d", c.w, c.h, got, c.want)
		}
	}
	for n, want := range map[int]string{0: "", 2: "2.0", 6: "5.1", 8: "7.1", 12: "12ch"} {
		if got := ChannelLayout(n); got != want {
			t.Errorf("ChannelLayout(%d) = %q", n, got)
		}
	}
}

func FuzzReadFrom(f *testing.F) {
	f.Add(mkvFile(el(idTracks, el(idTrackEntry, el(idTrackType, euint(trackVideo)), el(idCodecID, estr("V_MPEGH/ISO/HEVC")),
		el(idCodecPriv, hvcc(10, map[byte][]byte{33: buildSPS(spsOptions{transfer: 16, scalingList: true, refPicSets: 2})}))))))
	f.Add(append(box("ftyp", []byte("isom"), u32(0)), box("moov", mvhd(1, 1), trak("vide", "eng", "", visualEntry("avc1", 1, 1)))...))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ReadFrom(bytes.NewReader(b), int64(len(b))) // must not panic or hang
	})
}
