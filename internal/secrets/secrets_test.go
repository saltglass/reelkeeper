package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustBox(t *testing.T, key string) *Box {
	t.Helper()
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustBox(t, "a-long-enough-test-key")
	for _, plain := range []string{"x", "hunter2", "ünïcødé 🔑", strings.Repeat("a", 10000)} {
		sealed, err := b.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		// (Very short values can appear in base64 output by chance.)
		if len(plain) > 3 && strings.Contains(sealed, plain) {
			t.Errorf("sealed value contains the plaintext")
		}
		got, err := b.Open(sealed)
		if err != nil || got != plain {
			t.Errorf("Open = %q, %v; want %q", got, err, plain)
		}
	}
}

func TestEmptyStaysEmpty(t *testing.T) {
	b := mustBox(t, "a-long-enough-test-key")
	if s, _ := b.Seal(""); s != "" {
		t.Errorf("Seal(\"\") = %q", s)
	}
	if s, err := b.Open(""); s != "" || err != nil {
		t.Errorf("Open(\"\") = %q, %v", s, err)
	}
}

func TestSealIsRandomized(t *testing.T) {
	b := mustBox(t, "a-long-enough-test-key")
	a1, _ := b.Seal("same")
	a2, _ := b.Seal("same")
	if a1 == a2 {
		t.Error("sealing the same value twice should give different output")
	}
}

func TestWrongKeyOrTamperingFails(t *testing.T) {
	b1 := mustBox(t, "first-key-0123456789")
	b2 := mustBox(t, "second-key-0123456789")
	sealed, _ := b1.Seal("secret")

	if _, err := b2.Open(sealed); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("wrong key: err = %v", err)
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := b1.Open(tampered); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("tampered: err = %v", err)
	}
	for _, junk := range []string{"plaintext", "v1:", "v1:!!!", "v2:abc"} {
		if _, err := b1.Open(junk); !errors.Is(err, ErrUndecryptable) {
			t.Errorf("%q: err = %v", junk, err)
		}
	}
}

func TestShortKeyRejected(t *testing.T) {
	if _, err := New("short"); err == nil {
		t.Error("expected an error for a short key")
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()

	// The environment variable wins and nothing is written.
	k, gen, err := LoadKey("from-the-environment", dir)
	if err != nil || k != "from-the-environment" || gen {
		t.Fatalf("env: %q %v %v", k, gen, err)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyFileName)); !os.IsNotExist(err) {
		t.Error("key file should not be created when the env var is set")
	}

	// Without it, a key file is generated once and then reused.
	k1, gen1, err := LoadKey("", dir)
	if err != nil || !gen1 || len(k1) != 64 {
		t.Fatalf("generate: %q %v %v", k1, gen1, err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}
	k2, gen2, err := LoadKey("", dir)
	if err != nil || gen2 || k2 != k1 {
		t.Errorf("reuse: %q %v %v", k2, gen2, err)
	}
}

func TestLoadKeyRejectsDamagedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, KeyFileName), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadKey("", dir); err == nil {
		t.Error("an empty key file must not be silently replaced")
	}
}

// Values sealed before the rename to Reelkeeper must still open: the key
// derivation's label keeps the app's first name (keyInfo). This value was
// sealed by that code with a test key.
func TestOpensValuesSealedBeforeTheRename(t *testing.T) {
	b, err := New("pinned-test-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Open("v1:p1iINNTcDFbk0qeYD7+R4+6RNdZClrIT7AiELo2dwUArVn4")
	if err != nil || got != "hunter2" {
		t.Fatalf("open = %q, %v", got, err)
	}
}
