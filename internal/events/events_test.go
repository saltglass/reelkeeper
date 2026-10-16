package events

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
