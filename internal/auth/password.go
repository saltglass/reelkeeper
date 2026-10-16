package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// argon2id parameters: OWASP's recommended minimum (19 MiB, 2 passes, 1
// thread). Hashing takes a few tens of milliseconds and the memory is
// released right after, which suits a RAM-tight host.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// Password length limits. The upper limit stops someone making the server
// hash megabytes of input.
const (
	minPasswordLen = 8
	maxPasswordLen = 256
)

var errBadHash = errors.New("stored password hash is malformed")

// HashPassword returns an argon2id hash in the standard PHC string format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches hash. It reads the
// parameters from the hash, so hashes made with older settings still work.
func VerifyPassword(password, hash string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, errBadHash
	}
	// Refuse absurd parameters so a tampered hash can't exhaust memory.
	if mem == 0 || mem > 1024*1024 || t == 0 || t > 20 || p == 0 {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want))) // #nosec G115 -- len checked above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a username doesn't exist, so a wrong
// username takes as long as a wrong password and can't be told apart.
var dummyHash = func() string {
	h, err := HashPassword("not a real password, only used for timing")
	if err != nil {
		panic(err)
	}
	return h
}()

// checkPassword applies the password rules.
func checkPassword(field, password, username string, errs *validation.Errors) {
	n := utf8.RuneCountInString(password)
	switch {
	case n < minPasswordLen:
		errs.Add(field, fmt.Sprintf("Use at least %d characters.", minPasswordLen))
	case len(password) > maxPasswordLen:
		errs.Add(field, fmt.Sprintf("Use at most %d characters.", maxPasswordLen))
	case strings.EqualFold(password, username):
		errs.Add(field, "The password can't be the same as the username.")
	}
}
