package builtin

// The VPN: a WireGuard tunnel that runs inside Reelkeeper, as a Network.
//
// It is userspace only. wireguard-go encrypts the packets and a small network
// stack (gVisor's, bundled with wireguard-go's "netstack") plays the part of
// the computer on the far side of the tunnel, so the built-in client's
// connections are made inside that stack and nothing on the computer's own
// network ever carries them. No TUN device, no routes, no extra container
// privileges: the one real socket is WireGuard's own UDP socket to the VPN
// server.
//
// THE KILL SWITCH. In VPN mode the engine is built with this Network and no
// other. There is no fallback to the computer's network anywhere; if the
// tunnel is down, there is simply nowhere else for a connection to go. On top
// of that, while the tunnel is not known to be up, every dial and lookup is
// refused, the connections already open are closed, sockets already open
// refuse to send, and the engine stops all its torrents (Gate, Engine.hold).
// Keep it that way: a "fall back to the normal network" feature must never be
// added here.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ErrVPNDown is what every use of a VPN that isn't connected comes back with.
var ErrVPNDown = errors.New("the VPN isn't connected")

// VPNState is where the tunnel is.
type VPNState string

// The states. A tunnel starts Connecting; it is Connected while its last
// handshake is recent, and Down when it has been connected and the handshake
// has gone stale (or the tunnel failed). Only Connected carries traffic.
const (
	VPNConnecting VPNState = "connecting"
	VPNConnected  VPNState = "connected"
	VPNDown       VPNState = "down"
)

// Gate is a Network that can say it can't carry traffic right now. The engine
// stops its torrents while the gate is closed, and starts the right ones again
// when it opens.
type Gate interface {
	// Usable is true while traffic may flow.
	Usable() bool
	// OnChange registers a function to call (without holding any lock) when
	// Usable may have changed.
	OnChange(fn func())
}

// Defaults for the timing. A handshake counts for 180 seconds because that is
// how long WireGuard itself trusts a session; the tunnel is looked at every 5.
const (
	DefaultDownAfter  = 180 * time.Second
	DefaultVPNPoll    = 5 * time.Second
	DefaultNudgeEvery = 30 * time.Second
	endpointRetry     = 10 * time.Second
	// closeSettle is how long the tunnel's network gets to go quiet before it
	// is closed (see Close).
	closeSettle = 250 * time.Millisecond
)

// VPNOptions are the knobs of a VPNNetwork. Everything has a default; tests
// shorten the timing.
type VPNOptions struct {
	Logger *slog.Logger
	// ResolveEndpoint looks up the VPN server's name when the config gives a
	// name instead of an address. It is the one lookup that happens outside
	// the tunnel (there is no tunnel yet to ask through), and it is only
	// ever about the VPN server's own name. Nil means the computer's
	// resolver.
	ResolveEndpoint func(ctx context.Context, host string) ([]string, error)
	// Bind is WireGuard's UDP socket to the VPN server. Nil means the real
	// one; it is the only operating system socket VPN mode has. Tests
	// wrap it to prove that.
	Bind conn.Bind
	// DownAfter is how long a handshake counts as recent. Poll is how often
	// the tunnel is looked at. NudgeEvery is how often a tunnel that has gone
	// down is told to try a new handshake.
	DownAfter, Poll, NudgeEvery time.Duration
	// Now is the clock (tests move it).
	Now func() time.Time
}

// VPNStatus is what the tunnel looks like right now.
type VPNStatus struct {
	State VPNState
	// Since is when it got into this state.
	Since time.Time
	// LastHandshake is when the VPN server last answered (zero before the
	// first time).
	LastHandshake time.Time
	// RxBytes and TxBytes count what went through the tunnel since it
	// started.
	RxBytes, TxBytes int64
	// Leak: the tunnel is up, but the address peers see is the computer's
	// own (see SetLeak), so the state is Down.
	Leak bool
	// TunnelConnected: the tunnel itself is connected (its handshake is
	// recent), whatever Leak says.
	TunnelConnected bool
	// Generation goes up every time the tunnel becomes Connected, so anything
	// learnt about the connection (the address peers see) can be dropped when
	// it reconnects.
	Generation uint64
	// Problem says what is wrong, if the tunnel itself failed.
	Problem string
}

// VPNNetwork is a Network (and a Gate) whose every connection goes through a
// WireGuard tunnel.
type VPNNetwork struct {
	opts   VPNOptions
	logger *slog.Logger
	dev    *device.Device
	tnet   *netstack.Net
	local  netip.Addr // the address sockets are bound to

	endpointHost string
	endpointPort int
	peerSet      string // the peer's settings, as the IPC text (secrets inside)
	keyHex       string // the server's public key (not a secret)

	stop chan struct{}
	done chan struct{}

	mu            sync.Mutex
	status        VPNStatus
	everConnected bool
	// The newest handshake the poller has seen, and when it first saw it
	// (age is measured from that moment, not from WireGuard's timestamp, so a
	// change of the computer's clock can't fake a fresh tunnel).
	hsSeen    time.Time
	hsBase    time.Duration
	rxAcc     int64
	txAcc     int64
	rxPrev    int64
	txPrev    int64
	nudgedAt  time.Time
	endpoint  string // ip:port set on the peer; empty until it is known
	live      map[io.Closer]struct{}
	onChange  []func()
	closed    bool
	resolveAt time.Time
}

// NewVPN starts a tunnel for cfg. It returns at once: the tunnel is Connecting
// until the VPN server answers. An error means the tunnel couldn't even be set
// up (never that the server is unreachable).
func NewVPN(cfg *WireGuardConfig, opts VPNOptions) (*VPNNetwork, error) {
	if cfg == nil {
		return nil, errors.New("no VPN config")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.DownAfter <= 0 {
		opts.DownAfter = DefaultDownAfter
	}
	if opts.Poll <= 0 {
		opts.Poll = DefaultVPNPoll
	}
	if opts.NudgeEvery <= 0 {
		opts.NudgeEvery = DefaultNudgeEvery
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ResolveEndpoint == nil {
		opts.ResolveEndpoint = OSNetwork{}.LookupHost
	}
	if opts.Bind == nil {
		opts.Bind = conn.NewDefaultBind()
	}
	var addrs []netip.Addr
	for _, p := range cfg.Addresses {
		addrs = append(addrs, p.Addr())
	}
	tdev, tnet, err := netstack.CreateNetTUN(addrs, cfg.DNS, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("can't set up the VPN's network: %w", err)
	}
	v := &VPNNetwork{
		opts: opts, logger: opts.Logger.With("component", "vpn"), tnet: tnet,
		endpointHost: cfg.EndpointHost, endpointPort: cfg.EndpointPort,
		stop: make(chan struct{}), done: make(chan struct{}), live: map[io.Closer]struct{}{},
		keyHex: hex.EncodeToString(cfg.PublicKey[:]),
	}
	v.local = pickLocal(addrs)
	v.status = VPNStatus{State: VPNConnecting, Since: opts.Now()}
	v.nudgedAt = v.status.Since
	wgLog := &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(format string, a ...any) { v.logger.Debug("wireguard: " + fmt.Sprintf(format, a...)) },
	}
	v.dev = device.NewDevice(tdev, opts.Bind, wgLog)
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", hex.EncodeToString(cfg.PrivateKey[:]))
	if err := v.dev.IpcSet(b.String()); err != nil {
		v.dev.Close()
		return nil, fmt.Errorf("can't set up the VPN: %w", errors.New(scrub(err.Error())))
	}
	v.peerSet = peerText(cfg)
	first := v.peerSet
	if a, err := netip.ParseAddr(cfg.EndpointHost); err == nil {
		// An address needs no lookup: the tunnel knows where to go from the
		// start, so its first handshake isn't wasted.
		v.endpoint = net.JoinHostPort(a.String(), strconv.Itoa(cfg.EndpointPort))
		first += "endpoint=" + v.endpoint + "\n"
	}
	if err := v.dev.IpcSet(first); err != nil {
		v.dev.Close()
		return nil, fmt.Errorf("can't set up the VPN: %w", errors.New(scrub(err.Error())))
	}
	if err := v.dev.Up(); err != nil {
		v.dev.Close()
		return nil, fmt.Errorf("can't start the VPN: %w", errors.New(scrub(err.Error())))
	}
	go v.run()
	return v, nil
}

// scrub keeps anything key-like out of an error before it is shown.
func scrub(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) >= 40 {
			words[i] = "…"
		}
	}
	return strings.Join(words, " ")
}

// peerText is the IPC text that sets up the peer: all traffic is allowed to
// it, whatever the config's AllowedIPs said, because the built-in client's
// traffic all goes through the VPN. The endpoint is added once it is known.
func peerText(cfg *WireGuardConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(cfg.PublicKey[:]))
	if cfg.PresharedKey != nil {
		fmt.Fprintf(&b, "preshared_key=%s\n", hex.EncodeToString(cfg.PresharedKey[:]))
	}
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\nreplace_allowed_ips=true\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n", cfg.Keepalive)
	return b.String()
}

// pickLocal is the address sockets bind to: the first IPv4 one, else the first.
func pickLocal(addrs []netip.Addr) netip.Addr {
	for _, a := range addrs {
		if a.Is4() {
			return a
		}
	}
	return addrs[0]
}

// ---- the tunnel's life

func (v *VPNNetwork) run() {
	defer close(v.done)
	if v.endpointKnown() == "" {
		if ep := v.lookupEndpoint(); ep != "" {
			v.restartPeer(ep)
		}
	}
	tick := time.NewTicker(v.opts.Poll)
	defer tick.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-tick.C:
		}
		v.poll()
	}
}

func (v *VPNNetwork) endpointKnown() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.endpoint
}

// lookupEndpoint finds the VPN server's address when the config gives it as a
// name, and remembers it. A name that can't be looked up gives "" and is tried
// again later.
func (v *VPNNetwork) lookupEndpoint() string {
	host := v.endpointHost
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ips, err := v.opts.ResolveEndpoint(ctx, host)
	cancel()
	v.mu.Lock()
	v.resolveAt = v.opts.Now()
	v.mu.Unlock()
	if err != nil || len(ips) == 0 {
		v.logger.Warn("can't look up the VPN server's address; will try again", "host", host, "error", err)
		return ""
	}
	ip := ips[0]
	for _, c := range ips { // an IPv4 address first: more tunnels can use it
		if a, err := netip.ParseAddr(c); err == nil && a.Is4() {
			ip = c
			break
		}
	}
	ep := net.JoinHostPort(ip, strconv.Itoa(v.endpointPort))
	v.mu.Lock()
	v.endpoint = ep
	v.mu.Unlock()
	return ep
}

// restartPeer gives the tunnel its peer afresh, with the server's address:
// WireGuard forgets any old session and starts a handshake at once.
func (v *VPNNetwork) restartPeer(ep string) {
	if err := v.dev.IpcSet(fmt.Sprintf("public_key=%s\nremove=true\n", v.keyHex)); err != nil {
		return
	}
	text := v.peerSet
	if ep != "" {
		text += "endpoint=" + ep + "\n"
	}
	if err := v.dev.IpcSet(text); err != nil {
		v.logger.Warn("couldn't restart the VPN's handshake", "error", scrub(err.Error()))
	}
}

// poll reads the tunnel's handshake and counters and works out the state.
func (v *VPNNetwork) poll() {
	v.mu.Lock()
	needResolve := v.endpoint == "" && v.opts.Now().Sub(v.resolveAt) >= endpointRetry
	v.mu.Unlock()
	if needResolve {
		if ep := v.lookupEndpoint(); ep != "" {
			v.restartPeer(ep)
		}
	}
	out, err := v.dev.IpcGet()
	var hs time.Time
	var rx, tx int64
	if err == nil {
		hs, rx, tx = readStats(out)
	}
	v.mu.Lock()
	now := v.opts.Now()
	if err != nil {
		v.mu.Unlock()
		v.setState(VPNDown, "the VPN stopped working")
		return
	}
	if rx < v.rxPrev { // the peer was reset (nudge): carry the old count
		v.rxAcc += v.rxPrev
	}
	if tx < v.txPrev {
		v.txAcc += v.txPrev
	}
	v.rxPrev, v.txPrev = rx, tx
	v.status.RxBytes, v.status.TxBytes = v.rxAcc+rx, v.txAcc+tx
	if !hs.IsZero() && !hs.Equal(v.status.LastHandshake) {
		v.status.LastHandshake = hs
		v.hsSeen = now
		v.hsBase = max(time.Since(hs), 0)
	}
	state := v.stateAt(now)
	v.mu.Unlock()
	v.setState(state, "")
	v.nudge(state)
}

// stateAt works out the state from what the poller has seen. The caller holds
// the lock.
func (v *VPNNetwork) stateAt(now time.Time) VPNState {
	if !v.status.LastHandshake.IsZero() && v.hsBase+now.Sub(v.hsSeen) < v.opts.DownAfter {
		return VPNConnected
	}
	if v.everConnected {
		return VPNDown
	}
	return VPNConnecting
}

// setState records a change of state, closes what must not outlive a working
// tunnel, and tells whoever is listening.
func (v *VPNNetwork) setState(s VPNState, problem string) {
	v.mu.Lock()
	if v.closed || (v.status.State == s && v.status.Problem == problem) {
		v.mu.Unlock()
		return
	}
	was := v.usableLocked()
	if v.status.State != s {
		v.status.State = s
		v.status.Since = v.opts.Now()
		v.nudgedAt = v.status.Since // a new handshake is tried a while after this
		if s == VPNConnected {
			v.everConnected = true
			v.status.Generation++
		}
	}
	v.status.Problem = problem
	var toClose []io.Closer
	if !v.usableLocked() {
		toClose = v.takeLiveLocked()
	}
	changed := was != v.usableLocked()
	cbs := append([]func(){}, v.onChange...)
	v.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
	if changed || len(toClose) > 0 {
		v.logger.Info("the VPN's state changed", "state", string(s))
	}
	for _, f := range cbs {
		f()
	}
}

func (v *VPNNetwork) usableLocked() bool { return v.status.State == VPNConnected && !v.status.Leak }

// nudge asks a tunnel that isn't connected to try for a new handshake: it is
// given its peer afresh, which makes WireGuard forget the old session and start
// a handshake at once, instead of waiting for the old session to wear out (or,
// for a tunnel whose server didn't answer at first, instead of giving up after
// its first minute and a half of tries: nothing else would ever send it a
// packet to try again with, because nothing is allowed through). A server name
// is looked up again then too (the server may have moved).
func (v *VPNNetwork) nudge(s VPNState) {
	if s == VPNConnected {
		return
	}
	v.mu.Lock()
	if v.opts.Now().Sub(v.nudgedAt) < v.opts.NudgeEvery {
		v.mu.Unlock()
		return
	}
	v.nudgedAt = v.opts.Now()
	ep := v.endpoint
	v.mu.Unlock()
	if _, err := netip.ParseAddr(v.endpointHost); err != nil {
		if n := v.lookupEndpoint(); n != "" {
			ep = n
		}
	}
	v.restartPeer(ep)
}

// readStats pulls the handshake time and byte counts out of the tunnel's
// status text. That text also holds the private key; nothing else is read
// from it and none of it is ever logged.
func readStats(text string) (hs time.Time, rx, tx int64) {
	var sec, nsec int64
	for _, line := range strings.Split(text, "\n") {
		k, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			sec = n
		case "last_handshake_time_nsec":
			nsec = n
		case "rx_bytes":
			rx = n
		case "tx_bytes":
			tx = n
		}
	}
	if sec > 0 {
		hs = time.Unix(sec, nsec)
	}
	return hs, rx, tx
}

// Status is the tunnel as it is now.
func (v *VPNNetwork) Status() VPNStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.status
	st.TunnelConnected = st.State == VPNConnected
	if st.Leak && st.State == VPNConnected {
		// Up, but not hiding anything: reads as down.
		st.State = VPNDown
	}
	return st
}

// Refresh looks at the tunnel now instead of at the next beat.
func (v *VPNNetwork) Refresh() { v.poll() }

// SetLeak says the address peers would see is the computer's own, which the
// IP check found out. The tunnel then counts as down until a later check says
// otherwise (or it is restarted).
func (v *VPNNetwork) SetLeak(leak bool) {
	v.mu.Lock()
	if v.closed || v.status.Leak == leak {
		v.mu.Unlock()
		return
	}
	v.status.Leak = leak
	var toClose []io.Closer
	if !v.usableLocked() {
		toClose = v.takeLiveLocked()
	}
	cbs := append([]func(){}, v.onChange...)
	v.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
	for _, f := range cbs {
		f()
	}
}

// Usable implements Gate: true only while the tunnel is connected and not
// leaking.
func (v *VPNNetwork) Usable() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.closed && v.usableLocked()
}

// OnChange implements Gate.
func (v *VPNNetwork) OnChange(fn func()) {
	v.mu.Lock()
	v.onChange = append(v.onChange, fn)
	v.mu.Unlock()
}

// Close takes the tunnel down.
func (v *VPNNetwork) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	toClose := v.takeLiveLocked()
	close(v.stop)
	v.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
	<-v.done
	// wireguard-go's userspace network closes a channel its stack may still be
	// sending on, which is a crash (a panic in a goroutine of the library) if
	// anything is still arriving or leaving. So: stop receiving first (Down
	// closes the UDP socket), then give the stack a moment to send what the
	// closed sockets just made (their FINs, and resets for stragglers), and only
	// then close it.
	_ = v.dev.Down()
	time.Sleep(closeSettle)
	v.dev.Close()
	return nil
}

// ---- live connections

func (v *VPNNetwork) track(c io.Closer) {
	v.mu.Lock()
	v.live[c] = struct{}{}
	v.mu.Unlock()
}

func (v *VPNNetwork) untrack(c io.Closer) {
	v.mu.Lock()
	delete(v.live, c)
	v.mu.Unlock()
}

func (v *VPNNetwork) takeLiveLocked() []io.Closer {
	out := make([]io.Closer, 0, len(v.live))
	for c := range v.live {
		out = append(out, c)
	}
	v.live = map[io.Closer]struct{}{}
	return out
}

// ---- the Network

// Dial implements Network.
func (v *VPNNetwork) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if !v.Usable() {
		return nil, ErrVPNDown
	}
	c, err := v.tnet.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return v.wrapConn(c), nil
}

// Probe dials through the tunnel for checking what address the world sees. It
// works whenever the tunnel itself is connected, even when the leak check has
// closed the gate (the check is how a leak gets noticed and cleared). The
// engine never uses it.
func (v *VPNNetwork) Probe(ctx context.Context, network, addr string) (net.Conn, error) {
	v.mu.Lock()
	up := !v.closed && v.status.State == VPNConnected
	v.mu.Unlock()
	if !up {
		return nil, ErrVPNDown
	}
	return v.tnet.DialContext(ctx, network, addr)
}

// Listen implements Network. Peers can only reach a listener if the VPN
// provider forwards a port to this one, which is why asking for port 0 (the
// provider forwards nothing) gets no listener at all, only a stand-in that
// never accepts.
func (v *VPNNetwork) Listen(_ context.Context, network, addr string) (net.Listener, error) {
	if !v.Usable() {
		return nil, ErrVPNDown
	}
	ap, err := v.bindAddr(addr)
	if err != nil {
		return nil, err
	}
	if ap.Port() == 0 {
		return &idleListener{addr: &net.TCPAddr{IP: ap.Addr().AsSlice()}, closed: make(chan struct{})}, nil
	}
	l, err := v.tnet.ListenTCPAddrPort(ap)
	if err != nil {
		return nil, err
	}
	return &vpnListener{Listener: l, v: v}, nil
}

// ListenPacket implements Network: the UDP socket uTP, the DHT and UDP
// trackers use. It sends nothing while the tunnel is down.
func (v *VPNNetwork) ListenPacket(_ context.Context, network, addr string) (net.PacketConn, error) {
	if !v.Usable() {
		return nil, ErrVPNDown
	}
	ap, err := v.bindAddr(addr)
	if err != nil {
		return nil, err
	}
	pc, err := v.tnet.ListenUDPAddrPort(ap)
	if err != nil {
		return nil, err
	}
	return &vpnPacketConn{PacketConn: pc, v: v}, nil
}

// LookupHost implements Network: a name is looked up through the tunnel, by
// the DNS servers the config names.
func (v *VPNNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	if !v.Usable() {
		return nil, ErrVPNDown
	}
	return v.tnet.LookupContextHost(ctx, host)
}

// bindAddr turns host:port into the tunnel address to bind to; the engine
// asks for "any address", which in the tunnel means its own.
func (v *VPNNetwork) bindAddr(addr string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return netip.AddrPort{}, err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return netip.AddrPort{}, fmt.Errorf("%q isn't a port", port)
	}
	ip := v.local
	if host != "" && host != "0.0.0.0" && host != "::" {
		if ip, err = netip.ParseAddr(host); err != nil {
			return netip.AddrPort{}, fmt.Errorf("%q isn't an address", host)
		}
	}
	return netip.AddrPortFrom(ip, uint16(p)), nil
}

func (v *VPNNetwork) wrapConn(c net.Conn) net.Conn {
	w := &vpnConn{Conn: c, v: v}
	v.track(w)
	return w
}

// vpnConn refuses to send while the tunnel isn't up, and is closed when it
// goes down.
type vpnConn struct {
	net.Conn
	v *VPNNetwork
}

func (c *vpnConn) Write(b []byte) (int, error) {
	if !c.v.Usable() {
		return 0, ErrVPNDown
	}
	return c.Conn.Write(b)
}

func (c *vpnConn) Close() error {
	c.v.untrack(c)
	return c.Conn.Close()
}

// vpnListener closes connections that arrive while the tunnel isn't up.
type vpnListener struct {
	net.Listener
	v *VPNNetwork
}

func (l *vpnListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.v.Usable() {
			_ = c.Close()
			continue
		}
		return l.v.wrapConn(c), nil
	}
}

// idleListener is the listener for "no port is forwarded": it has an address
// and never accepts anything.
type idleListener struct {
	addr   net.Addr
	once   sync.Once
	closed chan struct{}
}

func (l *idleListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *idleListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *idleListener) Addr() net.Addr { return l.addr }

// vpnPacketConn refuses to send while the tunnel isn't up.
type vpnPacketConn struct {
	net.PacketConn
	v *VPNNetwork
}

func (p *vpnPacketConn) WriteTo(b []byte, a net.Addr) (int, error) {
	if !p.v.Usable() {
		return 0, ErrVPNDown
	}
	return p.PacketConn.WriteTo(b, a)
}

var (
	_ Network = (*VPNNetwork)(nil)
	_ Gate    = (*VPNNetwork)(nil)
)
