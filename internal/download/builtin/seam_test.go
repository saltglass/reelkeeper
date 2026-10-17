package builtin

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// forbidden are the ways to reach the network that skip the Network. Only
// network.go (the plain-OS implementation) may use them.
var forbiddenSelectors = map[string][]string{
	"net": {
		"Dial", "DialTimeout", "DialTCP", "DialUDP", "DialIP", "DialUnix",
		"Listen", "ListenTCP", "ListenUDP", "ListenIP", "ListenPacket", "ListenMulticastUDP",
		"LookupHost", "LookupIP", "LookupAddr", "LookupCNAME", "LookupMX", "LookupNS", "LookupPort", "LookupSRV", "LookupTXT",
		"ResolveTCPAddr", "ResolveUDPAddr", "ResolveIPAddr", "DefaultResolver", "Dialer", "ListenConfig", "Resolver",
	},
	"http": {"DefaultClient", "DefaultTransport", "Get", "Head", "Post", "PostForm", "Client", "Transport", "ListenAndServe", "Serve"},
	"tls":  {"Dial", "DialWithDialer", "Listen"},
}

var forbiddenImports = []string{"net/http", "os/exec", "net/rpc", "net/smtp", "net/http/httputil"}

// Reads the engine's own source, as a review would: every file but network.go
// (and the tests) must stay clear of direct network use.
func TestNoEngineCodeBypassesTheNetwork(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no source files", err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "network.go" {
			continue
		}
		checked++
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbiddenImports {
				if path == bad {
					t.Errorf("%s imports %s", f, path)
				}
			}
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
			for _, name := range forbiddenSelectors[id.Name] {
				if sel.Sel.Name == name {
					t.Errorf("%s: %s.%s skips the Network", fset.Position(sel.Pos()), id.Name, name)
				}
			}
			return true
		})
	}
	if checked < 8 {
		t.Errorf("only %d source files were looked at", checked)
	}
	// The glob must not be silently empty if files move: network.go is where
	// the exemption lives.
	src, err := os.ReadFile("network.go")
	if err != nil || !strings.Contains(string(src), "osResolver") {
		t.Errorf("network.go isn't where the OS network lives any more: %v", err)
	}
}

// refusingNet is a Network that lets the engine listen, but refuses to dial
// and to send a single UDP packet. If anything in the engine reached out
// another way, the peer on 127.0.0.1 would still be found and the download
// would go ahead.
type refusingNet struct {
	dials, sends atomic.Int64
}

type muteConn struct {
	net.PacketConn
	n *refusingNet
}

func (m muteConn) WriteTo(b []byte, a net.Addr) (int, error) {
	m.n.sends.Add(1)
	return 0, errors.New("test network: sending refused")
}

func (n *refusingNet) Dial(context.Context, string, string) (net.Conn, error) {
	n.dials.Add(1)
	return nil, errors.New("test network: dialling refused")
}
func (n *refusingNet) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(ctx, network, addr)
}
func (n *refusingNet) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	pc, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp4", addr)
	return muteConn{pc, n}, err
}
func (n *refusingNet) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("test network: lookups refused")
}

func TestARefusingNetworkStopsTheEngineReachingAPeer(t *testing.T) {
	tr := startTracker(t)
	c := makeContent(t, "Refused.2020", contentOpts{announce: tr.url()})
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())
	nw := &refusingNet{}
	e, _ := openEngine(t, nw, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	feed(t, e, hash, s)
	// Both the tracker and the peer are right there on 127.0.0.1.
	time.Sleep(2 * time.Second)
	st := status(t, e, hash)
	if st.Have != 0 || st.State == rktorrent.Seeding {
		t.Fatalf("the engine got data through a Network that refuses everything: %+v", st)
	}
	if nw.dials.Load() == 0 {
		t.Error("the engine never tried to dial through the Network")
	}
	if nw.sends.Load() == 0 {
		t.Error("the engine never tried to send a uTP packet through the Network's UDP socket")
	}
	if n := tr.called(c.hash); n != 0 {
		t.Errorf("the tracker was reached %d times without the Network", n)
	}
	tt, _ := s.cl.Torrent(torrentHash(c.hash))
	if n := len(tt.PeerConns()); n != 0 {
		t.Errorf("the peer saw %d connections from the engine", n)
	}
}

func torrentHash(hexHash string) metainfo.Hash { return metainfo.NewHashFromHex(hexHash) }
