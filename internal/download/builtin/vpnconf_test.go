package builtin

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeKey is a made-up but well-formed WireGuard key.
func fakeKey(seed string) string {
	sum := sha256.Sum256([]byte("reelkeeper test key " + seed))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// The files below are shaped like the ones real providers hand out (comments,
// spacing and extra settings included). Every key is made up, and every
// address is in a documentation range.

func mullvadStyle() string {
	return fmt.Sprintf(`[Interface]
# Device: Brave Otter
PrivateKey = %s
Address = 10.66.20.33/32,fc00:bbbb:bbbb:bb01::3:1420/128
DNS = 10.64.0.1

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0,::0/0
Endpoint = 203.0.113.50:51820
`, fakeKey("m-priv"), fakeKey("m-pub"))
}

func protonStyle() string {
	return fmt.Sprintf(`[Interface]
# Key for reelkeeper
# Bouncing = 3
# NetShield = 0
# Moderate NAT = off
# NAT-PMP (Port Forwarding) = on
# VPN Accelerator = on
PrivateKey = %s
Address = 10.2.0.2/32
DNS = 10.2.0.1

[Peer]
# NL#41
PublicKey = %s
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.9:51820
`, fakeKey("p-priv"), fakeKey("p-pub"))
}

func airvpnStyle() string {
	return fmt.Sprintf("[Interface]\r\nAddress = 10.167.12.4/32, fd7d:76ee:e68f:a993:1:2:3:4/128\r\nMTU = 1320\r\nPrivateKey = %s\r\nDNS = 10.128.0.1, fd7d:76ee:e68f:a993::1\r\n\r\n[Peer]\r\nPublicKey = %s\r\nPresharedKey = %s\r\nAllowedIPs = 0.0.0.0/0, ::/0\r\nEndpoint = nl3.vpn.example.org:1637\r\nPersistentKeepalive = 15\r\n",
		fakeKey("a-priv"), fakeKey("a-pub"), fakeKey("a-psk"))
}

func TestParseProviderShapedConfigs(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		check func(t *testing.T, c *WireGuardConfig)
	}{
		{"Mullvad", mullvadStyle(), func(t *testing.T, c *WireGuardConfig) {
			if len(c.Addresses) != 2 || c.Addresses[0].String() != "10.66.20.33/32" || !c.hasV6() {
				t.Errorf("addresses = %v", c.Addresses)
			}
			if len(c.DNS) != 1 || c.DNS[0].String() != "10.64.0.1" || c.DNSDefault {
				t.Errorf("dns = %v default=%v", c.DNS, c.DNSDefault)
			}
			if c.Endpoint != "203.0.113.50:51820" || c.EndpointPort != 51820 || c.EndpointHost != "203.0.113.50" {
				t.Errorf("endpoint = %q %q %d", c.Endpoint, c.EndpointHost, c.EndpointPort)
			}
			if c.Keepalive != 25 || c.MTU != 1420 || c.PresharedKey != nil {
				t.Errorf("keepalive %d, mtu %d, psk %v", c.Keepalive, c.MTU, c.PresharedKey != nil)
			}
			if len(c.Warnings) != 0 || len(c.Notes) != 0 {
				t.Errorf("warnings %v notes %v", c.Warnings, c.Notes)
			}
		}},
		{"Proton", protonStyle(), func(t *testing.T, c *WireGuardConfig) {
			if len(c.Addresses) != 1 || c.hasV6() {
				t.Errorf("addresses = %v", c.Addresses)
			}
			if len(c.AllowedIPs) != 2 || len(c.Warnings) != 0 {
				t.Errorf("allowed %v warnings %v", c.AllowedIPs, c.Warnings)
			}
		}},
		{"AirVPN with CRLF, a preshared key, a name and a smaller MTU", airvpnStyle(), func(t *testing.T, c *WireGuardConfig) {
			if c.PresharedKey == nil || c.MTU != 1320 || c.Keepalive != 15 {
				t.Errorf("psk %v mtu %d keepalive %d", c.PresharedKey != nil, c.MTU, c.Keepalive)
			}
			if c.EndpointHost != "nl3.vpn.example.org" || c.EndpointPort != 1637 {
				t.Errorf("endpoint = %q %d", c.EndpointHost, c.EndpointPort)
			}
			if len(c.DNS) != 2 || c.DNS[1].String() != "fd7d:76ee:e68f:a993::1" {
				t.Errorf("dns = %v", c.DNS)
			}
		}},
		{"no DNS line uses 1.1.1.1 through the tunnel", strings.Replace(protonStyle(), "DNS = 10.2.0.1\n", "", 1), func(t *testing.T, c *WireGuardConfig) {
			if !c.DNSDefault || len(c.DNS) != 1 || c.DNS[0].String() != "1.1.1.1" {
				t.Errorf("dns = %v default=%v", c.DNS, c.DNSDefault)
			}
		}},
		{"only an IPv6 address gets an IPv6 default DNS", strings.Replace(strings.Replace(protonStyle(), "DNS = 10.2.0.1\n", "", 1), "10.2.0.2/32", "fd00::2/128", 1), func(t *testing.T, c *WireGuardConfig) {
			if !c.DNSDefault || c.DNS[0].String() != "2606:4700:4700::1111" {
				t.Errorf("dns = %v", c.DNS)
			}
		}},
		{"DNS search domains are ignored", strings.Replace(protonStyle(), "DNS = 10.2.0.1", "DNS = 10.2.0.1, vpn.internal", 1), func(t *testing.T, c *WireGuardConfig) {
			if len(c.DNS) != 1 || c.DNSDefault {
				t.Errorf("dns = %v", c.DNS)
			}
		}},
		{"section and key names ignore case, spaces and a BOM", "\xef\xbb\xbf[interface]\n  privatekey=" + fakeKey("c-priv") + "\n ADDRESS =10.0.0.2\n[PEER]\npublickey = " + fakeKey("c-pub") + "\nendpoint=203.0.113.1:1\nallowedips=0.0.0.0/0\n", func(t *testing.T, c *WireGuardConfig) {
			if c.Addresses[0].String() != "10.0.0.2/32" || c.Endpoint != "203.0.113.1:1" {
				t.Errorf("parsed %v %q", c.Addresses, c.Endpoint)
			}
		}},
		{"an Address on several lines adds up", "[Interface]\nPrivateKey = " + fakeKey("d-priv") + "\nAddress = 10.0.0.2/32\nAddress = fd00::2/128\n[Peer]\nPublicKey = " + fakeKey("d-pub") + "\nEndpoint = [2001:db8::1]:51820\nAllowedIPs = 0.0.0.0/0\nAllowedIPs = ::/0\n", func(t *testing.T, c *WireGuardConfig) {
			if len(c.Addresses) != 2 || c.EndpointHost != "2001:db8::1" || len(c.AllowedIPs) != 2 || len(c.Warnings) != 0 {
				t.Errorf("addresses %v endpoint %q allowed %v warnings %v", c.Addresses, c.EndpointHost, c.AllowedIPs, c.Warnings)
			}
		}},
		{"wg-quick settings are ignored with a note", strings.Replace(protonStyle(), "DNS = 10.2.0.1\n", "DNS = 10.2.0.1\nPostUp = iptables -A FORWARD -j ACCEPT\nTable = off\nSaveConfig = true\nListenPort = 51820\nJc = 4\n", 1), func(t *testing.T, c *WireGuardConfig) {
			joined := strings.Join(c.Notes, "\n")
			for _, want := range []string{"Ignored PostUp", "Ignored Table", "Ignored SaveConfig", "Ignored ListenPort", "Ignored Jc"} {
				if !strings.Contains(joined, want) {
					t.Errorf("no note %q in %q", want, joined)
				}
			}
			if strings.Contains(joined, "iptables") {
				t.Error("a note repeats the command that was ignored")
			}
		}},
		{"a keepalive of 0 or off becomes 25 with a note", strings.Replace(protonStyle(), "Endpoint = 203.0.113.9:51820", "Endpoint = 203.0.113.9:51820\nPersistentKeepalive = off", 1), func(t *testing.T, c *WireGuardConfig) {
			if c.Keepalive != 25 || len(c.Notes) != 1 || !strings.Contains(c.Notes[0], "PersistentKeepalive") {
				t.Errorf("keepalive %d notes %v", c.Keepalive, c.Notes)
			}
		}},
		{"a very long keepalive is lowered", strings.Replace(protonStyle(), "Endpoint = 203.0.113.9:51820", "Endpoint = 203.0.113.9:51820\nPersistentKeepalive = 600", 1), func(t *testing.T, c *WireGuardConfig) {
			if c.Keepalive != 120 || len(c.Notes) != 1 {
				t.Errorf("keepalive %d notes %v", c.Keepalive, c.Notes)
			}
		}},
		{"a split-tunnel config is accepted with a warning", strings.Replace(protonStyle(), "AllowedIPs = 0.0.0.0/0, ::/0", "AllowedIPs = 10.2.0.0/24", 1), func(t *testing.T, c *WireGuardConfig) {
			if len(c.Warnings) != 1 || c.Warnings[0] != partialWarning {
				t.Errorf("warnings = %v", c.Warnings)
			}
		}},
		{"no ::/0 warns when there is an IPv6 address", strings.Replace(mullvadStyle(), "0.0.0.0/0,::0/0", "0.0.0.0/0", 1), func(t *testing.T, c *WireGuardConfig) {
			if len(c.Warnings) != 1 {
				t.Errorf("warnings = %v", c.Warnings)
			}
		}},
		{"no ::/0 is fine without an IPv6 address", strings.Replace(protonStyle(), "0.0.0.0/0, ::/0", "0.0.0.0/0", 1), func(t *testing.T, c *WireGuardConfig) {
			if len(c.Warnings) != 0 {
				t.Errorf("warnings = %v", c.Warnings)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseWireGuard(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, c)
		})
	}
}

func TestParseErrorsAreClearAndNeverContainAKey(t *testing.T) {
	priv, pub := fakeKey("e-priv"), fakeKey("e-pub")
	ok := func(mod func(string) string) string { return mod(protonStyle()) }
	cases := []struct {
		name string
		text string
		want string
	}{
		{"empty", "", "doesn't look like a WireGuard config"},
		{"an OpenVPN file", "client\ndev tun\nproto udp\nremote vpn.example.org 1194\n", "WireGuard config"},
		{"binary", "\x00\x01\x02", "isn't a text file"},
		{"too big", strings.Repeat("a", maxConfigBytes+1), "too big"},
		{"no peer", "[Interface]\nPrivateKey = " + priv + "\nAddress = 10.0.0.2/32\n", "no [Peer]"},
		{"two peers", protonStyle() + "\n[Peer]\nPublicKey = " + pub + "\nEndpoint = 203.0.113.2:1\nAllowedIPs = 0.0.0.0/0\n", "Reelkeeper supports one peer, which is what VPN providers give you."},
		{"two interfaces", protonStyle() + "\n[Interface]\n", "two [Interface] sections"},
		{"an unknown section", "[Wat]\n", "isn't a section"},
		{"text before a section", "PrivateKey = " + priv + "\n[Interface]\n", "comes before [Interface]"},
		{"a line that isn't a setting", "[Interface]\n" + priv + "\n", "Line 2 isn't in the form"},
		{"no private key", ok(func(s string) string { return strings.Replace(s, "PrivateKey", "Private", 1) }), "no PrivateKey"},
		{"a private key that isn't base64", ok(func(s string) string { return strings.Replace(s, fakeKey("p-priv"), "not a key at all!", 1) }), "PrivateKey isn't valid"},
		{"a private key of the wrong length", ok(func(s string) string { return strings.Replace(s, fakeKey("p-priv"), fakeKey("p-priv")[:20], 1) }), "PrivateKey isn't valid"},
		{"a private key given twice", ok(func(s string) string { return strings.Replace(s, "DNS", "PrivateKey = "+priv+"\nDNS", 1) }), "PrivateKey is given twice"},
		{"no address", ok(func(s string) string { return strings.Replace(s, "Address = 10.2.0.2/32\n", "", 1) }), "no Address"},
		{"a bad address", ok(func(s string) string { return strings.Replace(s, "10.2.0.2/32", "ten.two/32", 1) }), "isn't an IP address"},
		{"an address with a bad prefix", ok(func(s string) string { return strings.Replace(s, "10.2.0.2/32", "10.2.0.2/99", 1) }), "isn't an IP address"},
		{"a bad MTU", ok(func(s string) string { return strings.Replace(s, "DNS", "MTU = 12\nDNS", 1) }), "MTU"},
		{"no server key", ok(func(s string) string { return strings.Replace(s, "PublicKey", "Pub", 1) }), "no PublicKey"},
		{"a bad server key", ok(func(s string) string { return strings.Replace(s, fakeKey("p-pub"), "AAAA", 1) }), "server's PublicKey isn't valid"},
		{"a bad preshared key", ok(func(s string) string { return strings.Replace(s, "AllowedIPs", "PresharedKey = zzz\nAllowedIPs", 1) }), "PresharedKey isn't valid"},
		{"no endpoint", ok(func(s string) string { return strings.Replace(s, "Endpoint = 203.0.113.9:51820", "", 1) }), "no Endpoint"},
		{"an endpoint without a port", ok(func(s string) string { return strings.Replace(s, "203.0.113.9:51820", "203.0.113.9", 1) }), "address:port"},
		{"an endpoint with a bad port", ok(func(s string) string { return strings.Replace(s, "203.0.113.9:51820", "203.0.113.9:99999", 1) }), "port"},
		{"a bad AllowedIPs", ok(func(s string) string { return strings.Replace(s, "0.0.0.0/0, ::/0", "everything", 1) }), "AllowedIPs"},
		{"a bad keepalive", ok(func(s string) string {
			return strings.Replace(s, "Endpoint", "PersistentKeepalive = soon\nEndpoint", 1)
		}), "PersistentKeepalive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseWireGuard(tc.text)
			if err == nil {
				t.Fatalf("parsed: %+v", c.Summary())
			}
			var ce *ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("not a ConfigError: %T", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q doesn't say %q", err, tc.want)
			}
			for _, k := range []string{priv, pub, fakeKey("p-priv"), fakeKey("p-pub"), fakeKey("a-psk")} {
				if strings.Contains(err.Error(), k) || strings.Contains(err.Error(), k[:12]) {
					t.Errorf("the error contains a key: %q", err)
				}
			}
		})
	}
}

func TestSummaryHasNoSecretsAndShowsThePublicKey(t *testing.T) {
	c, err := ParseWireGuard(airvpnStyle())
	if err != nil {
		t.Fatal(err)
	}
	s := c.Summary()
	if s.PublicKey == "" || s.PublicKey == fakeKey("a-priv") || len(s.PublicKey) != 44 {
		t.Errorf("public key = %q", s.PublicKey)
	}
	if !s.HasPSK || s.Endpoint != "nl3.vpn.example.org:1637" || len(s.TunnelAddresses) != 2 || s.Keepalive != 15 {
		t.Errorf("summary = %+v", s)
	}
	text := fmt.Sprintf("%+v %#v", s, s)
	for _, k := range []string{fakeKey("a-priv"), fakeKey("a-psk")} {
		if strings.Contains(text, k) {
			t.Error("the summary contains a secret key")
		}
	}
}
