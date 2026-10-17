package builtin

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/tracker"
)

// trackerLog remembers what each tracker last said to each torrent. anacrolix
// doesn't offer that (it keeps the last announce's error to itself), but it
// logs every announce with its address, info hash and error, so the engine
// reads them from its log (capture).
type trackerLog struct {
	mu sync.Mutex
	// msg is the last failure per torrent and tracker; a later good announce
	// clears it.
	msg map[string]map[string]string
	// seeders is the seed count the last good announce reported.
	seeders map[string]map[string]int
	// names maps a tracker address the engine looked up back to the name.
	names map[string]string
}

func newTrackerLog() *trackerLog {
	return &trackerLog{msg: map[string]map[string]string{}, seeders: map[string]map[string]int{}, names: map[string]string{}}
}

func (l *trackerLog) alias(addr, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names[addr] = name
}

func (l *trackerLog) record(hash, url string, resp *tracker.AnnounceResponse, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n, ok := l.names[url]; ok {
		url = n
	}
	if err != nil {
		if l.msg[hash] == nil {
			l.msg[hash] = map[string]string{}
		}
		l.msg[hash][url] = failureText(err)
		return
	}
	delete(l.msg[hash], url)
	if resp != nil {
		if l.seeders[hash] == nil {
			l.seeders[hash] = map[string]int{}
		}
		l.seeders[hash][url] = int(resp.Seeders)
	}
}

// failureText is what a tracker said, without anacrolix's wrapping:
// `tracker gave failure reason: "Unregistered torrent"` becomes
// `Unregistered torrent`.
func failureText(err error) string {
	s := err.Error()
	if rest, ok := strings.CutPrefix(s, "tracker gave failure reason: "); ok {
		if u, uerr := strconv.Unquote(rest); uerr == nil {
			return u
		}
		return rest
	}
	return s
}

func (l *trackerLog) messages(hash string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, m := range l.msg[hash] {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (l *trackerLog) swarmSeeds(hash string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.seeders[hash] {
		n = max(n, s)
	}
	return n
}

func (l *trackerLog) forget(hash string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.msg, hash)
	delete(l.seeders, hash)
}

// capture is the log handler given to anacrolix. It picks the "announced"
// lines out for the trackerLog, and passes warnings on at debug level (the
// library is chatty and most of it is normal peer-to-peer weather).
type capture struct {
	trk   *trackerLog
	out   *slog.Logger
	attrs []slog.Attr
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }

func (c *capture) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "announced" {
		var hash, url string
		var resp *tracker.AnnounceResponse
		var err error
		visit := func(a slog.Attr) bool {
			switch a.Key {
			case "short infohash":
				hash = strings.ToLower(fmt.Sprint(a.Value.Any()))
			case "url":
				url = fmt.Sprint(a.Value.Any())
			case "resp":
				switch v := a.Value.Any().(type) {
				case tracker.AnnounceResponse:
					resp = &v
				case *tracker.AnnounceResponse:
					resp = v
				}
			case "err":
				if e, ok := a.Value.Any().(error); ok {
					err = e
				}
			}
			return true
		}
		for _, a := range c.attrs {
			visit(a)
		}
		r.Attrs(visit)
		if hash != "" && url != "" {
			c.trk.record(hash, url, resp, err)
		}
		return nil
	}
	if r.Level >= slog.LevelWarn {
		c.out.Log(ctx, slog.LevelDebug, r.Message, "component", "anacrolix")
	}
	return nil
}

func (c *capture) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *c
	n.attrs = append(append([]slog.Attr(nil), c.attrs...), attrs...)
	return &n
}

func (c *capture) WithGroup(string) slog.Handler { return c }
