package settings_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

const testKey = "test-secret-key-0123456789"

func newService(t *testing.T, db *store.Store, key string) *settings.Service {
	t.Helper()
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := settings.New(context.Background(), db, box, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// validUpdate returns an update that changes nothing about the defaults.
func validUpdate(sec settings.Security) settings.SecurityUpdate {
	return settings.SecurityUpdate{
		LocalLoginEnabled: sec.LocalLoginEnabled,
		BypassNetworks:    sec.BypassNetworks,
		TrustedProxies:    sec.TrustedProxies,
		SessionDays:       sec.SessionDays,
		OIDC: settings.OIDCUpdate{
			Enabled:       sec.OIDC.Enabled,
			IssuerURL:     sec.OIDC.IssuerURL,
			ClientID:      sec.OIDC.ClientID,
			Scopes:        sec.OIDC.Scopes,
			GroupsClaim:   sec.OIDC.GroupsClaim,
			RequiredGroup: sec.OIDC.RequiredGroup,
			ButtonLabel:   sec.OIDC.ButtonLabel,
		},
	}
}

func fieldErrors(t *testing.T, err error) validation.Errors {
	t.Helper()
	errs, ok := validation.As(err)
	if !ok {
		t.Fatalf("expected validation errors, got %v", err)
	}
	return errs
}

func TestDefaultsOnFirstStart(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		if g := svc.General(); g != settings.DefaultGeneral() {
			t.Errorf("general = %+v", g)
		}
		sec := svc.Security()
		if !sec.LocalLoginEnabled || sec.SessionDays != 30 || sec.OIDC.Enabled {
			t.Errorf("security defaults = %+v", sec)
		}
		if len(sec.APIKey) != 32 {
			t.Errorf("API key %q should be generated on first start", sec.APIKey)
		}
	})
}

func TestGeneralRoundTripAndValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)

		var changed []string
		svc.OnChange(func(g string) { changed = append(changed, g) })

		saved, err := svc.SaveGeneral(ctx, settings.General{InstanceName: "  Home  ", LogLevel: "debug"})
		if err != nil {
			t.Fatal(err)
		}
		if saved.InstanceName != "Home" {
			t.Errorf("name should be trimmed, got %q", saved.InstanceName)
		}
		if len(changed) != 1 || changed[0] != settings.GroupGeneral {
			t.Errorf("listeners got %v", changed)
		}

		// A fresh service (as after a restart) reads the same values.
		if g := newService(t, db, testKey).General(); g != saved {
			t.Errorf("after restart: %+v, want %+v", g, saved)
		}

		_, err = svc.SaveGeneral(ctx, settings.General{InstanceName: "", LogLevel: "loud"})
		errs := fieldErrors(t, err)
		if errs["instanceName"] == "" || errs["logLevel"] == "" {
			t.Errorf("expected both fields to be flagged: %v", errs)
		}
		if svc.General() != saved {
			t.Error("a rejected save must not change anything")
		}
	})
}

func TestAPIKeyEncryptedAndStable(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		key := svc.Security().APIKey

		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = 'security'").Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, key) {
			t.Error("the API key must not be stored in plain text")
		}

		if again := newService(t, db, testKey).Security().APIKey; again != key {
			t.Errorf("API key changed across restart: %q -> %q", key, again)
		}

		newKey, err := svc.RegenerateAPIKey(ctx)
		if err != nil || newKey == key || len(newKey) != 32 {
			t.Errorf("regenerate: %q, %v", newKey, err)
		}
		if svc.Security().APIKey != newKey {
			t.Error("regenerated key not in effect")
		}
	})
}

func TestNetworksNormalized(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		u := validUpdate(svc.Security())
		u.BypassNetworks = []string{" 192.168.1.7/24 ", "", "10.0.0.5", "192.168.1.0/24", "::ffff:10.0.0.9", "fd00::1"}
		u.TrustedProxies = []string{"172.16.0.2"}
		sec, err := svc.SaveSecurity(context.Background(), u)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"192.168.1.0/24", "10.0.0.5/32", "10.0.0.9/32", "fd00::1/128"}
		if strings.Join(sec.BypassNetworks, ",") != strings.Join(want, ",") {
			t.Errorf("bypass = %v, want %v", sec.BypassNetworks, want)
		}
		if strings.Join(sec.TrustedProxies, ",") != "172.16.0.2/32" {
			t.Errorf("proxies = %v", sec.TrustedProxies)
		}
	})
}

func TestSecurityValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*settings.SecurityUpdate)
		field  string
	}{
		{"bad address", func(u *settings.SecurityUpdate) { u.BypassNetworks = []string{"10.0.0.300"} }, "bypassNetworks.0"},
		{"bad range", func(u *settings.SecurityUpdate) { u.TrustedProxies = []string{"10.0.0.0/33"} }, "trustedProxies.0"},
		{"bypass everything", func(u *settings.SecurityUpdate) { u.BypassNetworks = []string{"0.0.0.0/0"} }, "bypassNetworks.0"},
		{"bypass everything v6", func(u *settings.SecurityUpdate) { u.BypassNetworks = []string{"::/0"} }, "bypassNetworks.0"},
		{"bypass covers proxy (Traefik trap)", func(u *settings.SecurityUpdate) {
			u.TrustedProxies = []string{"172.16.0.2"}
			u.BypassNetworks = []string{"10.0.0.0/24", "172.16.0.0/24"}
		}, "bypassNetworks.1"},
		{"bypass equals proxy", func(u *settings.SecurityUpdate) {
			u.TrustedProxies = []string{"172.16.0.2"}
			u.BypassNetworks = []string{"172.16.0.2"}
		}, "bypassNetworks.0"},
		{"session too short", func(u *settings.SecurityUpdate) { u.SessionDays = 0 }, "sessionDays"},
		{"session too long", func(u *settings.SecurityUpdate) { u.SessionDays = 366 }, "sessionDays"},
		{"oidc without issuer", func(u *settings.SecurityUpdate) {
			u.OIDC.Enabled, u.OIDC.ClientID = true, "reelkeeper"
		}, "oidc.issuerUrl"},
		{"oidc over http", func(u *settings.SecurityUpdate) {
			u.OIDC.Enabled, u.OIDC.ClientID, u.OIDC.IssuerURL = true, "reelkeeper", "http://auth.example.com/"
		}, "oidc.issuerUrl"},
		{"oidc without client id", func(u *settings.SecurityUpdate) {
			u.OIDC.Enabled, u.OIDC.IssuerURL = true, "https://auth.example.com/application/o/reelkeeper/"
		}, "oidc.clientId"},
		{"local login off without oidc", func(u *settings.SecurityUpdate) { u.LocalLoginEnabled = false }, "localLoginEnabled"},
		{"local login off before oidc ever worked", func(u *settings.SecurityUpdate) {
			u.LocalLoginEnabled = false
			u.OIDC.Enabled, u.OIDC.ClientID, u.OIDC.IssuerURL = true, "reelkeeper", "https://auth.example.com/application/o/reelkeeper/"
		}, "localLoginEnabled"},
		{"too many entries", func(u *settings.SecurityUpdate) {
			u.BypassNetworks = make([]string, 101)
			for i := range u.BypassNetworks {
				u.BypassNetworks[i] = "10.1.0.1"
			}
		}, "bypassNetworks"},
	}
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		before := svc.Security()
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				u := validUpdate(before)
				c.change(&u)
				_, err := svc.SaveSecurity(context.Background(), u)
				errs := fieldErrors(t, err)
				if errs[c.field] == "" {
					t.Errorf("expected a problem with %s, got %v", c.field, errs)
				}
			})
		}
		after := svc.Security()
		if after.LocalLoginEnabled != before.LocalLoginEnabled || len(after.BypassNetworks) != 0 {
			t.Error("rejected saves must not change anything")
		}
	})
}

func TestHTTPIssuerAllowedOnLoopback(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		for _, issuer := range []string{"http://localhost:9000/", "http://127.0.0.1:9000/", "http://[::1]:9000/"} {
			u := validUpdate(svc.Security())
			u.OIDC.Enabled, u.OIDC.ClientID, u.OIDC.IssuerURL = true, "reelkeeper", issuer
			if _, err := svc.SaveSecurity(context.Background(), u); err != nil {
				t.Errorf("%s: %v", issuer, err)
			}
		}
	})
}

// Password sign-in can be turned off only after SSO has worked, and that
// proof is forgotten when the provider changes.
func TestDisableLocalLoginFlow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)

		secret := "client-secret-value"
		u := validUpdate(svc.Security())
		u.OIDC = settings.OIDCUpdate{
			Enabled: true, IssuerURL: "https://auth.example.com/application/o/reelkeeper/",
			ClientID: "reelkeeper", ClientSecret: &secret, Scopes: []string{"profile", "groups openid"},
		}
		sec, err := svc.SaveSecurity(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(sec.OIDC.Scopes, " ") != "openid profile groups" {
			t.Errorf("scopes = %v", sec.OIDC.Scopes)
		}
		if sec.OIDC.GroupsClaim != "groups" || sec.OIDC.ButtonLabel != settings.DefaultButtonLabel {
			t.Errorf("defaults not filled: %+v", sec.OIDC)
		}

		if err := svc.RecordOIDCSuccess(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		u.LocalLoginEnabled = false
		u.OIDC.ClientSecret = nil // keep the stored secret
		sec, err = svc.SaveSecurity(ctx, u)
		if err != nil {
			t.Fatalf("should be allowed after a successful SSO sign-in: %v", err)
		}
		if sec.LocalLoginEnabled || sec.OIDC.ClientSecret != secret {
			t.Errorf("unexpected %+v", sec)
		}

		// Switching to another client while password sign-in is off is refused.
		u.OIDC.ClientID = "other-client"
		_, err = svc.SaveSecurity(ctx, u)
		if fieldErrors(t, err)["localLoginEnabled"] == "" {
			t.Error("changing the client must require a new successful sign-in")
		}

		// Recovery path (reelkeeper reset-auth).
		if err := svc.EnableLocalLogin(ctx); err != nil {
			t.Fatal(err)
		}
		if !svc.Security().LocalLoginEnabled {
			t.Error("EnableLocalLogin did not take effect")
		}
	})
}

// If the secret key changes, credentials become unreadable but aren't
// destroyed: saving other settings keeps them, and the old key still works.
func TestChangedSecretKeyKeepsCredentials(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t, store.SQLite)
	svc := newService(t, db, testKey)
	originalKey := svc.Security().APIKey

	other := newService(t, db, "a-completely-different-key")
	if got := other.UnreadableCredentials(); len(got) != 1 || got[0] != "apiKey" {
		t.Errorf("unreadable = %v", got)
	}
	if other.Security().APIKey != "" {
		t.Error("an unreadable API key must not be replaced by a guess")
	}
	if _, err := other.SaveGeneral(ctx, settings.General{InstanceName: "x", LogLevel: "info"}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.SaveSecurity(ctx, validUpdate(other.Security())); err != nil {
		t.Fatal(err)
	}

	if back := newService(t, db, testKey).Security().APIKey; back != originalKey {
		t.Errorf("restoring the old key should restore the API key, got %q", back)
	}
}

func TestParsePrefix(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1":            "10.0.0.1/32",
		"10.0.0.1/8":          "10.0.0.0/8",
		"::ffff:10.0.0.1":     "10.0.0.1/32",
		"::ffff:10.0.0.0/120": "10.0.0.0/24",
		"fe80::1%eth0":        "fe80::1/128",
		"2001:db8::/32":       "2001:db8::/32",
	}
	for in, want := range cases {
		p, err := settings.ParsePrefix(in)
		if err != nil || p.String() != want {
			t.Errorf("ParsePrefix(%q) = %v, %v; want %s", in, p, err, want)
		}
	}
	for _, bad := range []string{"", "nope", "10.0.0.0/", "10.0.0.1/40", "::ffff:10.0.0.0/90"} {
		if _, err := settings.ParsePrefix(bad); err == nil {
			t.Errorf("ParsePrefix(%q) should fail", bad)
		}
	}
}

func TestMetadataSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if m := svc.Metadata(); m.Language != "en-US" || m.TMDBAPIKey != "" {
			t.Errorf("defaults = %+v", m)
		}

		key := strings.Repeat("a1", 16)
		if _, err := svc.SaveMetadata(ctx, settings.MetadataUpdate{TMDBAPIKey: &key, Language: "de-DE"}); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = 'metadata'").Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, key) {
			t.Error("the TMDB key must be stored encrypted")
		}
		again := newService(t, db, testKey).Metadata()
		if again.TMDBAPIKey != key || again.Language != "de-DE" {
			t.Errorf("after restart: %+v", again)
		}

		// Leaving the key out keeps it.
		if m, _ := svc.SaveMetadata(ctx, settings.MetadataUpdate{Language: "en-GB"}); m.TMDBAPIKey != key {
			t.Error("key should be kept")
		}

		bad := "not-a-key"
		_, err := svc.SaveMetadata(ctx, settings.MetadataUpdate{TMDBAPIKey: &bad, Language: "english"})
		errs := fieldErrors(t, err)
		if errs["tmdbApiKey"] == "" || errs["language"] == "" {
			t.Errorf("errors = %v", errs)
		}
		token := "eyJhbGciOiJIUzI1NiJ9.something"
		if _, err := svc.SaveMetadata(ctx, settings.MetadataUpdate{TMDBAPIKey: &token, Language: "en-US"}); err != nil {
			t.Errorf("read access tokens are accepted: %v", err)
		}
	})
}

func TestNamingSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if svc.Naming() != naming.Defaults() {
			t.Errorf("defaults = %+v", svc.Naming())
		}
		want := naming.Defaults()
		want.MovieFolder, want.MovieFile = "{Movie CleanTitle} ({Release Year}) {tmdb-{TmdbId}}", "{Movie Title} {[Quality Full]}"
		want.EpisodeFile, want.MultiEpisodeStyle = "{Series Title} - S{season:00}E{episode:00}", "scene"
		if _, err := svc.SaveNaming(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got := newService(t, db, testKey).Naming(); got != want {
			t.Errorf("after restart: %+v", got)
		}
		bad := want
		bad.MovieFolder, bad.MovieFile, bad.EpisodeFile, bad.MultiEpisodeStyle = "{Release Year}", "{Movie Titel}", "{Series Title}", "sideways"
		_, err := svc.SaveNaming(ctx, bad)
		errs := fieldErrors(t, err)
		if errs["movieFolder"] == "" || errs["movieFile"] == "" || errs["episodeFile"] == "" || errs["multiEpisodeStyle"] == "" {
			t.Errorf("errors = %v", errs)
		}
		if svc.Naming() != want {
			t.Error("a rejected save must not change the settings")
		}
	})
}

func TestLabelSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.Labels(); !slices.Equal(got.Parts, settings.DefaultLabels().Parts) {
			t.Errorf("defaults = %v", got.Parts)
		}

		// Saved in display order, without duplicates, whatever order they're sent in.
		saved, err := svc.SaveLabels(ctx, settings.Labels{Parts: []string{"size", "quality", "audio", "quality"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"quality", "audio", "size"}
		if !slices.Equal(saved.Parts, want) {
			t.Errorf("saved = %v, want %v", saved.Parts, want)
		}
		if got := newService(t, db, testKey).Labels(); !slices.Equal(got.Parts, want) {
			t.Errorf("after restart: %v", got.Parts)
		}

		// Nothing at all is allowed: labels then only show warnings.
		if saved, err := svc.SaveLabels(ctx, settings.Labels{}); err != nil || saved.Parts == nil || len(saved.Parts) != 0 {
			t.Errorf("empty: %v, %v", saved.Parts, err)
		}

		_, err = svc.SaveLabels(ctx, settings.Labels{Parts: []string{"quality", "bitrate"}})
		if errs := fieldErrors(t, err); errs["parts"] == "" {
			t.Errorf("errors = %v", errs)
		}
		if len(svc.Labels().Parts) != 0 {
			t.Error("a rejected save must not change the settings")
		}
	})
}

func TestSolverSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.Solver(); got.URL != "" || got.TimeoutSeconds != 60 {
			t.Errorf("defaults = %+v", got)
		}
		saved, err := svc.SaveSolver(ctx, settings.Solver{URL: " http://trawl:8191/ ", TimeoutSeconds: 90})
		if err != nil || saved.URL != "http://trawl:8191" {
			t.Fatalf("save = %+v, %v", saved, err)
		}
		if got := newService(t, db, testKey).Solver(); got != saved {
			t.Errorf("after restart: %+v", got)
		}
		_, err = svc.SaveSolver(ctx, settings.Solver{URL: "trawl", TimeoutSeconds: 5})
		if errs := fieldErrors(t, err); errs["url"] == "" || errs["timeoutSeconds"] == "" {
			t.Errorf("errors = %v", errs)
		}
	})
}

func TestGrabbingSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.Grabbing(); got.Mode != settings.GrabDryRun || got.MinSeeders != 1 || got.QueueLimit != 0 {
			t.Errorf("defaults = %+v (new installs must start in dry run)", got)
		}
		saved, err := svc.SaveGrabbing(ctx, settings.Grabbing{Mode: settings.GrabOn, QueueLimit: 5, MinSeeders: 2})
		if err != nil {
			t.Fatal(err)
		}
		if got := newService(t, db, testKey).Grabbing(); got != saved {
			t.Errorf("after restart: %+v", got)
		}
		_, err = svc.SaveGrabbing(ctx, settings.Grabbing{Mode: "sometimes", QueueLimit: -1, MinSeeders: 500})
		if errs := fieldErrors(t, err); errs["mode"] == "" || errs["queueLimit"] == "" || errs["minSeeders"] == "" {
			t.Errorf("errors = %v", errs)
		}
	})
}

// The old "dailyCap" setting carries over as the queue limit, and is gone
// from the stored value once saved again.
func TestGrabbingDailyCapCarriesOver(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		put := func(v string) {
			t.Helper()
			if _, err := db.DB.NewRaw(`INSERT INTO settings (key, value, updated_at) VALUES ('grabbing', ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v, time.Now().UTC()).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
		put(`{"mode":"on","tvMode":"on","dailyCap":20,"minSeeders":2}`)
		svc := newService(t, db, testKey)
		if got := svc.Grabbing(); got.QueueLimit != 20 || got.Mode != settings.GrabOn || got.MinSeeders != 2 {
			t.Fatalf("carried over = %+v, want queue limit 20", got)
		}
		// A queue limit that was saved wins over a leftover dailyCap.
		put(`{"mode":"on","tvMode":"on","dailyCap":20,"queueLimit":7,"minSeeders":2}`)
		if got := newService(t, db, testKey).Grabbing(); got.QueueLimit != 7 {
			t.Errorf("queue limit = %d, want 7", got.QueueLimit)
		}
		// Saving writes the new key only.
		put(`{"mode":"on","tvMode":"on","dailyCap":20,"minSeeders":2}`)
		svc = newService(t, db, testKey)
		if _, err := svc.SaveGrabbing(ctx, svc.Grabbing()); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := db.DB.NewRaw(`SELECT value FROM settings WHERE key = 'grabbing'`).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			t.Fatal(err)
		}
		if _, old := stored["dailyCap"]; old || stored["queueLimit"] != float64(20) {
			t.Errorf("stored = %s", raw)
		}
		// Turning the limit off afterwards stays off.
		g := svc.Grabbing()
		g.QueueLimit = 0
		if _, err := svc.SaveGrabbing(ctx, g); err != nil {
			t.Fatal(err)
		}
		if got := newService(t, db, testKey).Grabbing(); got.QueueLimit != 0 {
			t.Errorf("after removing the limit: %+v", got)
		}
	})
}

func TestDownloadClientSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if svc.DownloadClient().Configured() {
			t.Error("configured by default")
		}
		pw := "s3cret"
		saved, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: " http://192.168.1.20:8080/ ", Username: "admin", Password: &pw})
		if err != nil || saved.URL != "http://192.168.1.20:8080" {
			t.Fatalf("save = %+v, %v", saved, err)
		}
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadClient).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, pw) {
			t.Error("the password must only be stored encrypted")
		}
		// A restart reads it back; saving without a password keeps it.
		again := newService(t, db, testKey)
		if got := again.DownloadClient(); got.Password != pw || got.Username != "admin" {
			t.Errorf("after restart: %+v", got)
		}
		if got, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://192.168.1.20:8080", Username: "reelkeeper"}); err != nil || got.Password != pw {
			t.Errorf("kept password: %+v, %v", got, err)
		}
		_, err = again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "192.168.1.20", Username: ""})
		if errs := fieldErrors(t, err); errs["url"] == "" || errs["username"] == "" {
			t.Errorf("errors = %v", errs)
		}
		// Clearing the address removes it, password and all.
		if got, err := again.SaveDownloadClient(ctx, settings.DownloadClientUpdate{}); err != nil || got.Configured() || got.Password != "" {
			t.Errorf("cleared: %+v, %v", got, err)
		}
	})
}

func TestDownloadClientType(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.DownloadClient(); got.ClientType() != settings.ClientQBittorrent || got.ClientName() != "qBittorrent" {
			t.Errorf("default = %+v", got)
		}

		// Settings saved before there was a choice have no type, and read as
		// qBittorrent.
		old := `{"url":"http://192.168.1.20:8080","username":"admin","folder":"/mnt/downloads/reelkeeper"}`
		if _, err := db.DB.NewRaw("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value",
			settings.GroupDownloadClient, old).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		svc = newService(t, db, testKey)
		if got := svc.DownloadClient(); got.Type != settings.ClientQBittorrent || got.URL != "http://192.168.1.20:8080" || got.ClientName() != "qBittorrent" {
			t.Fatalf("saved before the choice existed: %+v", got)
		}

		// Leaving the type out keeps it; a bad one is a field error.
		pw := "s3cret"
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://192.168.1.20:8080", Username: "admin", Password: &pw}); err != nil || got.Type != settings.ClientQBittorrent {
			t.Errorf("type left out: %+v, %v", got, err)
		}
		_, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: "rtorrent", URL: "http://192.168.1.20:8112", Username: "a"})
		if errs := fieldErrors(t, err); errs["type"] != "Choose qBittorrent, Transmission, Deluge or Built-in." {
			t.Errorf("bad type: %v", errs)
		}

		// Switching to Transmission: the login is optional, the stored password
		// isn't carried over to a different program, and it survives a restart.
		got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientTransmission, URL: "http://192.168.1.20:9091/"})
		if err != nil || got.Type != settings.ClientTransmission || got.ClientName() != "Transmission" || got.Username != "" || got.Password != "" || got.URL != "http://192.168.1.20:9091" {
			t.Fatalf("transmission = %+v, %v", got, err)
		}
		if got := newService(t, db, testKey).DownloadClient(); got.Type != settings.ClientTransmission || got.Password != "" {
			t.Errorf("after restart: %+v", got)
		}
		// Saving again without a type keeps Transmission, and its password is kept.
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://192.168.1.20:9091", Username: "tm", Password: &pw}); err != nil {
			t.Fatal(err)
		}
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://192.168.1.20:9091", Username: "tm"}); err != nil || got.Type != settings.ClientTransmission || got.Password != pw {
			t.Errorf("same type again: %+v, %v", got, err)
		}
		// qBittorrent still needs a username; the messages name the client.
		_, err = svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientQBittorrent, URL: "http://192.168.1.20:8080"})
		if errs := fieldErrors(t, err); errs["username"] == "" {
			t.Errorf("qBittorrent without a username: %v", errs)
		}
		_, err = svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientTransmission, URL: "192.168.1.20", Folder: "downloads"})
		errs := fieldErrors(t, err)
		if errs["url"] != "Enter Transmission's RPC address, like http://192.168.1.20:9091." || !strings.Contains(errs["folder"], "as Transmission sees it") || errs["username"] != "" {
			t.Errorf("Transmission errors = %v", errs)
		}
		_, err = svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientQBittorrent, URL: "192.168.1.20"})
		if errs := fieldErrors(t, err); errs["url"] != "Enter qBittorrent's Web UI address, like http://192.168.1.20:8080." {
			t.Errorf("qBittorrent errors = %v", errs)
		}

		// Clearing the address removes the client but remembers which kind it was,
		// so removing it isn't a way around the switching rule.
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientTransmission}); err != nil || got.Configured() || got.Type != settings.ClientTransmission || got.Password != "" {
			t.Errorf("cleared: %+v, %v", got, err)
		}
		if got := newService(t, db, testKey).DownloadClient(); got.Type != settings.ClientTransmission {
			t.Errorf("type after clearing and restarting: %+v", got)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// The built-in client has no address or login; it is set up by its folder.
func TestDownloadClientBuiltin(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		folder := t.TempDir()
		pw := "left-over"

		// A first save: the defaults, and nothing of another client's address or login.
		got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{
			Type: settings.ClientBuiltin, URL: "http://leftover:1", Username: "x", Password: &pw, Folder: folder + "/",
		})
		if err != nil {
			t.Fatal(err)
		}
		want := settings.DownloadClient{
			Type: settings.ClientBuiltin, Folder: folder, ListenPort: 6881, MaxActiveDownloads: 3, MaxActiveSeeds: 25,
		}
		if got != want || !got.Configured() || got.ClientName() != "the built-in client" || got.ClientType() != settings.ClientBuiltin {
			t.Fatalf("saved = %+v, want %+v", got, want)
		}
		if reread := newService(t, db, testKey).DownloadClient(); reread != want {
			t.Errorf("after a restart = %+v", reread)
		}

		// Values left out are kept; the ones given are stored and read back.
		got, err = svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{
			Folder: folder, ListenPort: ptr(51413), MaxActiveSeeds: ptr(0), DownloadLimitKBs: ptr(500),
		})
		if err != nil {
			t.Fatal(err)
		}
		want.ListenPort, want.MaxActiveSeeds, want.DownloadLimitKBs = 51413, 0, 500
		if got != want {
			t.Errorf("after changes = %+v, want %+v", got, want)
		}
		got, err = svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Folder: folder, UploadLimitKBs: ptr(80)})
		if err != nil || got.ListenPort != 51413 || got.MaxActiveSeeds != 0 || got.DownloadLimitKBs != 500 || got.UploadLimitKBs != 80 {
			t.Errorf("a later save forgot values: %+v, %v", got, err)
		}
		if reread := newService(t, db, testKey).DownloadClient(); reread != got {
			t.Errorf("after a restart = %+v, want %+v", reread, got)
		}

		// Clearing the folder removes the client but remembers the kind, and starts over.
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{}); err != nil || got.Configured() || got.Type != settings.ClientBuiltin ||
			got.ListenPort != 6881 || got.DownloadLimitKBs != 0 {
			t.Errorf("cleared = %+v, %v", got, err)
		}

		// Switching to another client drops the built-in fields, and back picks the defaults.
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder, ListenPort: ptr(7000), UploadLimitKBs: ptr(5)}); err != nil {
			t.Fatal(err)
		}
		got, err = svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientTransmission, URL: "http://tm:9091", Folder: "/dl"})
		if err != nil || got.ListenPort != 0 || got.MaxActiveDownloads != 0 || got.UploadLimitKBs != 0 || got.Folder != "/dl" || got.URL == "" {
			t.Errorf("transmission after the built-in client = %+v, %v", got, err)
		}
		got, err = svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: folder})
		if err != nil || got.ListenPort != 6881 || got.MaxActiveDownloads != 3 || got.MaxActiveSeeds != 25 || got.UploadLimitKBs != 0 || got.URL != "" {
			t.Errorf("built-in again = %+v, %v", got, err)
		}
	})
}

func TestDownloadClientBuiltinValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		dir := t.TempDir()
		file := filepath.Join(dir, "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		base := func() settings.DownloadClientUpdate {
			return settings.DownloadClientUpdate{Type: settings.ClientBuiltin, Folder: dir}
		}
		for _, tc := range []struct {
			name   string
			change func(*settings.DownloadClientUpdate)
			field  string
			text   string
		}{
			{"a relative folder", func(u *settings.DownloadClientUpdate) { u.Folder = "downloads" }, "folder", "full path"},
			{"a folder that isn't there", func(u *settings.DownloadClientUpdate) { u.Folder = dir + "/missing" }, "folder", "doesn't exist"},
			{"a file for a folder", func(u *settings.DownloadClientUpdate) { u.Folder = file }, "folder", "isn't a folder"},
			{"a port below 1024", func(u *settings.DownloadClientUpdate) { u.ListenPort = ptr(80) }, "listenPort", "1024 to 65535"},
			{"a port above 65535", func(u *settings.DownloadClientUpdate) { u.ListenPort = ptr(70000) }, "listenPort", "1024 to 65535"},
			{"no active downloads", func(u *settings.DownloadClientUpdate) { u.MaxActiveDownloads = ptr(0) }, "maxActiveDownloads", "1 to 50"},
			{"too many active downloads", func(u *settings.DownloadClientUpdate) { u.MaxActiveDownloads = ptr(51) }, "maxActiveDownloads", "1 to 50"},
			{"a negative seed limit", func(u *settings.DownloadClientUpdate) { u.MaxActiveSeeds = ptr(-1) }, "maxActiveSeeds", "0 (no limit) to 10000"},
			{"too many active seeds", func(u *settings.DownloadClientUpdate) { u.MaxActiveSeeds = ptr(10001) }, "maxActiveSeeds", "0 (no limit) to 10000"},
			{"a negative download limit", func(u *settings.DownloadClientUpdate) { u.DownloadLimitKBs = ptr(-5) }, "downloadLimitKBs", "KB/s"},
			{"a negative upload limit", func(u *settings.DownloadClientUpdate) { u.UploadLimitKBs = ptr(-5) }, "uploadLimitKBs", "KB/s"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				u := base()
				tc.change(&u)
				_, err := svc.ApplyDownloadClient(u)
				if errs := fieldErrors(t, err); !strings.Contains(errs[tc.field], tc.text) {
					t.Errorf("errors = %v, want %s to contain %q", errs, tc.field, tc.text)
				}
				if _, err := svc.SaveDownloadClient(context.Background(), u); err == nil {
					t.Error("saved")
				}
			})
		}
		// The limits that are fine.
		u := base()
		u.ListenPort, u.MaxActiveDownloads, u.MaxActiveSeeds = ptr(1024), ptr(50), ptr(10000)
		if _, err := svc.ApplyDownloadClient(u); err != nil {
			t.Errorf("edge values: %v", err)
		}
		// A folder Reelkeeper can look at but not write to.
		if os.Getuid() != 0 {
			ro := t.TempDir()
			if err := os.Chmod(ro, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
			u := base()
			u.Folder = ro
			if _, err := svc.ApplyDownloadClient(u); !strings.Contains(fieldErrors(t, err)["folder"], "can't write to") {
				t.Errorf("read-only folder: %v", err)
			}
		}
		// The other clients are unaffected by the built-in settings sent along.
		got, err := svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientQBittorrent, URL: "http://q:8080", Username: "a", ListenPort: ptr(80)})
		if err != nil || got.ListenPort != 0 {
			t.Errorf("qBittorrent with a built-in port: %+v, %v", got, err)
		}
		// A folder that doesn't need checking: the client being removed.
		if got, err := svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientBuiltin}); err != nil || got.Configured() {
			t.Errorf("no folder = %+v, %v", got, err)
		}
	})
}

// Deluge's web UI has a password and no username: a username sent for it is
// dropped, the address is Deluge's web UI, and the stored password is not
// carried across a switch.
func TestDownloadClientDeluge(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		pw := "web-pass"
		got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientDeluge, URL: "http://192.168.1.20:8112/", Username: "leftover", Password: &pw})
		if err != nil || got.Type != settings.ClientDeluge || got.ClientName() != "Deluge" || got.Username != "" || got.Password != pw || got.URL != "http://192.168.1.20:8112" {
			t.Fatalf("deluge = %+v, %v", got, err)
		}
		if got := newService(t, db, testKey).DownloadClient(); got.Type != settings.ClientDeluge || got.Password != pw || got.Username != "" {
			t.Errorf("after restart: %+v", got)
		}
		// Saving again without a type or password keeps both.
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://192.168.1.20:8112", Folder: "/mnt/downloads/reelkeeper"}); err != nil || got.Type != settings.ClientDeluge || got.Password != pw {
			t.Errorf("same type again: %+v, %v", got, err)
		}
		// No username is needed, and the messages name Deluge.
		_, err = svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientDeluge, URL: "192.168.1.20", Folder: "downloads"})
		errs := fieldErrors(t, err)
		if errs["url"] != "Enter Deluge's web UI address, like http://192.168.1.20:8112." || !strings.Contains(errs["folder"], "as Deluge sees it") || errs["username"] != "" {
			t.Errorf("Deluge errors = %v", errs)
		}
		if got, err := svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientDeluge, URL: "http://x:8112"}); err != nil || got.Username != "" {
			t.Errorf("no username: %+v, %v", got, err)
		}
		// Switching to another client doesn't carry Deluge's password along.
		next, err := svc.ApplyDownloadClient(settings.DownloadClientUpdate{Type: settings.ClientQBittorrent, URL: "http://qb:8080", Username: "admin"})
		if err != nil || next.Password != "" {
			t.Errorf("switch away: %+v, %v", next, err)
		}
		// Clearing the address removes the client but remembers it was Deluge.
		if got, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientDeluge}); err != nil || got.Configured() || got.Type != settings.ClientDeluge || got.Password != "" {
			t.Errorf("cleared: %+v, %v", got, err)
		}
		if settings.ClientDisplayName(settings.ClientDeluge) != "Deluge" {
			t.Error("display name")
		}
	})
}

func TestCleanupSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.Cleanup(); got.Mode != settings.CleanupDryRun || got.SeedRatio != 1 || got.SeedHours != 24 || got.EmptyReplacedDays != 30 {
			t.Errorf("defaults = %+v (new installs must start in dry run)", got)
		}
		c := settings.DefaultCleanup()
		c.Mode, c.StalledHours = settings.CleanupOn, 0
		saved, err := svc.SaveCleanup(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if got := newService(t, db, testKey).Cleanup(); got != saved {
			t.Errorf("after restart: %+v", got)
		}
		bad := settings.DefaultCleanup()
		bad.Mode, bad.SeedRatio, bad.SlowHours = "later", -1, 0
		errs := fieldErrors(t, mustCleanupErr(svc.SaveCleanup(ctx, bad)))
		if errs["mode"] == "" || errs["seedRatio"] == "" || errs["slowHours"] == "" {
			t.Errorf("errors = %v", errs)
		}
	})
}

func mustCleanupErr(_ settings.Cleanup, err error) error { return err }

func TestNotificationsSettings(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if got := svc.Notifications(); got.Email.Port != 587 || got.Emby.Enabled || got.Email.Enabled || got.Webhook.Enabled {
			t.Errorf("defaults = %+v", got)
		}
		key, pw, hook := "emby-key-123", "mail-pass-456", "hook-secret-789"
		u := settings.NotificationsUpdate{
			Notifications: settings.Notifications{
				Emby:    settings.EmbyNotify{Enabled: true, URL: " http://192.168.1.30:8096/ ", PathFrom: "/media/", PathTo: "/Volumes/Media"},
				Email:   settings.EmailNotify{Enabled: true, Host: "smtp.example.com", Username: "u", From: "reelkeeper@example.com", To: "dan@example.com"},
				Webhook: settings.WebhookNotify{Enabled: true, URL: "https://hooks.example.com/reelkeeper"},
			},
			EmbyAPIKey: &key, EmailPassword: &pw, WebhookSecret: &hook,
		}
		saved, err := svc.SaveNotifications(ctx, u)
		if err != nil || saved.Emby.URL != "http://192.168.1.30:8096" || saved.Emby.PathFrom != "/media" || saved.Email.Port != 587 {
			t.Fatalf("save = %+v, %v", saved, err)
		}
		var raw string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupNotifications).Scan(ctx, &raw); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{key, pw, hook} {
			if strings.Contains(raw, secret) {
				t.Errorf("%q must only be stored encrypted", secret)
			}
		}
		again := newService(t, db, testKey)
		if got := again.Notifications(); got.Emby.APIKey != key || got.Email.Password != pw || got.Webhook.Secret != hook {
			t.Errorf("after restart: %+v", got)
		}
		// Omitted secrets are kept.
		u.EmbyAPIKey, u.EmailPassword, u.WebhookSecret = nil, nil, nil
		u.Email.Username = "changed"
		if got, err := again.SaveNotifications(ctx, u); err != nil || got.Emby.APIKey != key || got.Email.Password != pw || got.Webhook.Secret != hook {
			t.Errorf("kept secrets: %+v, %v", got, err)
		}
		// An empty secret clears it.
		empty := ""
		u.WebhookSecret = &empty
		if got, err := again.SaveNotifications(ctx, u); err != nil || got.Webhook.Secret != "" || got.Emby.APIKey != key {
			t.Errorf("cleared: %+v, %v", got, err)
		}
		// A different secret key can't read them, but saving doesn't lose them.
		other := newService(t, db, "another-secret-key-0123456789")
		if got := other.Notifications(); got.Emby.APIKey != "" {
			t.Errorf("read with the wrong key: %+v", got)
		}
		u.EmbyAPIKey = nil
		u.Email.Username = "u"
		if _, err := other.SaveNotifications(ctx, settings.NotificationsUpdate{Notifications: u.Notifications}); err != nil {
			t.Fatal(err)
		}
		if got := newService(t, db, testKey).Notifications(); got.Emby.APIKey != key {
			t.Errorf("sealed key lost: %+v", got)
		}
	})
}

func TestNotificationsValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		svc := newService(t, db, testKey)
		_, err := svc.SaveNotifications(context.Background(), settings.NotificationsUpdate{Notifications: settings.Notifications{
			Emby:    settings.EmbyNotify{Enabled: true, URL: "192.168.1.30", PathFrom: "/media"},
			Email:   settings.EmailNotify{Enabled: true, Port: 70000, From: "nope", To: ""},
			Webhook: settings.WebhookNotify{Enabled: true},
		}})
		errs := fieldErrors(t, err)
		for _, f := range []string{"emby.url", "emby.apiKey", "emby.pathTo", "email.host", "email.port", "email.from", "email.to", "webhook.url"} {
			if errs[f] == "" {
				t.Errorf("no error for %s: %v", f, errs)
			}
		}
		// Switched-off channels with nothing filled in are fine.
		if _, err := svc.SaveNotifications(context.Background(), settings.NotificationsUpdate{}); err != nil {
			t.Errorf("empty save: %v", err)
		}
	})
}

// A move between download clients keeps the old client's connection until it
// ends, the password encrypted, and forgets it afterwards. Switching the
// client's type drops the stored password, so the move has to keep its own.
func TestDownloadMoveKeepsTheOldConnectionUntilItEnds(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newService(t, db, testKey)
		if mv := svc.DownloadMove(); mv.Active {
			t.Fatalf("a move at the start: %+v", mv)
		}
		pw := "qb-secret"
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{URL: "http://qb:8080", Username: "admin", Password: &pw}); err != nil {
			t.Fatal(err)
		}
		old := svc.DownloadClient()
		// What the API does: remember the old connection, then switch.
		if err := svc.SaveDownloadMove(ctx, settings.DownloadMove{Active: true, Old: old}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SaveDownloadClient(ctx, settings.DownloadClientUpdate{Type: settings.ClientTransmission, URL: "http://tm:9091"}); err != nil {
			t.Fatal(err)
		}
		if svc.DownloadClient().Password != "" {
			t.Error("the new client got the old password")
		}
		// Encrypted in the database, whole.
		var stored string
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadMove).Scan(ctx, &stored); err != nil {
			t.Fatal(err)
		}
		// (Postgres's jsonb adds a space after each colon.)
		stored = strings.ReplaceAll(stored, `": "`, `":"`)
		if strings.Contains(stored, pw) || !strings.Contains(stored, `"passwordSealed":"v1:`) || !strings.Contains(stored, "http://qb:8080") {
			t.Errorf("stored = %s", stored)
		}
		// After a restart the move is still there, with the password.
		again := newService(t, db, testKey)
		mv := again.DownloadMove()
		if !mv.Active || mv.Old.URL != "http://qb:8080" || mv.Old.Username != "admin" || mv.Old.Password != pw || mv.Old.ClientType() != settings.ClientQBittorrent {
			t.Errorf("after a restart: %+v", mv)
		}
		// Never sent to the browser.
		if b, _ := json.Marshal(mv); strings.Contains(string(b), pw) {
			t.Errorf("the password is in the JSON: %s", b)
		}
		// With another key the password can't be read, and the app still starts.
		other := newService(t, db, "another-secret-key-0123456789")
		if mv := other.DownloadMove(); !mv.Active || mv.Old.Password != "" {
			t.Errorf("with the wrong key: %+v", mv)
		}
		// Ending the move forgets it all.
		if err := again.ClearDownloadMove(ctx); err != nil {
			t.Fatal(err)
		}
		if mv := again.DownloadMove(); mv.Active || mv.Old.URL != "" || mv.Old.Password != "" {
			t.Errorf("after clearing: %+v", mv)
		}
		if err := db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", settings.GroupDownloadMove).Scan(ctx, &stored); err != nil || strings.Contains(stored, "qb:8080") {
			t.Errorf("stored after clearing: %s %v", stored, err)
		}
		if mv := newService(t, db, testKey).DownloadMove(); mv.Active {
			t.Errorf("a restart brought it back: %+v", mv)
		}
	})
}
