package main

import (
	"fmt"
	"hash/crc32"
	"strings"
)

// lineLength is how many encoded bytes go on one line, the usual value.
const lineLength = 128

// yencPart encodes one part of a file the way Usenet uploaders do: the
// =ybegin/=ypart header, the encoded lines, and =yend with the part's CRC.
// begin is the part's first byte's offset in the file (0-based); yEnc
// itself counts from 1.
func yencPart(name string, part, total int, fileSize int64, begin int64, data []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=ybegin part=%d total=%d line=%d size=%d name=%s\r\n", part, total, lineLength, fileSize, name)
	fmt.Fprintf(&b, "=ypart begin=%d end=%d\r\n", begin+1, begin+int64(len(data)))
	b.WriteString(yencEncode(data))
	fmt.Fprintf(&b, "=yend size=%d part=%d pcrc32=%08x\r\n", len(data), part, crc32.ChecksumIEEE(data))
	return b.String()
}

// yencEncode encodes data as yEnc lines, each ending in CRLF. Every byte is
// shifted by 42; the few results a news server or reader would mangle (NUL,
// LF, CR, "=") are escaped as "=" plus the byte shifted by a further 64, and
// so are a tab or space at either end of a line and a "." starting one.
func yencEncode(data []byte) string {
	var b strings.Builder
	col := 0
	for i, c := range data {
		o := c + 42
		esc := o == 0x00 || o == 0x0A || o == 0x0D || o == '='
		last := col >= lineLength-1 || i == len(data)-1
		if (o == '\t' || o == ' ') && (col == 0 || last) {
			esc = true
		}
		if o == '.' && col == 0 {
			esc = true
		}
		if esc {
			b.WriteByte('=')
			b.WriteByte(o + 64)
			col += 2
		} else {
			b.WriteByte(o)
			col++
		}
		if col >= lineLength {
			b.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		b.WriteString("\r\n")
	}
	return b.String()
}

// yencDecode reverses yencEncode on the encoded lines alone (no header or
// trailer). Only the tests use it.
func yencDecode(lines string) []byte {
	var out []byte
	escaped := false
	for i := 0; i < len(lines); i++ {
		c := lines[i]
		switch {
		case c == '\r' || c == '\n':
			continue
		case escaped:
			out = append(out, c-64-42)
			escaped = false
		case c == '=':
			escaped = true
		default:
			out = append(out, c-42)
		}
	}
	return out
}
