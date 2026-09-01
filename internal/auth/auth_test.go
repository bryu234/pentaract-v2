package auth

import (
	"bytes"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("short password was accepted")
	}
	hash, err := HashPassword("a sufficiently long test password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a sufficiently long test password") {
		t.Fatal("correct password was rejected")
	}
	if VerifyPassword(hash, "a different sufficiently long password") {
		t.Fatal("incorrect password was accepted")
	}
}

func TestRecoveryCodesAreUniqueAndHashed(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(codes))
	for i, code := range codes {
		if seen[code] {
			t.Fatalf("duplicate recovery code %q", code)
		}
		seen[code] = true
		if bytes.Equal(hashes[i], []byte(code)) || len(hashes[i]) != 32 {
			t.Fatal("recovery code was not SHA-256 hashed")
		}
	}
}
