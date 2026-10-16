package download

import (
	"crypto/sha1" //nolint:gosec // matches InfoHash
	"encoding/hex"
	"testing"
)

func TestInfoHash(t *testing.T) {
	info := "d6:lengthi12345e4:name8:test.mkv12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae"
	file := "d8:announce20:http://tracker/a/b/c13:creation datei1700000000e4:info" + info + "e"
	sum := sha1.Sum([]byte(info)) //nolint:gosec // see import
	got, err := InfoHash([]byte(file))
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("InfoHash = %q, %v", got, err)
	}
	for _, bad := range []string{"", "<html>", "d4:infoi1", "d3:foo", "d4:name4:teste", "d99999999999:x"} {
		if _, err := InfoHash([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestMagnetHash(t *testing.T) {
	h, err := MagnetHash("magnet:?xt=urn:btih:ABCDEF0123456789ABCDEF0123456789ABCDEF01&dn=x")
	if err != nil || h != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("hex: %q, %v", h, err)
	}
	// base32 of 20 bytes "abcdefghijklmnopqrst".
	h, err = MagnetHash("magnet:?dn=x&xt=urn:btih:MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U")
	if err != nil || h != hex.EncodeToString([]byte("abcdefghijklmnopqrst")) {
		t.Errorf("base32: %q, %v", h, err)
	}
	for _, bad := range []string{"http://x", "magnet:?dn=x", "magnet:?xt=urn:btih:zz"} {
		if _, err := MagnetHash(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func FuzzInfoHash(f *testing.F) {
	f.Add([]byte("d4:infod4:name1:xee"))
	f.Add([]byte("dl" + "l" + "e"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = InfoHash(b) }) // must not panic
}
