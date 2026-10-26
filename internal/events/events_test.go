package events

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStream(t *testing.T) {
	b := NewBroker()
	srv := httptest.NewServer(b)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// Wait until the handler has subscribed, then publish.
	deadline := time.Now().Add(2 * time.Second)
	for b.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	b.Publish(Event{Type: SettingsChanged, Data: map[string]string{"group": "general"}})

	sc := bufio.NewScanner(resp.Body)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if strings.HasPrefix(sc.Text(), "data: ") {
			break
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "event: settings.changed\ndata: {\"group\":\"general\"}") {
		t.Errorf("stream was:\n%s", got)
	}

	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for b.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if b.Subscribers() != 0 {
		t.Error("subscriber not removed after disconnect")
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBroker()
	_, stop := b.Subscribe()
	defer stop()
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			b.Publish(Event{Type: SettingsChanged})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that isn't reading")
	}
}

func TestFallingBehindForcesAResync(t *testing.T) {
	b := NewBroker()
	ch, stop := b.Subscribe()
	defer stop()
	for i := 0; i < subscriberBuffer; i++ {
		b.Publish(Event{Type: TaskUpdated})
	}
	// The buffer is full; this one can't be queued.
	b.Publish(Event{Type: LibraryChanged})

	// The subscriber either gets the change or is cut off (and so
	// reconnects and refetches): never neither.
	got, closed := 0, false
	for !closed {
		select {
		case ev, ok := <-ch:
			if !ok {
				closed = true
				break
			}
			if ev.Type == LibraryChanged {
				return
			}
			got++
		case <-time.After(2 * time.Second):
			t.Fatalf("after %d events the subscriber was neither sent the change nor cut off", got)
		}
	}
	if got != subscriberBuffer {
		t.Errorf("received %d queued events before the cut-off, want %d", got, subscriberBuffer)
	}
	if b.Subscribers() != 0 {
		t.Error("a cut-off subscriber is still registered")
	}
	stop() // still safe after the cut-off
}

// stuckWriter is a ResponseWriter whose event writes wait until release
// is closed, like a browser that has stopped reading.
type stuckWriter struct {
	header  http.Header
	release chan struct{}
	mu      sync.Mutex
	out     strings.Builder
}

func (w *stuckWriter) Header() http.Header { return w.header }
func (w *stuckWriter) WriteHeader(int)     {}
func (w *stuckWriter) Flush()              {}
func (w *stuckWriter) Write(p []byte) (int, error) {
	if strings.HasPrefix(string(p), "event:") {
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

func TestStreamEndsWhenItFallsBehind(t *testing.T) {
	b := NewBroker()
	w := &stuckWriter{header: http.Header{}, release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", "/", nil)
	done := make(chan struct{})
	go func() {
		b.ServeHTTP(w, req)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for b.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// One event for the stream to get stuck writing, a full buffer behind
	// it, then the change that doesn't fit.
	// (Whether or not the stream has taken the first one yet, the change
	// doesn't fit.)
	for i := 0; i < subscriberBuffer+1; i++ {
		b.Publish(Event{Type: TaskUpdated, Data: i})
	}
	b.Publish(Event{Type: LibraryChanged})
	close(w.release)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream that fell behind is still open, so the browser never resyncs")
	}
	if b.Subscribers() != 0 {
		t.Error("subscriber not removed")
	}
}

func TestConcurrentPublishAndUnsubscribe(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	var published atomic.Int64
	stopPublishing := make(chan struct{})
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopPublishing:
					return
				default:
					b.Publish(Event{Type: LibraryChanged})
					published.Add(1)
				}
			}
		}()
	}
	var subs sync.WaitGroup
	for s := 0; s < 50; s++ {
		subs.Add(1)
		go func() {
			defer subs.Done()
			ch, stop := b.Subscribe()
			if s%2 == 0 {
				// A reader that keeps up for a while, then leaves.
				for i := 0; i < 10; i++ {
					if _, ok := <-ch; !ok {
						break
					}
				}
			} else {
				// One that never reads: it leaves once enough has been
				// published to overflow it, racing the cut-off.
				start := published.Load()
				for published.Load() < start+2*subscriberBuffer {
					runtime.Gosched()
				}
			}
			stop()
			stop() // twice is harmless
		}()
	}
	subs.Wait()
	close(stopPublishing)
	wg.Wait()
	if n := b.Subscribers(); n != 0 {
		t.Errorf("%d subscribers left after all of them stopped", n)
	}
}
