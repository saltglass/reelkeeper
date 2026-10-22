package indexer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
)

// Fetched is what a release downloads as: a magnet link or a .torrent
// file's contents for a torrent, or an .nzb file's contents for Usenet.
type Fetched struct {
	Magnet  string
	Torrent []byte
	NZB     []byte
}

// ErrGrabLimit means the indexer's daily grab limit is used up.
var ErrGrabLimit = errors.New("this indexer has reached its daily grab limit")

// maxTorrentSize bounds a .torrent download; real ones are kilobytes, a
// few megabytes for huge season packs.
const maxTorrentSize = 20 << 20

// maxNZBSize bounds an .nzb download. An .nzb lists every article of a
// release, about 150 bytes each, so a 100 GB remux's is around 20 MB.
const maxNZBSize = 64 << 20

// Fetch gets what a release downloads as, through the indexer's own
// session: private trackers only hand .torrent files to a logged-in
// browser (and some only through the Cloudflare solver), so Reelkeeper fetches
// the file itself instead of handing qBittorrent a link it couldn't open.
// It counts as one of the indexer's grabs for the day.
func (s *Service) Fetch(ctx context.Context, rel Release) (Fetched, error) {
	ix, err := s.Get(ctx, rel.IndexerID)
	if err != nil {
		return Fetched{}, err
	}
	if s.overGrabLimit(ctx, ix) {
		return Fetched{}, ErrGrabLimit
	}
	c, err := s.client(ix)
	if err != nil {
		return Fetched{}, err
	}
	start := time.Now()
	f, err := fetch(ctx, c, rel.Release, ix.Protocol() == ProtocolUsenet)
	s.record(ctx, ix, time.Since(start), err, usageGrab)
	if err != nil {
		return Fetched{}, fmt.Errorf("%s: %w", ix.Name, err)
	}
	return f, nil
}

func fetch(ctx context.Context, c core.Client, rel core.Release, usenet bool) (Fetched, error) {
	link, err := c.DownloadLink(ctx, rel)
	if err != nil {
		return Fetched{}, err
	}
	what, limit := ".torrent file", int64(maxTorrentSize)
	if usenet {
		what, limit = ".nzb file", maxNZBSize
	}
	if strings.HasPrefix(link, "magnet:") {
		if usenet {
			return Fetched{}, errors.New("a Usenet indexer gave a magnet link, which is for torrents")
		}
		return Fetched{Magnet: link}, nil
	}
	sess, ok := c.(interface{ Session() core.HTTP })
	if !ok {
		return Fetched{}, fmt.Errorf("can't download %ss from this kind of indexer", what)
	}
	// Follow redirects by hand: some trackers answer a download link with
	// a redirect to a magnet link, which an HTTP client can't follow.
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return Fetched{}, err
		}
		resp, err := sess.Session().Do(req, false)
		if err != nil {
			return Fetched{}, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		_ = resp.Body.Close()
		if err != nil {
			return Fetched{}, err
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc := resp.Header.Get("Location")
			if strings.HasPrefix(loc, "magnet:") && !usenet {
				return Fetched{Magnet: loc}, nil
			}
			next, err := req.URL.Parse(loc)
			if err != nil || loc == "" {
				return Fetched{}, fmt.Errorf("the download link redirected nowhere useful")
			}
			link = next.String()
			continue
		case resp.StatusCode != http.StatusOK:
			return Fetched{}, fmt.Errorf("downloading the %s failed: %s", what, resp.Status)
		case int64(len(body)) > limit:
			return Fetched{}, fmt.Errorf("the %s is larger than %d MB, which no real one is", what, limit>>20)
		case usenet && !IsNZB(body):
			// Usually an error page, or a Newznab error such as "request limit
			// reached".
			return Fetched{}, fmt.Errorf("the indexer sent something other than an .nzb file (%s): %s", host(link), nzbRefusal(body))
		case usenet:
			return Fetched{NZB: body}, nil
		case len(body) == 0 || body[0] != 'd':
			// A torrent file is a bencoded dictionary; anything else is
			// usually a login or error page.
			return Fetched{}, fmt.Errorf("the indexer sent a web page instead of a .torrent file (%s); its login may have expired", host(link))
		}
		return Fetched{Torrent: body}, nil
	}
	return Fetched{}, fmt.Errorf("the download link redirected too many times")
}

// IsNZB reports whether data looks like an .nzb file: XML whose root
// element (after any declaration, doctype or comments) is <nzb>.
func IsNZB(data []byte) bool {
	head := bytes.ToLower(data[:min(len(data), 4096)])
	i := bytes.Index(head, []byte("<nzb"))
	if i < 0 || i+4 >= len(head) {
		return false
	}
	if c := head[i+4]; c != '>' && c != ' ' && c != '\t' && c != '\r' && c != '\n' {
		return false // e.g. <nzbsomething>
	}
	// Nothing but the prolog may come before it.
	for _, tag := range bytes.SplitAfter(head[:i], []byte(">")) {
		t := bytes.TrimSpace(tag)
		if len(t) > 0 && !bytes.HasPrefix(t, []byte("<?")) && !bytes.HasPrefix(t, []byte("<!")) {
			return false
		}
	}
	return true
}

// nzbRefusal is what an indexer said instead of sending an .nzb, short
// enough for a message: a Newznab error's description, or the start of the
// text.
func nzbRefusal(body []byte) string {
	var e struct {
		Code        string `xml:"code,attr"`
		Description string `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &e) == nil && e.Description != "" {
		return e.Description
	}
	text := strings.Join(strings.Fields(string(body[:min(len(body), 200)])), " ")
	if text == "" {
		return "an empty answer"
	}
	return text
}

func host(link string) string {
	if u, err := url.Parse(link); err == nil {
		return u.Host
	}
	return "unknown site"
}

// overGrabLimit says whether an indexer has used its daily grabs.
func (s *Service) overGrabLimit(ctx context.Context, ix *Indexer) bool {
	if ix.GrabLimit == nil {
		return false
	}
	u := new(Usage)
	err := s.db.DB.NewSelect().Model(u).Where("indexer_id = ? AND day = ?", ix.ID, s.day(s.now())).Scan(ctx)
	return err == nil && u.Grabs >= *ix.GrabLimit
}
