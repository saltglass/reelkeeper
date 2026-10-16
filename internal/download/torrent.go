package download

import (
	"crypto/sha1" //nolint:gosec // BitTorrent v1 info hashes are SHA-1 by definition
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// InfoHash is a torrent's v1 info hash in lower-case hex: the SHA-1 of its
// bencoded "info" dictionary. qBittorrent identifies torrents by it, so
// Reelkeeper knows which torrent is which from the moment it adds one.
func InfoHash(torrent []byte) (string, error) {
	if len(torrent) == 0 || torrent[0] != 'd' {
		return "", errors.New("not a torrent file")
	}
	i := 1
	for i < len(torrent) && torrent[i] != 'e' {
		key, next, err := bstring(torrent, i)
		if err != nil {
			return "", err
		}
		end, err := skip(torrent, next, 0)
		if err != nil {
			return "", err
		}
		if key == "info" {
			sum := sha1.Sum(torrent[next:end]) //nolint:gosec // see import
			return hex.EncodeToString(sum[:]), nil
		}
		i = end
	}
	return "", errors.New("the torrent file has no info section")
}

// MagnetHash is the info hash in a magnet link, in lower-case hex.
func MagnetHash(magnet string) (string, error) {
	u, err := url.Parse(magnet)
	if err != nil || u.Scheme != "magnet" {
		return "", errors.New("not a magnet link")
	}
	for _, xt := range u.Query()["xt"] {
		h, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:")
		if !ok {
			continue
		}
		switch len(h) {
		case 40:
			if _, err := hex.DecodeString(h); err == nil {
				return h, nil
			}
		case 32: // base32, as some old magnets have
			b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
			if err == nil {
				return hex.EncodeToString(b), nil
			}
		}
	}
	return "", errors.New("the magnet link has no BitTorrent info hash")
}

// maxDepth bounds nesting, so a malicious file can't exhaust the stack.
const maxDepth = 64

// skip returns the index just past the bencoded value starting at i.
func skip(b []byte, i, depth int) (int, error) {
	if depth > maxDepth {
		return 0, errors.New("the torrent file is nested too deeply")
	}
	if i >= len(b) {
		return 0, errors.New("the torrent file is cut short")
	}
	switch c := b[i]; {
	case c == 'i':
		for j := i + 1; j < len(b); j++ {
			if b[j] == 'e' {
				return j + 1, nil
			}
		}
		return 0, errors.New("the torrent file is cut short")
	case c == 'l' || c == 'd':
		j := i + 1
		for j < len(b) && b[j] != 'e' {
			var err error
			if j, err = skip(b, j, depth+1); err != nil {
				return 0, err
			}
		}
		if j >= len(b) {
			return 0, errors.New("the torrent file is cut short")
		}
		return j + 1, nil
	case c >= '0' && c <= '9':
		_, next, err := bstring(b, i)
		return next, err
	default:
		return 0, fmt.Errorf("the torrent file is damaged (unexpected %q)", c)
	}
}

// bstring reads a bencoded string ("4:spam") at i.
func bstring(b []byte, i int) (string, int, error) {
	n := 0
	j := i
	for ; j < len(b) && b[j] >= '0' && b[j] <= '9'; j++ {
		n = n*10 + int(b[j]-'0')
		if n > len(b) {
			return "", 0, errors.New("the torrent file is damaged (string too long)")
		}
	}
	if j == i || j >= len(b) || b[j] != ':' || j+1+n > len(b) {
		return "", 0, errors.New("the torrent file is damaged")
	}
	return string(b[j+1 : j+1+n]), j + 1 + n, nil
}

// PieceLength is the size of a .torrent file's pieces, or 0 when it can't be
// read. The client checks a torrent's files piece by piece, so this is the
// grain of "bytes verified".
func PieceLength(torrent []byte) int64 {
	if len(torrent) == 0 || torrent[0] != 'd' {
		return 0
	}
	i := 1
	for i < len(torrent) && torrent[i] != 'e' {
		key, next, err := bstring(torrent, i)
		if err != nil {
			return 0
		}
		end, err := skip(torrent, next, 0)
		if err != nil {
			return 0
		}
		if key != "info" {
			i = end
			continue
		}
		// Inside the info dictionary: look for "piece length".
		j := next + 1
		for j < end && torrent[j] != 'e' {
			k, vstart, err := bstring(torrent, j)
			if err != nil {
				return 0
			}
			vend, err := skip(torrent, vstart, 0)
			if err != nil {
				return 0
			}
			if k == "piece length" && torrent[vstart] == 'i' {
				var n int64
				for _, c := range torrent[vstart+1 : vend-1] {
					if c < '0' || c > '9' {
						return 0
					}
					n = n*10 + int64(c-'0')
				}
				return n
			}
			j = vend
		}
		return 0
	}
	return 0
}
