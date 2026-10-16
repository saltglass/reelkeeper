package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address a request really came from.
//
// The connection's own address is used unless it belongs to a trusted
// proxy. Only then is X-Forwarded-For consulted, read from right to left,
// skipping further trusted proxies; the first untrusted address is the
// client. Believing the header from anyone else would let a visitor claim
// to be on the bypass list just by sending it.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	remote := remoteAddr(r)
	if !remote.IsValid() || !inAny(remote, trusted) {
		return remote
	}

	// Several X-Forwarded-For headers are combined in order.
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for part := range strings.SplitSeq(h, ",") {
			hops = append(hops, strings.TrimSpace(part))
		}
	}
	client := remote
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(hops[i])
		if err != nil {
			// Garbage in the chain: stop and use the last address we trust
			// to have reported honestly.
			return client
		}
		addr = addr.Unmap().WithZone("")
		client = addr
		if !inAny(addr, trusted) {
			return addr
		}
	}
	return client
}

// IsHTTPS reports whether the browser reached Reelkeeper over HTTPS, either
// directly or through a trusted proxy that says so in X-Forwarded-Proto.
func IsHTTPS(r *http.Request, trusted []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	if inAny(remoteAddr(r), trusted) {
		return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
	}
	return false
}

// ExternalHost returns the host name the browser used, taking
// X-Forwarded-Host from trusted proxies only.
func ExternalHost(r *http.Request, trusted []netip.Prefix) string {
	if inAny(remoteAddr(r), trusted) {
		if h := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); h != "" {
			return h
		}
	}
	return r.Host
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap().WithZone("")
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
