package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestNotificationsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()

	var gotToken string
	emby := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Emby-Token")
		if gotToken != "emby-key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ServerName":"Emby-server","Version":"4.9.3"}`))
	}))
	defer emby.Close()

	r := a.do("GET", "/api/v1/settings/notifications", "")
	if r.status != 200 || strings.Contains(r.raw, `"apiKeySet":true`) || !strings.Contains(r.raw, `"port":587`) {
		t.Errorf("defaults: %d %s", r.status, r.raw)
	}

	// Validation: field errors.
	r = a.do("PUT", "/api/v1/settings/notifications", `{"emby":{"enabled":true,"url":"nope"},"email":{"enabled":true,"host":"","port":587,"from":"x","to":""},"webhook":{"enabled":true,"url":""}}`)
	if r.status != 422 || !strings.Contains(r.raw, "emby.url") || !strings.Contains(r.raw, "email.host") || !strings.Contains(r.raw, "webhook.url") {
		t.Errorf("validation: %d %s", r.status, r.raw)
	}

	// Save; secrets never come back.
	body := fmt.Sprintf(`{"emby":{"enabled":true,"url":%q,"apiKey":"emby-key","pathFrom":"/media","pathTo":"/Volumes/Media"},`+
		`"email":{"enabled":true,"host":"smtp.example.com","port":587,"username":"u","password":"mail-pw","from":"a@example.com","to":"b@example.com"},`+
		`"webhook":{"enabled":true,"url":"https://hooks.example.com/x","secret":"hook-secret"}}`, emby.URL)
	r = a.do("PUT", "/api/v1/settings/notifications", body)
	if r.status != 200 || !strings.Contains(r.raw, `"apiKeySet":true`) || !strings.Contains(r.raw, `"passwordSet":true`) || !strings.Contains(r.raw, `"secretSet":true`) {
		t.Fatalf("save: %d %s", r.status, r.raw)
	}
	for _, s := range []string{"emby-key", "mail-pw", "hook-secret"} {
		if strings.Contains(r.raw, s) {
			t.Errorf("response leaks %q", s)
		}
	}

	// Omitting secrets keeps them.
	r = a.do("PUT", "/api/v1/settings/notifications", fmt.Sprintf(`{"emby":{"enabled":true,"url":%q,"pathFrom":"/media","pathTo":"/Volumes/Media"},`+
		`"email":{"enabled":false,"host":"smtp.example.com","port":587,"from":"a@example.com","to":"b@example.com"},`+
		`"webhook":{"enabled":false,"url":"https://hooks.example.com/x"}}`, emby.URL))
	if r.status != 200 || !strings.Contains(r.raw, `"apiKeySet":true`) {
		t.Errorf("keep: %d %s", r.status, r.raw)
	}
	n := a.settings.Notifications()
	if n.Emby.APIKey != "emby-key" || n.Email.Password != "mail-pw" || n.Webhook.Secret != "hook-secret" || n.Email.Enabled {
		t.Errorf("stored = %+v", n)
	}

	// Test uses the typed values, falls back to the stored key, and saves nothing.
	r = a.do("POST", "/api/v1/settings/notifications/test", fmt.Sprintf(`{"channel":"emby","settings":{"emby":{"enabled":true,"url":%q},`+
		`"email":{"enabled":false,"host":"","port":587,"from":"","to":""},"webhook":{"enabled":false,"url":""}}}`, emby.URL))
	if r.status != 200 || r.body["message"] != "Connected to Emby 4.9.3 (Emby-server)" {
		t.Errorf("test ok: %d %s", r.status, r.raw)
	}
	r = a.do("POST", "/api/v1/settings/notifications/test", fmt.Sprintf(`{"channel":"emby","settings":{"emby":{"enabled":true,"url":%q,"apiKey":"wrong"},`+
		`"email":{"enabled":false,"host":"","port":587,"from":"","to":""},"webhook":{"enabled":false,"url":""}}}`, emby.URL))
	if r.status != 422 || !strings.Contains(r.raw, "API key") || strings.Contains(r.raw, "wrong") {
		t.Errorf("test bad key: %d %s", r.status, r.raw)
	}
	// Validation problems in the typed values are field errors.
	r = a.do("POST", "/api/v1/settings/notifications/test", `{"channel":"webhook","settings":{"emby":{"enabled":false,"url":""},`+
		`"email":{"enabled":false,"host":"","port":587,"from":"","to":""},"webhook":{"enabled":true,"url":""}}}`)
	if r.status != 422 || !strings.Contains(r.raw, "webhook.url") {
		t.Errorf("test invalid: %d %s", r.status, r.raw)
	}
	// Testing Emby ignores a half-filled email section (it only matters
	// when saving), and the other way round.
	r = a.do("POST", "/api/v1/settings/notifications/test", fmt.Sprintf(`{"channel":"emby","settings":{"emby":{"enabled":true,"url":%q},`+
		`"email":{"enabled":true,"host":"","port":587,"from":"not an address","to":""},"webhook":{"enabled":false,"url":""}}}`, emby.URL))
	if r.status != 200 {
		t.Errorf("emby test with unfinished email: %d %s", r.status, r.raw)
	}
	r = a.do("POST", "/api/v1/settings/notifications/test", `{"channel":"email","settings":{"emby":{"enabled":true,"url":"nonsense"},`+
		`"email":{"enabled":true,"host":"","port":587,"from":"not an address","to":""},"webhook":{"enabled":false,"url":""}}}`)
	if r.status != 422 || !strings.Contains(r.raw, "email.from") || strings.Contains(r.raw, "emby.url") {
		t.Errorf("email test errors: %d %s", r.status, r.raw)
	}
	if got := a.settings.Notifications().Emby.APIKey; got != "emby-key" {
		t.Errorf("test changed the stored key: %q", got)
	}
}
