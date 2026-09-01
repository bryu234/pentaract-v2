package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL       string
	ListenAddress     string
	DataDir           string
	SharedDir         string
	MasterKeyFile     string
	PublicURL         string
	CookieSecure      bool
	TelegramAPIURL    string
	TrashRetention    time.Duration
	TelegramChunkSize int64
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:    env("PENTARACT_DATABASE_URL", "postgres://pentaract:pentaract@localhost:5432/pentaract"),
		ListenAddress:  env("PENTARACT_LISTEN_ADDRESS", ":8080"),
		DataDir:        env("PENTARACT_DATA_DIR", "/data"),
		SharedDir:      env("PENTARACT_SHARED_DIR", "/shared"),
		MasterKeyFile:  env("PENTARACT_MASTER_KEY_FILE", "/run/pentaract-secrets/master.key"),
		PublicURL:      env("PENTARACT_PUBLIC_URL", "http://localhost:8088"),
		CookieSecure:   envBool("PENTARACT_COOKIE_SECURE", false),
		TelegramAPIURL: env("PENTARACT_TELEGRAM_API_URL", "http://telegram-bot-api:8081"),
	}
	days, err := envInt("PENTARACT_TRASH_RETENTION_DAYS", 30)
	if err != nil || days < 1 {
		return Config{}, errors.New("PENTARACT_TRASH_RETENTION_DAYS must be a positive integer")
	}
	chunkMiB, err := envInt("PENTARACT_TELEGRAM_CHUNK_MIB", 256)
	if err != nil || chunkMiB < 1 || chunkMiB > 1900 {
		return Config{}, errors.New("PENTARACT_TELEGRAM_CHUNK_MIB must be between 1 and 1900")
	}
	c.TrashRetention = time.Duration(days) * 24 * time.Hour
	c.TelegramChunkSize = int64(chunkMiB) * 1024 * 1024
	return c, nil
}

func (c Config) EnsureDirectories() error {
	for _, path := range []string{filepath.Join(c.DataDir, "uploads"), filepath.Join(c.DataDir, "backups"), filepath.Join(c.SharedDir, "outbox")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	return nil
}

func LoadMasterKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(raw) == 32 {
		return raw, nil
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(string(raw))
	if decodeErr == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, errors.New("master key must contain exactly 32 random bytes or their base64 encoding")
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
