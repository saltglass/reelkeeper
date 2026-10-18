// Package vpntest is a WireGuard "VPN server" for tests, inside the test
// process. It is a wireguard-go device with its own userspace network stack,
// reached over UDP on 127.0.0.1, and whatever a test needs lives on its network
// at ServerIP: HTTP stubs and a DNS server that knows a few names. Nothing here
// reaches beyond loopback, and the keys are made up per test.
//
// It is only ever imported by tests.
package vpntest

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// The tunnel's two addresses: the server's, where tests put their services,
// and the client's.
const (
	ServerIP = "10.66.0.1"
	ClientIP = "10.66.0.2"
)

// Keys are a WireGuard key pair, made up for a test.
type Keys struct{ Priv, Pub [32]byte }

// NewKeys makes a key pair.
func NewKeys(t testing.TB) Keys {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var out Keys
	copy(out.Priv[:], k.Bytes())
	copy(out.Pub[:], k.PublicKey().Bytes())
	return out
}

// B64 is a key as WireGuard config files write it.
func B64(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

// GuardBind wraps WireGuard's real UDP socket and writes down what it does:
// how many times it was opened, and every place it sent to. In VPN mode it is
// the only operating system socket there is, so the tests insist it is opened
// once and sends only to the VPN server (which is on loopback here).
type GuardBind struct {
	conn.Bind
	opens      atomic.Int64
	sends      atomic.Int64
	violations atomic.Int64
	mu         sync.Mutex
	dests      map[string]bool
}

// NewGuardBind wraps a real bind.
func NewGuardBind() *GuardBind {
	return &GuardBind{Bind: conn.NewDefaultBind(), dests: map[string]bool{}}
}

// Open implements conn.Bind.
func (g *GuardBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	g.opens.Add(1)
	return g.Bind.Open(port)
}

// Send implements conn.Bind. Anything not headed for loopback is refused.
func (g *GuardBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	if !ep.DstIP().IsLoopback() {
		g.violations.Add(1)
		return errors.New("test guard: refusing to send beyond loopback")
	}
	g.sends.Add(1)
	g.mu.Lock()
	g.dests[ep.DstToString()] = true
	g.mu.Unlock()
	return g.Bind.Send(bufs, ep)
}

// Opens is how many times the socket was opened.
func (g *GuardBind) Opens() int64 { return g.opens.Load() }

// Violations is how many sends to somewhere other than loopback were refused.
func (g *GuardBind) Violations() int64 { return g.violations.Load() }

// Destinations are the places it sent to.
func (g *GuardBind) Destinations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for d := range g.dests {
		out = append(out, d)
	}
	return out
}

// Server is the VPN server.
type Server struct {
	t         testing.TB
	keys      Keys
	clientPub [32]byte
	port      int
	setup     func(*Server)

	mu      sync.Mutex
	dev     *device.Device
	tnet    *netstack.Net
	closers []io.Closer
	dnsLog  []string
}

// FreeUDPPort finds a UDP port on 127.0.0.1 nobody is using.
func FreeUDPPort(t testing.TB) int {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// New makes a server for this client (its public key is all the server needs).
// setup puts the services on the server's network; it runs again each time the
// server starts. The server is stopped when the test ends. Call Start.
func New(t testing.TB, client Keys, setup func(*Server)) *Server {
	t.Helper()
	s := &Server{t: t, keys: NewKeys(t), clientPub: client.Pub, port: FreeUDPPort(t), setup: setup}
	t.Cleanup(s.Stop)
	return s
}

// Port is the UDP port the server listens on, on 127.0.0.1.
func (s *Server) Port() int { return s.port }

// Start brings the server up (on the same port as before, with no memory of any
// earlier session, like a server that was restarted).
func (s *Server) Start() {
	s.t.Helper()
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr(ServerIP)}, nil, 1420)
	if err != nil {
		s.t.Fatal(err)
	}
	logger := &device.Logger{Verbosef: func(string, ...any) {}, Errorf: func(string, ...any) {}}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), logger)
	conf := fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=%s/32\n",
		hex.EncodeToString(s.keys.Priv[:]), s.port, hex.EncodeToString(s.clientPub[:]), ClientIP)
	var last error
	for i := 0; i < 50; i++ { // the port may take a moment to come free after a stop
		if last = dev.IpcSet(conf); last == nil {
			if last = dev.Up(); last == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last != nil {
		dev.Close()
		s.t.Fatalf("the test VPN server can't start: %v", last)
	}
	s.mu.Lock()
	s.dev, s.tnet = dev, tnet
	s.mu.Unlock()
	if s.setup != nil {
		s.setup(s)
	}
}

// Stop takes the server down: its device, its network and everything on it.
func (s *Server) Stop() {
	s.mu.Lock()
	dev, closers := s.dev, s.closers
	s.dev, s.tnet, s.closers = nil, nil, nil
	s.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
	if dev != nil {
		// Stop receiving and let the stack go quiet first (see
		// builtin.VPNNetwork.Close).
		_ = dev.Down()
		time.Sleep(250 * time.Millisecond)
		dev.Close()
	}
}

// Net is the server's network (nil while it is stopped).
func (s *Server) Net() *netstack.Net {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tnet
}

// Own hands something to the server to close when it stops.
func (s *Server) Own(c io.Closer) {
	s.mu.Lock()
	s.closers = append(s.closers, c)
	s.mu.Unlock()
}

// Serve runs an HTTP handler on the server's network.
func (s *Server) Serve(port uint16, h http.Handler) {
	l, err := s.Net().ListenTCPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(ServerIP), port))
	if err != nil {
		s.t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	s.Own(srv)
}

// ServeDNS answers A questions for the given names (and says no to the rest).
func (s *Server) ServeDNS(names map[string]string) {
	pc, err := s.Net().ListenUDPAddrPort(netip.AddrPortFrom(netip.MustParseAddr(ServerIP), 53))
	if err != nil {
		s.t.Fatal(err)
	}
	s.Own(pc)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			reply, name := answerDNS(buf[:n], names)
			if name != "" {
				s.mu.Lock()
				s.dnsLog = append(s.dnsLog, name)
				s.mu.Unlock()
			}
			if reply != nil {
				_, _ = pc.WriteTo(reply, from)
			}
		}
	}()
}

// DNSQueries are the names the server was asked about, in order.
func (s *Server) DNSQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dnsLog...)
}

func answerDNS(query []byte, names map[string]string) ([]byte, string) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, ""
	}
	q, err := p.Question()
	if err != nil {
		return nil, ""
	}
	name := strings.TrimSuffix(q.Name.String(), ".")
	resp := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionAvailable: true},
		Questions: []dnsmessage.Question{q},
	}
	ip, known := names[name]
	switch {
	case !known:
		resp.RCode = dnsmessage.RCodeNameError
	case q.Type == dnsmessage.TypeA:
		a := netip.MustParseAddr(ip).As4()
		resp.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: a},
		}}
	}
	out, err := resp.Pack()
	if err != nil {
		return nil, name
	}
	return out, name
}

// TraceHandler is the stub of Cloudflare's /cdn-cgi/trace page. ip may be
// changed while it runs through the returned setter.
func TraceHandler(ip, loc string) (h http.Handler, set func(ip string)) {
	var mu sync.Mutex
	h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		cur := ip
		mu.Unlock()
		_, _ = fmt.Fprintf(w, "fl=1f1\nh=1.1.1.1\nip=%s\nts=1.0\nvisit_scheme=https\nuag=stub\ncolo=AMS\nsliver=none\nhttp=http/2\nloc=%s\ntls=TLSv1.3\nsni=off\nwarp=off\ngateway=off\nrbi=off\nkex=X25519\n", cur, loc)
	})
	return h, func(v string) { mu.Lock(); ip = v; mu.Unlock() }
}

// ConfigText is a wg-quick file for the client. An empty endpoint means the
// server on loopback.
func (s *Server) ConfigText(client Keys, endpoint string) string {
	if endpoint == "" {
		endpoint = fmt.Sprintf("127.0.0.1:%d", s.port)
	}
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = %s/32\nDNS = %s\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\nPersistentKeepalive = 1\n",
		B64(client.Priv), ClientIP, ServerIP, B64(s.keys.Pub), endpoint)
}
