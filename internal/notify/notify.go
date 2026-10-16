// Package notify tells the outside world what Reelkeeper does: Emby (so a new
// movie shows up without a library scan), email (problems only) and a
// webhook (everything).
//
// Sending never blocks the caller and never fails it: each channel runs in
// its own goroutine with a timeout, and a channel that fails is logged
// (without secrets) and otherwise ignored.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
)

// Event kinds.
const (
	// KindImported: a movie was imported or upgraded.
	KindImported = "imported"
	// KindDeleted: a movie's files were deleted.
	KindDeleted = "deleted"
	// KindRenamed: a movie's files were renamed.
	KindRenamed = "renamed"

	// Problems, the only events that are emailed.

	// KindImportFailed: a finished download couldn't be imported.
	KindImportFailed = "import-failed"
	// KindDownloadRemoved: cleanup removed a stuck download.
	KindDownloadRemoved = "download-removed"
	// KindIndexerFailing: an indexer has been failing for a day.
	KindIndexerFailing = "indexer-failing"
	// KindClientUnreachable: the download client can't be reached.
	KindClientUnreachable = "client-unreachable"
	// KindGrabFailed: a chosen release couldn't be sent to the client.
	KindGrabFailed = "grab-failed"
	// KindDownloadFolderMissing: Reelkeeper can't see the download client's
	// files, usually because the folder isn't mounted.
	KindDownloadFolderMissing = "download-folder-missing"
)

// IsProblem reports whether kind is something going wrong (emailed), as
// opposed to something that happened to the library.
func IsProblem(kind string) bool {
	switch kind {
	case KindImportFailed, KindDownloadRemoved, KindIndexerFailing, KindClientUnreachable, KindGrabFailed, KindDownloadFolderMissing:
		return true
	}
	return false
}

// Event is one thing worth telling people about.
type Event struct {
	Kind    string
	Title   string
	Message string
	TitleID int64
	// Path is a file or folder, as Reelkeeper sees it. Emby is told about its
	// folder, translated into Emby's own path.
	Path string
	At   time.Time
}

// Channel names, as used by Test.
const (
	ChannelEmby    = "emby"
	ChannelEmail   = "email"
	ChannelWebhook = "webhook"
)

const (
	sendTimeout = 30 * time.Second
	// emailRepeat is how long the same problem isn't emailed again.
	emailRepeat = 6 * time.Hour
)

// Service sends events to the channels the settings turn on.
type Service struct {
	cfg    func() settings.Notifications
	logger *slog.Logger
	now    func() time.Time

	wg sync.WaitGroup

	mu       sync.Mutex
	live     func(Event)          // shows the event in open browsers (SetLive)
	lastMail map[string]time.Time // Kind+Title -> when it was last emailed
}

// New returns a Service reading its channels from cfg on every event, so
// saved changes apply at once.
func New(cfg func() settings.Notifications, logger *slog.Logger) *Service {
	return &Service{cfg: cfg, logger: logger, now: time.Now, lastMail: map[string]time.Time{}}
}

// SetLive makes every event also go to fn, which shows it to people who have
// Reelkeeper open (a pop-up notice). It runs at once, before any channel, and
// must not block.
func (s *Service) SetLive(fn func(Event)) {
	s.mu.Lock()
	s.live = fn
	s.mu.Unlock()
}

// Notify sends the event to every channel that wants it, in the
// background. It returns at once and never fails.
func (s *Service) Notify(ctx context.Context, ev Event) {
	if ev.At.IsZero() {
		ev.At = s.now().UTC()
	}
	s.mu.Lock()
	live := s.live
	s.mu.Unlock()
	if live != nil {
		live(ev)
	}
	cfg := s.cfg()
	// The caller's request may end before sending does.
	bg := context.WithoutCancel(ctx)

	if cfg.Emby.Enabled && (ev.Kind == KindImported || ev.Kind == KindDeleted || ev.Kind == KindRenamed) && ev.Path != "" {
		s.send(bg, ChannelEmby, func(ctx context.Context) error { return sendEmby(ctx, cfg.Emby, ev) })
	}
	if cfg.Email.Enabled && IsProblem(ev.Kind) && s.shouldEmail(ev) {
		s.send(bg, ChannelEmail, func(ctx context.Context) error { return sendEmail(ctx, cfg.Email, subjectFor(ev), bodyFor(ev)) })
	}
	if cfg.Webhook.Enabled {
		s.send(bg, ChannelWebhook, func(ctx context.Context) error { return sendWebhook(ctx, cfg.Webhook, ev) })
	}
}

// Wait blocks until every send in progress has finished (for shutdown
// and tests).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) send(ctx context.Context, channel string, fn func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(ctx, sendTimeout)
		defer cancel()
		if err := fn(ctx); err != nil {
			s.logger.Warn("notification failed", "channel", channel, "error", err)
		}
	}()
}

// shouldEmail is false if the same problem was emailed in the last six
// hours; otherwise it records that it's being emailed now.
func (s *Service) shouldEmail(ev Event) bool {
	key := ev.Kind + "\x00" + ev.Title
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastMail[key]; ok && now.Sub(last) < emailRepeat {
		return false
	}
	s.lastMail[key] = now
	for k, t := range s.lastMail { // forget old ones
		if now.Sub(t) >= emailRepeat {
			delete(s.lastMail, k)
		}
	}
	return true
}

// Test tries one channel with the given (possibly unsaved) settings and
// returns a plain-English success message, or why it failed.
func (s *Service) Test(ctx context.Context, channel string, cfg settings.Notifications) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch channel {
	case ChannelEmby:
		return testEmby(ctx, cfg.Emby)
	case ChannelEmail:
		err := sendEmail(ctx, cfg.Email, "Reelkeeper: test", "This is a test email from Reelkeeper. If you can read it, email notifications work.\n\nSent by Reelkeeper")
		if err != nil {
			return "", err
		}
		return "Test email sent to " + cfg.Email.To + ".", nil
	case ChannelWebhook:
		err := sendWebhook(ctx, cfg.Webhook, Event{Kind: "test", Title: "Reelkeeper test", Message: "This is a test from Reelkeeper.", At: s.now().UTC()})
		if err != nil {
			return "", err
		}
		return "The webhook accepted the test event.", nil
	}
	return "", fmt.Errorf("unknown channel %q", channel)
}

// translatePath turns a path as Reelkeeper sees it into the path as Emby sees it.
func translatePath(p, from, to string) string {
	if from == "" {
		return p
	}
	if p == from {
		return to
	}
	if rest, ok := strings.CutPrefix(p, from+"/"); ok {
		return to + "/" + rest
	}
	return p
}

// fileExts are the file types whose folder Emby should be told about.
var fileExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true,
	".ts": true, ".m2ts": true, ".webm": true, ".mpg": true, ".mpeg": true,
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true,
	".nfo": true, ".jpg": true, ".jpeg": true, ".png": true,
}

// folderOf returns p if it's a folder, or the folder holding it if it's a
// file.
func folderOf(p string) string {
	if fileExts[strings.ToLower(path.Ext(p))] {
		return path.Dir(p)
	}
	return p
}

// cleanErr strips the request URL out of network errors, because a
// webhook address can contain a secret token.
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s failed: %w", ue.Op, ue.Err)
	}
	return err
}
