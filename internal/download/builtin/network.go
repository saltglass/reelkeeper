package builtin

import (
	"context"
	"net"
)

// Network is where every socket of the engine comes from. The engine never
// opens a connection, a listener or a name lookup any other way (a test reads
// the source to make sure), so a later version can hand it a network that
// leads somewhere else, such as a VPN tunnel with a kill switch, and
// nothing in the engine needs to change.
//
// Names are only ever resolved through LookupHost: the engine turns a tracker's
// or a DHT bootstrap node's name into addresses itself, before it dials, so
// that DNS can't leave by another road than the connections do.
type Network interface {
	// Dial opens an outgoing connection: to peers, trackers and web
	// requests. addr is always an IP address and a port; "tcp" is the only
	// network the engine dials with.
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	// Listen accepts incoming peer connections ("tcp").
	Listen(ctx context.Context, network, addr string) (net.Listener, error)
	// ListenPacket opens a UDP socket ("udp"): the one uTP peers and the
	// DHT share, and the ones UDP trackers are talked to with.
	ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error)
	// LookupHost resolves a name to its IP addresses.
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// OSNetwork is the computer's own network, which is what Reelkeeper uses until
// there is a VPN to send the traffic through. It is the only place in this
// package that touches the net package's dialing, listening and name lookup
// directly.
type OSNetwork struct{}

var (
	osDialer   = &net.Dialer{}
	osListener = &net.ListenConfig{}
	osResolver = &net.Resolver{}
)

// Dial implements Network.
func (OSNetwork) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return osDialer.DialContext(ctx, network, addr)
}

// Listen implements Network.
func (OSNetwork) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	return osListener.Listen(ctx, network, addr)
}

// ListenPacket implements Network.
func (OSNetwork) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	return osListener.ListenPacket(ctx, network, addr)
}

// LookupHost implements Network.
func (OSNetwork) LookupHost(ctx context.Context, host string) ([]string, error) {
	return osResolver.LookupHost(ctx, host)
}

var _ Network = OSNetwork{}
