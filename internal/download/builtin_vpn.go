package download

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// The built-in client's VPN, from the host's side: which network the engine
// gets, what the status page shows, and when people are told the VPN is down.
//
// THE KILL SWITCH lives in builtin/vpn.go and builtin/run.go. What matters
// here is that there is no road from a VPN setup to the computer's own network:
//
//   - With a VPN config the engine is given the VPN as its network, and nothing
//     else. If the VPN can't even start, there is no engine at all.
//   - A config that can't be read or used makes the engine start held (no
//     sockets, no torrents), never "unprotected".
//   - The only engine that ever gets the computer's network has no VPN config,
//     and either runs because the person said so ("Start without VPN") or has
//     never had a VPN.

// vpnDownNoticeAfter is how long the VPN is down before anybody is told.
const vpnDownNoticeAfter = 10 * time.Minute

type engineMode int

const (
	// modeOwn: the computer's own network; no VPN has been set up.
	modeOwn engineMode = iota
	// modeVPN: everything goes through the VPN.
	modeVPN
	// modeBlocked: a VPN is set up but can't be used (its config can't be read
	// or understood). The engine is held and has no network at all.
	modeBlocked
)

// enginePlan is how an engine is set up. A different plan means a new engine.
type enginePlan struct {
	mode      engineMode
	vpnText   string
	forwarded int
	// hold: no torrent runs (the VPN was removed and the person hasn't said
	// to go on without it, or the VPN can't be used).
	hold bool
	// problem says why, for the status page.
	problem string
}

// planFor works out how to run the engine for these settings. The second
// result is the parsed VPN config, for modeVPN.
func planFor(c settings.DownloadClient) (enginePlan, *builtin.WireGuardConfig) {
	if !c.HasVPN() {
		return enginePlan{mode: modeOwn, hold: c.VPNHeld}, nil
	}
	if _, problem := c.VPNInfo(); problem != "" {
		return enginePlan{mode: modeBlocked, hold: true, problem: problem}, nil
	}
	cfg, err := builtin.ParseWireGuard(c.VPNConfig)
	if err != nil {
		return enginePlan{mode: modeBlocked, hold: true, problem: err.Error()}, nil
	}
	return enginePlan{mode: modeVPN, vpnText: c.VPNConfig, forwarded: c.ForwardedPort}, cfg
}

// startVPNLocked starts the tunnel and the watch that tells people when it is
// down. The caller holds h.mu.
func (h *BuiltinHost) startVPNLocked(cfg *builtin.WireGuardConfig) (*builtin.VPNNetwork, error) {
	opts := builtin.VPNOptions{
		Logger: h.logger,
		// The one lookup outside the tunnel: the VPN server's own name.
		ResolveEndpoint: h.network.LookupHost,
	}
	if h.tuneVPN != nil {
		h.tuneVPN(&opts)
	}
	vpn, err := builtin.NewVPN(cfg, opts)
	if err != nil {
		return nil, err
	}
	sum := cfg.Summary()
	h.vpn, h.sum, h.ips = vpn, &sum, &ipState{now: h.ipClock}
	h.watch = newDownWatch(h.notifyAfter, h.sendVPNNotice)
	w := h.watch
	vpn.OnChange(func() { w.update(vpn.Usable()) })
	w.update(vpn.Usable())
	return vpn, nil
}

func (h *BuiltinHost) stopVPNLocked() {
	if h.watch != nil {
		h.watch.stop()
		h.watch = nil
	}
	if h.vpn != nil {
		_ = h.vpn.Close()
		h.vpn = nil
	}
	h.sum, h.ips = nil, nil
	h.plan = enginePlan{}
}

// SetNotifier connects notifications.
func (h *BuiltinHost) SetNotifier(fn func(notify.Event)) {
	h.mu.Lock()
	h.notify = fn
	h.mu.Unlock()
}

// vpnDownTitle is what the notice is filed under (an email about it isn't
// repeated for a while).
const vpnDownTitle = "Built-in client's VPN"

func (h *BuiltinHost) sendVPNNotice(down bool) {
	h.mu.Lock()
	fn := h.notify
	h.mu.Unlock()
	if fn == nil {
		return
	}
	if down {
		fn(notify.Event{Kind: notify.KindClientUnreachable, Title: vpnDownTitle,
			Message: "The built-in client's VPN is down; its downloads are stopped."})
		return
	}
	fn(notify.Event{Kind: notify.KindClientRestored, Title: vpnDownTitle,
		Message: "The built-in client's VPN is back; its downloads have started again."})
}

// downWatch tells people once when the VPN has been down for a while, and once
// when it is back.
type downWatch struct {
	after time.Duration
	send  func(down bool)

	mu      sync.Mutex
	timer   *time.Timer
	told    bool
	stopped bool
}

func newDownWatch(after time.Duration, send func(down bool)) *downWatch {
	return &downWatch{after: after, send: send}
}

// update is told whether the VPN can carry traffic right now.
func (w *downWatch) update(up bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	if up {
		if w.timer != nil {
			w.timer.Stop()
			w.timer = nil
		}
		if w.told {
			w.told = false
			go w.send(false)
		}
		return
	}
	if w.timer != nil || w.told {
		return
	}
	w.timer = time.AfterFunc(w.after, func() {
		w.mu.Lock()
		if w.stopped || w.timer == nil {
			w.mu.Unlock()
			return
		}
		w.timer, w.told = nil, true
		w.mu.Unlock()
		w.send(true)
	})
}

func (w *downWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

// ---- state and status

// The states of the built-in client's protection (BuiltinStatus.State).
const (
	// BuiltinUnprotected: no VPN; peers see the real address.
	BuiltinUnprotected = "unprotected"
	// BuiltinConnecting: the VPN is coming up; nothing runs yet.
	BuiltinConnecting = "connecting"
	// BuiltinConnected: the VPN is up.
	BuiltinConnected = "connected"
	// BuiltinDown: the VPN has dropped (or leaks); everything is stopped.
	BuiltinDown = "down"
	// BuiltinHeld: downloads are stopped until the person starts them without
	// the VPN, or fixes the VPN config.
	BuiltinHeld = "held"
)

// ErrNotBuiltin means the built-in client isn't the one in use.
var ErrNotBuiltin = errors.New("the built-in client isn't the download client")

// BuiltinState is the built-in client's protection state, cheap enough to ask
// on every Queue refresh (no lookups). It is "" when the built-in client isn't
// running or isn't wanted.
func (h *BuiltinHost) BuiltinState() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, _ := h.stateLocked()
	return st
}

// stateLocked is the state and, for the held ones, why.
func (h *BuiltinHost) stateLocked() (state, message string) {
	if h.eng == nil {
		if h.err != nil {
			return BuiltinDown, h.err.Error()
		}
		return "", ""
	}
	switch h.plan.mode {
	case modeVPN:
		st := h.vpn.Status()
		switch st.State {
		case builtin.VPNConnected:
			return BuiltinConnected, ""
		case builtin.VPNConnecting:
			return BuiltinConnecting, "The VPN is connecting. Downloads start when it is."
		}
		if st.Leak {
			return BuiltinDown, leakMessage
		}
		if st.Problem != "" {
			return BuiltinDown, "The VPN stopped working: " + st.Problem + "."
		}
		return BuiltinDown, "The VPN isn't connected, so downloads are stopped until it is."
	case modeBlocked:
		return BuiltinHeld, h.plan.problem
	}
	if h.plan.hold {
		return BuiltinHeld, "Downloads are stopped until you start them without the VPN."
	}
	return BuiltinUnprotected, ""
}

const leakMessage = "Peers would see your real IP: the VPN isn't hiding it."

// BuiltinStatus is what the status page shows.
type BuiltinStatus struct {
	State   string
	Message string
	// Since is when it got into this state (zero when not known).
	Since         time.Time
	LastHandshake time.Time
	// Endpoint, TunnelAddresses and DNS describe the VPN (empty without one).
	Endpoint        string
	TunnelAddresses []string
	DNS             []string
	DNSDefault      bool
	RxBytes         int64
	TxBytes         int64
	ForwardedPort   int
	// PublicIP is the address the computer's own network shows the world;
	// VPNIP the one the VPN shows (only looked up while the VPN is connected).
	PublicIP      *IPInfo
	PublicIPError string
	VPNIP         *IPInfo
	VPNIPError    string
	// Leak: the VPN shows the same address as the computer.
	Leak      bool
	CheckedAt time.Time
}

// Status describes the built-in client's protection, looking up the addresses
// the world sees when they are due (see ipState). refresh asks for new lookups
// now (at most once in 30 seconds).
func (h *BuiltinHost) Status(ctx context.Context, refresh bool) (BuiltinStatus, error) {
	if h == nil {
		return BuiltinStatus{}, ErrNotBuiltin
	}
	h.mu.Lock()
	state, msg := h.stateLocked()
	if state == "" {
		h.mu.Unlock()
		return BuiltinStatus{}, ErrNotBuiltin
	}
	st := BuiltinStatus{State: state, Message: msg, ForwardedPort: h.plan.forwarded}
	vpn, ips, sum := h.vpn, h.ips, h.sum
	direct, tunnel := h.traceDirect, h.traceTunnel
	network := h.network
	mode := h.plan.mode
	h.mu.Unlock()

	if sum != nil {
		st.Endpoint, st.TunnelAddresses, st.DNS, st.DNSDefault = sum.Endpoint, sum.TunnelAddresses, sum.DNS, sum.DNSDefault
	}
	if ips == nil {
		// Nothing to look up behind (a held engine has no VPN); the real
		// address is still worth showing.
		ips = h.sharedIPState()
	}
	if mode != modeBlocked {
		ips.look(ctx, refresh, ipLookups{network: network, vpn: vpn, direct: direct, tunnel: tunnel})
		ips.fill(&st)
		// What the lookups found out can close the gate (the VPN shows the
		// same address as the computer) or open it again.
		if vpn != nil {
			if leak, decided := ips.leak(); decided {
				vpn.SetLeak(leak)
			}
		}
	}
	if vpn != nil {
		h.mu.Lock()
		st.State, st.Message = h.stateLocked()
		h.mu.Unlock()
		v := vpn.Status()
		st.Since, st.LastHandshake, st.RxBytes, st.TxBytes, st.Leak = v.Since, v.LastHandshake, v.RxBytes, v.TxBytes, v.Leak
	}
	return st, nil
}

// sharedIPState is the lookups' memory for an engine with no VPN.
func (h *BuiltinHost) sharedIPState() *ipState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ownIPs == nil {
		h.ownIPs = &ipState{now: h.ipClock}
	}
	return h.ownIPs
}
