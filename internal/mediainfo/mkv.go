package mediainfo

import (
	"encoding/binary"
	"io"
	"math"
	"strings"
	"time"
)

// Matroska is built from EBML elements: an ID, a size and a payload, where
// payloads of "master" elements are more elements. Both ID and size are
// variable-length integers whose first byte says how many bytes follow.
//
// We read the EBML header, then walk the Segment's top-level children:
// Info (duration, title), Tracks (the tracks), and SeekHead (where Tracks
// is, if it comes after the video data). We stop at the first Cluster,
// which is where video data begins.

// Element IDs we use (with their marker bits, as written in the spec).
const (
	idEBML        = 0x1A45DFA3
	idDocType     = 0x4282
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idInfo        = 0x1549A966
	idTimecodeSc  = 0x2AD7B1
	idDuration    = 0x4489
	idTitle       = 0x7BA9
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackType   = 0x83
	idCodecID     = 0x86
	idCodecPriv   = 0x63A2
	idName        = 0x536E
	idLanguage    = 0x22B59C
	idLanguageIET = 0x22B59D
	idFlagDefault = 0x88
	idFlagForced  = 0x55AA
	idVideo       = 0xE0
	idPixelWidth  = 0xB0
	idPixelHeight = 0xBA
	idColour      = 0x55B0
	idBitsPerChan = 0x55B2
	idTransfer    = 0x55BA
	idAudio       = 0xE1
	idChannels    = 0x9F
	idBlockAddMap = 0x41E4
	idBlockAddTyp = 0x41E7
	idBlockAddExt = 0x41ED
	idCluster     = 0x1F43B675
)

const (
	trackVideo    = 1
	trackAudio    = 2
	trackSubtitle = 17
)

// unknownSize marks an element whose size isn't written (live streams).
const unknownSize = -1

type ebml struct {
	r    io.ReaderAt
	size int64
}

// vint reads a variable-length integer at off. keepMarker keeps the length
// marker bit (element IDs are compared with it). It returns the value, its
// length in bytes, and whether all value bits were 1 (an unknown size).
func (e ebml) vint(off int64, keepMarker bool) (val int64, n int, allOnes bool, err error) {
	var first [1]byte
	if _, err := e.r.ReadAt(first[:], off); err != nil {
		return 0, 0, false, err
	}
	b := first[0]
	if b == 0 {
		return 0, 0, false, errCorrupt
	}
	n = 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, false, errCorrupt
	}
	buf := make([]byte, n)
	if _, err := e.r.ReadAt(buf, off); err != nil {
		return 0, 0, false, err
	}
	if !keepMarker {
		buf[0] &^= byte(0x80) >> (n - 1)
	}
	for _, c := range buf {
		val = val<<8 | int64(c)
	}
	// A size whose value bits are all 1 means "unknown size".
	allOnes = !keepMarker && val == (int64(1)<<(7*n))-1
	return val, n, allOnes, nil
}

// header reads the element header at off: its ID, the payload's offset and
// the payload's size (unknownSize if not written).
func (e ebml) header(off int64) (id int64, data int64, size int64, err error) {
	id, n, _, err := e.vint(off, true)
	if err != nil {
		return 0, 0, 0, err
	}
	sz, m, unknown, err := e.vint(off+int64(n), false)
	if err != nil {
		return 0, 0, 0, err
	}
	data = off + int64(n) + int64(m)
	if unknown {
		return id, data, unknownSize, nil
	}
	if sz < 0 || data+sz > e.size+1 && e.size > 0 {
		// Truncated files are common in download folders: clamp.
		sz = e.size - data
	}
	return id, data, sz, nil
}

// children calls fn for each child of the master element [start, end).
// fn returns false to stop.
func (e ebml) children(start, end int64, fn func(id, data, size int64) bool) error {
	for off := start; off < end; {
		id, data, size, err := e.header(off)
		if err != nil {
			return err
		}
		if size == unknownSize {
			size = end - data
		}
		if !fn(id, data, size) {
			return nil
		}
		off = data + size
	}
	return nil
}

func (e ebml) bytes(off, size int64) ([]byte, error) {
	if size < 0 || size > maxRead {
		return nil, errCorrupt
	}
	b := make([]byte, size)
	_, err := e.r.ReadAt(b, off)
	return b, err
}

func (e ebml) uint(off, size int64) int64 {
	b, err := e.bytes(off, min(size, 8))
	if err != nil {
		return 0
	}
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	return v
}

func (e ebml) float(off, size int64) float64 {
	b, err := e.bytes(off, size)
	if err != nil {
		return 0
	}
	switch size {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

func (e ebml) str(off, size int64) string {
	if size > 4096 {
		size = 4096
	}
	b, err := e.bytes(off, size)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\x00")
}

func readMKV(r io.ReaderAt, size int64) (*Info, error) {
	e := ebml{r: r, size: size}
	id, data, sz, err := e.header(0)
	if err != nil || id != idEBML {
		return nil, ErrUnsupported
	}
	docType := ""
	_ = e.children(data, data+sz, func(id, d, s int64) bool {
		if id == idDocType {
			docType = e.str(d, s)
		}
		return true
	})
	if docType != "matroska" && docType != "webm" {
		return nil, ErrUnsupported
	}

	// The Segment follows the EBML header.
	segID, segData, segSize, err := e.header(data + sz)
	if err != nil || segID != idSegment {
		return nil, errCorrupt
	}
	segEnd := segData + segSize
	if segSize == unknownSize || segEnd > size {
		segEnd = size
	}

	info := &Info{Container: "mkv"}
	scale := int64(1_000_000) // nanoseconds per timestamp unit
	var duration float64
	var tracksAt, infoAt int64 = -1, -1
	foundTracks, foundInfo := false, false

	readInfo := func(d, s int64) {
		foundInfo = true
		_ = e.children(d, d+s, func(id, d, s int64) bool {
			switch id {
			case idTimecodeSc:
				if v := e.uint(d, s); v > 0 {
					scale = v
				}
			case idDuration:
				duration = e.float(d, s)
			case idTitle:
				info.Title = e.str(d, s)
			}
			return true
		})
	}

	err = e.children(segData, segEnd, func(id, d, s int64) bool {
		switch id {
		case idSeekHead:
			_ = e.children(d, d+s, func(id, d, s int64) bool {
				if id != idSeek {
					return true
				}
				var target, pos int64 = 0, -1
				_ = e.children(d, d+s, func(id, d, s int64) bool {
					switch id {
					case idSeekID:
						target = e.uint(d, s)
					case idSeekPos:
						pos = e.uint(d, s)
					}
					return true
				})
				switch target {
				case idTracks:
					tracksAt = segData + pos
				case idInfo:
					infoAt = segData + pos
				}
				return true
			})
		case idInfo:
			readInfo(d, s)
		case idTracks:
			foundTracks = true
			readTracks(e, d, s, info)
		case idCluster:
			return false // video data starts; everything we need is before it
		}
		return !foundTracks || !foundInfo
	})
	if err != nil && !foundTracks {
		return nil, err
	}

	// Some muxers put Tracks or Info after the clusters; the SeekHead says
	// where.
	if !foundTracks && tracksAt > 0 {
		if id, d, s, err := e.header(tracksAt); err == nil && id == idTracks {
			foundTracks = true
			readTracks(e, d, s, info)
		}
	}
	if !foundInfo && infoAt > 0 {
		if id, d, s, err := e.header(infoAt); err == nil && id == idInfo {
			readInfo(d, s)
		}
	}
	if !foundTracks {
		return nil, errCorrupt
	}
	if duration > 0 {
		info.Duration = time.Duration(duration * float64(scale))
	}
	return info, nil
}

func readTracks(e ebml, start, size int64, info *Info) {
	_ = e.children(start, start+size, func(id, d, s int64) bool {
		if id != idTrackEntry {
			return true
		}
		var (
			kind              int64
			codec, name, lang string
			langBCP           string
			isDefault         = true
			forced            bool
			width, height     int64
			bits, transfer    int64
			channels          int64
			dvProfile         int
			private           []byte
		)
		_ = e.children(d, d+s, func(id, d, s int64) bool {
			switch id {
			case idTrackType:
				kind = e.uint(d, s)
			case idCodecID:
				codec = e.str(d, s)
			case idCodecPriv:
				private, _ = e.bytes(d, min(s, 64<<10))
			case idName:
				name = e.str(d, s)
			case idLanguage:
				lang = e.str(d, s)
			case idLanguageIET:
				langBCP = e.str(d, s)
			case idFlagDefault:
				isDefault = e.uint(d, s) == 1
			case idFlagForced:
				forced = e.uint(d, s) == 1
			case idVideo:
				_ = e.children(d, d+s, func(id, d, s int64) bool {
					switch id {
					case idPixelWidth:
						width = e.uint(d, s)
					case idPixelHeight:
						height = e.uint(d, s)
					case idColour:
						_ = e.children(d, d+s, func(id, d, s int64) bool {
							switch id {
							case idBitsPerChan:
								bits = e.uint(d, s)
							case idTransfer:
								transfer = e.uint(d, s)
							}
							return true
						})
					}
					return true
				})
			case idAudio:
				_ = e.children(d, d+s, func(id, d, s int64) bool {
					if id == idChannels {
						channels = e.uint(d, s)
					}
					return true
				})
			case idBlockAddMap:
				// Dolby Vision configuration ("dvcC"/"dvvC"/"dvwC").
				var typ int64
				var extra []byte
				_ = e.children(d, d+s, func(id, d, s int64) bool {
					switch id {
					case idBlockAddTyp:
						typ = e.uint(d, s)
					case idBlockAddExt:
						extra, _ = e.bytes(d, min(s, 64))
					}
					return true
				})
				if typ == 0x64766343 || typ == 0x64767643 || typ == 0x64767743 {
					dvProfile = dvProfileFrom(extra)
				}
			}
			return true
		})
		if langBCP != "" {
			lang = langBCP
		} else if lang == "" {
			lang = "eng" // the Matroska default when the element is absent
		}

		switch kind {
		case trackVideo:
			vcodec := mkvVideoCodec(codec)
			if info.Video != nil {
				// A second HEVC track no larger than the first, in a file
				// without other DV signalling, is a Dolby Vision enhancement
				// layer stored as its own track (profile 7, "dual track").
				if vcodec == "H.265" && info.Video.Codec == "H.265" && int(height) <= info.Video.Height && info.Video.DVProfile == 0 {
					info.Video.DVProfile = 7
					info.Video.HDR = append([]string{"DV"}, info.Video.HDR...)
				}
				return true // the first video track is the movie
			}
			v := &Video{Codec: vcodec, Width: int(width), Height: int(height), BitDepth: int(bits), DVProfile: dvProfile}
			if dvProfile > 0 {
				v.HDR = addUnique(v.HDR, "DV")
			}
			hdr := hdrFromTransfer(int(transfer))
			if vcodec == "H.265" && len(private) > 0 {
				cfg := parseHVCC(private)
				if v.BitDepth == 0 {
					v.BitDepth = cfg.bitDepth
				}
				if hdr == "" {
					hdr = cfg.hdr()
				}
			}
			if vcodec == "H.264" && v.BitDepth == 0 && len(private) > 0 {
				v.BitDepth = avcBitDepth(private)
			}
			if vcodec == "AV1" && v.BitDepth == 0 && len(private) >= 3 {
				v.BitDepth = av1BitDepth(private)
			}
			v.HDR = addUnique(v.HDR, hdr)
			info.Video = v
		case trackAudio:
			info.Audio = append(info.Audio, Audio{
				Codec: mkvAudioCodec(codec), Channels: int(channels), Language: normLang(lang), Title: name, Default: isDefault,
			})
		case trackSubtitle:
			info.Subtitles = append(info.Subtitles, Subtitle{
				Codec: mkvSubCodec(codec), Language: normLang(lang), Title: name,
				Forced: forced || strings.Contains(strings.ToLower(name), "forced"),
			})
		}
		return true
	})
}

// dvProfileFrom reads the profile from a Dolby Vision configuration record:
// dv_version_major (8), dv_version_minor (8), dv_profile (7 bits) ...
func dvProfileFrom(b []byte) int {
	if len(b) < 3 {
		return 1 // present, profile unknown
	}
	if p := int(b[2] >> 1); p > 0 {
		return p
	}
	return 1
}

func mkvVideoCodec(id string) string {
	switch {
	case strings.HasPrefix(id, "V_MPEGH/ISO/HEVC"):
		return "H.265"
	case strings.HasPrefix(id, "V_MPEG4/ISO/AVC"):
		return "H.264"
	case id == "V_AV1":
		return "AV1"
	case id == "V_VP9":
		return "VP9"
	case id == "V_VP8":
		return "VP8"
	case strings.HasPrefix(id, "V_MPEG2"):
		return "MPEG-2"
	case strings.HasPrefix(id, "V_MPEG4/ISO/"), strings.HasPrefix(id, "V_MPEG4/MS"):
		return "MPEG-4"
	case id == "V_MS/VFW/FOURCC":
		return "VFW"
	}
	return strings.TrimPrefix(id, "V_")
}

func mkvAudioCodec(id string) string {
	switch {
	case id == "A_TRUEHD", id == "A_MLP":
		return "TrueHD"
	case strings.HasPrefix(id, "A_DTS"):
		return "DTS"
	case id == "A_EAC3":
		return "DD+"
	case id == "A_AC3", strings.HasPrefix(id, "A_AC3/"):
		return "DD"
	case strings.HasPrefix(id, "A_AAC"):
		return "AAC"
	case id == "A_FLAC":
		return "FLAC"
	case id == "A_OPUS":
		return "Opus"
	case strings.HasPrefix(id, "A_PCM"):
		return "LPCM"
	case id == "A_MPEG/L3":
		return "MP3"
	case id == "A_MPEG/L2":
		return "MP2"
	case id == "A_VORBIS":
		return "Vorbis"
	}
	return strings.TrimPrefix(id, "A_")
}

func mkvSubCodec(id string) string {
	switch id {
	case "S_TEXT/UTF8", "S_TEXT/ASCII":
		return "SRT"
	case "S_TEXT/ASS", "S_TEXT/SSA", "S_ASS", "S_SSA":
		return "ASS"
	case "S_HDMV/PGS":
		return "PGS"
	case "S_VOBSUB":
		return "VobSub"
	case "S_TEXT/WEBVTT":
		return "WebVTT"
	case "S_DVBSUB":
		return "DVB"
	}
	return strings.TrimPrefix(id, "S_")
}
