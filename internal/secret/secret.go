// Package secret encrypts platform tokens at rest with AES-256-GCM.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Box struct {
	aead    cipher.AEAD
	signKey []byte // derived from the key; signs short-lived public links
}

// NewKey returns a fresh random key, base64 encoded.
func NewKey() string {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// Open creates a Box from a base64 key.
func Open(key string) (*Box, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return nil, fmt.Errorf("secret key is not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	signKey := sha256.Sum256(append([]byte("powerposter-sign:"), raw...))
	return &Box{aead: aead, signKey: signKey[:]}, nil
}

// LoadOrCreate uses key if set; otherwise it reads (or creates) dataDir/secret.key.
// The returned bool reports whether a new key file was written.
func LoadOrCreate(key, dataDir string) (*Box, bool, error) {
	if key != "" {
		b, err := Open(key)
		return b, false, err
	}
	path := filepath.Join(dataDir, "secret.key")
	if data, err := os.ReadFile(path); err == nil {
		b, err := Open(string(data))
		return b, false, err
	}
	k := NewKey()
	if err := os.WriteFile(path, []byte(k+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	b, err := Open(k)
	return b, true, err
}

func (b *Box) Seal(plain string) []byte {
	if plain == "" {
		return nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), nil)
}

func (b *Box) Unseal(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt token (was POWERPOSTER_SECRET_KEY changed?): %w", err)
	}
	return string(plain), nil
}

// Sign returns an HMAC-SHA256 over data, base64url encoded.
func (b *Box) Sign(data string) string {
	m := hmac.New(sha256.New, b.signKey)
	m.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks a signature made by Sign in constant time.
func (b *Box) Verify(data, sig string) bool {
	return hmac.Equal([]byte(b.Sign(data)), []byte(sig))
}
