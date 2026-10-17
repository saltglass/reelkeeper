package builtin

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/torrent"
)

// dialFunc adapts a function to the dialer anacrolix wants.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f dialFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

func dialerOf(f func(ctx context.Context, network, addr string) (net.Conn, error)) dialFunc {
	return dialFunc(f)
}

// dialHost opens a connection through the Network. A name is looked up through
// the Network too, so that a name lookup can't take a road the connection
// doesn't (the OS resolver would).
func (e *Engine) dialHost(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(host) != nil {
		return e.netw.Dial(ctx, network, addr)
	}
	ips, err := e.netw.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		c, err := e.netw.Dial(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no address for " + host)
	}
	return nil, last
}

// lookupTracker is the tracker lookup anacrolix has a hook for.
func (e *Engine) lookupTracker(u *url.URL) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addrs, err := e.netw.LookupHost(ctx, u.Hostname())
	var out []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			out = append(out, ip)
		}
	}
	return out, err
}

// dhtStartingNodes are the DHT's well-known entry points, looked up through
// the Network.
func (e *Engine) dhtStartingNodes(string) dht.StartingNodesGetter {
	return func() ([]dht.Addr, error) {
		var out []dht.Addr
		for _, hp := range dht.DefaultGlobalBootstrapHostPorts {
			host, port, err := net.SplitHostPort(hp)
			if err != nil {
				continue
			}
			n, err := strconv.Atoi(port)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ips, err := e.netw.LookupHost(ctx, host)
			cancel()
			if err != nil {
				continue
			}
			for _, ip := range ips {
				if p := net.ParseIP(ip); p != nil {
					out = append(out, dht.NewAddr(&net.UDPAddr{IP: p, Port: n}))
				}
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no DHT entry point could be looked up")
		}
		return out, nil
	}
}

// splitTrackers separates the trackers anacrolix can use as they are (HTTP
// ones, and UDP ones given as an address) from UDP ones given as a name.
// anacrolix looks UDP trackers' names up with the operating system's
// resolver, so the engine looks them up itself, through the Network, and
// hands over the addresses (resolveTrackers). Anything else (WebSocket
// trackers, say) is left out.
func splitTrackers(tiers [][]string) (known [][]string, names []string) {
	for _, tier := range tiers {
		var keep []string
		for _, s := range tier {
			u, err := url.Parse(s)
			if err != nil {
				continue
			}
			switch u.Scheme {
			case "http", "https":
				keep = append(keep, s)
			case "udp", "udp4", "udp6":
				if net.ParseIP(u.Hostname()) != nil {
					keep = append(keep, s)
				} else {
					names = append(names, s)
				}
			}
		}
		if len(keep) > 0 {
			known = append(known, keep)
		}
	}
	return known, names
}

// resolveTrackers looks up UDP trackers' names through the Network and gives
// the torrent the addresses. A name that can't be looked up is skipped.
func (e *Engine) resolveTrackers(t *torrent.Torrent, gone <-chan struct{}, names []string) {
	var tier []string
	for _, s := range names {
		select {
		case <-gone:
			return
		default:
		}
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ips, err := e.netw.LookupHost(ctx, u.Hostname())
		cancel()
		if err != nil {
			e.logger.Debug("a tracker's name couldn't be looked up", "tracker", u.Host, "error", err)
			continue
		}
		for _, ip := range ips {
			a := *u
			a.Host = net.JoinHostPort(ip, u.Port())
			if u.Port() == "" {
				a.Host = ip
			}
			e.trk.alias(a.String(), s)
			tier = append(tier, a.String())
		}
	}
	if len(tier) > 0 {
		t.AddTrackers([][]string{tier})
	}
}
