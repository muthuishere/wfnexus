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
)

// Passwords (ADR 0025). A password is stored ONLY as an argon2id hash in the
// PHC string form, so the parameters travel with the hash and can be raised
// later without invalidating what is already stored. Nothing in this file logs,
// and nothing returns the value it was given.

// MinPasswordLen is the shortest password accepted, counted in characters.
const MinPasswordLen = 12

// RFC 9106 §4's second recommended option (64 MiB, 3 passes), with 2 lanes.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrPasswordTooShort is what a password under MinPasswordLen gets.
var ErrPasswordTooShort = fmt.Errorf("a password must be at least %d characters", MinPasswordLen)

// ValidatePassword is the one rule a new password must meet.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	return nil
}

// HashPassword returns `$argon2id$v=19$m=…,t=…,p=…$salt$key`.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// dummyHash is checked against when there is no real hash to check, so an
// unknown username costs the same time as a wrong password.
var dummyHash, _ = HashPassword("wfx-no-such-user-placeholder")

// VerifyPassword reports whether pw matches the stored hash. An empty or
// malformed hash never matches, but is still paid for in time.
func VerifyPassword(encoded, pw string) bool {
	ok, err := verify(encoded, pw)
	if err != nil {
		_, _ = verify(dummyHash, pw)
		return false
	}
	return ok
}

func verify(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, errors.New("bad argon2 parameters")
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("bad argon2 key")
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewPassword is a one-time password for the first admin and for resets:
// 20 characters from an unambiguous alphabet (~115 bits).
func NewPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
