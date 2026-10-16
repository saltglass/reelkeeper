package mediainfo

import "errors"

// HEVC (H.265) streams describe themselves in an "hvcC" configuration
// record (Matroska's CodecPrivate, MP4's hvcC box). It holds the sequence
// parameter set (SPS), whose VUI section states the colour transfer: PQ
// (HDR10) or HLG. It may also hold SEI messages such as HDR10 mastering
// display metadata.
//
// The SPS is bit-packed with variable-length (Exp-Golomb) numbers, so it's
// read field by field up to the VUI, following ITU-T H.265 section 7.3.2.2.

// hevcConfig is what we learn from an hvcC record.
type hevcConfig struct {
	bitDepth     int
	transfer     int  // 0 if the SPS couldn't be read or didn't say
	masteringSEI bool // HDR10 static metadata present
	contentLight bool
}

// parseHVCC reads an HEVCDecoderConfigurationRecord.
func parseHVCC(b []byte) hevcConfig {
	var c hevcConfig
	if len(b) < 23 {
		return c
	}
	c.bitDepth = int(b[17]&0x07) + 8
	off := 23
	for a := 0; a < int(b[22]) && off+3 <= len(b); a++ {
		typ := b[off] & 0x3F
		n := int(b[off+1])<<8 | int(b[off+2])
		off += 3
		for k := 0; k < n && off+2 <= len(b); k++ {
			l := int(b[off])<<8 | int(b[off+1])
			off += 2
			if off+l > len(b) {
				return c
			}
			nal := unescape(b[off : off+l])
			switch typ {
			case 33: // SPS
				if tc, err := spsTransfer(nal); err == nil && tc > 0 {
					c.transfer = tc
				}
			case 39, 40: // prefix / suffix SEI
				for _, p := range seiPayloadTypes(nal) {
					switch p {
					case 137:
						c.masteringSEI = true
					case 144:
						c.contentLight = true
					}
				}
			}
			off += l
		}
	}
	return c
}

// hdr returns the HDR format this configuration signals, if any.
func (c hevcConfig) hdr() string {
	if f := hdrFromTransfer(c.transfer); f != "" {
		return f
	}
	if c.transfer == 0 && c.masteringSEI && c.bitDepth >= 10 {
		return "HDR10" // SPS unreadable, but HDR10 metadata is present
	}
	return ""
}

// unescape removes emulation-prevention bytes (00 00 03 → 00 00).
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
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

var errBits = errors.New("ran out of bits")

type bits struct {
	b   []byte
	pos int // in bits
}

func (r *bits) u(n int) (uint64, error) {
	var v uint64
	for range n {
		if r.pos >= len(r.b)*8 {
			return 0, errBits
		}
		bit := r.b[r.pos/8] >> (7 - r.pos%8) & 1
		v = v<<1 | uint64(bit)
		r.pos++
	}
	return v, nil
}

func (r *bits) skip(n int) error {
	if r.pos+n > len(r.b)*8 {
		return errBits
	}
	r.pos += n
	return nil
}

// ue reads an unsigned Exp-Golomb number.
func (r *bits) ue() (uint64, error) {
	zeros := 0
	for {
		b, err := r.u(1)
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, errBits
		}
	}
	rest, err := r.u(zeros)
	if err != nil {
		return 0, err
	}
	return (1<<zeros - 1) + rest, nil
}

func (r *bits) se() error { _, err := r.ue(); return err }

// spsTransfer walks an HEVC SPS up to the VUI and returns
// transfer_characteristics, or 0 if the SPS doesn't state it.
func spsTransfer(nal []byte) (int, error) {
	r := &bits{b: nal}
	if err := r.skip(16); err != nil { // NAL unit header
		return 0, err
	}
	if err := r.skip(4); err != nil { // sps_video_parameter_set_id
		return 0, err
	}
	maxSub3, err := r.u(3)
	if err != nil {
		return 0, err
	}
	maxSub := int(maxSub3)            // #nosec G115 -- a 3-bit field, at most 7
	if err := r.skip(1); err != nil { // temporal_id_nesting
		return 0, err
	}
	if err := profileTierLevel(r, maxSub); err != nil {
		return 0, err
	}
	if _, err := r.ue(); err != nil { // sps_seq_parameter_set_id
		return 0, err
	}
	chroma, err := r.ue()
	if err != nil {
		return 0, err
	}
	if chroma == 3 {
		if err := r.skip(1); err != nil {
			return 0, err
		}
	}
	for range 2 { // pic width, height
		if _, err := r.ue(); err != nil {
			return 0, err
		}
	}
	if conf, err := r.u(1); err != nil {
		return 0, err
	} else if conf == 1 {
		for range 4 {
			if _, err := r.ue(); err != nil {
				return 0, err
			}
		}
	}
	for range 2 { // bit depths
		if _, err := r.ue(); err != nil {
			return 0, err
		}
	}
	log2MaxPOC64, err := r.ue()
	if err != nil || log2MaxPOC64 > 12 { // the standard allows 0..12
		return 0, errBits
	}
	log2MaxPOC := int(log2MaxPOC64) // #nosec G115 -- checked above
	orderingAll, err := r.u(1)
	if err != nil {
		return 0, err
	}
	first := maxSub
	if orderingAll == 1 {
		first = 0
	}
	for i := first; i <= maxSub; i++ {
		for range 3 {
			if _, err := r.ue(); err != nil {
				return 0, err
			}
		}
	}
	for range 6 { // coding block / transform sizes and depths
		if _, err := r.ue(); err != nil {
			return 0, err
		}
	}
	if scaling, err := r.u(1); err != nil {
		return 0, err
	} else if scaling == 1 {
		if present, err := r.u(1); err != nil {
			return 0, err
		} else if present == 1 {
			if err := scalingListData(r); err != nil {
				return 0, err
			}
		}
	}
	if err := r.skip(2); err != nil { // amp, sample_adaptive_offset
		return 0, err
	}
	if pcm, err := r.u(1); err != nil {
		return 0, err
	} else if pcm == 1 {
		if err := r.skip(8); err != nil {
			return 0, err
		}
		for range 2 {
			if _, err := r.ue(); err != nil {
				return 0, err
			}
		}
		if err := r.skip(1); err != nil {
			return 0, err
		}
	}
	numSets, err := r.ue()
	if err != nil || numSets > 64 {
		return 0, errBits
	}
	numDeltaPocs := make([]int, numSets)
	for i := 0; i < int(numSets); i++ {
		n, err := stRefPicSet(r, i, int(numSets), numDeltaPocs)
		if err != nil {
			return 0, err
		}
		numDeltaPocs[i] = n
	}
	if lt, err := r.u(1); err != nil {
		return 0, err
	} else if lt == 1 {
		n, err := r.ue()
		if err != nil || n > 32 {
			return 0, errBits
		}
		for range n {
			if err := r.skip(log2MaxPOC + 4 + 1); err != nil {
				return 0, err
			}
		}
	}
	if err := r.skip(2); err != nil { // temporal_mvp, strong_intra_smoothing
		return 0, err
	}
	vui, err := r.u(1)
	if err != nil || vui == 0 {
		return 0, err
	}
	// vui_parameters()
	if ar, err := r.u(1); err != nil {
		return 0, err
	} else if ar == 1 {
		idc, err := r.u(8)
		if err != nil {
			return 0, err
		}
		if idc == 255 {
			if err := r.skip(32); err != nil {
				return 0, err
			}
		}
	}
	if os, err := r.u(1); err != nil {
		return 0, err
	} else if os == 1 {
		if err := r.skip(1); err != nil {
			return 0, err
		}
	}
	signal, err := r.u(1)
	if err != nil || signal == 0 {
		return 0, err
	}
	if err := r.skip(4); err != nil { // video_format, full_range
		return 0, err
	}
	desc, err := r.u(1)
	if err != nil || desc == 0 {
		return 0, err
	}
	if err := r.skip(8); err != nil { // colour_primaries
		return 0, err
	}
	tc, err := r.u(8)
	return int(tc), err // #nosec G115 -- an 8-bit field
}

func profileTierLevel(r *bits, maxSub int) error {
	if err := r.skip(88 + 8); err != nil { // general profile (88 bits) + level
		return err
	}
	profilePresent := make([]bool, maxSub)
	levelPresent := make([]bool, maxSub)
	for i := range maxSub {
		p, err := r.u(1)
		if err != nil {
			return err
		}
		l, err := r.u(1)
		if err != nil {
			return err
		}
		profilePresent[i], levelPresent[i] = p == 1, l == 1
	}
	if maxSub > 0 {
		if err := r.skip(2 * (8 - maxSub)); err != nil {
			return err
		}
	}
	for i := range maxSub {
		if profilePresent[i] {
			if err := r.skip(88); err != nil {
				return err
			}
		}
		if levelPresent[i] {
			if err := r.skip(8); err != nil {
				return err
			}
		}
	}
	return nil
}

func scalingListData(r *bits) error {
	for size := 0; size < 4; size++ {
		step := 1
		if size == 3 {
			step = 3
		}
		for matrix := 0; matrix < 6; matrix += step {
			pred, err := r.u(1)
			if err != nil {
				return err
			}
			if pred == 0 {
				if _, err := r.ue(); err != nil { // delta
					return err
				}
				continue
			}
			coefs := min(64, 1<<(4+size*2))
			if size > 1 {
				if err := r.se(); err != nil { // dc coefficient
					return err
				}
			}
			for range coefs {
				if err := r.se(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// stRefPicSet reads st_ref_pic_set(idx) and returns its NumDeltaPocs.
func stRefPicSet(r *bits, idx, num int, numDeltaPocs []int) (int, error) {
	interPred := uint64(0)
	if idx != 0 {
		var err error
		if interPred, err = r.u(1); err != nil {
			return 0, err
		}
	}
	if interPred == 1 {
		// In the SPS, the reference set is always the previous one.
		if err := r.skip(1); err != nil { // delta_rps_sign
			return 0, err
		}
		if _, err := r.ue(); err != nil { // abs_delta_rps_minus1
			return 0, err
		}
		ref := numDeltaPocs[idx-1]
		n := 0
		for j := 0; j <= ref; j++ {
			used, err := r.u(1)
			if err != nil {
				return 0, err
			}
			useDelta := uint64(1)
			if used == 0 {
				if useDelta, err = r.u(1); err != nil {
					return 0, err
				}
			}
			if used == 1 || useDelta == 1 {
				n++
			}
		}
		return n, nil
	}
	neg, err := r.ue()
	if err != nil || neg > 16 {
		return 0, errBits
	}
	pos, err := r.ue()
	if err != nil || pos > 16 {
		return 0, errBits
	}
	for range neg + pos {
		if _, err := r.ue(); err != nil {
			return 0, err
		}
		if err := r.skip(1); err != nil {
			return 0, err
		}
	}
	return int(neg + pos), nil
}

// seiPayloadTypes lists the payload types in an SEI NAL unit.
func seiPayloadTypes(nal []byte) []int {
	var out []int
	i := 2 // NAL header
	for i < len(nal) {
		if nal[i] == 0x80 && i == len(nal)-1 { // rbsp trailing bits
			break
		}
		t := 0
		for i < len(nal) && nal[i] == 0xFF {
			t += 255
			i++
		}
		if i >= len(nal) {
			break
		}
		t += int(nal[i])
		i++
		size := 0
		for i < len(nal) && nal[i] == 0xFF {
			size += 255
			i++
		}
		if i >= len(nal) {
			break
		}
		size += int(nal[i])
		i++
		out = append(out, t)
		i += size
	}
	return out
}
