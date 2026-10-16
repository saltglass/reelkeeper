package api

import (
	"net/http"
	"strings"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/settings"
)

func toNotifications(n settings.Notifications) gen.NotificationSettings {
	var out gen.NotificationSettings
	out.Emby.Enabled, out.Emby.Url, out.Emby.ApiKeySet = n.Emby.Enabled, n.Emby.URL, n.Emby.APIKey != ""
	out.Emby.PathFrom, out.Emby.PathTo = n.Emby.PathFrom, n.Emby.PathTo
	out.Email.Enabled, out.Email.Host, out.Email.Port = n.Email.Enabled, n.Email.Host, n.Email.Port
	out.Email.Username, out.Email.PasswordSet = n.Email.Username, n.Email.Password != ""
	out.Email.From, out.Email.To = n.Email.From, n.Email.To
	out.Webhook.Enabled, out.Webhook.Url, out.Webhook.SecretSet = n.Webhook.Enabled, n.Webhook.URL, n.Webhook.Secret != ""
	return out
}

func fromNotifications(b gen.NotificationSettingsUpdate) settings.NotificationsUpdate {
	return settings.NotificationsUpdate{
		Notifications: settings.Notifications{
			Emby: settings.EmbyNotify{
				Enabled: b.Emby.Enabled, URL: b.Emby.Url,
				PathFrom: derefStr(b.Emby.PathFrom), PathTo: derefStr(b.Emby.PathTo),
			},
			Email: settings.EmailNotify{
				Enabled: b.Email.Enabled, Host: b.Email.Host, Port: b.Email.Port,
				Username: derefStr(b.Email.Username), From: b.Email.From, To: b.Email.To,
			},
			Webhook: settings.WebhookNotify{Enabled: b.Webhook.Enabled, URL: b.Webhook.Url},
		},
		EmbyAPIKey:    b.Emby.ApiKey,
		EmailPassword: b.Email.Password,
		WebhookSecret: b.Webhook.Secret,
	}
}

// GetNotifications returns the notification settings, without secrets.
func (h *Handlers) GetNotifications(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toNotifications(h.Settings.Notifications()))
}

// PutNotifications saves the notification settings.
func (h *Handlers) PutNotifications(w http.ResponseWriter, r *http.Request) {
	var body gen.PutNotificationsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	saved, err := h.Settings.SaveNotifications(r.Context(), fromNotifications(body))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toNotifications(saved))
}

// TestNotification tries one channel with unsaved values.
func (h *Handlers) TestNotification(w http.ResponseWriter, r *http.Request) {
	var body gen.TestNotificationJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	cfg, err := h.Settings.ApplyNotificationsFor(fromNotifications(body.Settings), string(body.Channel))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	msg, err := h.Notify.Test(r.Context(), string(body.Channel), cfg)
	if err != nil {
		text := capitalize(strings.TrimSpace(err.Error()))
		if !strings.HasSuffix(text, ".") {
			text += "."
		}
		writeError(w, http.StatusUnprocessableEntity, "notification", text, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}
