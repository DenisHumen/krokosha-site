// Package auth guards the admin area (brief B6): argon2id passwords, optional TOTP, server-side
// sessions with CSRF tokens, and throttling of login attempts.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. 64 MiB and 3 passes is well above the OWASP minimum and still fits a
// 2 GB server, because logins are rare and at most two hashes run at a time (see hashSlots).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// MinPasswordLength is enforced when a password is set, not when it is checked.
const MinPasswordLength = 12

// hashSlots bounds concurrent hashing: each run takes 64 MiB, and the login form is public.
var hashSlots = make(chan struct{}, 2)

// HashPassword returns the PHC-formatted argon2id hash of password.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", fmt.Errorf("the password must be at least %d characters long", MinPasswordLength)
	}
	if len(password) > 1024 {
		return "", errors.New("the password is too long")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hashSlots <- struct{}{}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	<-hashSlots
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-formatted hash. The parameters are read
// from the hash itself, so they can be raised later without locking anyone out.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || len(password) > 1024 {
		return false
	}
	var version int
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	// Never let a tampered row make the server allocate gigabytes.
	if memory > 1024*1024 || time > 10 || threads == 0 || threads > 8 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false
	}
	hashSlots <- struct{}{}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want))) //nolint:gosec // 1…128, checked above
	<-hashSlots
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummy is verified against when the login does not exist, so that an unknown user takes as long
// as a wrong password and the form does not reveal which logins are real. Computed on first use:
// the CLI and most tests never need it.
var dummy = sync.OnceValue(func() string {
	hash, err := HashPassword("a password nobody will ever type in")
	if err != nil {
		panic(err)
	}
	return hash
})
