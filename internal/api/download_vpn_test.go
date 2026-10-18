package api_test

// The built-in client's VPN through the API. No test here reaches beyond
// 127.0.0.1: the VPN, where there is one, is pointed at a loopback port nobody
// listens on (so it stays "connecting"), and the "what is my IP" page is a stub.

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func init() {
	// A test that forgets to point the IP check at a stub still can't reach
	// Cloudflare: nothing listens on port 1.
	download.TraceURL = "http://127.0.0.1:1/cdn-cgi/trace"
}

func apiKey(seed string) string {
	sum := sha256.Sum256([]byte("api vpn test " + seed))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func apiVPNConfig(seed, endpoint string) string {
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.2.0.2/32\nDNS = 10.2.0.1\n\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = %s\n",
		apiKey(seed+"-priv"), apiKey(seed+"-pub"), apiKey(seed+"-psk"), endpoint)
}

// jsonText escapes a config file for a JSON body.
func jsonText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n").Replace(s)
}

// noKeys fails the test if any part of the made-up keys is in the text.
func noKeys(t *testing.T, what, text string, seeds ...string) {
	t.Helper()
	for _, seed := range seeds {
		for _, k := range []string{"priv", "pub", "psk"} {
			key := apiKey(seed + "-" + k)
			// Whole, and in pieces (a key is never shown, even in part).
			for _, part := range []string{key, key[:12], key[len(key)-12:], key[10:22]} {
				if strings.Contains(text, part) {
					t.Errorf("%s contains part of the %s key of %s: %q", what, k, seed, part)
				}
			}
		}
	}
}

func TestBuiltinVPNConfigThroughTheAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	host := download.NewBuiltinHost(t.TempDir(), slog.New(slog.DiscardHandler))
	t.Cleanup(host.Close)
	a.downloads.SetBuiltin(host)
	apply := func() { host.Apply(a.settings.DownloadClient(), a.settings.DownloadMove()) }
	folder := t.TempDir()
	// The VPN server is a loopback port with nothing on it.
	endpoint := fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t))
	text := apiVPNConfig("one", endpoint)

	// Reading a file without saving it: what it holds, no secrets.
	r := a.do("POST", "/api/v1/settings/download-client/vpn/check", `{"config":"`+jsonText(text)+`"}`)
	if r.status != 200 || r.body["endpoint"] != endpoint || r.body["hasPresharedKey"] != true || r.body["dnsDefault"] != false || r.body["keepalive"] != 25.0 ||
		len(r.body["publicKey"].(string)) != 44 {
		t.Fatalf("check: %d %s", r.status, r.raw)
	}
	noKeys(t, "the check answer", r.raw, "one")
	if r.body["publicKey"] == apiKey("one-priv") {
		t.Error("the 'public key' is the private key")
	}
	for _, bad := range []string{"", "client\\ndev tun\\n", "[Interface]\\nPrivateKey = " + apiKey("bad-priv")} {
		r = a.do("POST", "/api/v1/settings/download-client/vpn/check", `{"config":"`+bad+`"}`)
		if r.status != 422 || !strings.Contains(r.raw, `"config"`) {
			t.Errorf("check %q: %d %s", bad, r.status, r.raw)
		}
		noKeys(t, "the check error", r.raw, "bad")
	}

	// Saving it: the answer says what it holds, never the config.
	body := fmt.Sprintf(`{"type":"builtin","url":"","username":"","folder":%q,"vpnConfig":"%s","forwardedPort":51413}`, folder, jsonText(text))
	r = a.do("POST", "/api/v1/settings/download-client/test", body)
	if r.status != 200 {
		t.Errorf("test with a VPN: %d %s", r.status, r.raw)
	}
	noKeys(t, "the test answer", r.raw, "one")
	r = a.do("PUT", "/api/v1/settings/download-client", body)
	if r.status != 200 || r.body["hasVpn"] != true || r.body["vpnHeld"] != false || r.body["forwardedPort"] != 51413.0 || r.body["vpnProblem"] != nil {
		t.Fatalf("save: %d %s", r.status, r.raw)
	}
	noKeys(t, "the save answer", r.raw, "one")
	vpn, _ := r.body["vpn"].(map[string]any)
	if vpn["endpoint"] != endpoint || vpn["tunnelAddresses"].([]any)[0] != "10.2.0.2/32" || vpn["dns"].([]any)[0] != "10.2.0.1" {
		t.Errorf("vpn summary = %v", vpn)
	}
	r = a.do("GET", "/api/v1/settings/download-client", "")
	if r.status != 200 || r.body["hasVpn"] != true {
		t.Errorf("get: %d %s", r.status, r.raw)
	}
	noKeys(t, "GET download client", r.raw, "one")
	// An unrelated save keeps it; a bad config is refused and changes nothing.
	r = a.do("PUT", "/api/v1/settings/download-client", fmt.Sprintf(`{"type":"builtin","url":"","username":"","folder":%q,"uploadLimitKBs":50}`, folder))
	if r.status != 200 || r.body["hasVpn"] != true || r.body["uploadLimitKBs"] != 50.0 {
		t.Errorf("unrelated save: %d %s", r.status, r.raw)
	}
	r = a.do("PUT", "/api/v1/settings/download-client", fmt.Sprintf(`{"type":"builtin","url":"","username":"","folder":%q,"vpnConfig":"[Interface]\nPrivateKey = %s\n"}`, folder, apiKey("two-priv")))
	if r.status != 422 || !strings.Contains(r.raw, `"vpnConfig"`) {
		t.Errorf("bad config: %d %s", r.status, r.raw)
	}
	noKeys(t, "the bad config answer", r.raw, "two")
	if r := a.do("PUT", "/api/v1/settings/download-client", fmt.Sprintf(`{"type":"builtin","url":"","username":"","folder":%q,"forwardedPort":70000}`, folder)); r.status != 422 || !strings.Contains(r.raw, `"forwardedPort"`) {
		t.Errorf("bad port: %d %s", r.status, r.raw)
	}

	// With the engine running behind it: connecting, and the Queue says so.
	apply()
	st := a.do("GET", "/api/v1/settings/download-client/builtin/status", "")
	if st.status != 200 || st.body["state"] != "connecting" || st.body["endpoint"] != endpoint || st.body["forwardedPort"] != 51413.0 || st.body["vpnIP"] != nil || st.body["leak"] != false {
		t.Fatalf("status: %d %s", st.status, st.raw)
	}
	noKeys(t, "the status", st.raw, "one")
	if q := a.do("GET", "/api/v1/queue", ""); q.status != 200 || q.body["builtinState"] != "connecting" {
		t.Errorf("queue: %d %s", q.status, q.raw)
	}
	// "Start without VPN" is refused while there is a VPN.
	if r := a.do("POST", "/api/v1/settings/download-client/builtin/start-unprotected", ""); r.status != 422 {
		t.Errorf("start without VPN with a VPN: %d %s", r.status, r.raw)
	}

	// Removing it: not without saying yes; then downloads are held.
	rm := fmt.Sprintf(`{"type":"builtin","url":"","username":"","folder":%q,"vpnConfig":""}`, folder)
	r = a.do("PUT", "/api/v1/settings/download-client", rm)
	if r.status != 422 || !strings.Contains(r.raw, `"confirmUnprotected"`) || !strings.Contains(r.raw, "real IP") {
		t.Fatalf("removing without confirming: %d %s", r.status, r.raw)
	}
	if got := a.do("GET", "/api/v1/settings/download-client", ""); got.body["hasVpn"] != true {
		t.Error("a refused removal removed it")
	}
	r = a.do("PUT", "/api/v1/settings/download-client", strings.TrimSuffix(rm, "}")+`,"confirmUnprotected":true}`)
	if r.status != 200 || r.body["hasVpn"] != false || r.body["vpnHeld"] != true || r.body["vpn"] != nil || r.body["forwardedPort"] != 0.0 {
		t.Fatalf("removing: %d %s", r.status, r.raw)
	}
	apply()

	// The status shows it, with the real address from the stub page.
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "fl=1\nip=198.51.100.9\nloc=US\n")
	}))
	defer page.Close()
	oldTrace := download.TraceURL
	download.TraceURL = page.URL
	defer func() { download.TraceURL = oldTrace }()
	st = a.do("GET", "/api/v1/settings/download-client/builtin/status", "")
	pub, _ := st.body["publicIP"].(map[string]any)
	if st.status != 200 || st.body["state"] != "held" || !strings.Contains(fmt.Sprint(st.body["message"]), "Downloads are stopped until you start them without the VPN") ||
		pub["ip"] != "198.51.100.9" || pub["country"] != "US" || st.body["vpnIP"] != nil {
		t.Fatalf("held status: %d %s", st.status, st.raw)
	}
	if q := a.do("GET", "/api/v1/queue", ""); q.body["builtinState"] != "held" {
		t.Errorf("queue: %s", q.raw)
	}

	// Start without VPN.
	r = a.do("POST", "/api/v1/settings/download-client/builtin/start-unprotected", "")
	if r.status != 200 || r.body["vpnHeld"] != false || r.body["hasVpn"] != false {
		t.Fatalf("start without VPN: %d %s", r.status, r.raw)
	}
	apply()
	st = a.do("GET", "/api/v1/settings/download-client/builtin/status?refresh=true", "")
	if st.status != 200 || st.body["state"] != "unprotected" {
		t.Errorf("unprotected status: %d %s", st.status, st.raw)
	}
	if q := a.do("GET", "/api/v1/queue", ""); q.body["builtinState"] != "unprotected" {
		t.Errorf("queue: %s", q.raw)
	}

	// Another client: no status, and the Queue says nothing about the built-in one.
	a.do("PUT", "/api/v1/settings/download-client", `{"type":"transmission","url":"http://tm:9091","username":""}`)
	apply()
	if r := a.do("GET", "/api/v1/settings/download-client/builtin/status", ""); r.status != 404 {
		t.Errorf("status for another client: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/settings/download-client/builtin/start-unprotected", ""); r.status != 422 {
		t.Errorf("start without VPN for another client: %d %s", r.status, r.raw)
	}
}

func TestBuiltinVPNEndpointsNeedASignIn(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	// A client with no session or key.
	anon := *a
	anon.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range [][2]string{
		{"GET", "/api/v1/settings/download-client/builtin/status"},
		{"POST", "/api/v1/settings/download-client/builtin/start-unprotected"},
		{"POST", "/api/v1/settings/download-client/vpn/check"},
	} {
		if r := anon.do(c[0], c[1], ""); r.status != 401 {
			t.Errorf("%s %s without signing in: %d", c[0], c[1], r.status)
		}
	}
}
