package download

// The built-in client's VPN as the host runs it. The VPN server is a
// WireGuard device inside the test process (builtin/vpntest), reached over
// 127.0.0.1; "the computer's own network" is testNet, which refuses anything
// that isn't loopback and counts what it is asked. Nothing here reaches the
// internet, and the keys are made up.

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
	"github.com/saltglass/reelkeeper/internal/download/builtin/vpntest"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

func init() {
	// A test that forgets to point the IP check at a stub still can't reach
	// Cloudflare: nothing listens on port 1.
	TraceURL = "http://127.0.0.1:1/cdn-cgi/trace"
}

const (
	realIP = "198.51.100.9" // what the computer's own network shows (documentation range)
	vpnIP  = "203.0.113.7"  // what the VPN shows
)

// vpnWorld is a VPN server with a trace page, a tracker that answers every
// announce with no peers, and a stub of the trace page for the computer's own
// network.
type vpnWorld struct {
	t       *testing.T
	server  *vpntest.Server
	keys    vpntest.Keys
	setExit func(string)
	// announces counts the announces the tracker in the tunnel received.
	announces atomic.Int64

	realMu  sync.Mutex
	realIP  string
	direct  *httptest.Server
	directN atomic.Int64
	clock   *fakeClock // for the address lookups
	vpnTime *fakeClock // for the VPN
	guard   *vpntest.GuardBind
}

// fakeClock is the real time plus whatever a test has moved it forward by.
type fakeClock struct {
	skew atomic.Int64
}

func (c *fakeClock) Now() time.Time { return time.Now().Add(time.Duration(c.skew.Load())) }

func (c *fakeClock) add(d time.Duration) { c.skew.Add(int64(d)) }

func newVPNWorld(t *testing.T) *vpnWorld {
	t.Helper()
	w := &vpnWorld{t: t, keys: vpntest.NewKeys(t), realIP: realIP, guard: vpntest.NewGuardBind(), clock: &fakeClock{}, vpnTime: &fakeClock{}}
	trace, set := vpntest.TraceHandler(vpnIP, "NL")
	w.setExit = set
	w.server = vpntest.New(t, w.keys, func(s *vpntest.Server) {
		s.ServeDNS(map[string]string{"tracker.vpn-test.invalid": vpntest.ServerIP})
		s.Serve(80, trace)
		s.Serve(6969, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			w.announces.Add(1)
			_, _ = rw.Write([]byte("d8:completei0e10:incompletei0e8:intervali1e5:peers0:e"))
		}))
	})
	w.server.Start()
	w.direct = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.directN.Add(1)
		w.realMu.Lock()
		ip := w.realIP
		w.realMu.Unlock()
		_, _ = fmt.Fprintf(rw, "fl=1\nip=%s\nloc=US\n", ip)
	}))
	t.Cleanup(w.direct.Close)
	return w
}

func (w *vpnWorld) setRealIP(ip string) {
	w.realMu.Lock()
	w.realIP = ip
	w.realMu.Unlock()
}

// config is a wg-quick file for the host's VPN.
func (w *vpnWorld) config() string { return w.server.ConfigText(w.keys, "") }

// host makes a built-in host whose VPN talks to this world.
func (w *vpnWorld) host(t *testing.T, data string, nw *testNet) *BuiltinHost {
	t.Helper()
	h := newHost(t, data, nw)
	h.tuneVPN = func(o *builtin.VPNOptions) {
		o.Bind = w.guard
		o.Poll, o.NudgeEvery = 100*time.Millisecond, time.Second
		o.Logger = slog.New(slog.DiscardHandler)
		o.Now = w.vpnTime.Now
	}
	h.traceDirect = w.direct.URL + "/cdn-cgi/trace"
	h.traceTunnel = "http://" + vpntest.ServerIP + "/cdn-cgi/trace"
	h.ipClock = w.clock.Now
	h.notifyAfter = 400 * time.Millisecond
	return h
}

func vpnSettings(folder, text string) settings.DownloadClient {
	return settings.DownloadClient{
		Type: settings.ClientBuiltin, Folder: folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25, VPNConfig: text,
	}
}

func waitHostState(t *testing.T, h *BuiltinHost, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if h.BuiltinState() == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the built-in client never got to %q; it is %q", want, h.BuiltinState())
}

func status(t *testing.T, h *BuiltinHost, refresh bool) BuiltinStatus {
	t.Helper()
	st, err := h.Status(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestHostRunsTheEngineBehindTheVPNAndShowsBothAddresses(t *testing.T) {
	w := newVPNWorld(t)
	nw := &testNet{}
	h := w.host(t, t.TempDir(), nw)
	h.Apply(vpnSettings(t.TempDir(), w.config()), settings.DownloadMove{})
	if e, _, err := h.running(); e == nil || err != nil {
		t.Fatalf("no engine: %v", err)
	}
	waitHostState(t, h, BuiltinConnected, 15*time.Second)

	// Nothing has looked at the addresses yet: the engine and the VPN's own
	// polling never contact the page.
	if n := w.directN.Load(); n != 0 {
		t.Errorf("the page was asked %d times before anybody looked", n)
	}
	st := status(t, h, false)
	if st.State != BuiltinConnected || st.Leak || st.Endpoint == "" || len(st.TunnelAddresses) != 1 || st.TunnelAddresses[0] != vpntest.ClientIP+"/32" ||
		len(st.DNS) != 1 || st.DNS[0] != vpntest.ServerIP || st.LastHandshake.IsZero() || st.Since.IsZero() {
		t.Errorf("status = %+v", st)
	}
	if st.PublicIP == nil || st.PublicIP.IP != realIP || st.PublicIP.Country != "US" || st.VPNIP == nil || st.VPNIP.IP != vpnIP || st.VPNIP.Country != "NL" ||
		st.CheckedAt.IsZero() || st.PublicIPError != "" || st.VPNIPError != "" {
		t.Errorf("addresses = %+v / %+v (%q, %q)", st.PublicIP, st.VPNIP, st.PublicIPError, st.VPNIPError)
	}
	// A page that polls every few seconds doesn't make a new lookup each time,
	// and "Check again" can't be used more than once in 30 seconds.
	for i := 0; i < 5; i++ {
		status(t, h, false)
	}
	if n := w.directN.Load(); n != 1 {
		t.Errorf("%d direct lookups for 6 status calls", n)
	}
	status(t, h, true)
	status(t, h, true)
	if n := w.directN.Load(); n != 2 {
		t.Errorf("a first and a second quick refresh made %d lookups in all, want 2 (the first one counts, the second is too soon)", n)
	}
	w.clock.add(31 * time.Second)
	status(t, h, true)
	if n := w.directN.Load(); n != 3 {
		t.Errorf("a refresh after 30 seconds made %d lookups in all, want 3", n)
	}
	w.clock.add(6 * time.Minute)
	status(t, h, false)
	if n := w.directN.Load(); n != 4 {
		t.Errorf("after 5 minutes the answer should be asked again: %d", n)
	}

	// The engine resolved the tracker's name through the tunnel's DNS, and the
	// computer's own network was never used for the engine's traffic.
	c := &Builtin{cfg: vpnSettings("", w.config()), host: h}
	hash := hashOf(4242)
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Through&tr=" + "http%3A%2F%2Ftracker.vpn-test.invalid%3A6969%2Fannounce"
	if _, err := c.Add(context.Background(), Add{Magnet: magnet, Category: settings.MovieCategory}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for w.announces.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if w.announces.Load() == 0 {
		t.Error("the tracker, reached by name through the tunnel, was never announced to")
	}
	if !contains(w.server.DNSQueries(), "tracker.vpn-test.invalid") {
		t.Errorf("the tunnel's DNS was asked %v", w.server.DNSQueries())
	}
	// Only the page lookups and nothing of the engine's reached testNet: no
	// listening, no UDP socket, no name lookup, and dials only to the stub page.
	if nw.listens.Load() != 0 || nw.packets.Load() != 0 || nw.look.Load() != 0 || nw.dials.Load() != w.directN.Load() {
		t.Errorf("the computer's network was used: %d listens, %d UDP sockets, %d lookups, %d dials (%d page lookups)",
			nw.listens.Load(), nw.packets.Load(), nw.look.Load(), nw.dials.Load(), w.directN.Load())
	}
	if w.guard.Opens() != 1 || w.guard.Violations() != 0 {
		t.Errorf("WireGuard's socket: %d opens, %d sends beyond loopback", w.guard.Opens(), w.guard.Violations())
	}
	if ts, err := c.Torrents(context.Background(), settings.MovieCategory); err != nil || len(ts) != 1 || ts[0].State != torrent.Metadata {
		t.Errorf("torrents = %+v %v", ts, err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// If the VPN shows the same address as the computer, it isn't hiding anything:
// that counts as down.
func TestHostTreatsAVPNThatShowsTheRealAddressAsDown(t *testing.T) {
	w := newVPNWorld(t)
	w.setRealIP(vpnIP) // the "VPN" and the computer look the same to the page
	h := w.host(t, t.TempDir(), &testNet{})
	h.Apply(vpnSettings(t.TempDir(), w.config()), settings.DownloadMove{})
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	c := &Builtin{cfg: vpnSettings("", w.config()), host: h}
	if _, err := c.Add(context.Background(), Add{Magnet: magnetOf(hashOf(9)), Category: settings.MovieCategory}); err != nil {
		t.Fatal(err)
	}
	e, _, _ := h.running()
	running := func() int { return len(runningTorrents(t, c)) }
	if running() != 1 {
		t.Fatalf("the magnet link isn't running: %d", running())
	}
	_ = e

	st := status(t, h, false)
	if st.State != BuiltinDown || !st.Leak || !strings.Contains(st.Message, "Peers would see your real IP: the VPN isn't hiding it") {
		t.Fatalf("status = %+v", st)
	}
	if h.BuiltinState() != BuiltinDown {
		t.Errorf("state = %s", h.BuiltinState())
	}
	deadline := time.Now().Add(5 * time.Second)
	for running() != 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if running() != 0 {
		t.Error("torrents kept running while the VPN showed the real address")
	}
	// It stays that way on the next look (the tunnel is still checked, now
	// every 30 seconds), and is released when the addresses differ.
	if st := status(t, h, false); st.State != BuiltinDown || !st.Leak {
		t.Errorf("a second look: %+v", st)
	}
	w.setRealIP(realIP)
	w.clock.add(31 * time.Second)
	st = status(t, h, true)
	if st.State != BuiltinConnected || st.Leak || st.Message != "" {
		t.Fatalf("after the leak was fixed: %+v", st)
	}
	deadline = time.Now().Add(5 * time.Second)
	for running() != 1 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if running() != 1 {
		t.Error("the torrent didn't start again after the leak was fixed")
	}
}

// runningTorrents are the built-in client's torrents that are using the network
// (a magnet link with no details reads as metadata while it runs, and as queued
// while it is held).
func runningTorrents(t *testing.T, c *Builtin) []Torrent {
	t.Helper()
	ts, err := c.Torrents(context.Background(), settings.MovieCategory)
	if err != nil {
		t.Fatal(err)
	}
	var out []Torrent
	for _, x := range ts {
		if x.State == torrent.Metadata || x.State == torrent.Downloading || x.State == torrent.Stalled {
			out = append(out, x)
		}
	}
	return out
}

func TestHostHoldsTheClientWhenTheVPNGoesAway(t *testing.T) {
	w := newVPNWorld(t)
	h := w.host(t, t.TempDir(), &testNet{})
	h.Apply(vpnSettings(t.TempDir(), w.config()), settings.DownloadMove{})
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	c := &Builtin{cfg: vpnSettings("", w.config()), host: h}
	if _, err := c.Add(context.Background(), Add{Magnet: magnetOf(hashOf(10)), Category: settings.MovieCategory}); err != nil {
		t.Fatal(err)
	}
	w.server.Stop()
	w.vpnTime.add(200 * time.Second)
	waitHostState(t, h, BuiltinDown, 10*time.Second)
	st := status(t, h, false)
	if st.State != BuiltinDown || !strings.Contains(st.Message, "isn't connected") {
		t.Errorf("status = %+v", st)
	}
	if n := len(runningTorrents(t, c)); n != 0 {
		t.Errorf("%d torrents run with the VPN down", n)
	}
	// While down, no lookup through the tunnel is attempted: the real address
	// only (it is the computer's own network, not the engine's).
	if st.VPNIP != nil {
		t.Errorf("a VPN address while down: %+v", st.VPNIP)
	}
	w.server.Start()
	waitHostState(t, h, BuiltinConnected, 30*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for len(runningTorrents(t, c)) != 1 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if n := len(runningTorrents(t, c)); n != 1 {
		t.Errorf("%d torrents run after the VPN came back", n)
	}
}

// Removing the VPN holds the downloads until the person says to go on without
// it, and the hold survives the server restarting.
func TestHostHoldsDownloadsAfterTheVPNIsRemovedUntilToldOtherwise(t *testing.T) {
	w := newVPNWorld(t)
	data, folder := t.TempDir(), t.TempDir()
	h := w.host(t, data, &testNet{})
	withVPN := vpnSettings(folder, w.config())
	h.Apply(withVPN, settings.DownloadMove{})
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	c := &Builtin{cfg: withVPN, host: h}
	hash := hashOf(11)
	if _, err := c.Add(context.Background(), Add{Magnet: magnetOf(hash), Category: settings.MovieCategory}); err != nil {
		t.Fatal(err)
	}
	if len(runningTorrents(t, c)) != 1 {
		t.Fatal("not running behind the VPN")
	}

	// What the settings say right after "Remove" is confirmed.
	removed := settings.DownloadClient{Type: settings.ClientBuiltin, Folder: folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25, VPNHeld: true}
	h.Apply(removed, settings.DownloadMove{})
	if got := h.BuiltinState(); got != BuiltinHeld {
		t.Fatalf("state after removing the VPN = %q", got)
	}
	c2 := &Builtin{cfg: removed, host: h}
	time.Sleep(300 * time.Millisecond)
	if n := len(runningTorrents(t, c2)); n != 0 {
		t.Errorf("%d torrents run while held", n)
	}
	if ts, _ := c2.Torrents(context.Background(), settings.MovieCategory); len(ts) != 1 || ts[0].State != torrent.Queued {
		t.Errorf("held torrents = %+v", ts)
	}
	st := status(t, h, false)
	if st.State != BuiltinHeld || !strings.Contains(st.Message, "Downloads are stopped until you start them without the VPN") || st.PublicIP == nil {
		t.Errorf("held status = %+v", st)
	}
	// A server restart doesn't release it.
	h.Close()
	h2 := w.host(t, data, &testNet{})
	h2.Apply(removed, settings.DownloadMove{})
	time.Sleep(300 * time.Millisecond)
	if got := h2.BuiltinState(); got != BuiltinHeld {
		t.Fatalf("state after a restart = %q", got)
	}
	c3 := &Builtin{cfg: removed, host: h2}
	if n := len(runningTorrents(t, c3)); n != 0 {
		t.Errorf("%d torrents run after a restart while held", n)
	}
	// "Start without VPN".
	released := removed
	released.VPNHeld = false
	h2.Apply(released, settings.DownloadMove{})
	if got := h2.BuiltinState(); got != BuiltinUnprotected {
		t.Fatalf("state after starting without the VPN = %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(runningTorrents(t, c3)) != 1 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if n := len(runningTorrents(t, c3)); n != 1 {
		t.Errorf("%d torrents run after starting without the VPN", n)
	}
	if st := status(t, h2, false); st.State != BuiltinUnprotected || st.PublicIP == nil || st.PublicIP.IP != realIP || st.VPNIP != nil || st.Endpoint != "" {
		t.Errorf("unprotected status = %+v", st)
	}
}

// A VPN that is set up but can't be used must never mean "download unprotected".
func TestHostNeverFallsBackToTheComputersNetwork(t *testing.T) {
	cases := map[string]settings.DownloadClient{
		"a config that can't be read": {VPNUnreadable: true},
		"a config that can't be used": {VPNConfig: "[Interface]\nnonsense"},
	}
	for name, vpn := range cases {
		t.Run(name, func(t *testing.T) {
			nw := &testNet{}
			h := newHost(t, t.TempDir(), nw)
			cfg := vpnSettings(t.TempDir(), "")
			cfg.VPNConfig, cfg.VPNUnreadable = vpn.VPNConfig, vpn.VPNUnreadable
			h.Apply(cfg, settings.DownloadMove{})
			if got := h.BuiltinState(); got != BuiltinHeld {
				t.Fatalf("state = %q", got)
			}
			c := &Builtin{cfg: cfg, host: h}
			if _, err := c.Add(context.Background(), Add{Magnet: magnetOf(hashOf(12)), Category: settings.MovieCategory}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(400 * time.Millisecond)
			if n := len(runningTorrents(t, c)); n != 0 {
				t.Errorf("%d torrents run", n)
			}
			if nw.listens.Load() != 0 || nw.packets.Load() != 0 || nw.dials.Load() != 0 || nw.look.Load() != 0 {
				t.Errorf("the computer's network was used: %d listens, %d UDP, %d dials, %d lookups", nw.listens.Load(), nw.packets.Load(), nw.dials.Load(), nw.look.Load())
			}
			st, err := h.Status(context.Background(), false)
			if err != nil || st.State != BuiltinHeld || st.Message == "" {
				t.Errorf("status = %+v, %v", st, err)
			}
			// Held for good: the settings never say otherwise until the person fixes the VPN.
			h.Apply(cfg, settings.DownloadMove{})
			if got := h.BuiltinState(); got != BuiltinHeld {
				t.Errorf("state after applying again = %q", got)
			}
		})
	}
}

// Replacing the config restarts the engine in VPN mode.
func TestHostRestartsForANewVPNConfig(t *testing.T) {
	w := newVPNWorld(t)
	h := w.host(t, t.TempDir(), &testNet{})
	cfg := vpnSettings(t.TempDir(), w.config())
	h.Apply(cfg, settings.DownloadMove{})
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	first, _, _ := h.running()

	// The same settings, new limits: the same engine.
	same := cfg
	same.DownloadLimitKBs = 100
	h.Apply(same, settings.DownloadMove{})
	if e, _, _ := h.running(); e != first {
		t.Error("new limits restarted the engine")
	}
	// A new forwarded port or config: a new engine, connecting first.
	other := cfg
	other.ForwardedPort = 51999
	h.Apply(other, settings.DownloadMove{})
	e2, _, _ := h.running()
	if e2 == nil || e2 == first {
		t.Fatal("a new forwarded port didn't restart the engine")
	}
	if st := status(t, h, false); st.ForwardedPort != 51999 {
		t.Errorf("forwarded port = %d", st.ForwardedPort)
	}
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	// Changing to a config for a server that isn't there: connecting, nothing runs.
	h.Apply(vpnSettings(cfg.Folder, w.server.ConfigText(w.keys, "127.0.0.1:"+fmt.Sprint(vpntest.FreeUDPPort(t)))), settings.DownloadMove{})
	if got := h.BuiltinState(); got != BuiltinConnecting {
		t.Errorf("state with an unreachable server = %q", got)
	}
	if st := status(t, h, false); st.State != BuiltinConnecting || st.VPNIP != nil {
		t.Errorf("connecting status = %+v", st)
	}
}

func TestHostTellsPeopleOnceWhenTheVPNStaysDownAndOnceWhenItIsBack(t *testing.T) {
	w := newVPNWorld(t)
	h := w.host(t, t.TempDir(), &testNet{})
	// Long enough that a slow first handshake (a busy machine) isn't "down for a
	// while"; short enough to wait for.
	h.notifyAfter = 2 * time.Second
	var mu sync.Mutex
	var events []notify.Event
	h.SetNotifier(func(ev notify.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	got := func() []notify.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]notify.Event(nil), events...)
	}
	h.Apply(vpnSettings(t.TempDir(), w.config()), settings.DownloadMove{})
	waitHostState(t, h, BuiltinConnected, 15*time.Second)
	time.Sleep(700 * time.Millisecond)
	if n := len(got()); n != 0 {
		t.Fatalf("%d notices while the VPN was up", n)
	}
	w.server.Stop()
	w.vpnTime.add(200 * time.Second)
	waitHostState(t, h, BuiltinDown, 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for len(got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(2500 * time.Millisecond) // and not again
	evs := got()
	if len(evs) != 1 || evs[0].Kind != notify.KindClientUnreachable || evs[0].Message != "The built-in client's VPN is down; its downloads are stopped." || !notify.IsProblem(evs[0].Kind) {
		t.Fatalf("notices = %+v", evs)
	}
	w.server.Start()
	waitHostState(t, h, BuiltinConnected, 30*time.Second)
	deadline = time.Now().Add(5 * time.Second)
	for len(got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	evs = got()
	if len(evs) != 2 || evs[1].Kind != notify.KindClientRestored || !strings.Contains(evs[1].Message, "back") || notify.IsProblem(evs[1].Kind) {
		t.Fatalf("notices = %+v", evs)
	}
	// A short blip tells nobody.
	h.mu.Lock()
	vpn := h.vpn
	h.mu.Unlock()
	vpn.SetLeak(true)
	time.Sleep(100 * time.Millisecond)
	vpn.SetLeak(false)
	time.Sleep(900 * time.Millisecond)
	if n := len(got()); n != 2 {
		t.Errorf("%d notices after a short blip", n)
	}
}

func TestHostStatusIsNotFoundWhenTheBuiltinClientIsNotInUse(t *testing.T) {
	h := newHost(t, t.TempDir(), &testNet{})
	if _, err := h.Status(context.Background(), false); !errors.Is(err, ErrNotBuiltin) {
		t.Errorf("status with no engine: %v", err)
	}
	if got := h.BuiltinState(); got != "" {
		t.Errorf("state with no engine: %q", got)
	}
}

func TestParseTrace(t *testing.T) {
	good, err := parseTrace("fl=1\nip=203.0.113.7\nloc=nl\nts=1\n")
	if err != nil || good != (IPInfo{IP: "203.0.113.7", Country: "NL"}) {
		t.Errorf("good = %+v %v", good, err)
	}
	if v6, err := parseTrace("ip=2001:db8::1\n"); err != nil || v6.IP != "2001:db8::1" || v6.Country != "" {
		t.Errorf("v6 = %+v %v", v6, err)
	}
	if mapped, err := parseTrace("ip=::ffff:203.0.113.7\n"); err != nil || mapped.IP != "203.0.113.7" {
		t.Errorf("mapped = %+v %v", mapped, err)
	}
	for _, bad := range []string{"", "loc=NL\n", "ip=not an address\n", "<html>Cloudflare error</html>"} {
		if _, err := parseTrace(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestIPLookupFailuresAreShownNotFatal(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusBadGateway) }))
	defer bad.Close()
	h := newHost(t, t.TempDir(), &testNet{})
	h.traceDirect = bad.URL
	h.Apply(settings.DownloadClient{Type: settings.ClientBuiltin, Folder: t.TempDir(), ListenPort: 6881, MaxActiveDownloads: 3}, settings.DownloadMove{})
	st := status(t, h, false)
	if st.State != BuiltinUnprotected || st.PublicIP != nil || !strings.Contains(st.PublicIPError, "502") || st.CheckedAt.IsZero() {
		t.Errorf("status = %+v", st)
	}
	// A refused address (the stub isn't on loopback) is an error too.
	h.traceDirect = "http://192.0.2.1/cdn-cgi/trace"
	h.mu.Lock()
	h.ownIPs = nil
	h.mu.Unlock()
	if st := status(t, h, false); st.PublicIP != nil || st.PublicIPError == "" {
		t.Errorf("status = %+v", st)
	}
}

// The VPN and IP-check code reach the network only through what they are given
// (the engine's Network, the VPN's Probe): nothing in them dials, listens or
// looks up a name on its own, and the HTTP client they make takes its dialer as
// a parameter and no proxy from the environment.
func TestVPNAndIPCheckCodeDoNotReachTheNetworkThemselves(t *testing.T) {
	for _, name := range []string{"builtin_vpn.go", "builtin_ip.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			bad := map[string][]string{
				"net":  {"Dial", "DialTimeout", "DialTCP", "DialUDP", "Listen", "ListenTCP", "ListenUDP", "ListenPacket", "LookupHost", "LookupIP", "DefaultResolver", "Dialer", "ListenConfig", "Resolver"},
				"http": {"Get", "Head", "Post", "PostForm", "DefaultClient", "DefaultTransport", "ProxyFromEnvironment", "ListenAndServe"},
			}
			for _, b := range bad[id.Name] {
				if sel.Sel.Name == b {
					t.Errorf("%s uses %s.%s directly", name, id.Name, b)
				}
			}
			return true
		})
		if name == "builtin_ip.go" && !strings.Contains(string(src), "Proxy:               nil") {
			t.Error("the IP check's HTTP client must take no proxy from the environment")
		}
	}
}

// Sentences that name the download client read naturally with the built-in
// client's name ("the built-in client"), mid-sentence and at the start.
func TestTheBuiltinClientsNameReadsNaturallyInSentences(t *testing.T) {
	b := &Builtin{}
	if b.Name() != "the built-in client" || settings.ClientDisplayName(settings.ClientBuiltin) != "the built-in client" {
		t.Fatalf("names: %q / %q", b.Name(), settings.ClientDisplayName(settings.ClientBuiltin))
	}
	for name, want := range map[string]string{"the built-in client": "The built-in client", "qBittorrent": "qBittorrent", "Transmission": "Transmission", "": ""} {
		if got := sentenceName(name); got != want {
			t.Errorf("sentenceName(%q) = %q, want %q", name, got, want)
		}
	}
}
