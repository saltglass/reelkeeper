package settings

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// Installs from before the rename sealed their key check with "aerie";
// changing the constant would report every existing key as wrong.
func TestKeyCheckKeepsTheFirstName(t *testing.T) {
	if keyCheckPlaintext != "aerie" {
		t.Fatalf("keyCheckPlaintext = %q; existing installs sealed %q", keyCheckPlaintext, "aerie")
	}
}

// A backup's key check opens with the key it was made with, not another.
func TestBackupKeyCheck(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		boxA, _ := secrets.New("key-A-0123456789abcdef")
		boxB, _ := secrets.New("key-B-0123456789abcdef")
		if _, err := New(ctx, db, boxA, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
		sealed, err := ReadKeyCheck(ctx, db)
		if err != nil || sealed == "" {
			t.Fatalf("key check = %q, %v", sealed, err)
		}
		if !KeyOpens(boxA, sealed) {
			t.Error("the key it was sealed with doesn't open it")
		}
		if KeyOpens(boxB, sealed) {
			t.Error("another key opens it")
		}
	})
}
