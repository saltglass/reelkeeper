package builtin

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin/vpntest"
	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// The engine in VPN mode, against a WireGuard server that lives in the test
// process. See vpn_harness_test.go for what is on the other side.

func TestVPNEngineDownloadsThroughTheTunnel(t *testing.T) {
	c := makeContent(t, "Through.The.Tunnel.2021", contentOpts{announce: announceURL()})
	w := newTunnelWorld(t, c)
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	e, cfg := engineOverVPN(t, rig, engineOpts{})

	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	if err != nil {
		t.Fatal(err)
	}
	done := waitFor(t, e, hash, "finishing through the tunnel", 40*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
	if done.Left != 0 {
		t.Errorf("status = %+v", done)
	}
	// The tracker was reached by its name, which only the tunnel's DNS knows.
	if q := w.server.DNSQueries(); !slices.Contains(q, tunnelTrackerName) {
		t.Errorf("the tunnel's DNS was asked about %v, never about the tracker", q)
	}
	if n := w.tracker.called(c.hash); n == 0 {
		t.Error("the tracker was never reached")
	}
	st := rig.vpn.Status()
	if st.State != VPNConnected || st.RxBytes == 0 || st.TxBytes == 0 || st.LastHandshake.IsZero() {
		t.Errorf("tunnel status = %+v", st)
	}
	// The only operating system socket was WireGuard's, opened once, and it
	// only ever sent to the VPN server.
	if n := rig.guard.Opens(); n != 1 {
		t.Errorf("WireGuard's socket was opened %d times", n)
	}
	if v := rig.guard.Violations(); v != 0 {
		t.Errorf("WireGuard's socket sent beyond the VPN server %d times", v)
	}
	if d := rig.guard.Destinations(); len(d) != 1 || !strings.HasPrefix(d[0], "127.0.0.1:") {
		t.Errorf("WireGuard's socket sent to %v", d)
	}
}

func TestVPNLooksUpTheEndpointNameOnceWithTheOSAndNothingElse(t *testing.T) {
	w := newTunnelWorld(t)
	var asked []string
	text := w.server.ConfigText(w.client, "vpn.endpoint-test.invalid:"+strconv.Itoa(w.server.Port()))
	rig := newVPNRig(t, text, func(o *VPNOptions) {
		// Only the first lookup is under test: a slow first handshake would
		// make the tunnel look the name up again (on purpose) after NudgeEvery.
		o.NudgeEvery = time.Hour
		o.ResolveEndpoint = func(_ context.Context, host string) ([]string, error) {
			asked = append(asked, host)
			return []string{"127.0.0.1"}, nil
		}
	})
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	e, _ := engineOverVPN(t, rig, engineOpts{})
	_ = e
	time.Sleep(300 * time.Millisecond)
	if len(asked) != 1 || asked[0] != "vpn.endpoint-test.invalid" {
		t.Errorf("the OS resolver was asked %v", asked)
	}
}

// In VPN mode the engine looks everything up through the tunnel, the DHT's
// entry points included.
func TestVPNEngineLooksUpTheDHTEntryPointsThroughTheTunnel(t *testing.T) {
	w := newTunnelWorld(t)
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	_, _ = openEngine(t, rig.vpn, engineOpts{cfg: func(c *Config) { c.ListenHost, c.DHT, c.PEX = "", true, true }})
	eventuallyWithin(t, 20*time.Second, "the DHT entry points to be looked up in the tunnel", func() bool {
		for _, q := range w.server.DNSQueries() {
			if strings.Contains(q, "bittorrent.com") || strings.Contains(q, "transmissionbt.com") {
				return true
			}
		}
		return false
	})
	if v := rig.guard.Violations(); v != 0 {
		t.Errorf("WireGuard's socket sent beyond the VPN server %d times", v)
	}
}

func TestVPNUsesThePortTheProviderForwards(t *testing.T) {
	w := newTunnelWorld(t)
	// A stub that answers on the client's tunnel address is what a peer
	// would be. It tries the engine's address from the server's side.
	tryConnect := func(port int) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := w.server.Net().DialContext(ctx, "tcp", vpntest.ClientIP+":"+strconv.Itoa(port))
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	t.Run("a forwarded port is listened on", func(t *testing.T) {
		rig := newVPNRig(t, w.config(), nil)
		waitState(t, rig.vpn, VPNConnected, 15*time.Second)
		const port = 51999
		e, _ := engineOverVPN(t, rig, engineOpts{cfg: func(c *Config) { c.ListenPort = port }})
		if e.Port() != port {
			t.Fatalf("the engine listens on %d", e.Port())
		}
		if !tryConnect(port) {
			t.Error("a peer couldn't reach the forwarded port")
		}
	})
	t.Run("with no forwarded port nothing listens", func(t *testing.T) {
		rig := newVPNRig(t, w.config(), nil)
		waitState(t, rig.vpn, VPNConnected, 15*time.Second)
		e, _ := engineOverVPN(t, rig, engineOpts{})
		if e.Port() != 0 {
			t.Fatalf("the engine says it listens on %d", e.Port())
		}
		for _, port := range []int{6881, 51999, 0} {
			if port != 0 && tryConnect(port) {
				t.Errorf("something listens on %d", port)
			}
		}
	})
}

func TestVPNStartupWithAnUnreachableServerStaysConnectingAndStartsNothing(t *testing.T) {
	c := makeContent(t, "Never.Starts.2021", contentOpts{announce: announceURL()})
	w := newTunnelWorld(t, c)
	// Nothing listens on the endpoint's port yet.
	w.server.Stop()
	rig := newVPNRig(t, w.config(), nil)
	e, _ := engineOverVPN(t, rig, engineOpts{})
	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	if st := rig.vpn.Status(); st.State != VPNConnecting {
		t.Fatalf("tunnel = %s", st.State)
	}
	if isRunning(e, hash) || running(e) != 0 {
		t.Error("a torrent started before the VPN was connected")
	}
	if st := status(t, e, hash); st.State != rktorrent.Queued {
		t.Errorf("a held torrent reads %s, not queued", st.State)
	}
	if _, err := rig.vpn.Dial(context.Background(), "tcp", vpntest.ServerIP+":80"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("Dial while connecting: %v", err)
	}
	if _, err := rig.vpn.LookupHost(context.Background(), "example.org"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("LookupHost while connecting: %v", err)
	}
	if n := w.tracker.called(c.hash); n != 0 {
		t.Errorf("the tracker was reached %d times", n)
	}
	// The server turns up: the tunnel connects (the client keeps trying) and
	// the torrent starts by itself.
	w.server.Start()
	waitState(t, rig.vpn, VPNConnected, 20*time.Second)
	waitFor(t, e, hash, "finishing once connected", 40*time.Second, func(s Status) bool { return s.Progress >= 1 })
}

// The core of the kill switch: the VPN drops in the middle of a download.
func TestVPNKillSwitch(t *testing.T) {
	// Three downloads, but only two slots: the third waits its turn. A fourth
	// is paused by the person. Exactly the two running ones must come back.
	mk := func(name string) *content {
		return makeContent(t, name, contentOpts{announce: announceURL(), big: 3 << 20})
	}
	a, b, queued, paused := mk("Kill.A.2021"), mk("Kill.B.2021"), mk("Kill.C.2021"), mk("Kill.D.2021")
	w := newTunnelWorld(t, a, b, queued, paused)
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	e, cfg := engineOverVPN(t, rig, engineOpts{cfg: func(c *Config) {
		c.MaxActiveDownloads = 2
		c.DownloadLimitKBs = 700
	}})
	var hashes []string
	for _, c := range []*content{a, b, queued} {
		h, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	hp, err := e.Add(AddRequest{Torrent: paused.torrent, Category: cat, Stopped: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hashes[:2] {
		waitFor(t, e, h, "some progress", 30*time.Second, func(s Status) bool { return s.Have > 200<<10 })
	}
	if running(e) != 2 || isRunning(e, hashes[2]) || isRunning(e, hp) {
		t.Fatalf("before the drop: %d running", running(e))
	}

	// The VPN server goes away; its handshake grows old.
	w.server.Stop()
	rig.jump(200 * time.Second)
	waitState(t, rig.vpn, VPNDown, 10*time.Second)
	eventuallyWithin(t, 10*time.Second, "all torrents to be held", func() bool { return running(e) == 0 })

	for _, h := range hashes[:2] {
		if st := status(t, e, h); st.State != rktorrent.Queued {
			t.Errorf("a held torrent reads %s, not queued", st.State)
		}
	}
	if st := status(t, e, hp); st.State != rktorrent.Paused {
		t.Errorf("the person's paused torrent reads %s", st.State)
	}
	if _, err := rig.vpn.Dial(context.Background(), "tcp", vpntest.ServerIP+":80"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("Dial while down: %v", err)
	}
	if _, err := rig.vpn.LookupHost(context.Background(), tunnelTrackerName); !errors.Is(err, ErrVPNDown) {
		t.Errorf("LookupHost while down: %v", err)
	}
	if rig.vpn.Usable() {
		t.Error("the gate is open while down")
	}
	before := []int64{status(t, e, hashes[0]).Have, status(t, e, hashes[1]).Have}
	time.Sleep(1500 * time.Millisecond)
	for i, h := range hashes[:2] {
		if got := status(t, e, h).Have; got != before[i] {
			t.Errorf("bytes arrived while the VPN was down: %d -> %d", before[i], got)
		}
	}
	if v := rig.guard.Violations(); v != 0 {
		t.Errorf("WireGuard's socket sent beyond the VPN server %d times", v)
	}

	// The server comes back: the same two torrents run again, nothing else.
	w.server.Start()
	waitState(t, rig.vpn, VPNConnected, 30*time.Second)
	eventuallyWithin(t, 10*time.Second, "the torrents to start again", func() bool { return running(e) == 2 })
	if !isRunning(e, hashes[0]) || !isRunning(e, hashes[1]) || isRunning(e, hashes[2]) || isRunning(e, hp) {
		t.Errorf("after the reconnect the wrong torrents run: a=%v b=%v queued=%v paused=%v",
			isRunning(e, hashes[0]), isRunning(e, hashes[1]), isRunning(e, hashes[2]), isRunning(e, hp))
	}
	for _, h := range hashes[:2] {
		waitFor(t, e, h, "finishing after the reconnect", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
	}
	sameFiles(t, cfg.Folder, a)
	sameFiles(t, cfg.Folder, b)
	if v := rig.guard.Violations(); v != 0 {
		t.Errorf("WireGuard's socket sent beyond the VPN server %d times", v)
	}
}

func TestVPNLeakFlagHoldsDownloadsLikeAnOutage(t *testing.T) {
	c := makeContent(t, "Leaky.2021", contentOpts{announce: announceURL(), big: 3 << 20})
	w := newTunnelWorld(t, c)
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	e, _ := engineOverVPN(t, rig, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 500 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	waitFor(t, e, hash, "some progress", 30*time.Second, func(s Status) bool { return s.Have > 100<<10 })

	rig.vpn.SetLeak(true)
	eventuallyWithin(t, 5*time.Second, "the hold", func() bool { return running(e) == 0 })
	if st := rig.vpn.Status(); st.State != VPNDown || !st.Leak {
		t.Errorf("status = %+v", st)
	}
	if _, err := rig.vpn.Dial(context.Background(), "tcp", vpntest.ServerIP+":80"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("Dial while leaking: %v", err)
	}
	// The checker can still look through the tunnel to see whether it is fixed.
	conn, err := rig.vpn.Probe(context.Background(), "tcp", vpntest.ServerIP+":80")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	_ = conn.Close()
	rig.vpn.SetLeak(false)
	waitFor(t, e, hash, "finishing after the leak ended", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
}

func TestVPNProbeReachesTheTunnelsTracePage(t *testing.T) {
	w := newTunnelWorld(t)
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	client := &http.Client{Transport: &http.Transport{DialContext: rig.vpn.Probe}, Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + vpntest.ServerIP + "/cdn-cgi/trace")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ip="+tunnelExitIP) {
		t.Errorf("trace page = %q", body)
	}
}

func TestEngineHoldKeepsTorrentsStoppedWithoutTouchingPauses(t *testing.T) {
	c1 := makeContent(t, "Hold.One.2021", contentOpts{})
	c2 := makeContent(t, "Hold.Two.2021", contentOpts{})
	s := startSeeder(t, seederOpts{}, c1, c2)
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.Hold = true }})
	h1, _ := e.Add(AddRequest{Torrent: c1.torrent, Category: cat})
	h2, _ := e.Add(AddRequest{Magnet: c2.magnet(), Category: cat})
	time.Sleep(400 * time.Millisecond)
	if running(e) != 0 || !e.Held() {
		t.Fatalf("started held, but %d run", running(e))
	}
	// A magnet link with no details yet reads queued, not "stuck on details".
	for _, h := range []string{h1, h2} {
		if st := status(t, e, h); st.State != rktorrent.Queued {
			t.Errorf("held torrent %s reads %s", h[:6], st.State)
		}
	}
	// The person pauses one while everything is held, then the hold ends.
	if err := e.Stop(cat, []string{h2}); err != nil {
		t.Fatal(err)
	}
	e.SetHold(false)
	eventuallyWithin(t, 5*time.Second, "the unpaused torrent to run", func() bool { return isRunning(e, h1) })
	if isRunning(e, h2) {
		t.Error("the hold ending restarted a torrent the person had paused")
	}
	feed(t, e, h1, s)
	waitFor(t, e, h1, "finishing", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	e.SetHold(true)
	if running(e) != 0 {
		t.Error("holding again left something running")
	}
}

// Everything a Network offers works through the tunnel (TCP and UDP, dialled
// and listening), and when the tunnel goes down the sockets that are open stop
// working: connections are closed, a UDP socket refuses to send.
func TestVPNNetworkMethodsWorkThroughTheTunnelAndStopWhenItIsDown(t *testing.T) {
	w := newTunnelWorld(t)
	// A UDP echo on the server's network.
	echo, err := w.server.Net().ListenUDPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(vpntest.ServerIP), 7000))
	if err != nil {
		t.Fatal(err)
	}
	w.server.Own(echo)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], from)
		}
	}()
	rig := newVPNRig(t, w.config(), nil)
	waitState(t, rig.vpn, VPNConnected, 15*time.Second)
	ctx := context.Background()

	// UDP, dialled.
	uc, err := rig.vpn.Dial(ctx, "udp", vpntest.ServerIP+":7000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uc.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	_ = uc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := uc.Read(buf); err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("udp dial echo: %q %v", buf[:n], err)
	}
	// UDP, listening: the socket the DHT and uTP use.
	pc, err := rig.vpn.ListenPacket(ctx, "udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if ip := pc.LocalAddr().(*net.UDPAddr).IP.String(); ip != vpntest.ClientIP {
		t.Errorf("the UDP socket is bound to %s, not the tunnel address", ip)
	}
	dst := &net.UDPAddr{IP: net.ParseIP(vpntest.ServerIP), Port: 7000}
	if _, err := pc.WriteTo([]byte("pong"), dst); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, _, err := pc.ReadFrom(buf); err != nil || string(buf[:n]) != "pong" {
		t.Fatalf("udp listen echo: %q %v", buf[:n], err)
	}
	// TCP, dialled by name through the tunnel's DNS, and held open.
	if ips, err := rig.vpn.LookupHost(ctx, tunnelTrackerName); err != nil || len(ips) != 1 || ips[0] != vpntest.ServerIP {
		t.Fatalf("lookup = %v %v", ips, err)
	}
	tc, err := rig.vpn.Dial(ctx, "tcp", vpntest.ServerIP+":80")
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()

	// The tunnel goes down.
	w.server.Stop()
	rig.jump(200 * time.Second)
	waitState(t, rig.vpn, VPNDown, 10*time.Second)
	_ = tc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := tc.Read(buf); err == nil {
		t.Error("an open connection was still readable after the tunnel went down")
	}
	if _, err := tc.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err == nil {
		t.Error("an open connection could still send")
	}
	if _, err := pc.WriteTo([]byte("leak"), dst); !errors.Is(err, ErrVPNDown) {
		t.Errorf("the UDP socket sent while down: %v", err)
	}
	if _, err := rig.vpn.ListenPacket(ctx, "udp", ":0"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("ListenPacket while down: %v", err)
	}
	if _, err := rig.vpn.Listen(ctx, "tcp", ":5555"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("Listen while down: %v", err)
	}
	if _, err := rig.vpn.Dial(ctx, "udp", vpntest.ServerIP+":7000"); !errors.Is(err, ErrVPNDown) {
		t.Errorf("Dial while down: %v", err)
	}
	if v := rig.guard.Violations(); v != 0 {
		t.Errorf("WireGuard's socket sent beyond the VPN server %d times", v)
	}
}
