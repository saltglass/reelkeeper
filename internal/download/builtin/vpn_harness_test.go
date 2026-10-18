package builtin

// What the VPN tests share. The "VPN server" itself is in vpntest (a second
// wireguard-go device with its own network stack, on 127.0.0.1). On its network
// at 10.66.0.1 the tests put a stub tracker, a seeder, a DNS server that knows
// one name, and a stub of the page that says which address a visitor has.
// Nothing here reaches beyond loopback, and none of the keys are real.

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin/vpntest"
)

const (
	// tunnelTrackerName is a name only the tunnel's DNS server knows.
	tunnelTrackerName = "tracker.vpn-test.invalid"
	tunnelTrackerPort = 6969
	tunnelSeederPort  = 51413
	// tunnelExitIP is the address the stub "what is my IP" page reports: what
	// peers would see for the VPN.
	tunnelExitIP = "203.0.113.7"
)

// vpnRig is a VPN client wired to a test server, with a clock the test can
// move and a guard on WireGuard's socket.
type vpnRig struct {
	vpn   *VPNNetwork
	guard *vpntest.GuardBind
	skew  *atomic.Int64
	cfg   *WireGuardConfig
}

// jump moves the VPN's clock forward, which is how a test makes a handshake
// old without waiting minutes for it.
func (r *vpnRig) jump(d time.Duration) { r.skew.Add(int64(d)) }

func newVPNRig(t testing.TB, text string, tune func(*VPNOptions)) *vpnRig {
	t.Helper()
	cfg, err := ParseWireGuard(text)
	if err != nil {
		t.Fatal(err)
	}
	r := &vpnRig{guard: vpntest.NewGuardBind(), skew: &atomic.Int64{}, cfg: cfg}
	opts := VPNOptions{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Bind: r.guard,
		Poll: 100 * time.Millisecond, NudgeEvery: time.Second,
		Now: func() time.Time { return time.Now().Add(time.Duration(r.skew.Load())) },
	}
	if tune != nil {
		tune(&opts)
	}
	if r.vpn, err = NewVPN(cfg, opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.vpn.Close() })
	return r
}

// waitState waits for the tunnel to be in a state.
func waitState(t testing.TB, v *VPNNetwork, want VPNState, within time.Duration) VPNStatus {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if st := v.Status(); st.State == want {
			return st
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the VPN never got to %s; it is %s", want, v.Status().State)
	return VPNStatus{}
}

// tunnelWorld is the usual server: tracker, seeder, DNS and trace page.
type tunnelWorld struct {
	server  *vpntest.Server
	tracker *stubTracker
	seeder  *seeder
	client  vpntest.Keys
}

// newTunnelWorld builds a server whose network has a tracker that knows the
// seeder, the seeder itself (holding cs), a DNS server for the tracker's name,
// and the trace page. The server is started.
func newTunnelWorld(t *testing.T, cs ...*content) *tunnelWorld {
	t.Helper()
	w := &tunnelWorld{tracker: startTracker(t), client: vpntest.NewKeys(t)}
	for _, c := range cs {
		w.tracker.set(c.hash, "", fmt.Sprintf("%s:%d", vpntest.ServerIP, tunnelSeederPort))
	}
	trace, _ := vpntest.TraceHandler(tunnelExitIP, "NL")
	w.server = vpntest.New(t, w.client, func(s *vpntest.Server) {
		s.ServeDNS(map[string]string{tunnelTrackerName: vpntest.ServerIP})
		s.Serve(tunnelTrackerPort, w.tracker.handler)
		s.Serve(80, trace)
		w.seeder = startTunnelSeeder(t, s, cs...)
	})
	w.server.Start()
	return w
}

// config is a wg-quick file for this world's client.
func (w *tunnelWorld) config() string { return w.server.ConfigText(w.client, "") }

// announceURL is the tracker's address as a torrent would carry it: a name
// only the tunnel's DNS can resolve.
func announceURL() string {
	return fmt.Sprintf("http://%s:%d/announce", tunnelTrackerName, tunnelTrackerPort)
}

// startTunnelSeeder is a seeder (an ordinary anacrolix client) whose only
// socket is a listener on the server's network.
func startTunnelSeeder(t *testing.T, s *vpntest.Server, cs ...*content) *seeder {
	t.Helper()
	cfg := newSeederConfig(false)
	cfg.DisableTCP, cfg.DisableUTP = true, true
	sd := newSeederFrom(t, cfg)
	l, err := s.Net().ListenTCPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(vpntest.ServerIP), tunnelSeederPort))
	if err != nil {
		t.Fatal(err)
	}
	sd.cl.AddListener(l)
	s.Own(closerFunc(func() error { sd.cl.Close(); return nil }))
	for _, c := range cs {
		sd.add(t, c)
	}
	return sd
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// engineOverVPN opens an engine whose only network is the tunnel.
func engineOverVPN(t *testing.T, rig *vpnRig, o engineOpts) (*Engine, Config) {
	t.Helper()
	prev := o.cfg
	o.cfg = func(c *Config) {
		c.ListenHost, c.DHT, c.PEX = "", false, false
		if prev != nil {
			prev(c)
		}
	}
	return openEngine(t, rig.vpn, o)
}

// eventuallyWithin polls cond for as long as asked.
func eventuallyWithin(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(30 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("gave up waiting for %s", what)
}
