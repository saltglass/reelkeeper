package auth

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"testing"
)

var traefik = []netip.Prefix{netip.MustParsePrefix("172.16.0.2/32")}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"direct, no header", "192.168.1.50:5000", nil, traefik, "192.168.1.50"},
		{"spoofed header from an untrusted client is ignored", "203.0.113.9:5000", []string{"192.168.1.50"}, traefik, "203.0.113.9"},
		{"spoofed header with no trusted proxies is ignored", "203.0.113.9:5000", []string{"192.168.1.50"}, nil, "203.0.113.9"},
		{"trusted proxy forwards the client", "172.16.0.2:443", []string{"192.168.1.50"}, traefik, "192.168.1.50"},
		{"client-supplied prefix before the real hop is ignored", "172.16.0.2:443", []string{"10.0.0.1, 192.168.1.50"}, traefik, "192.168.1.50"},
		{"multiple header lines are combined", "172.16.0.2:443", []string{"1.1.1.1", "192.168.1.50"}, traefik, "192.168.1.50"},
		{"chain of trusted proxies", "172.16.0.2:443", []string{"198.51.100.7, 172.16.0.3"}, []netip.Prefix{netip.MustParsePrefix("172.16.0.0/24")}, "198.51.100.7"},
		{"trusted proxy without header is itself the client", "172.16.0.2:443", nil, traefik, "172.16.0.2"},
		{"garbage hop stops the walk", "172.16.0.2:443", []string{"192.168.1.50, not-an-ip"}, traefik, "172.16.0.2"},
		{"IPv4-mapped IPv6 is unmapped", "[::ffff:192.168.1.50]:5000", nil, traefik, "192.168.1.50"},
		{"IPv6 client", "[fd00::5]:5000", nil, traefik, "fd00::5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ClientIP(r, c.trusted); got.String() != c.want {
				t.Errorf("ClientIP = %s, want %s", got, c.want)
			}
		})
	}
}

func TestIsHTTPSAndHost(t *testing.T) {
	r := httptest.NewRequest("GET", "http://reelkeeper.local/", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	if IsHTTPS(r, traefik) {
		t.Error("X-Forwarded-Proto from an untrusted client must be ignored")
	}
	if h := ExternalHost(r, traefik); h != "reelkeeper.local" {
		t.Errorf("host = %s; X-Forwarded-Host from an untrusted client must be ignored", h)
	}

	r.RemoteAddr = "172.16.0.2:443"
	r.Header.Set("X-Forwarded-Host", "reelkeeper.example.com")
	if !IsHTTPS(r, traefik) {
		t.Error("trusted proxy says https")
	}
	if got := CallbackURL(r, traefik, "/reelkeeper"); got != "https://reelkeeper.example.com/reelkeeper/api/v1/auth/oidc/callback" {
		t.Errorf("callback = %s", got)
	}

	direct := httptest.NewRequest("GET", "https://reelkeeper.local/", nil)
	direct.TLS = &tls.ConnectionState{}
	if !IsHTTPS(direct, nil) {
		t.Error("direct TLS is https")
	}
}

func TestSafeReturnPath(t *testing.T) {
	cases := map[string]string{
		"/movies":              "/movies",
		"/settings?tab=x":      "/settings?tab=x",
		"":                     "/",
		"movies":               "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"/ok\r\nSet-Cookie: x": "/",
	}
	for in, want := range cases {
		if got := SafeReturnPath(in); got != want {
			t.Errorf("SafeReturnPath(%q) = %q, want %q", in, got, want)
		}
	}
}
