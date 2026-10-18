package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
)

// The "what address do peers see" check, for the built-in client's status card.
//
// It asks Cloudflare's trace page (a plain text page that says which address
// the visit came from), twice: once over the computer's own network, which says
// what the real address is, and once through the VPN, which says what peers
// would see. If those are the same, the VPN isn't hiding anything and the
// client stops (the status endpoint tells the VPN, and the engine's gate shuts).
//
// These lookups are not the engine's traffic. They happen only while somebody
// is looking at the status (the page is open), results are kept for five
// minutes, and "Check again" can't hammer Cloudflare (once in 30 seconds).

// TraceURL is the page asked. Tests replace it.
var TraceURL = "https://1.1.1.1/cdn-cgi/trace"

const (
	// ipCacheFor is how long an answer (or a failure) is kept.
	ipCacheFor = 5 * time.Minute
	// ipRecheckLeak is how soon a leak is looked at again: it keeps downloads
	// stopped, so waiting five minutes to find out it is fixed is too long.
	ipRecheckLeak = 30 * time.Second
	// ipForceEvery is the least time between two "Check again"s that do
	// anything.
	ipForceEvery = 30 * time.Second
	// ipTimeout bounds one lookup.
	ipTimeout = 8 * time.Second
	// maxTraceBytes is far more than the page has.
	maxTraceBytes = 16 << 10
)

// IPInfo is an address the world sees, and the country it is in.
type IPInfo struct {
	IP      string
	Country string
}

type ipResult struct {
	info IPInfo
	err  string
	at   time.Time
	// gen is the VPN connection the answer is about.
	gen uint64
	ok  bool
}

// ipState remembers the two lookups. It belongs to one run of the engine.
type ipState struct {
	// now is the clock (tests move it); nil means the real one.
	now       func() time.Time
	mu        sync.Mutex
	direct    ipResult
	tunnel    ipResult
	lastForce time.Time
}

// dialer is how a lookup connects: the computer's network, or the VPN.
type dialer = func(ctx context.Context, network, addr string) (net.Conn, error)

// ipLookups is what a lookup needs.
type ipLookups struct {
	network builtin.Network
	vpn     *builtin.VPNNetwork
	// direct and tunnel are the pages asked ("" means TraceURL).
	direct, tunnel string
}

// look does the lookups that are due. It does nothing at all when both
// answers are fresh, which is what keeps a page that polls every few seconds
// from reaching the internet more than every few minutes.
func (s *ipState) look(ctx context.Context, refresh bool, l ipLookups) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	force := refresh && now.Sub(s.lastForce) >= ipForceEvery
	if force {
		s.lastForce = now
	}
	if force || s.direct.at.IsZero() || now.Sub(s.direct.at) >= ipCacheFor {
		s.direct = lookupOne(ctx, l.direct, l.network.Dial, 0, now)
	}
	if l.vpn == nil {
		return
	}
	vs := l.vpn.Status()
	if !vs.TunnelConnected {
		return // only through a connected tunnel
	}
	ttl := ipCacheFor
	if s.leakingLocked() {
		ttl = ipRecheckLeak
	}
	if force || s.tunnel.at.IsZero() || s.tunnel.gen != vs.Generation || now.Sub(s.tunnel.at) >= ttl {
		s.tunnel = lookupOne(ctx, l.tunnel, l.vpn.Probe, vs.Generation, now)
	}
}

func (s *ipState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *ipState) leakingLocked() bool {
	return s.direct.ok && s.tunnel.ok && s.direct.info.IP == s.tunnel.info.IP
}

// leak says whether the VPN shows the same address as the computer, once both
// are known.
func (s *ipState) leak() (leaks, decided bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.direct.ok || !s.tunnel.ok {
		return false, false
	}
	return s.leakingLocked(), true
}

// fill puts what is known into the status.
func (s *ipState) fill(st *BuiltinStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	put := func(r ipResult, info **IPInfo, errText *string) {
		if r.at.IsZero() {
			return
		}
		if r.ok {
			i := r.info
			*info = &i
		} else {
			*errText = r.err
		}
		if r.at.After(st.CheckedAt) {
			st.CheckedAt = r.at
		}
	}
	put(s.direct, &st.PublicIP, &st.PublicIPError)
	put(s.tunnel, &st.VPNIP, &st.VPNIPError)
}

// lookupOne asks one page, over the given dialer.
func lookupOne(ctx context.Context, url string, dial dialer, gen uint64, at time.Time) ipResult {
	if url == "" {
		url = TraceURL
	}
	r := ipResult{at: at, gen: gen}
	ctx, cancel := context.WithTimeout(ctx, ipTimeout)
	defer cancel()
	info, err := fetchTrace(ctx, url, dial)
	if err != nil {
		r.err = capitalizeFirst(err.Error()) + "."
		return r
	}
	r.info, r.ok = info, true
	return r
}

// fetchTrace gets the trace page with its own HTTP client, which dials only
// with the function it is given, takes no proxy from the environment and
// follows no redirect.
func fetchTrace(ctx context.Context, url string, dial dialer) (IPInfo, error) {
	client := &http.Client{
		Timeout: ipTimeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dial,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return IPInfo{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return IPInfo{}, fmt.Errorf("couldn't ask for the address: %s", simplifyErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IPInfo{}, fmt.Errorf("the address page answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTraceBytes))
	if err != nil {
		return IPInfo{}, err
	}
	return parseTrace(string(body))
}

// parseTrace reads the page: lines of key=value, of which ip and loc matter.
func parseTrace(body string) (IPInfo, error) {
	var info IPInfo
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			a, err := netip.ParseAddr(v)
			if err != nil {
				return IPInfo{}, errors.New("the address page didn't give an address")
			}
			info.IP = a.Unmap().String()
		case "loc":
			if len(v) == 2 {
				info.Country = strings.ToUpper(v)
			}
		}
	}
	if info.IP == "" {
		return IPInfo{}, errors.New("the address page didn't give an address")
	}
	return info, nil
}

// simplifyErr trims a network error to its cause.
func simplifyErr(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}
		err = next
	}
}
