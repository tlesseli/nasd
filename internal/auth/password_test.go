package auth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestPasswordAndRehash(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong") {
		t.Error("check")
	}
	if NeedsRehash(h) {
		t.Error("a fresh hash needs a rehash")
	}
	// A hash from an older nasd (200,000 iterations) still works, and is
	// flagged for an upgrade.
	salt := []byte("0123456789abcdef")
	key, _ := pbkdf2.Key(sha256.New, "old one", salt, 200_000, 32)
	old := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", 200_000, b64.EncodeToString(salt), b64.EncodeToString(key))
	if !CheckPassword(old, "old one") || !NeedsRehash(old) {
		t.Error("old hash")
	}
	if !NeedsRehash("garbage") {
		t.Error("garbage")
	}
}
