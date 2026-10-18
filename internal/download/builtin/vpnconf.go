package builtin

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// The VPN is WireGuard, given as the file every VPN provider hands out
// (wg-quick's format). This file reads it. Nothing here touches the network.
//
// The keys in the file are secrets. They are never put in an error message, a
// log line or a summary; the only key that is ever shown is the public key
// worked out from the private one.

// Limits and defaults for a WireGuard config.
const (
	// maxConfigBytes is far bigger than any provider's file (about 400 bytes).
	maxConfigBytes = 64 << 10
	// DefaultMTU is WireGuard's usual.
	DefaultMTU = 1420
	// DefaultKeepalive keeps the tunnel's handshake fresh, which is how the
	// kill switch tells that the VPN is up: with no traffic a tunnel would
	// otherwise go quiet and look dead.
	DefaultKeepalive = 25
	// maxKeepalive is the longest keepalive that still renews the handshake in
	// time (a handshake counts for 180 seconds).
	maxKeepalive = 120
	// DefaultDNS is used when the config names no DNS server. It is reached
	// through the tunnel, like everything else.
	DefaultDNS = "1.1.1.1"
	// defaultDNS6 is its IPv6 twin, for a config that has no IPv4 address.
	defaultDNS6 = "2606:4700:4700::1111"
)

// partialWarning is what a config gets when it would only send some traffic
// through the VPN.
const partialWarning = "This config only sends some traffic through the VPN; Reelkeeper sends all of the built-in client's traffic through it anyway."

// WireGuardConfig is a parsed wg-quick file.
type WireGuardConfig struct {
	// PrivateKey and PresharedKey are secrets.
	PrivateKey   [32]byte
	PresharedKey *[32]byte
	// Addresses are the tunnel addresses of this side.
	Addresses []netip.Prefix
	// DNS are the DNS servers to ask through the tunnel; DNSDefault is true
	// when the file named none and DefaultDNS stands in.
	DNS        []netip.Addr
	DNSDefault bool
	MTU        int

	// The one peer: the VPN server.
	PublicKey [32]byte
	// Endpoint is host:port as written; EndpointHost and EndpointPort are its
	// parts (the host may be a name).
	Endpoint     string
	EndpointHost string
	EndpointPort int
	AllowedIPs   []netip.Prefix
	// Keepalive is in seconds.
	Keepalive int

	// Warnings are things worth saying that don't stop the config working.
	Warnings []string
	// Notes say what was ignored or changed.
	Notes []string
}

// VPNSummary is what can be shown about a config: everything but the secrets.
type VPNSummary struct {
	Endpoint        string
	TunnelAddresses []string
	DNS             []string
	// DNSDefault: the config named no DNS server, so DefaultDNS is asked
	// through the tunnel.
	DNSDefault bool
	// PublicKey is this side's public key, worked out from the private one:
	// the value the VPN provider has on file for this config.
	PublicKey    string
	ServerKey    string
	HasPSK       bool
	Keepalive    int
	MTU          int
	Warnings     []string
	Notes        []string
	AllowedIPs   []string
	EndpointHost string
}

// Summary describes the config without any secret in it.
func (c *WireGuardConfig) Summary() VPNSummary {
	s := VPNSummary{
		Endpoint: c.Endpoint, EndpointHost: c.EndpointHost, DNSDefault: c.DNSDefault, HasPSK: c.PresharedKey != nil,
		Keepalive: c.Keepalive, MTU: c.MTU, PublicKey: c.InterfacePublicKey(),
		ServerKey: base64.StdEncoding.EncodeToString(c.PublicKey[:]),
		Warnings:  append([]string{}, c.Warnings...), Notes: append([]string{}, c.Notes...),
	}
	for _, a := range c.Addresses {
		s.TunnelAddresses = append(s.TunnelAddresses, a.String())
	}
	for _, d := range c.DNS {
		s.DNS = append(s.DNS, d.String())
	}
	for _, a := range c.AllowedIPs {
		s.AllowedIPs = append(s.AllowedIPs, a.String())
	}
	return s
}

// InterfacePublicKey is the public key that goes with the private one, in the
// base64 form WireGuard shows.
func (c *WireGuardConfig) InterfacePublicKey() string {
	k, err := ecdh.X25519().NewPrivateKey(c.PrivateKey[:])
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// ConfigError is a config Reelkeeper can't use, in words for the person who
// pasted it. It never contains any part of a key.
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

func configErrorf(format string, a ...any) error {
	return &ConfigError{Msg: fmt.Sprintf(format, a...)}
}

// ignoredKeys are wg-quick settings that make no sense for Reelkeeper's
// userspace tunnel (it changes no routes, runs no commands, listens on no
// fixed port). They are skipped, and the parse result says so.
var ignoredKeys = map[string]bool{
	"postup": true, "preup": true, "postdown": true, "predown": true, "table": true, "saveconfig": true,
	"fwmark": true, "listenport": true,
}

// ParseWireGuard reads a wg-quick style file: an [Interface] section and
// exactly one [Peer].
func ParseWireGuard(text string) (*WireGuardConfig, error) {
	if len(text) > maxConfigBytes {
		return nil, configErrorf("That file is too big to be a WireGuard config (they are a few hundred bytes).")
	}
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	if strings.ContainsRune(text, 0) {
		return nil, configErrorf("That isn't a text file. A WireGuard config is a small text file that starts with [Interface].")
	}
	var (
		cfg        = &WireGuardConfig{MTU: DefaultMTU}
		section    string
		sawIface   bool
		peers      int
		ifaceKeys  = map[string]string{}
		peerKeys   = map[string]string{}
		ignoredSet = map[string]bool{}
		unknownSet = map[string]bool{}
		order      []string // ignored names, in the order met
	)
	for n, raw := range strings.Split(text, "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name := strings.ToLower(strings.TrimSpace(strings.Trim(line, "[]")))
			switch name {
			case "interface":
				if sawIface {
					return nil, configErrorf("The config has two [Interface] sections; a WireGuard config has one.")
				}
				sawIface, section = true, "interface"
			case "peer":
				peers++
				if peers > 1 {
					return nil, configErrorf("Reelkeeper supports one peer, which is what VPN providers give you.")
				}
				section = "peer"
			default:
				return nil, configErrorf("Line %d: [%s] isn't a section a WireGuard config has (it has [Interface] and [Peer]).", n+1, strings.Trim(line, "[]"))
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			// Not echoed: the line may be a key.
			return nil, configErrorf("Line %d isn't in the form Name = value. Is this a WireGuard config?", n+1)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !isSettingName(key) {
			// Not echoed: a line that isn't a setting may be a bare key.
			return nil, configErrorf("Line %d isn't in the form Name = value. Is this a WireGuard config?", n+1)
		}
		original := key
		key = strings.ToLower(key)
		if section == "" {
			return nil, configErrorf("Line %d comes before [Interface]. Is this a WireGuard config?", n+1)
		}
		if ignoredKeys[key] {
			if !ignoredSet[key] {
				ignoredSet[key] = true
				order = append(order, key)
			}
			continue
		}
		target := ifaceKeys
		known := map[string]bool{"privatekey": true, "address": true, "dns": true, "mtu": true}
		if section == "peer" {
			target = peerKeys
			known = map[string]bool{"publickey": true, "presharedkey": true, "endpoint": true, "allowedips": true, "persistentkeepalive": true}
		}
		if !known[key] {
			if !unknownSet[section+key] {
				unknownSet[section+key] = true
				order = append(order, original)
			}
			continue
		}
		// A key given twice: lists add up (Address, DNS, AllowedIPs), the rest
		// would be ambiguous.
		if prev, dup := target[key]; dup {
			if key == "address" || key == "dns" || key == "allowedips" {
				target[key] = prev + "," + val
				continue
			}
			return nil, configErrorf("%s is given twice. Keep one.", displayKey(key))
		}
		target[key] = val
	}
	if !sawIface {
		return nil, configErrorf("This doesn't look like a WireGuard config: it has no [Interface] section. Download the WireGuard (not OpenVPN) file from your VPN provider.")
	}
	if peers == 0 {
		return nil, configErrorf("The config has no [Peer] section, so there is no VPN server to connect to.")
	}
	if err := cfg.readInterface(ifaceKeys); err != nil {
		return nil, err
	}
	if err := cfg.readPeer(peerKeys); err != nil {
		return nil, err
	}
	for _, k := range order {
		if ignoredKeys[strings.ToLower(k)] {
			cfg.Notes = append(cfg.Notes, fmt.Sprintf("Ignored %s: Reelkeeper keeps the VPN inside the app and doesn't change your computer's network or run commands.", displayKey(k)))
		} else {
			cfg.Notes = append(cfg.Notes, fmt.Sprintf("Ignored %s: Reelkeeper doesn't use it.", displayKey(k)))
		}
	}
	cfg.checkCoverage()
	return cfg, nil
}

var keyNames = map[string]string{
	"privatekey": "PrivateKey", "address": "Address", "dns": "DNS", "mtu": "MTU", "publickey": "PublicKey",
	"presharedkey": "PresharedKey", "endpoint": "Endpoint", "allowedips": "AllowedIPs", "persistentkeepalive": "PersistentKeepalive",
	"postup": "PostUp", "preup": "PreUp", "postdown": "PostDown", "predown": "PreDown", "table": "Table",
	"saveconfig": "SaveConfig", "fwmark": "FwMark", "listenport": "ListenPort",
}

func displayKey(k string) string {
	if n, ok := keyNames[k]; ok {
		return n
	}
	return k
}

func (c *WireGuardConfig) readInterface(kv map[string]string) error {
	pk, ok := kv["privatekey"]
	if !ok || pk == "" {
		return configErrorf("The [Interface] section has no PrivateKey. Download a fresh config from your VPN provider.")
	}
	k, err := decodeKey(pk)
	if err != nil {
		return configErrorf("The PrivateKey isn't valid: a WireGuard key is 44 letters and numbers ending in \"=\".")
	}
	c.PrivateKey = k

	addrs, ok := kv["address"]
	if !ok {
		return configErrorf("The [Interface] section has no Address, so Reelkeeper doesn't know which address the VPN gives it.")
	}
	for _, a := range splitList(addrs) {
		p, err := parsePrefixOrAddr(a)
		if err != nil {
			return configErrorf("The Address %q isn't an IP address like 10.2.0.2/32.", a)
		}
		c.Addresses = append(c.Addresses, p)
	}
	if len(c.Addresses) == 0 {
		return configErrorf("The Address in the [Interface] section is empty.")
	}
	for _, d := range splitList(kv["dns"]) {
		// A DNS line may also list search domains; only addresses matter.
		if ip, err := netip.ParseAddr(d); err == nil {
			c.DNS = append(c.DNS, ip.Unmap())
		}
	}
	if len(c.DNS) == 0 {
		c.DNSDefault = true
		fallback := DefaultDNS
		if !c.hasV4() {
			fallback = defaultDNS6
		}
		c.DNS = []netip.Addr{netip.MustParseAddr(fallback)}
	}
	if m, ok := kv["mtu"]; ok {
		n, err := strconv.Atoi(m)
		if err != nil || n < 576 || n > 1500 {
			return configErrorf("The MTU %q isn't a number from 576 to 1500 (most configs use 1420).", m)
		}
		c.MTU = n
	}
	return nil
}

func (c *WireGuardConfig) readPeer(kv map[string]string) error {
	pub, ok := kv["publickey"]
	if !ok || pub == "" {
		return configErrorf("The [Peer] section has no PublicKey (the VPN server's key).")
	}
	k, err := decodeKey(pub)
	if err != nil {
		return configErrorf("The server's PublicKey isn't valid: a WireGuard key is 44 letters and numbers ending in \"=\".")
	}
	c.PublicKey = k
	if psk, ok := kv["presharedkey"]; ok && psk != "" {
		k, err := decodeKey(psk)
		if err != nil {
			return configErrorf("The PresharedKey isn't valid: a WireGuard key is 44 letters and numbers ending in \"=\".")
		}
		c.PresharedKey = &k
	}
	ep, ok := kv["endpoint"]
	if !ok || ep == "" {
		return configErrorf("The [Peer] section has no Endpoint (the VPN server's address and port, like vpn.example.com:51820).")
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil || host == "" {
		return configErrorf("The Endpoint %q isn't in the form address:port, like vpn.example.com:51820.", ep)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return configErrorf("The Endpoint %q has a port that isn't from 1 to 65535.", ep)
	}
	if strings.ContainsAny(host, " /\\") {
		return configErrorf("The Endpoint %q isn't a valid address.", ep)
	}
	c.Endpoint, c.EndpointHost, c.EndpointPort = ep, host, n

	for _, a := range splitList(kv["allowedips"]) {
		p, err := parsePrefixOrAddr(a)
		if err != nil {
			return configErrorf("The AllowedIPs entry %q isn't an address range like 0.0.0.0/0.", a)
		}
		c.AllowedIPs = append(c.AllowedIPs, p)
	}
	c.Keepalive = DefaultKeepalive
	if ka, ok := kv["persistentkeepalive"]; ok {
		switch v := strings.ToLower(ka); v {
		case "off", "0", "":
			c.Notes = append(c.Notes, fmt.Sprintf("PersistentKeepalive is off in this config; Reelkeeper uses %d seconds, which keeps the connection status up to date.", DefaultKeepalive))
		default:
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 65535 {
				return configErrorf("The PersistentKeepalive %q isn't a number of seconds (like 25).", ka)
			}
			c.Keepalive = n
			if n > maxKeepalive {
				c.Keepalive = maxKeepalive
				c.Notes = append(c.Notes, fmt.Sprintf("PersistentKeepalive %d was lowered to %d seconds, which keeps the connection status up to date.", n, maxKeepalive))
			}
		}
	}
	return nil
}

func (c *WireGuardConfig) hasV4() bool {
	for _, a := range c.Addresses {
		if a.Addr().Is4() {
			return true
		}
	}
	return false
}

func (c *WireGuardConfig) hasV6() bool {
	for _, a := range c.Addresses {
		if a.Addr().Is6() {
			return true
		}
	}
	return false
}

// checkCoverage warns when AllowedIPs wouldn't send everything through the VPN.
func (c *WireGuardConfig) checkCoverage() {
	all4, all6 := false, false
	for _, a := range c.AllowedIPs {
		if a.Bits() == 0 && a.Addr().Is4() {
			all4 = true
		}
		if a.Bits() == 0 && a.Addr().Is6() {
			all6 = true
		}
	}
	if !all4 || (c.hasV6() && !all6) {
		c.Warnings = append(c.Warnings, partialWarning)
	}
}

// isSettingName tells a setting's name from other text: names are short words.
// (A line holding just a base64 key has its "=" padding where a setting's
// would be, and what comes before it must not be taken for a name.)
func isSettingName(s string) bool {
	if s == "" || len(s) > 24 {
		return false
	}
	for i, r := range s {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// splitList splits "a, b,c" into its items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePrefixOrAddr reads 10.2.0.2/32, or a bare address (all bits).
func parsePrefixOrAddr(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		// Keep the host part: the address the VPN gave us, not its network.
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// decodeKey reads a base64 WireGuard key. The error never includes the key.
func decodeKey(s string) ([32]byte, error) {
	var k [32]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return k, errors.New("not base64")
	}
	if len(b) != 32 {
		return k, errors.New("wrong length")
	}
	copy(k[:], b)
	return k, nil
}
