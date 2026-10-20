package api

import (
	"net/http"
	"strings"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/settings"
)

func toNotifications(n settings.Notifications) gen.NotificationSettings {
	var out gen.NotificationSettings
	out.Emby, out.Jellyfin, out.Plex = toMediaServer(n.Emby), toMediaServer(n.Jellyfin), toMediaServer(n.Plex)
	out.Email.Enabled, out.Email.Host, out.Email.Port = n.Email.Enabled, n.Email.Host, n.Email.Port
	out.Email.Username, out.Email.PasswordSet = n.Email.Username, n.Email.Password != ""
	out.Email.From, out.Email.To = n.Email.From, n.Email.To
	out.Webhook.Enabled, out.Webhook.Url, out.Webhook.SecretSet = n.Webhook.Enabled, n.Webhook.URL, n.Webhook.Secret != ""
	return out
}

func toMediaServer(c settings.MediaServerNotify) gen.MediaServerNotify {
	return gen.MediaServerNotify{Enabled: c.Enabled, Url: c.URL, ApiKeySet: c.APIKey != "", PathFrom: c.PathFrom, PathTo: c.PathTo}
}

func fromMediaServer(b gen.MediaServerNotifyUpdate) settings.MediaServerNotify {
	return settings.MediaServerNotify{Enabled: b.Enabled, URL: b.Url, PathFrom: derefStr(b.PathFrom), PathTo: derefStr(b.PathTo)}
}

func fromNotifications(b gen.NotificationSettingsUpdate) settings.NotificationsUpdate {
	return settings.NotificationsUpdate{
		Notifications: settings.Notifications{
			Emby:     fromMediaServer(b.Emby),
			Jellyfin: fromMediaServer(b.Jellyfin),
			Plex:     fromMediaServer(b.Plex),
			Email: settings.EmailNotify{
				Enabled: b.Email.Enabled, Host: b.Email.Host, Port: b.Email.Port,
				Username: derefStr(b.Email.Username), From: b.Email.From, To: b.Email.To,
			},
			Webhook: settings.WebhookNotify{Enabled: b.Webhook.Enabled, URL: b.Webhook.Url},
		},
		EmbyAPIKey:     b.Emby.ApiKey,
		JellyfinAPIKey: b.Jellyfin.ApiKey,
		PlexToken:      b.Plex.ApiKey,
		EmailPassword:  b.Email.Password,
		WebhookSecret:  b.Webhook.Secret,
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
