package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
)

// Fetched is what a release downloads as: a magnet link, or a .torrent
// file's contents.
type Fetched struct {
	Magnet  string
	Torrent []byte
}

// ErrGrabLimit means the indexer's daily grab limit is used up.
var ErrGrabLimit = errors.New("this indexer has reached its daily grab limit")

// maxTorrentSize bounds a .torrent download; real ones are kilobytes, a
// few megabytes for huge season packs.
const maxTorrentSize = 20 << 20

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
	f, err := fetch(ctx, c, rel.Release)
	s.record(ctx, ix, time.Since(start), err, usageGrab)
	if err != nil {
		return Fetched{}, fmt.Errorf("%s: %w", ix.Name, err)
	}
	return f, nil
}

func fetch(ctx context.Context, c core.Client, rel core.Release) (Fetched, error) {
	link, err := c.DownloadLink(ctx, rel)
	if err != nil {
		return Fetched{}, err
	}
	if strings.HasPrefix(link, "magnet:") {
		return Fetched{Magnet: link}, nil
	}
	sess, ok := c.(interface{ Session() core.HTTP })
	if !ok {
		return Fetched{}, fmt.Errorf("can't download .torrent files from this kind of indexer")
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
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentSize+1))
		_ = resp.Body.Close()
		if err != nil {
			return Fetched{}, err
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc := resp.Header.Get("Location")
			if strings.HasPrefix(loc, "magnet:") {
				return Fetched{Magnet: loc}, nil
			}
			next, err := req.URL.Parse(loc)
			if err != nil || loc == "" {
				return Fetched{}, fmt.Errorf("the download link redirected nowhere useful")
			}
			link = next.String()
			continue
		case resp.StatusCode != http.StatusOK:
			return Fetched{}, fmt.Errorf("downloading the .torrent file failed: %s", resp.Status)
		case len(body) > maxTorrentSize:
			return Fetched{}, fmt.Errorf("the .torrent file is larger than %d MB, which no real one is", maxTorrentSize>>20)
		case len(body) == 0 || body[0] != 'd':
			// A torrent file is a bencoded dictionary; anything else is
			// usually a login or error page.
			return Fetched{}, fmt.Errorf("the indexer sent a web page instead of a .torrent file (%s); its login may have expired", host(link))
		}
		return Fetched{Torrent: body}, nil
	}
	return Fetched{}, fmt.Errorf("the download link redirected too many times")
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
