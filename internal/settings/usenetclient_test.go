package settings_test

import (
	"context"
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
