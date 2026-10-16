// Package secrets encrypts credentials Reelkeeper stores in its database, such as
// the OIDC client secret, indexer passwords and the API key.
//
// The key comes from REELKEEPER_SECRET_KEY. Without it, Reelkeeper generates a random
// key in its data folder the first time it starts. Losing that key makes
// stored credentials unreadable (they must then be re-entered), so a
// deployment should set REELKEEPER_SECRET_KEY and keep it somewhere safe.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keyInfo labels the key derivation. It keeps the app's former name because changing it would break existing credentials and older backups.
const keyInfo = "aerie secrets v1"

// KeyFileName is the generated key's file name inside the data folder.
const KeyFileName = "secret.key"

// minKeyLength is the shortest REELKEEPER_SECRET_KEY accepted. The value is
// stretched with HKDF, but a short passphrase is still guessable.
const minKeyLength = 16

// prefix marks the format of sealed values so it can change later.
const prefix = "v1:"

// ErrUndecryptable means a stored value can't be opened with the current key,
// usually because REELKEEPER_SECRET_KEY changed.
var ErrUndecryptable = errors.New("stored secret can't be decrypted: REELKEEPER_SECRET_KEY may have changed; re-enter this value")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for the given key material (any string of at least 16
// characters; it is stretched into an AES-256 key).
func New(keyMaterial string) (*Box, error) {
	if len(keyMaterial) < minKeyLength {
		return nil, fmt.Errorf("REELKEEPER_SECRET_KEY must be at least %d characters", minKeyLength)
	}
	key, err := hkdf.Key(sha256.New, []byte(keyMaterial), nil, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. The empty string stays empty, so "not set" is
// visible without decrypting.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value made by Seal. It returns ErrUndecryptable if the
// value was sealed with a different key or has been tampered with.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	data, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", ErrUndecryptable
	}
	raw, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", ErrUndecryptable
	}
	nonce, ciphertext := raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrUndecryptable
	}
	return string(plain), nil
}

// LoadKey returns the key material to use: REELKEEPER_SECRET_KEY if set,
// otherwise the key file in dataDir, created if missing. generated is true
// when a new key file was just written, so the caller can warn about it.
func LoadKey(envKey, dataDir string) (key string, generated bool, err error) {
	if envKey != "" {
		return envKey, false, nil
	}
	path := filepath.Join(dataDir, KeyFileName)
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is inside the configured data dir
		k := strings.TrimSpace(string(b))
		if len(k) < minKeyLength {
			return "", false, fmt.Errorf("%s is too short or empty; restore it from a backup or delete it to start over (stored credentials will need re-entering)", path)
		}
		return k, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false, err
	}
	k := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return "", false, err
	}
	// O_EXCL: never overwrite a key file another process just created.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- see above
	if err != nil {
		return "", false, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.WriteString(k + "\n"); err != nil {
		_ = f.Close()
		return "", false, err
	}
	if err := f.Close(); err != nil {
		return "", false, err
	}
	return k, true, nil
}
