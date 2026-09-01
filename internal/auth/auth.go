package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)

func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func GenerateTOTP(email, issuer string) (secret, uri string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: email, Period: 30, SecretSize: 20})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func ValidateTOTP(code, secret string) bool { return totp.Validate(code, secret) }

func GenerateRecoveryCodes(amount int) ([]string, [][]byte, error) {
	codes := make([]string, amount)
	hashes := make([][]byte, amount)
	for i := range amount {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		encoded := strings.ToUpper(base64.RawStdEncoding.EncodeToString(raw))
		codes[i] = encoded[:5] + "-" + encoded[5:10] + "-" + encoded[10:]
		hash := sha256.Sum256([]byte(codes[i]))
		hashes[i] = hash[:]
	}
	return codes, hashes, nil
}

func RandomToken(bytes int) (raw string, hash []byte, err error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(data)
	sum := sha256.Sum256([]byte(raw))
	return raw, sum[:], nil
}

func TokenHash(raw string) []byte { sum := sha256.Sum256([]byte(raw)); return sum[:] }

func SessionExpiry() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
