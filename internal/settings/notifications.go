package settings

import (
	"context"
	"net/mail"
	"net/url"
	"strings"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupNotifications is the settings group for Settings → Notifications.
const GroupNotifications = "notifications"

// Sealed-secret field paths (see sealOrKeep).
const (
	notifyEmbyKey      = "notifications.emby.apiKey"
	notifyJellyfinKey  = "notifications.jellyfin.apiKey"
	notifyEmailPass    = "notifications.email.password"
	notifyWebhookToken = "notifications.webhook.secret"
)

// Notifications holds the notification channels. Secrets are encrypted in
// the database and never sent to the browser.
type Notifications struct {
	Emby     MediaServerNotify `json:"emby"`
	Jellyfin MediaServerNotify `json:"jellyfin"`
	Email    EmailNotify       `json:"email"`
	Webhook  WebhookNotify     `json:"webhook"`
}

// MediaServerNotify tells a media server (Emby or Jellyfin, which share the
// call) about new, changed and deleted movie and show folders.
type MediaServerNotify struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	APIKey  string `json:"-"`
	// PathFrom/PathTo translate Reelkeeper's library paths into the server's:
	// when Reelkeeper sees /media/movies and the server sees
	// /Volumes/Media/movies, PathFrom is /media and PathTo is /Volumes/Media.
	// Both empty means the paths are the same.
	PathFrom string `json:"pathFrom"`
	PathTo   string `json:"pathTo"`
}

// EmailNotify emails problems.
type EmailNotify struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"-"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// WebhookNotify posts every event as JSON.
type WebhookNotify struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	// Secret, if set, is sent as the X-Reelkeeper-Secret header.
	Secret string `json:"-"`
}

type storedNotifications struct {
	Notifications
	EmbyKeySealed       string `json:"embyApiKeySealed"`
	JellyfinKeySealed   string `json:"jellyfinApiKeySealed,omitempty"`
	EmailPasswordSealed string `json:"emailPasswordSealed"`
	WebhookSecretSealed string `json:"webhookSecretSealed"`
}

// NotificationsUpdate is what the user can change. A nil secret keeps the
// stored one (an empty string clears it). The secrets inside Notifications
// are ignored.
type NotificationsUpdate struct {
	Notifications
	EmbyAPIKey     *string
	JellyfinAPIKey *string
	EmailPassword  *string
	WebhookSecret  *string
}

// DefaultNotifications returns the settings used until the user saves any.
func DefaultNotifications() Notifications {
	return Notifications{Email: EmailNotify{Port: 587}}
}

// Notifications returns the current notification settings.
func (s *Service) Notifications() Notifications {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.notifications
}

// ApplyNotifications validates an update and fills in what it keeps (the
// stored secrets).
func (s *Service) ApplyNotifications(u NotificationsUpdate) (Notifications, error) {
	next, errs := s.applyNotifications(u)
	if errs != nil {
		return Notifications{}, errs
	}
	return next, nil
}

// ApplyNotificationsFor is ApplyNotifications for testing one channel
// ("emby", "jellyfin", "email" or "webhook") before saving: only that channel's fields
// must be valid, so a half-filled email section doesn't stop an Emby test.
func (s *Service) ApplyNotificationsFor(u NotificationsUpdate, channel string) (Notifications, error) {
	next, errs := s.applyNotifications(u)
	var mine validation.Errors
	for field, msg := range errs {
		if strings.HasPrefix(field, channel+".") {
			mine.Add(field, msg)
		}
	}
	if mine != nil {
		return Notifications{}, mine
	}
	return next, nil
}

func (s *Service) applyNotifications(u NotificationsUpdate) (Notifications, validation.Errors) {
	s.mu.RLock()
	cur := s.notifications
	embyKeyKept := s.sealedKeep[notifyEmbyKey] != ""
	jellyfinKeyKept := s.sealedKeep[notifyJellyfinKey] != ""
	s.mu.RUnlock()

	next := u.Notifications
	tidyMediaServer(&next.Emby, cur.Emby.APIKey, u.EmbyAPIKey)
	tidyMediaServer(&next.Jellyfin, cur.Jellyfin.APIKey, u.JellyfinAPIKey)
	e := &next.Email
	e.Host = strings.TrimSpace(e.Host)
	e.Username = strings.TrimSpace(e.Username)
	e.From = strings.TrimSpace(e.From)
	e.To = strings.TrimSpace(e.To)
	e.Password = cur.Email.Password
	if u.EmailPassword != nil {
		e.Password = *u.EmailPassword
	}
	if e.Port == 0 {
		e.Port = 587
	}
	next.Webhook.URL = strings.TrimSpace(next.Webhook.URL)
	next.Webhook.Secret = cur.Webhook.Secret
	if u.WebhookSecret != nil {
		next.Webhook.Secret = strings.TrimSpace(*u.WebhookSecret)
	}

	var errs validation.Errors
	validURL := func(field, v string) {
		p, err := url.Parse(v)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs.Add(field, "Enter a full address, like http://192.168.1.30:8096.")
		}
	}

	checkMediaServer := func(prefix, name, keyWhere string, c MediaServerNotify, keyKept, keyTyped bool) {
		if c.URL != "" {
			validURL(prefix+".url", c.URL)
		} else if c.Enabled {
			errs.Add(prefix+".url", "Enter "+name+"'s address, like http://192.168.1.30:8096.")
		}
		if c.Enabled && c.APIKey == "" && (!keyKept || keyTyped) {
			errs.Add(prefix+".apiKey", "Enter a "+name+" API key ("+keyWhere+").")
		}
		if (c.PathFrom == "") != (c.PathTo == "") {
			errs.Add(prefix+".pathTo", "Fill in both paths, or leave both empty if "+name+" sees the same paths as Reelkeeper.")
		}
		for field, v := range map[string]string{prefix + ".pathFrom": c.PathFrom, prefix + ".pathTo": c.PathTo} {
			if v != "" && !strings.HasPrefix(v, "/") {
				errs.Add(field, "Enter a full path, starting with /.")
			}
		}
	}
	checkMediaServer("emby", "Emby", "Emby dashboard → Advanced → API Keys", next.Emby, embyKeyKept, u.EmbyAPIKey != nil)
	checkMediaServer("jellyfin", "Jellyfin", "Jellyfin dashboard → API Keys", next.Jellyfin, jellyfinKeyKept, u.JellyfinAPIKey != nil)

	if e.Port < 1 || e.Port > 65535 {
		errs.Add("email.port", "Use a port from 1 to 65535 (587 is usual).")
	}
	if e.Enabled && e.Host == "" {
		errs.Add("email.host", "Enter your mail server's address.")
	}
	if e.Enabled || e.From != "" {
		if _, err := mail.ParseAddress(e.From); err != nil {
			errs.Add("email.from", "Enter a valid email address.")
		}
	}
	if e.Enabled || e.To != "" {
		if _, err := mail.ParseAddress(e.To); err != nil {
			errs.Add("email.to", "Enter a valid email address.")
		}
	}

	if next.Webhook.URL != "" {
		validURL("webhook.url", next.Webhook.URL)
	} else if next.Webhook.Enabled {
		errs.Add("webhook.url", "Enter the address Reelkeeper should post to.")
	}

	return next, errs
}

// tidyMediaServer trims what was typed for a media server and fills in its
// API key: the stored one, or the one typed (nil keeps the stored one).
func tidyMediaServer(c *MediaServerNotify, storedKey string, typed *string) {
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	c.PathFrom = strings.TrimRight(strings.TrimSpace(c.PathFrom), "/")
	c.PathTo = strings.TrimRight(strings.TrimSpace(c.PathTo), "/")
	c.APIKey = storedKey
	if typed != nil {
		c.APIKey = strings.TrimSpace(*typed)
	}
}

// SaveNotifications validates and stores the notification settings.
func (s *Service) SaveNotifications(ctx context.Context, u NotificationsUpdate) (Notifications, error) {
	next, err := s.ApplyNotifications(u)
	if err != nil {
		return Notifications{}, err
	}
	s.mu.Lock()
	if u.EmbyAPIKey != nil {
		delete(s.sealedKeep, notifyEmbyKey)
	}
	if u.JellyfinAPIKey != nil {
		delete(s.sealedKeep, notifyJellyfinKey)
	}
	if u.EmailPassword != nil {
		delete(s.sealedKeep, notifyEmailPass)
	}
	if u.WebhookSecret != nil {
		delete(s.sealedKeep, notifyWebhookToken)
	}
	stored := storedNotifications{Notifications: next}
	for _, f := range []struct {
		field, plain string
		dst          *string
	}{
		{notifyEmbyKey, next.Emby.APIKey, &stored.EmbyKeySealed},
		{notifyJellyfinKey, next.Jellyfin.APIKey, &stored.JellyfinKeySealed},
		{notifyEmailPass, next.Email.Password, &stored.EmailPasswordSealed},
		{notifyWebhookToken, next.Webhook.Secret, &stored.WebhookSecretSealed},
	} {
		if f.plain == "" && s.sealedKeep[f.field] == "" {
			continue // nothing to store
		}
		sealed, err := s.sealOrKeep(f.field, f.plain)
		if err != nil {
			s.mu.Unlock()
			return Notifications{}, err
		}
		*f.dst = sealed
	}
	if err := s.put(ctx, GroupNotifications, stored); err != nil {
		s.mu.Unlock()
		return Notifications{}, err
	}
	s.notifications = next
	s.mu.Unlock()
	s.notify(GroupNotifications)
	return next, nil
}

func (s *Service) loadNotifications(ctx context.Context) error {
	stored := storedNotifications{Notifications: DefaultNotifications()}
	if _, err := s.load(ctx, GroupNotifications, &stored); err != nil {
		return err
	}
	n := stored.Notifications
	n.Emby.APIKey = s.openSecret(notifyEmbyKey, stored.EmbyKeySealed)
	n.Jellyfin.APIKey = s.openSecret(notifyJellyfinKey, stored.JellyfinKeySealed)
	n.Email.Password = s.openSecret(notifyEmailPass, stored.EmailPasswordSealed)
	n.Webhook.Secret = s.openSecret(notifyWebhookToken, stored.WebhookSecretSealed)
	s.notifications = n
	return nil
}
