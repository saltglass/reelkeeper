package settings_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestUsenetClientSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.UsenetClient(); got.Configured() || got.ClientType() != settings.UsenetSABnzbd || got.ClientName() != "SABnzbd" {
			t.Errorf("default = %+v", got)
		}
		key := "0123456789abcdef0123456789abcdef"
		saved, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: " http://192.168.1.20:8085/ ", APIKey: &key})
		if err != nil || saved.URL != "http://192.168.1.20:8085" || saved.APIKey != key {
			t.Fatalf("save = %+v, %v", saved, err)
		}
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupUsenetClient).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, key) {
			t.Error("the API key must only be stored encrypted")
		}
		// The torrent client is a separate setting, untouched.
		if svc.DownloadClient().Configured() {
			t.Error("saving the Usenet client set up a torrent client")
		}
		// A restart reads it back; saving without a key keeps it.
		again := newService(t, db, testKey)
		if got := again.UsenetClient(); got.APIKey != key || got.URL != "http://192.168.1.20:8085" {
			t.Errorf("after restart: %+v", got)
		}
		if got, err := again.SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "http://sabnzbd:8080"}); err != nil || got.APIKey != key {
			t.Errorf("kept key: %+v, %v", got, err)
		}
		// Clearing the address removes it, key and all.
		if got, err := again.SaveUsenetClient(ctx, settings.UsenetClientUpdate{}); err != nil || got.Configured() || got.APIKey != "" {
			t.Errorf("cleared: %+v, %v", got, err)
		}
	})
}

func TestUsenetClientValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		empty := ""
		_, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "sabnzbd:8080", APIKey: &empty})
		if errs := fieldErrors(t, err); errs["url"] == "" || errs["apiKey"] == "" {
			t.Errorf("errors = %v", errs)
		}
		key := "k"
		_, err = svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: "nzbvortex", URL: "http://x:1", APIKey: &key})
		if errs := fieldErrors(t, err); errs["type"] == "" {
			t.Errorf("errors = %v", errs)
		}
	})
}

// A key that can't be decrypted (the secret key changed) is kept as it was,
// and the client still counts as set up, so saving the address alone works.
func TestUsenetClientKeyKeptWhenUnreadable(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	ctx := context.Background()
	key := "0123456789abcdef"
	if _, err := newService(t, db, testKey).SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "http://sab:8080", APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	other := newService(t, db, "a-completely-different-key")
	if got := other.UsenetClient(); got.APIKey != "" || !got.Configured() {
		t.Fatalf("unreadable key: %+v", got)
	}
	if _, err := other.SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "http://sab:8081"}); err != nil {
		t.Fatalf("saving without re-entering the key: %v", err)
	}
	if got := newService(t, db, testKey).UsenetClient(); got.APIKey != key || got.URL != "http://sab:8081" {
		t.Errorf("with the old secret key back: %+v", got)
	}
}

func TestNZBGetSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		pass := "s3cret-nzbget-pass"
		saved, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{
			Type: settings.UsenetNZBGet, URL: " http://192.168.1.20:6789/ ", Username: " nzbget ", Password: &pass})
		if err != nil || saved.URL != "http://192.168.1.20:6789" || saved.Username != "nzbget" || saved.Password != pass ||
			saved.ClientName() != "NZBGet" || saved.APIKey != "" {
			t.Fatalf("save = %+v, %v", saved, err)
		}
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupUsenetClient).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, pass) {
			t.Error("the password must only be stored encrypted")
		}
		// A restart reads it back; saving without a password keeps it.
		again := newService(t, db, testKey)
		if got := again.UsenetClient(); got.Type != settings.UsenetNZBGet || got.Password != pass || got.Username != "nzbget" {
			t.Errorf("after restart: %+v", got)
		}
		if got, err := again.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetNZBGet, URL: "http://nzbget:6789", Username: "nzbget"}); err != nil || got.Password != pass {
			t.Errorf("kept password: %+v, %v", got, err)
		}
		// Clearing the address removes it, login and all.
		if got, err := again.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetNZBGet}); err != nil || got.Configured() || got.Password != "" || got.Username != "" {
			t.Errorf("cleared: %+v, %v", got, err)
		}
	})
}

func TestNZBGetValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		empty := ""
		_, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetNZBGet, URL: "nzbget:6789", Password: &empty})
		if errs := fieldErrors(t, err); errs["url"] == "" || errs["username"] == "" || errs["password"] == "" || errs["apiKey"] != "" {
			t.Errorf("errors = %v", errs)
		}
		if errs := fieldErrors(t, err); !strings.Contains(errs["url"], "NZBGet") {
			t.Errorf("the address prompt should name NZBGet: %q", errs["url"])
		}
		// SABnzbd still needs its key, and not a login.
		_, err = svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetSABnzbd, URL: "http://sab:8080", Username: "x", Password: &empty})
		if errs := fieldErrors(t, err); errs["apiKey"] == "" || errs["username"] != "" || errs["password"] != "" {
			t.Errorf("errors = %v", errs)
		}
	})
}

// Switching programs drops the secrets of the old one: a SABnzbd API key is
// no use to NZBGet, and a NZBGet password must not hang about in SABnzbd's
// settings.
func TestUsenetClientTypeChangeDropsSecrets(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		key := "0123456789abcdef"
		if _, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "http://sab:8080", APIKey: &key}); err != nil {
			t.Fatal(err)
		}
		// To NZBGet without a password: refused, since the key doesn't carry over.
		_, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetNZBGet, URL: "http://nzbget:6789", Username: "nzbget"})
		if errs := fieldErrors(t, err); errs["password"] == "" {
			t.Errorf("errors = %v", errs)
		}
		pass := "nzbget-pass"
		got, err := svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetNZBGet, URL: "http://nzbget:6789", Username: "nzbget", Password: &pass})
		if err != nil || got.APIKey != "" || got.Password != pass {
			t.Fatalf("to NZBGet: %+v, %v", got, err)
		}
		// And back: the password is gone, the key must be entered again.
		_, err = svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetSABnzbd, URL: "http://sab:8080"})
		if errs := fieldErrors(t, err); errs["apiKey"] == "" {
			t.Errorf("errors = %v", errs)
		}
		got, err = svc.SaveUsenetClient(ctx, settings.UsenetClientUpdate{Type: settings.UsenetSABnzbd, URL: "http://sab:8080", APIKey: &key})
		if err != nil || got.Password != "" || got.Username != "" || got.APIKey != key {
			t.Fatalf("back to SABnzbd: %+v, %v", got, err)
		}
		if again := newService(t, db, testKey).UsenetClient(); again.Password != "" || again.Username != "" || again.APIKey != key {
			t.Errorf("after restart: %+v", again)
		}
	})
}

// Settings saved before NZBGet existed have no type, no username and no
// password, and still read as SABnzbd.
func TestOlderSABnzbdSettingsStillLoad(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		key := "0123456789abcdef"
		if _, err := newService(t, db, testKey).SaveUsenetClient(ctx, settings.UsenetClientUpdate{URL: "http://sab:8080", APIKey: &key}); err != nil {
			t.Fatal(err)
		}
		// Rewrite the stored value the way Part A saved it: no type, no login fields.
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupUsenetClient).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "type")
		delete(m, "username")
		delete(m, "passwordSealed")
		old, _ := json.Marshal(m)
		if _, err := db.DB.NewRaw("UPDATE settings SET value = ? WHERE key = ?", string(old), settings.GroupUsenetClient).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		got := newService(t, db, testKey).UsenetClient()
		if got.ClientType() != settings.UsenetSABnzbd || got.Type != settings.UsenetSABnzbd || got.APIKey != key || got.URL != "http://sab:8080" {
			t.Errorf("loaded: %+v", got)
		}
	})
}
