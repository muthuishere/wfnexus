// Package secrets seals and opens the values held in the platform's env store.
//
// The threat this addresses is the ordinary one: somebody reads the database.
// A pg_dump, a backup on a laptop, a replica, a screenshot of a table — none of
// those should hand over a token. So every value is encrypted before it is
// written and the key never goes in with it.
//
// What this is NOT: protection from someone who already runs the platform
// process. The process has to decrypt to use a value, so it can. Anything
// stronger means the platform never holds the secret at all — which is exactly
// what `${VAR}` references are for, and why they remain the better answer
// wherever they fit.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNoKey means the store cannot be used because there is no key.
var ErrNoKey = errors.New("no encryption key: set WFX_SECRET_KEY, or let the platform write one on first boot")

// Box seals and opens values with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// KeyBytes is the key length, in bytes. AES-256.
const KeyBytes = 32

// NewBox builds a box from a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeyBytes, len(key))
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

// Load resolves the key: WFX_SECRET_KEY if set, else the key file, which is
// created on first use.
//
// The env var wins so an operator can hold the key somewhere this process never
// writes — a systemd credential, a Kubernetes secret, a password manager — which
// is the arrangement worth having. The file exists so a fresh install works
// without ceremony, and its path is logged (never its contents) because losing
// it means losing every stored value.
func Load(keyEnv, keyPath string) (*Box, string, error) {
	if v := os.Getenv(keyEnv); v != "" {
		key, err := decodeKey(v)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", keyEnv, err)
		}
		b, err := NewBox(key)
		return b, keyEnv, err
	}
	if keyPath == "" {
		return nil, "", ErrNoKey
	}
	raw, err := os.ReadFile(keyPath)
	if err == nil {
		key, err := decodeKey(string(raw))
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", keyPath, err)
		}
		b, err := NewBox(key)
		return b, keyPath, err
	}
	if !os.IsNotExist(err) {
		return nil, "", err
	}
	// First boot. 0600 and a directory the operator owns; anyone who can read
	// this file can read every stored value, which is why it is worth moving
	// into WFX_SECRET_KEY on anything shared.
	key := make([]byte, KeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		return nil, "", err
	}
	b, err := NewBox(key)
	return b, keyPath, err
}

func decodeKey(s string) ([]byte, error) {
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	} {
		if key, err := dec(trimSpace(s)); err == nil && len(key) == KeyBytes {
			return key, nil
		}
	}
	// A raw 32-character passphrase is accepted too: refusing it would only
	// push people to store the base64 of something weaker.
	if raw := trimSpace(s); len(raw) == KeyBytes {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("want %d bytes, base64 or raw", KeyBytes)
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// Seal encrypts a value. The nonce is random per call and stored in front of
// the ciphertext, so the same value written twice does not produce the same
// bytes — otherwise the table would leak which entries share a value.
func (b *Box) Seal(plain string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), nil), nil
}

// Open decrypts. A failure here is almost always the wrong key — a restored
// database with a fresh key file, usually — so it says so.
func (b *Box) Open(sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("value is too short to be encrypted; the store may be corrupt")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("could not decrypt: this is the wrong key for these values")
	}
	return string(plain), nil
}

// NewKey returns a fresh base64 key, for `wfx env key`.
func NewKey() (string, error) {
	key := make([]byte, KeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}
