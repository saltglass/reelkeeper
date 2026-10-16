package mediainfo

// Builders for synthetic test files. They write just enough Matroska / MP4
// structure for the reader, with no real media, so the tests are self-
// contained and safe to publish.

import (
	"bytes"
	"encoding/binary"
	"math"
)

// --- Matroska ---------------------------------------------------------------

// el writes an EBML element. id is given with its marker bits, as in the
// spec (e.g. 0x1A45DFA3).
func el(id uint32, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	var out []byte
	switch {
	case id >= 0x1000000:
		out = append(out, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id >= 0x10000:
		out = append(out, byte(id>>16), byte(id>>8), byte(id))
	case id >= 0x100:
		out = append(out, byte(id>>8), byte(id))
	default:
		out = append(out, byte(id))
	}
	out = append(out, vsize(len(body))...)
	return append(out, body...)
}

// vsize encodes a size as an 8-byte EBML variable-length integer.
func vsize(n int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(n))
	b[0] = 0x01
	return b
}

func euint(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func efloat(v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return b
}

func estr(s string) []byte { return []byte(s) }

// mkvFile wraps Segment children in an EBML header and a Segment.
func mkvFile(segmentChildren ...[]byte) []byte {
	head := el(idEBML, el(idDocType, estr("matroska")))
	return append(head, el(idSegment, segmentChildren...)...)
}

// mkvFileUnknownSize writes the Segment with an "unknown size", as live
// recordings do.
func mkvFileUnknownSize(segmentChildren ...[]byte) []byte {
	head := el(idEBML, el(idDocType, estr("matroska")))
	seg := []byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	return append(append(head, seg...), bytes.Join(segmentChildren, nil)...)
}

// --- MP4 ----------------------------------------------------------------------

func box(typ string, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b, uint32(8+len(body)))
	copy(b[4:], typ)
	return append(b, body...)
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func mvhd(timescale, duration uint32) []byte {
	return box("mvhd", []byte{0, 0, 0, 0}, u32(0), u32(0), u32(timescale), u32(duration), make([]byte, 80))
}

func mdhd(lang string) []byte {
	l := uint16(lang[0]-0x60)<<10 | uint16(lang[1]-0x60)<<5 | uint16(lang[2]-0x60)
	return box("mdhd", []byte{0, 0, 0, 0}, u32(0), u32(0), u32(1000), u32(0), u16(l), u16(0))
}

func hdlr(kind, name string) []byte {
	return box("hdlr", []byte{0, 0, 0, 0}, u32(0), []byte(kind), make([]byte, 12), []byte(name), []byte{0})
}

// visualEntry is a VisualSampleEntry with the given child boxes.
func visualEntry(typ string, w, h uint16, children ...[]byte) []byte {
	fixed := make([]byte, 78)
	binary.BigEndian.PutUint16(fixed[6:8], 1) // data reference index
	binary.BigEndian.PutUint16(fixed[24:26], w)
	binary.BigEndian.PutUint16(fixed[26:28], h)
	return box(typ, append([][]byte{fixed}, children...)...)
}

func audioEntry(typ string, channels uint16, children ...[]byte) []byte {
	fixed := make([]byte, 28)
	binary.BigEndian.PutUint16(fixed[6:8], 1)
	binary.BigEndian.PutUint16(fixed[16:18], channels)
	binary.BigEndian.PutUint16(fixed[18:20], 16)
	return box(typ, append([][]byte{fixed}, children...)...)
}

func trak(handler, lang, name string, entry []byte) []byte {
	stsd := box("stsd", []byte{0, 0, 0, 0}, u32(1), entry)
	return box("trak",
		box("tkhd", []byte{0, 0, 0, 1}, make([]byte, 80)),
		box("mdia", mdhd(lang), hdlr(handler, name), box("minf", box("stbl", stsd))))
}

// --- HEVC -------------------------------------------------------------------

// bitWriter writes bits and Exp-Golomb numbers, mirroring the reader, to
// build HEVC parameter sets for tests.
type bitWriter struct {
	b   []byte
	pos int
}

func (w *bitWriter) u(n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		if w.pos%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 == 1 {
			w.b[len(w.b)-1] |= 1 << (7 - w.pos%8)
		}
		w.pos++
	}
}

func (w *bitWriter) ue(v uint64) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

// spsOptions picks which optional SPS structures a test SPS contains, to
// exercise the reader's handling of each.
type spsOptions struct {
	transfer     int // 0 = no VUI colour description
	maxSubLayers int // sps_max_sub_layers_minus1
	scalingList  bool
	refPicSets   int
	longTerm     bool
	pcm          bool
}

// buildSPS writes an HEVC SPS NAL unit following H.265 7.3.2.2.
func buildSPS(o spsOptions) []byte {
	w := &bitWriter{}
	w.u(16, 0x4201)                // NAL header: type 33 (SPS)
	w.u(4, 0)                      // vps id
	w.u(3, uint64(o.maxSubLayers)) // max_sub_layers_minus1
	w.u(1, 1)                      // temporal_id_nesting
	w.u(88, 0)                     // general profile
	w.u(8, 153)                    // general level
	for range o.maxSubLayers {
		w.u(1, 1) // sub_layer_profile_present
		w.u(1, 1) // sub_layer_level_present
	}
	if o.maxSubLayers > 0 {
		w.u(2*(8-o.maxSubLayers), 0)
	}
	for range o.maxSubLayers {
		w.u(88, 0)
		w.u(8, 0)
	}
	w.ue(0)    // sps id
	w.ue(1)    // chroma 4:2:0
	w.ue(3840) // width
	w.ue(2160) // height
	w.u(1, 1)  // conformance window
	for range 4 {
		w.ue(0)
	}
	w.ue(2) // bit depth luma - 8
	w.ue(2) // bit depth chroma - 8
	w.ue(4) // log2_max_poc_lsb - 4 → 8 bits
	w.u(1, 1)
	for range o.maxSubLayers + 1 {
		w.ue(4)
		w.ue(0)
		w.ue(0)
	}
	for range 6 {
		w.ue(1)
	}
	if o.scalingList {
		w.u(1, 1) // scaling_list_enabled
		w.u(1, 1) // sps_scaling_list_data_present
		for size := 0; size < 4; size++ {
			step := 1
			if size == 3 {
				step = 3
			}
			for m := 0; m < 6; m += step {
				if m == 0 {
					w.u(1, 1) // explicit list
					if size > 1 {
						w.ue(0) // dc coef (se, written as ue 0)
					}
					for range min(64, 1<<(4+size*2)) {
						w.ue(0)
					}
				} else {
					w.u(1, 0) // predicted
					w.ue(0)
				}
			}
		}
	} else {
		w.u(1, 0)
	}
	w.u(1, 0) // amp
	w.u(1, 1) // sao
	if o.pcm {
		w.u(1, 1)
		w.u(8, 0x77)
		w.ue(0)
		w.ue(0)
		w.u(1, 0)
	} else {
		w.u(1, 0)
	}
	w.ue(uint64(o.refPicSets))
	prevDeltaPocs := 0
	for i := range o.refPicSets {
		if i == 0 {
			w.ue(1) // num negative
			w.ue(1) // num positive
			for range 2 {
				w.ue(0)
				w.u(1, 1)
			}
			prevDeltaPocs = 2
		} else {
			w.u(1, 1) // inter_ref_pic_set_prediction
			w.u(1, 0) // sign
			w.ue(0)   // abs delta
			// One flag per picture of the previous set, plus one; all
			// used, so this set has one more picture than the last.
			for j := 0; j <= prevDeltaPocs; j++ {
				w.u(1, 1) // used_by_curr_pic
			}
			prevDeltaPocs++
		}
	}
	if o.longTerm {
		w.u(1, 1)
		w.ue(1)
		w.u(8+1, 0)
	} else {
		w.u(1, 0)
	}
	w.u(1, 1) // temporal mvp
	w.u(1, 1) // strong intra smoothing
	if o.transfer == 0 {
		w.u(1, 0) // no VUI
	} else {
		w.u(1, 1) // VUI present
		w.u(1, 0) // aspect ratio
		w.u(1, 0) // overscan
		w.u(1, 1) // video signal type
		w.u(3, 5) // video format
		w.u(1, 0) // full range
		w.u(1, 1) // colour description
		w.u(8, 9) // BT.2020 primaries
		w.u(8, uint64(o.transfer))
		w.u(8, 9)
	}
	w.u(1, 1) // rbsp stop bit
	return escape(w.b)
}

// escape inserts emulation-prevention bytes, as an encoder must.
func escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// hvcc builds an HEVCDecoderConfigurationRecord holding the given NAL
// units (each tagged with its type).
func hvcc(bitDepth int, nals map[byte][]byte) []byte {
	b := make([]byte, 23)
	b[0] = 1
	b[17] = 0xF8 | byte(bitDepth-8)
	b[18] = 0xF8 | byte(bitDepth-8)
	b[22] = byte(len(nals))
	for _, typ := range []byte{32, 33, 34, 39} {
		nal, ok := nals[typ]
		if !ok {
			continue
		}
		b = append(b, 0x80|typ, 0, 1, byte(len(nal)>>8), byte(len(nal)))
		b = append(b, nal...)
	}
	return b
}

// seiMastering is a prefix SEI NAL with mastering-display (137) and
// content-light-level (144) payloads.
func seiMastering() []byte {
	nal := []byte{0x4E, 0x01}              // NAL header: type 39
	nal = append(nal, 137, 24)             // payload type, size
	nal = append(nal, make([]byte, 24)...) // values don't matter here
	nal = append(nal, 144, 4, 0, 1, 0, 1)  // max CLL / FALL
	return append(nal, 0x80)
}
