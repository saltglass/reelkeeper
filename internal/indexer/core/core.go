// Package core holds what every kind of indexer shares: the search query,
// the release it returns, and how it reaches the internet. The kinds are
// tracker definitions (package cardigann), Torznab/Newznab feeds and
// Knaben (package newznab and knaben); package indexer manages them.
package core

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Query is one search. Categories are standard (Newznab) IDs.
type Query struct {
	Type       string // "search", "tv-search" or "movie-search"
	Keywords   string // the title searched for, without year or episode
	Year       int
	Season     int    // 0 means none (TV only)
	Episode    string // "5", or for daily shows "03/15"
	IMDbID     string // "tt0111161"
	TMDBID     int
	TVDBID     int
	Categories []int
}

// Release is one release an indexer returned.
type Release struct {
	Title           string
	GUID            string
	DetailsURL      string
	DownloadURL     string // a .torrent link (some need DownloadLink to resolve)
	MagnetURL       string
	InfoHash        string // lower case
	Size            int64
	Files           int
	Grabs           int
	Seeders         int
	Leechers        int
	PublishDate     time.Time
	Categories      []int // standard IDs
	IMDbID          int
	TMDBID          int
	TVDBID          int
	Year            int
	PosterURL       string
	Description     string
	Genres          string
	DownloadFactor  float64 // 0 is freeleech, 1 normal
	UploadFactor    float64
	MinimumRatio    float64 // the tracker's own rule for this release, if it says
	MinimumSeedTime int64   // seconds
}

// HTTP is how an indexer reaches its site. The indexer layer supplies it:
// it keeps the session's cookies, waits between requests as the indexer
// asks, and can send everything through the Cloudflare solver.
type HTTP interface {
	// Do sends a request; follow says whether redirects are followed.
	Do(req *http.Request, follow bool) (*http.Response, error)
	SetCookies(u *url.URL, cookies []*http.Cookie)
	Cookies(u *url.URL) []*http.Cookie
}

// Client is one configured indexer, ready to use.
type Client interface {
	// Search runs a search. Results that can't be read are skipped; the
	// error is for a search that failed as a whole.
	Search(ctx context.Context, q Query) ([]Release, error)
	// Test checks the indexer works: it logs in if needed and runs an
	// empty search, which must find something.
	Test(ctx context.Context) error
	// DownloadLink returns where a release is downloaded from: a magnet
	// link or a .torrent URL (fetched with the same session).
	DownloadLink(ctx context.Context, r Release) (string, error)
}
