package cryptostore

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	frameSize = 4 * 1024 * 1024
	magic     = "PV2C0001"
)

type ChunkMeta struct {
	PlainSize   int64
	CipherSize  int64
	NoncePrefix []byte
	PlainHash   []byte
	CipherHash  []byte
}

func RandomKey() ([]byte, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	_, err := rand.Read(key)
	return key, err
}

func SealSecret(masterKey, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(masterKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, plaintext, nil)...), nil
}

func OpenSecret(masterKey, sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(masterKey)
	if err != nil {
		return nil, err
	}
	if len(sealed) < chacha20poly1305.NonceSizeX+aead.Overhead() {
		return nil, errors.New("encrypted secret is truncated")
	}
	return aead.Open(nil, sealed[:chacha20poly1305.NonceSizeX], sealed[chacha20poly1305.NonceSizeX:], nil)
}

func EncryptChunk(r io.Reader, w io.Writer, key []byte, fileID uuid.UUID, chunkIndex int, maxPlain int64) (ChunkMeta, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return ChunkMeta{}, err
	}
	prefix := make([]byte, 16)
	if _, err := rand.Read(prefix); err != nil {
		return ChunkMeta{}, err
	}
	plainHasher, cipherHasher := blake3.New(), blake3.New()
	counter := int64(0)
	writeCipher := io.MultiWriter(w, cipherHasher)
	if _, err := writeCipher.Write([]byte(magic)); err != nil {
		return ChunkMeta{}, err
	}
	if _, err := writeCipher.Write(prefix); err != nil {
		return ChunkMeta{}, err
	}
	var plainSize int64
	buf := make([]byte, frameSize)
	limited := io.LimitReader(r, maxPlain)
	for {
		n, readErr := io.ReadFull(limited, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return ChunkMeta{}, readErr
		}
		if n == 0 {
			break
		}
		plain := buf[:n]
		_, _ = plainHasher.Write(plain)
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		copy(nonce, prefix)
		binary.BigEndian.PutUint64(nonce[16:], uint64(counter))
		ciphertext := aead.Seal(nil, nonce, plain, aad(fileID, chunkIndex, counter))
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(n))
		if _, err := writeCipher.Write(length[:]); err != nil {
			return ChunkMeta{}, err
		}
		if _, err := writeCipher.Write(ciphertext); err != nil {
			return ChunkMeta{}, err
		}
		plainSize += int64(n)
		counter++
		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
	}
	cipherSize := int64(len(magic)+len(prefix)) + counter*(4+int64(aead.Overhead())) + plainSize
	return ChunkMeta{PlainSize: plainSize, CipherSize: cipherSize, NoncePrefix: prefix, PlainHash: plainHasher.Sum(nil), CipherHash: cipherHasher.Sum(nil)}, nil
}

func DecryptChunk(r io.Reader, w io.Writer, key []byte, fileID uuid.UUID, chunkIndex int, expectedCipherHash []byte, globalOffset, rangeStart, rangeEnd int64) error {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	cipherHasher := blake3.New()
	r = io.TeeReader(r, cipherHasher)
	header := make([]byte, len(magic)+16)
	if _, err := io.ReadFull(r, header); err != nil {
		return err
	}
	if string(header[:len(magic)]) != magic {
		return errors.New("invalid encrypted chunk header")
	}
	prefix := header[len(magic):]
	var frameIndex int64
	plainCursor := globalOffset
	for {
		var length [4]byte
		_, err := io.ReadFull(r, length[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		plainLength := int(binary.BigEndian.Uint32(length[:]))
		if plainLength < 0 || plainLength > frameSize {
			return errors.New("invalid encrypted frame size")
		}
		ciphertext := make([]byte, plainLength+aead.Overhead())
		if _, err := io.ReadFull(r, ciphertext); err != nil {
			return err
		}
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		copy(nonce, prefix)
		binary.BigEndian.PutUint64(nonce[16:], uint64(frameIndex))
		plain, err := aead.Open(nil, nonce, ciphertext, aad(fileID, chunkIndex, frameIndex))
		if err != nil {
			return fmt.Errorf("authenticate frame %d: %w", frameIndex, err)
		}
		frameStart, frameEnd := plainCursor, plainCursor+int64(len(plain))-1
		if frameEnd >= rangeStart && frameStart <= rangeEnd {
			from, to := int64(0), int64(len(plain))
			if rangeStart > frameStart {
				from = rangeStart - frameStart
			}
			if rangeEnd < frameEnd {
				to = rangeEnd - frameStart + 1
			}
			if _, err := w.Write(plain[from:to]); err != nil {
				return err
			}
		}
		plainCursor += int64(len(plain))
		frameIndex++
	}
	if expectedCipherHash != nil && !equal(cipherHasher.Sum(nil), expectedCipherHash) {
		return errors.New("encrypted chunk checksum mismatch")
	}
	return nil
}

func aad(fileID uuid.UUID, chunkIndex int, frameIndex int64) []byte {
	b := make([]byte, 16+4+8)
	copy(b, fileID[:])
	binary.BigEndian.PutUint32(b[16:], uint32(chunkIndex))
	binary.BigEndian.PutUint64(b[20:], uint64(frameIndex))
	return b
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var result byte
	for i := range a {
		result |= a[i] ^ b[i]
	}
	return result == 0
}
