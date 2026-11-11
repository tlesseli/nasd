// Package auth handles password hashing, login sessions and brute-force
// throttling. It only uses the Go standard library.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Never store passwords, only a slow salted hash. PBKDF2 runs SHA-256 many
// times so guessing is expensive. 600,000 is OWASP's recommendation for
// PBKDF2-SHA256 (2023): about 4 s per login on the Atom D410, a cost
// accepted for the admin account.
// Hashes made with fewer iterations still work and are upgraded at the
// next successful login (NeedsRehash).
const iterations = 600_000

var b64 = base64.RawStdEncoding

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<key>".
// Storing the parameters in the string lets us raise the iteration count
// later without breaking existing hashes.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func CheckPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	// Constant-time comparison: a normal == leaks timing information.
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash tells whether a hash was made with weaker parameters than
// HashPassword uses today.
func NeedsRehash(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return true
	}
	iter, err := strconv.Atoi(parts[1])
	return err != nil || iter < iterations
}
