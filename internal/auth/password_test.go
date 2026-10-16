package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected hash format %q", h)
	}
	if ok, err := VerifyPassword("correct horse", h); !ok || err != nil {
		t.Errorf("correct password: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong horse", h); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Error("hashes of the same password should differ (random salt)")
	}
}

func TestMalformedHashesRejected(t *testing.T) {
	for _, h := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=99999999,t=2,p=1$c2FsdA$aGFzaA", // absurd memory
		"$argon2id$v=19$m=19456,t=999,p=1$c2FsdA$aGFzaA",  // absurd time
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$",
	} {
		if ok, err := VerifyPassword("x", h); ok || err == nil {
			t.Errorf("%q: got %v, %v; want rejection", h, ok, err)
		}
	}
}
