package cryptostore

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestSecretRoundTripAndTamperDetection(t *testing.T) {
	key, err := RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := SealSecret(key, []byte("sensitive"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenSecret(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != "sensitive" {
		t.Fatalf("unexpected plaintext %q", opened)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := OpenSecret(key, sealed); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestEncryptedChunkRoundTripAndRange(t *testing.T) {
	key, _ := RandomKey()
	fileID := uuid.New()
	plain := bytes.Repeat([]byte("0123456789abcdef"), 600_000)
	var encrypted bytes.Buffer
	meta, err := EncryptChunk(bytes.NewReader(plain), &encrypted, key, fileID, 0, int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.PlainSize != int64(len(plain)) {
		t.Fatalf("plain size = %d", meta.PlainSize)
	}
	var restored bytes.Buffer
	if err := DecryptChunk(bytes.NewReader(encrypted.Bytes()), &restored, key, fileID, 0, meta.CipherHash, 0, 100, 999); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.Bytes(), plain[100:1000]) {
		t.Fatal("range plaintext mismatch")
	}
}

func TestEncryptedChunkRejectsTampering(t *testing.T) {
	key, _ := RandomKey()
	fileID := uuid.New()
	var encrypted bytes.Buffer
	meta, err := EncryptChunk(bytes.NewReader([]byte("hello")), &encrypted, key, fileID, 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data := encrypted.Bytes()
	data[len(data)-1] ^= 1
	if err := DecryptChunk(bytes.NewReader(data), &bytes.Buffer{}, key, fileID, 1, meta.CipherHash, 0, 0, 4); err == nil {
		t.Fatal("tampered frame was accepted")
	}
}
