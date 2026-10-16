// Package mediainfo reads what a video file really contains (resolution,
// codecs, HDR/Dolby Vision, audio and subtitle tracks, languages, runtime)
// from its container headers. It supports Matroska (.mkv) and MP4 (.mp4,
// .m4v).
//
// It reads only the header structures, never the video data, so probing a
// 60 GB remux over a network share costs a few hundred kilobytes. Damaged or
// unusual files return an error, never a panic.
//
// Some facts aren't in the headers: HDR10+ is signalled inside the video
// stream, and Atmos or DTS-HD MA inside the audio stream. For those the
// scanner falls back on the release name (internal/parser) or on track names
// like "TrueHD Atmos 7.1".
package mediainfo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Info is what a file contains.
type Info struct {
	Container string        `json:"container"` // "mkv" or "mp4"
	Duration  time.Duration `json:"duration"`
	Title     string        `json:"title,omitempty"` // the container's own title tag, if any

	Video     *Video     `json:"video,omitempty"`
	Audio     []Audio    `json:"audio,omitempty"`
	Subtitles []Subtitle `json:"subtitles,omitempty"`
}

// Video describes the main video track.
type Video struct {
	Codec    string   `json:"codec"` // H.264, H.265, AV1, VC-1, MPEG-2, VP9, MPEG-4
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	BitDepth int      `json:"bitDepth,omitempty"`
	HDR      []string `json:"hdr,omitempty"` // DV, HDR10, HLG
	// DVProfile is the Dolby Vision profile (5, 7, 8...), 0 if none.
	DVProfile int `json:"dvProfile,omitempty"`
}

// Resolution is the nominal resolution class (2160, 1080, 720, 576, 480),
// judged mostly by width because films are often cropped in height:
// 3840x1600 is still a 2160p release.
func (v Video) Resolution() int {
	switch {
	case v.Width >= 3200 || v.Height >= 1800:
		return 2160
	case v.Width >= 1700 || v.Height >= 1000:
		return 1080
	case v.Width >= 1100 || v.Height >= 700:
		return 720
	case v.Height >= 560 || v.Width >= 1000:
		return 576
	case v.Width > 0:
		return 480
	}
	return 0
}

// Audio describes one audio track.
type Audio struct {
	Codec    string `json:"codec"` // TrueHD, DTS, DD+, DD, AAC, FLAC, Opus, LPCM, MP3
	Channels int    `json:"channels,omitempty"`
	Language string `json:"language,omitempty"` // ISO 639-2, e.g. "eng"; empty if undetermined
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// Subtitle describes one subtitle track.
type Subtitle struct {
	Codec    string `json:"codec"` // SRT, ASS, PGS, VobSub, WebVTT, TX3G
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// ChannelLayout returns "7.1", "5.1", "2.0" and so on for a channel count.
func ChannelLayout(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1.0"
	case 2:
		return "2.0"
	case 3:
		return "2.1"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", n)
}

// ErrUnsupported means the file isn't a container this package reads.
var ErrUnsupported = errors.New("not an MKV or MP4 file")

// errCorrupt is returned for structures that don't make sense.
var errCorrupt = errors.New("damaged or unusual file structure")

// maxRead caps how much a single header structure may make us read, so a
// corrupt size field can't make the scanner allocate gigabytes.
const maxRead = 16 << 20

// Read probes a file on disk.
func Read(path string) (*Info, error) {
	f, err := os.Open(path) // #nosec G304 -- the scanner passes paths inside configured media folders
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	info, err := ReadFrom(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return info, nil
}

// ReadFrom probes a file of the given size, recognising the container from
// its first bytes rather than its extension.
func ReadFrom(r io.ReaderAt, size int64) (info *Info, err error) {
	// The parsers index into byte slices read from the file; a damaged
	// file must never take the scanner down.
	defer func() {
		if p := recover(); p != nil {
			info, err = nil, fmt.Errorf("%w: %v", errCorrupt, p)
		}
	}()
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, ErrUnsupported
	}
	switch {
	case head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		return readMKV(r, size)
	case string(head[4:8]) == "ftyp" || string(head[4:8]) == "moov" || string(head[4:8]) == "free" ||
		string(head[4:8]) == "mdat" || string(head[4:8]) == "wide":
		return readMP4(r, size)
	}
	return nil, ErrUnsupported
}

// normLang turns container language codes into ISO 639-2 ("eng"),
// treating "und" and empty as unknown.
func normLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if i := strings.IndexAny(l, "-_"); i > 0 { // BCP 47 "en-US"
		l = l[:i]
	}
	if two, ok := iso6391[l]; ok {
		return two
	}
	if l == "und" || l == "" || l == "zxx" || l == "mis" || l == "mul" {
		return ""
	}
	return l
}

// iso6391 maps common two-letter codes (used by BCP 47) to ISO 639-2.
var iso6391 = map[string]string{
	"en": "eng", "fr": "fre", "de": "ger", "es": "spa", "it": "ita", "ja": "jpn", "ko": "kor",
	"zh": "chi", "ru": "rus", "pt": "por", "nl": "dut", "sv": "swe", "no": "nor", "nb": "nor",
	"da": "dan", "fi": "fin", "pl": "pol", "cs": "cze", "hu": "hun", "tr": "tur", "el": "gre",
	"he": "heb", "ar": "ara", "hi": "hin", "th": "tha", "uk": "ukr", "ro": "rum", "vi": "vie",
	"id": "ind", "ms": "may", "is": "ice",
}

// hdrFromTransfer maps the colour transfer characteristic (the same numbers
// in MKV and MP4, from ITU-T H.273) to an HDR format.
func hdrFromTransfer(tc int) string {
	switch tc {
	case 16: // SMPTE ST 2084 (PQ)
		return "HDR10"
	case 18: // ARIB STD-B67
		return "HLG"
	}
	return ""
}

func addUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// av1BitDepth reads the high_bitdepth / twelve_bit flags from byte 2 of an
// AV1CodecConfigurationRecord.
func av1BitDepth(c []byte) int {
	switch {
	case c[2]&0x40 != 0 && c[2]&0x20 != 0:
		return 12
	case c[2]&0x40 != 0:
		return 10
	}
	return 8
}

// avcBitDepth reads the bit depth an H.264 configuration record implies
// from its profile: High 10 and above are 10-bit; everything else is 8-bit.
func avcBitDepth(c []byte) int {
	if len(c) < 2 {
		return 0
	}
	switch c[1] { // AVCProfileIndication
	case 110, 122, 244:
		return 10
	}
	return 8
}
