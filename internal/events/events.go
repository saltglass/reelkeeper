// Package events broadcasts live updates to connected browsers over
// Server-Sent Events, so pages refresh when something changes instead of
// polling.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event types. Keep in step with EventType in api/openapi.yaml.
const (
	SettingsChanged = "settings.changed"
	SessionEnded    = "session.ended"
	TaskUpdated     = "task.updated"
	LibraryChanged  = "library.changed"
	Notice          = "notice"
)

// Event is one update. Data is encoded as JSON.
type Event struct {
	Type string
	Data any
}

// subscriberBuffer is how many events a slow browser may fall behind
// before it is disconnected. Dropping events quietly would leave its pages
// stale, so instead its stream ends: the browser reconnects by itself and
// refetches everything on reconnect (useLiveEvents' "open" handler).
const subscriberBuffer = 32

// keepAlive is how often a comment line is sent on an idle stream so
// proxies don't close it.
const keepAlive = 25 * time.Second

// Broker fans events out to every connected stream.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBroker returns an empty Broker.
func NewBroker() *Broker {
	return &Broker{subs: map[chan Event]struct{}{}}
}

// Publish sends ev to every subscriber without waiting for slow ones. A
// subscriber whose buffer is full has fallen too far behind: it is dropped
// and its channel closed, which ends its stream so the browser reconnects
// and catches up.
func (b *Broker) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Only Publish closes, and only while holding mu and removing
			// ch from subs, so nothing sends on a closed channel and no
			// channel is closed twice.
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns a channel of events and a function to stop receiving.
// The channel is closed if the subscriber falls too far behind (see
// Publish); calling stop afterwards is still safe.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Subscribers returns the number of connected streams.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// ServeHTTP streams events until the client disconnects.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Tell nginx-style proxies not to buffer the stream.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()

	ch, cancel := b.Subscribe()
	defer cancel()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				// Fell behind and was dropped: end the stream so the
				// browser reconnects and refetches.
				return
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
