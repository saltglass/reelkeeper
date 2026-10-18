package settings_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// madeUpKey is a well-formed WireGuard key that belongs to nobody.
func madeUpKey(seed string) string {
	sum := sha256.Sum256([]byte("settings test " + seed))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func vpnText(seed string) string {
	return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.2.0.2/32\nDNS = 10.2.0.1\n\n[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.9:51820\n",
		madeUpKey(seed+"-priv"), madeUpKey(seed+"-pub"), madeUpKey(seed+"-psk"))
}

func TestBuiltinVPNConfigIsSealedAndNeverInTheJSON(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		folder := t.TempDir()
		text := vpnText("a")
		got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, VPNConfig: &text, ForwardedPort: ptr(51413)})
		if err != nil {
			t.Fatal(err)
		}
		if got.VPNConfig != text || !got.HasVPN() || got.ForwardedPort != 51413 || got.VPNHeld {
			t.Fatalf("saved = %+v", got)
		}
		// Encrypted in the database: no part of any key is stored in the clear.
		var stored string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadClient).Scan(ctx, &stored); err != nil {
			t.Fatal(err)
		}
		stored = strings.ReplaceAll(stored, `": "`, `":"`)
		for _, k := range []string{"priv", "pub", "psk"} {
			if strings.Contains(stored, madeUpKey("a-" + k)[:16]) {
				t.Errorf("the %s key is in the database in the clear: %s", k, stored)
			}
		}
		if strings.Contains(stored, "PrivateKey") || strings.Contains(stored, "203.0.113.9") || !strings.Contains(stored, `"vpnConfigSealed":"v1:`) {
			t.Errorf("stored = %s", stored)
		}
		// Never in the JSON (which is what the browser would get).
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), "PrivateKey") || strings.Contains(string(b), madeUpKey("a-priv")[:16]) {
			t.Errorf("the VPN config is in the JSON: %s", b)
		}
		// Back after a restart, and kept by a save that doesn't mention it.
		again := newService(t, db, testKey)
		if c := again.DownloadClient(); c.VPNConfig != text || c.ForwardedPort != 51413 || c.VPNUnreadable {
			t.Errorf("after a restart = %+v", c)
		}
		if c, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, UploadLimitKBs: ptr(9)}); err != nil || c.VPNConfig != text || c.UploadLimitKBs != 9 {
			t.Errorf("an unrelated save lost the VPN: %+v %v", c, err)
		}
		// The listen port isn't checked behind a VPN (the forwarded port is used).
		if _, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, ListenPort: ptr(80)}); err != nil {
			t.Errorf("listen port with a VPN: %v", err)
		}
		// A replacement is stored; a bad one is refused with a field error and changes nothing.
		next := vpnText("b")
		if c, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &next}); err != nil || c.VPNConfig != next {
			t.Errorf("replacing = %+v %v", c, err)
		}
		bad := strings.Replace(next, "Endpoint", "Nowhere", 1)
		_, err = again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &bad})
		fe := fieldErrors(t, err)
		if msg := fe["vpnConfig"]; !strings.Contains(msg, "Endpoint") || strings.Contains(msg, madeUpKey("b-priv")) {
			t.Errorf("errors = %v", fe)
		}
		if again.DownloadClient().VPNConfig != next {
			t.Error("a refused config replaced the good one")
		}
		_, err = again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, ForwardedPort: ptr(70000)})
		if fe := fieldErrors(t, err); fe["forwardedPort"] == "" {
			t.Errorf("errors = %v", fe)
		}
	})
}

func TestRemovingTheVPNNeedsConfirmationAndThenHoldsDownloads(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		folder := t.TempDir()
		text := vpnText("r")
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, VPNConfig: &text, ForwardedPort: ptr(40000)}); err != nil {
			t.Fatal(err)
		}
		none := ""
		// Not without saying yes.
		_, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none})
		if fe := fieldErrors(t, err); !strings.Contains(fe["confirmUnprotected"], "real IP") {
			t.Fatalf("errors = %v", fe)
		}
		if _, err := svc.ApplyDownloadClient(settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none}); err == nil {
			t.Error("the dry run let it through too")
		}
		if c := svc.DownloadClient(); c.VPNConfig != text || c.VPNHeld {
			t.Fatalf("a refused removal changed things: %+v", c)
		}
		// With it: the VPN goes, and downloads are held.
		c, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none, ConfirmUnprotected: true})
		if err != nil {
			t.Fatal(err)
		}
		if c.HasVPN() || !c.VPNHeld || c.ForwardedPort != 0 {
			t.Fatalf("after removing = %+v", c)
		}
		// The hold survives a restart, and an ordinary save doesn't release it.
		again := newService(t, db, testKey)
		if !again.DownloadClient().VPNHeld {
			t.Error("a restart released the hold")
		}
		if c, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, DownloadLimitKBs: ptr(5)}); err != nil || !c.VPNHeld {
			t.Errorf("a save released the hold: %+v %v", c, err)
		}
		var stored string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadClient).Scan(ctx, &stored); err != nil || strings.Contains(stored, "vpnConfigSealed\":\"v1") {
			t.Errorf("the removed VPN is still stored: %s %v", stored, err)
		}
		// "Start without VPN".
		c, err = again.ReleaseVPNHold(ctx)
		if err != nil || c.VPNHeld {
			t.Fatalf("release = %+v %v", c, err)
		}
		if newService(t, db, testKey).DownloadClient().VPNHeld {
			t.Error("the release wasn't saved")
		}
		// Removing when there is no VPN needs no confirmation and holds nothing.
		if c, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none}); err != nil || c.VPNHeld {
			t.Errorf("removing nothing = %+v %v", c, err)
		}
		// Releasing is refused while a VPN is set up.
		if _, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &text}); err != nil {
			t.Fatal(err)
		}
		if _, err := again.ReleaseVPNHold(ctx); err == nil {
			t.Error("released the hold with a VPN set up")
		}
		// Adding a VPN again lifts a hold by itself (it is what the hold waits for).
		if _, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none, ConfirmUnprotected: true}); err != nil {
			t.Fatal(err)
		}
		if c, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &text}); err != nil || c.VPNHeld || !c.HasVPN() {
			t.Errorf("adding it back = %+v %v", c, err)
		}
	})
}

// If the secret key changes, the stored VPN can't be read. That must never
// look like "no VPN", or the client would start downloading unprotected.
func TestAnUnreadableVPNConfigIsNotTakenForNoVPN(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		folder := t.TempDir()
		text := vpnText("u")
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, VPNConfig: &text}); err != nil {
			t.Fatal(err)
		}
		other := newService(t, db, "another-secret-key-0123456789")
		c := other.DownloadClient()
		if c.VPNConfig != "" || !c.VPNUnreadable || !c.HasVPN() {
			t.Fatalf("with the wrong key = %+v", c)
		}
		if _, err := other.ReleaseVPNHold(ctx); err == nil {
			t.Error("the hold could be released without ever removing the unreadable VPN")
		}
		// A save that doesn't touch the VPN writes the unreadable value back as it was.
		if _, err := other.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, UploadLimitKBs: ptr(7)}); err != nil {
			t.Fatal(err)
		}
		if back := newService(t, db, testKey).DownloadClient(); back.VPNConfig != text || back.UploadLimitKBs != 7 {
			t.Errorf("restoring the key = %+v", back)
		}
		// Removing it takes the confirmation, as for a readable one.
		none := ""
		if _, err := other.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none}); err == nil {
			t.Error("removed an unreadable VPN without confirmation")
		}
		c, err := other.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, VPNConfig: &none, ConfirmUnprotected: true})
		if err != nil || c.HasVPN() || !c.VPNHeld {
			t.Errorf("removed = %+v %v", c, err)
		}
	})
}

func TestMovingAwayFromTheBuiltinClientDropsItsVPN(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		folder := t.TempDir()
		text := vpnText("m")
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, VPNConfig: &text, ForwardedPort: ptr(1234)}); err != nil {
			t.Fatal(err)
		}
		old := svc.DownloadClient()
		if err := svc.SaveDownloadMove(ctx, settings.DownloadMove{Active: true, Old: old}); err != nil {
			t.Fatal(err)
		}
		c, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientTransmission, URL: "http://tm:9091"})
		if err != nil || c.HasVPN() || c.ForwardedPort != 0 || c.VPNHeld {
			t.Fatalf("transmission = %+v %v", c, err)
		}
		// The move still has the old client's VPN, encrypted, until it ends.
		mv := newService(t, db, testKey).DownloadMove()
		if mv.Old.VPNConfig != text || mv.Old.ForwardedPort != 1234 {
			t.Errorf("the move lost the old VPN: %+v", mv.Old)
		}
		var stored string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadMove).Scan(ctx, &stored); err != nil || strings.Contains(stored, "PrivateKey") || strings.Contains(stored, madeUpKey("m-priv")[:16]) {
			t.Errorf("stored move = %s %v", stored, err)
		}
		wrong := newService(t, db, "another-secret-key-0123456789").DownloadMove()
		if !wrong.Old.VPNUnreadable || wrong.Old.VPNConfig != "" {
			t.Errorf("with the wrong key = %+v", wrong.Old)
		}
		if err := svc.ClearDownloadMove(ctx); err != nil {
			t.Fatal(err)
		}
		if mv := newService(t, db, testKey).DownloadMove(); mv.Old.HasVPN() {
			t.Errorf("after the move: %+v", mv.Old)
		}
	})
}

// A VPN config saved before the folder is filled in used to vanish: with no
// folder the client counts as not set up, and the save reset it.
func TestAVPNWithoutAFolderIsRefusedNotDropped(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		text := vpnText("nf")
		_, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, VPNConfig: &text})
		if fe := fieldErrors(t, err); !strings.Contains(fe["folder"], "download folder first") {
			t.Fatalf("errors = %v", fe)
		}
		folder := t.TempDir()
		c, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, VPNConfig: &text})
		if err != nil || !c.HasVPN() {
			t.Fatalf("with a folder: %+v, %v", c, err)
		}
	})
}
