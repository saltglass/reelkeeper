package download

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // BitTorrent v1 pieces are SHA-1 by definition
	"fmt"
	"strings"
	"testing"
)

// testTorrentFile builds a small single-file .torrent for these bytes, the way
// a torrent program would, with a minimal bencode writer (tests only). The
// torrent is marked private, so a client that opens it doesn't look for peers
// through DHT or other outside services, and has no tracker.
func testTorrentFile(t testing.TB, name string, data []byte) []byte {
	t.Helper()
	const pieceLen = 16384
	var pieces bytes.Buffer
	for off := 0; off < len(data); off += pieceLen {
		sum := sha1.Sum(data[off:min(off+pieceLen, len(data))]) //nolint:gosec // see import
		pieces.Write(sum[:])
	}
	// Keys of a bencoded dictionary are in sorted order.
	var info bytes.Buffer
	fmt.Fprintf(&info, "d6:lengthi%de4:name%d:%s12:piece lengthi%de6:pieces%d:", len(data), len(name), name, pieceLen, pieces.Len())
	info.Write(pieces.Bytes())
	info.WriteString("7:privatei1ee")
	var file bytes.Buffer
	file.WriteString("d4:info")
	file.Write(info.Bytes())
	file.WriteString("e")
	return file.Bytes()
}

// testMultiTorrentFile is testTorrentFile for a torrent of several files in
// folders. name is the torrent's own folder; each file's path starts with it
// ("Name/Subs/en.srt") and its bytes follow one another in the pieces, in the
// order given.
func testMultiTorrentFile(t testing.TB, name string, files []tfile) []byte {
	t.Helper()
	const pieceLen = 16384
	var all []byte
	var list bytes.Buffer
	for _, f := range files {
		rel, ok := strings.CutPrefix(f.path, name+"/")
		if !ok {
			t.Fatalf("%s isn't in the folder %s", f.path, name)
		}
		all = append(all, f.data...)
		fmt.Fprintf(&list, "d6:lengthi%de4:pathl", len(f.data))
		for _, part := range strings.Split(rel, "/") {
			fmt.Fprintf(&list, "%d:%s", len(part), part)
		}
		list.WriteString("ee")
	}
	var pieces bytes.Buffer
	for off := 0; off < len(all); off += pieceLen {
		sum := sha1.Sum(all[off:min(off+pieceLen, len(all))]) //nolint:gosec // see import
		pieces.Write(sum[:])
	}
	var info bytes.Buffer
	fmt.Fprintf(&info, "d5:filesl%se4:name%d:%s12:piece lengthi%de6:pieces%d:", list.String(), len(name), name, pieceLen, pieces.Len())
	info.Write(pieces.Bytes())
	info.WriteString("7:privatei1ee")
	var file bytes.Buffer
	file.WriteString("d4:info")
	file.Write(info.Bytes())
	file.WriteString("e")
	return file.Bytes()
}
