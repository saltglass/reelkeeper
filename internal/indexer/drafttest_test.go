package indexer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// Testing settings in the add/edit dialog and then saving them must not
// leave the indexer "Not tested yet".
func TestDraftTestCountsWhenSaved(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)

		// Tested, then saved unchanged: working. The test has no name yet,
		// as when someone presses Test before typing one; it must still use
		// the settings and password (it once silently dropped them).
		if err := f.svc.TestInput(ctx, 0, f.input("")); err != nil {
			t.Fatal(err)
		}
		a, err := f.svc.Create(ctx, f.input("A"))
		if err != nil {
			t.Fatal(err)
		}
		if h := f.svc.HealthOf(a); h.Status != "ok" {
			t.Errorf("tested then saved: %+v", h)
		}
		if a, _ = f.svc.Get(ctx, a.ID); f.svc.HealthOf(a).Status != "ok" {
			t.Errorf("stored health: %+v", f.svc.HealthOf(a))
		}

		// A test counts once: saving the same settings again later doesn't
		// reuse it.
		b, _ := f.svc.Create(ctx, f.input("B"))
		if h := f.svc.HealthOf(b); h.Status != "unknown" {
			t.Errorf("untested: %+v", h)
		}

		// Changed after the test (here, the password): the test doesn't count.
		_ = f.svc.TestInput(ctx, 0, f.input(""))
		in := f.input("C")
		in.Settings["password"] = "changed"
		c, _ := f.svc.Create(ctx, in)
		if h := f.svc.HealthOf(c); h.Status != "unknown" {
			t.Errorf("changed after testing: %+v", h)
		}

		// A failed test, then saved anyway: failing, with the reason, but
		// no wait before Reelkeeper tries it.
		f.fail.Store(true)
		if err := f.svc.TestInput(ctx, 0, f.input("")); err == nil {
			t.Fatal("expected the test to fail")
		}
		d, _ := f.svc.Create(ctx, f.input("D"))
		if h := f.svc.HealthOf(d); h.Status != "failing" || h.Message == "" || d.RetryAfter != nil {
			t.Errorf("failed test then saved: %+v, retry %v", h, d.RetryAfter)
		}

		// Editing: test the edited settings, then save them.
		f.fail.Store(false)
		if err := f.svc.TestInput(ctx, d.ID, f.input("D")); err != nil {
			t.Fatal(err)
		}
		d, _ = f.svc.Update(ctx, d.ID, f.input("D"))
		if h := f.svc.HealthOf(d); h.Status != "ok" {
			t.Errorf("edited, tested, saved: %+v", h)
		}

		// A test of another indexer's settings doesn't count for this one.
		_ = f.svc.TestInput(ctx, a.ID, f.input("A"))
		b, _ = f.svc.Update(ctx, b.ID, f.input("B"))
		if h := f.svc.HealthOf(b); h.Status != "unknown" {
			t.Errorf("another indexer's test: %+v", h)
		}

		// Too old: doesn't count.
		_ = f.svc.TestInput(ctx, 0, f.input(""))
		f.svc.now = func() time.Time { return time.Now().Add(draftTestTTL + time.Minute) }
		e, _ := f.svc.Create(ctx, f.input("E"))
		if h := f.svc.HealthOf(e); h.Status != "unknown" {
			t.Errorf("stale test: %+v", h)
		}
	})
}

// The remembered fingerprint must not contain the secrets themselves.
func TestDraftKeyHidesSecrets(t *testing.T) {
	k := draftKey(0, Input{Kind: KindCardigann}, map[string]string{"username": "dan"}, map[string]string{"password": "hunter2"})
	if len(k) != 64 {
		t.Errorf("key %q is not a SHA-256 hash", k)
	}
	for _, s := range []string{"hunter2", "dan"} {
		if strings.Contains(k, s) {
			t.Errorf("key contains %q", s)
		}
	}
}
