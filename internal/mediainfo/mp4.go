package mediainfo

import (
	"encoding/binary"
	"io"
	"math"
	"strings"
	"time"
)

// MP4 (ISO base media) files are a tree of boxes: a 32-bit size, a
// four-letter type, then the payload. The track descriptions live under
// moov/trak; the video data (mdat) can come before or after moov, so we
// skip over boxes by their size and never read mdat.
//
//	moov
//	  mvhd                 duration
//	  trak
//	    mdia
//	      mdhd             language
//	      hdlr             track kind: vide / soun / subt / text / sbtl
//	      minf/stbl/stsd   sample description: codec, size, channels,
//	                       colour (HDR) and Dolby Vision boxes

type mp4 struct {
	r    io.ReaderAt
	size int64
}

// box reads the box header at off: type, payload offset and payload size.
func (m mp4) box(off, end int64) (typ string, data, size int64, err error) {
	var h [16]byte
	if _, err := m.r.ReadAt(h[:8], off); err != nil {
		return "", 0, 0, err
	}
	size = int64(binary.BigEndian.Uint32(h[0:4]))
	typ = string(h[4:8])
	data = off + 8
	switch size {
	case 0: // extends to the end of the enclosing box or file
		size = end - off
	case 1: // 64-bit size follows
		if _, err := m.r.ReadAt(h[8:16], off+8); err != nil {
			return "", 0, 0, err
		}
		big := binary.BigEndian.Uint64(h[8:16])
		if big > math.MaxInt64 {
			return "", 0, 0, errCorrupt
		}
		size = int64(big)
		data = off + 16
	}
	if size < data-off || off+size > end {
		if off+size > end && off < end {
			size = end - off // truncated: read what's there
		} else {
			return "", 0, 0, errCorrupt
		}
	}
	return typ, data, off + size - data, nil
}

// each calls fn for every box in [start, end); fn returns false to stop.
func (m mp4) each(start, end int64, fn func(typ string, data, size int64) bool) error {
	for off := start; off+8 <= end; {
		typ, data, size, err := m.box(off, end)
		if err != nil {
			return err
		}
		if !fn(typ, data, size) {
			return nil
		}
		off = data + size
	}
	return nil
}

func (m mp4) read(off, n int64) ([]byte, error) {
	if n < 0 || n > maxRead {
		return nil, errCorrupt
	}
	b := make([]byte, n)
	_, err := m.r.ReadAt(b, off)
	return b, err
}

func readMP4(r io.ReaderAt, size int64) (*Info, error) {
	m := mp4{r: r, size: size}
	info := &Info{Container: "mp4"}
	found := false
	err := m.each(0, size, func(typ string, data, sz int64) bool {
		if typ == "moov" {
			found = true
			m.moov(data, sz, info)
			return false
		}
		return true // skip ftyp, mdat, free...
	})
	if !found {
		if err != nil {
			return nil, err
		}
		return nil, errCorrupt
	}
	return info, nil
}

func (m mp4) moov(start, size int64, info *Info) {
	_ = m.each(start, start+size, func(typ string, data, sz int64) bool {
		switch typ {
		case "mvhd":
			info.Duration = m.mvhd(data, sz)
		case "trak":
			m.trak(data, sz, info)
		case "udta":
			m.udta(data, sz, info)
		}
		return true
	})
}

// mvhd: version(1) flags(3), then 32- or 64-bit times, timescale, duration.
func (m mp4) mvhd(data, size int64) time.Duration {
	b, err := m.read(data, min(size, 32))
	if err != nil || len(b) < 20 {
		return 0
	}
	var scale, dur uint64
	if b[0] == 1 {
		if len(b) < 32 {
			return 0
		}
		scale = uint64(binary.BigEndian.Uint32(b[20:24]))
		dur = binary.BigEndian.Uint64(b[24:32])
	} else {
		scale = uint64(binary.BigEndian.Uint32(b[12:16]))
		dur = uint64(binary.BigEndian.Uint32(b[16:20]))
	}
	if scale == 0 || dur == 0 || dur == ^uint64(0) || dur == 0xFFFFFFFF {
		return 0
	}
	return time.Duration(float64(dur) / float64(scale) * float64(time.Second))
}

// udta/meta/ilst/©nam holds an iTunes-style title.
func (m mp4) udta(start, size int64, info *Info) {
	_ = m.each(start, start+size, func(typ string, data, sz int64) bool {
		if typ == "meta" {
			// "meta" is a full box: skip version and flags.
			_ = m.each(data+4, data+sz, func(typ string, data, sz int64) bool {
				if typ == "ilst" {
					_ = m.each(data, data+sz, func(typ string, data, sz int64) bool {
						if typ == "\xa9nam" {
							_ = m.each(data, data+sz, func(typ string, data, sz int64) bool {
								if typ == "data" && sz > 8 {
									if b, err := m.read(data+8, min(sz-8, 1024)); err == nil {
										info.Title = string(b)
									}
								}
								return true
							})
						}
						return true
					})
				}
				return true
			})
		}
		return true
	})
}

type mp4Track struct {
	handler  string
	lang     string
	name     string
	enabled  bool
	codec    string // sample entry type
	width    int
	height   int
	channels int
	bits     int
	transfer int
	hevcHDR  string // from the HEVC configuration, used when there is no colr box
	dv       int
}

func (m mp4) trak(start, size int64, info *Info) {
	t := mp4Track{enabled: true}
	_ = m.each(start, start+size, func(typ string, data, sz int64) bool {
		switch typ {
		case "tkhd":
			if b, err := m.read(data, 4); err == nil {
				t.enabled = b[3]&1 == 1
			}
		case "mdia":
			_ = m.each(data, data+sz, func(typ string, data, sz int64) bool {
				switch typ {
				case "mdhd":
					t.lang = m.mdhdLang(data, sz)
				case "hdlr":
					if b, err := m.read(data, min(sz, 256)); err == nil && len(b) >= 12 {
						t.handler = string(b[8:12])
						if len(b) > 24 {
							t.name = strings.TrimRight(string(b[24:]), "\x00")
						}
					}
				case "minf":
					_ = m.each(data, data+sz, func(typ string, data, sz int64) bool {
						if typ == "stbl" {
							_ = m.each(data, data+sz, func(typ string, data, sz int64) bool {
								if typ == "stsd" {
									m.stsd(data, sz, &t)
									return false
								}
								return true
							})
						}
						return true
					})
				}
				return true
			})
		}
		return true
	})

	lang := normLang(t.lang)
	switch t.handler {
	case "vide":
		if info.Video != nil || t.codec == "" {
			return
		}
		v := &Video{Codec: mp4VideoCodec(t.codec), Width: t.width, Height: t.height, BitDepth: t.bits, DVProfile: t.dv}
		if t.dv > 0 || t.codec == "dvh1" || t.codec == "dvhe" || t.codec == "dva1" || t.codec == "dvav" {
			if v.DVProfile == 0 {
				v.DVProfile = 1
			}
			v.HDR = addUnique(v.HDR, "DV")
		}
		hdr := hdrFromTransfer(t.transfer)
		if hdr == "" {
			hdr = t.hevcHDR
		}
		v.HDR = addUnique(v.HDR, hdr)
		info.Video = v
	case "soun":
		info.Audio = append(info.Audio, Audio{
			Codec: mp4AudioCodec(t.codec), Channels: t.channels, Language: lang, Title: cleanHandlerName(t.name), Default: t.enabled,
		})
	case "subt", "text", "sbtl":
		info.Subtitles = append(info.Subtitles, Subtitle{Codec: mp4SubCodec(t.codec), Language: lang, Title: cleanHandlerName(t.name)})
	}
}

// cleanHandlerName drops generic muxer names that aren't real track titles.
func cleanHandlerName(n string) string {
	n = strings.TrimSpace(n)
	switch strings.ToLower(n) {
	case "", "soundhandler", "videohandler", "subtitlehandler", "texthandler", "core media audio",
		"core media video", "core media text", "isomedia file produced by google, 5-11-2011":
		return ""
	}
	return n
}

// mdhd language: 15 bits, three letters of 5 bits each, offset by 0x60.
func (m mp4) mdhdLang(data, size int64) string {
	b, err := m.read(data, min(size, 36))
	if err != nil || len(b) < 24 {
		return ""
	}
	off := 20
	if b[0] == 1 {
		off = 32
	}
	if len(b) < off+2 {
		return ""
	}
	v := binary.BigEndian.Uint16(b[off : off+2])
	if v == 0 || v == 0x7FFF {
		return ""
	}
	return string([]byte{byte(v>>10&0x1F) + 0x60, byte(v>>5&0x1F) + 0x60, byte(v&0x1F) + 0x60})
}

// stsd is a full box holding sample entries; we read the first.
func (m mp4) stsd(data, size int64, t *mp4Track) {
	if size < 16 {
		return
	}
	// version/flags (4) + entry_count (4), then the first entry.
	typ, edata, esize, err := m.box(data+8, data+size)
	if err != nil {
		return
	}
	t.codec = typ
	switch t.handler {
	case "vide":
		// VisualSampleEntry: 6 reserved, 2 data ref, 16 predefined/reserved,
		// width(2), height(2), then 50 more bytes before child boxes.
		b, err := m.read(edata, min(esize, 78))
		if err != nil || len(b) < 78 {
			return
		}
		t.width = int(binary.BigEndian.Uint16(b[24:26]))
		t.height = int(binary.BigEndian.Uint16(b[26:28]))
		_ = m.each(edata+78, edata+esize, func(typ string, data, sz int64) bool {
			switch typ {
			case "colr":
				if c, err := m.read(data, min(sz, 10)); err == nil && len(c) >= 6 &&
					(string(c[0:4]) == "nclx" || string(c[0:4]) == "nclc") {
					t.transfer = int(binary.BigEndian.Uint16(c[6:8]))
				}
			case "hvcC":
				if c, err := m.read(data, min(sz, 64<<10)); err == nil {
					cfg := parseHVCC(c)
					t.bits = cfg.bitDepth
					t.hevcHDR = cfg.hdr()
				}
			case "avcC":
				if c, err := m.read(data, min(sz, 8)); err == nil {
					t.bits = avcBitDepth(c)
				}
			case "av1C":
				if c, err := m.read(data, min(sz, 4)); err == nil && len(c) >= 3 {
					t.bits = av1BitDepth(c)
				}
			case "dvcC", "dvvC", "dvwC":
				if c, err := m.read(data, min(sz, 24)); err == nil {
					t.dv = dvProfileFrom(c)
				}
			}
			return true
		})
	case "soun":
		// AudioSampleEntry: 6 reserved, 2 data ref, 8 reserved,
		// channelcount(2), samplesize(2), 4 reserved, samplerate(4).
		b, err := m.read(edata, min(esize, 28))
		if err != nil || len(b) < 28 {
			return
		}
		t.channels = int(binary.BigEndian.Uint16(b[16:18]))
		_ = m.each(edata+28, edata+esize, func(typ string, data, sz int64) bool {
			if typ == "dac3" {
				if c, err := m.read(data, min(sz, 3)); err == nil && len(c) == 3 {
					t.channels = ac3Channels(c)
				}
			}
			return true
		})
	}
}

// ac3Channels decodes the AC-3 specific box: fscod(2) bsid(5) bsmod(3)
// acmod(3) lfeon(1) bit_rate_code(5) reserved(5).
func ac3Channels(b []byte) int {
	v := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	acmod := v >> 11 & 0x07
	lfe := v >> 10 & 0x01
	ch := []int{2, 1, 2, 3, 3, 4, 4, 5}[acmod]
	return ch + int(lfe)
}

func mp4VideoCodec(t string) string {
	switch t {
	case "avc1", "avc3", "dva1", "dvav":
		return "H.264"
	case "hvc1", "hev1", "dvh1", "dvhe":
		return "H.265"
	case "av01":
		return "AV1"
	case "vp09":
		return "VP9"
	case "mp4v":
		return "MPEG-4"
	}
	return t
}

func mp4AudioCodec(t string) string {
	switch t {
	case "mp4a":
		return "AAC"
	case "ac-3":
		return "DD"
	case "ec-3":
		return "DD+"
	case "mlpa":
		return "TrueHD"
	case "dtsc", "dtsh", "dtsl", "dtse", "dtsx":
		return "DTS"
	case "fLaC":
		return "FLAC"
	case "Opus":
		return "Opus"
	case "alac":
		return "ALAC"
	case "lpcm", "sowt", "twos", "ipcm":
		return "LPCM"
	case ".mp3":
		return "MP3"
	}
	return t
}

func mp4SubCodec(t string) string {
	switch t {
	case "tx3g":
		return "TX3G"
	case "wvtt":
		return "WebVTT"
	case "stpp":
		return "TTML"
	case "c608":
		return "CEA-608"
	}
	return t
}
