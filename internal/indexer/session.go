package indexer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/saltglass/reelkeeper/internal/indexer/solver"
)

// A browser's user agent: several trackers refuse anything else. A
// definition can set its own in its headers.
const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0"

// session is one indexer's connection: its cookies, the pause between
// requests its definition asks for, and the solver if it uses one. It is
// the core.HTTP the indexer kinds use.
type session struct {
	jar    *cookiejar.Jar
	client *http.Client
	ua     string
	delay  time.Duration
	solver *solver.Client // nil: requests go straight out

	mu   sync.Mutex
	next time.Time // earliest start of the next request

	solving sync.Mutex // held during a solver request
}

func newSession(delay time.Duration, ua string, sol *solver.Client) *session {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &session{
		jar: jar, ua: ua, delay: delay, solver: sol,
		client: &http.Client{Jar: jar, Timeout: 60 * time.Second},
	}
}

// wait spaces requests out, so a tracker is never hammered.
func (s *session) wait(req *http.Request) error {
	s.mu.Lock()
	now := time.Now()
	start := s.next
	if start.Before(now) {
		start = now
	}
	s.next = start.Add(s.delay)
	s.mu.Unlock()
	if d := time.Until(start); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-req.Context().Done():
			return req.Context().Err()
		case <-t.C:
		}
	}
	return nil
}

// Do sends a request, through the solver if the indexer uses one.
func (s *session) Do(req *http.Request, follow bool) (*http.Response, error) {
	if err := s.wait(req); err != nil {
		return nil, err
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", s.ua)
	}
	if s.solver != nil {
		// One solver request at a time per indexer: each one ties up a
		// browser in the solver, and RSS sync and a search running together
		// exhausted TRAWL's browsers (seen 2026-10-09).
		s.solving.Lock()
		resp, cookies, err := s.solver.Do(req, s.jar.Cookies(req.URL))
		s.solving.Unlock()
		if err != nil {
			// The solver may repeat the address (and any key in it).
			return nil, errors.New(strings.ReplaceAll(err.Error(), req.URL.String(), req.URL.Host))
		}
		s.jar.SetCookies(resp.Request.URL, cookies)
		return resp, nil
	}
	c := *s.client
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := c.Do(req) //nolint:gosec // indexer addresses are what the user configured
	return resp, redact(err)
}

// redact removes the address from connection errors: Go puts the full URL
// in them, and indexer URLs can carry API keys and passkeys. The site's
// host name is enough to say what failed, and errors end up in logs and on
// the indexer's page.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ""
		if u, perr := url.Parse(ue.URL); perr == nil {
			host = u.Host
		}
		return fmt.Errorf("can't reach %s: %w", host, ue.Err)
	}
	return err
}

func (s *session) SetCookies(u *url.URL, c []*http.Cookie) { s.jar.SetCookies(u, c) }
func (s *session) Cookies(u *url.URL) []*http.Cookie       { return s.jar.Cookies(u) }
