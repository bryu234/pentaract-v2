package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/bryu234/pentaract-v2/migrations"
)

type transferManifest struct {
	Schema    int               `json:"schema_version"`
	Format    int               `json:"format"`
	Created   time.Time         `json:"created_at"`
	Revision  string            `json:"source_revision"`
	APIID     string            `json:"telegram_api_id"`
	APIHash   string            `json:"telegram_api_hash"`
	Chunk     int64             `json:"chunk_mib"`
	Retention int               `json:"retention_days"`
	Hashes    map[string]string `json:"sha256"`
}

func fileHash(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readTransfer(dir string) (transferManifest, error) {
	var m transferManifest
	b, e := os.ReadFile(filepath.Join(dir, "restore.json"))
	if e != nil {
		return m, e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return m, e
	}
	entries, _ := migrations.Files.ReadDir(".")
	supported := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			v, _ := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
			if v > supported {
				supported = v
			}
		}
	}
	if m.Schema < 1 || m.Schema > supported {
		return m, errors.New("backup schema is newer than this application; use a compatible release")
	}
	if m.Format != 1 || m.APIID == "" || m.APIHash == "" || m.Chunk < 1 || m.Chunk > 1900 || m.Retention < 1 || m.Retention > 3 {
		return m, errors.New("invalid migration manifest")
	}
	for _, n := range []string{"master.key", "database.dump"} {
		h, e := fileHash(filepath.Join(dir, n))
		if e != nil || h != m.Hashes[n] {
			return m, errors.New("migration checksum mismatch: " + n)
		}
	}
	return m, nil
}
func migrationCommand(ctx context.Context, c config.Config, l *slog.Logger, command string) error {
	dir := os.Getenv("MIGRATION_DIR")
	if dir == "" {
		dir = "/migration"
	}
	if command == "backup-db" {
		out := filepath.Join(c.DataDir, "backups", "database-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".dump")
		if e := runCommand(ctx, "pg_dump", "--dbname", c.DatabaseURL, "--format=custom", "--file", out); e != nil {
			return e
		}
		if e := os.Chmod(out, 0600); e != nil {
			return e
		}
		l.Info("database backup created", "path", out)
		return migrationOwner(out)
	}
	if command == "migration-export" || command == "migration-check" {
		db, e := database.Open(ctx, c.DatabaseURL)
		if e != nil {
			return e
		}
		defer db.Close()
		var busy bool
		if e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upload_sessions) OR EXISTS(SELECT 1 FROM nodes WHERE kind='file' AND (state<>'ready' OR recovery_pending))`).Scan(&busy); e != nil {
			return e
		}
		if busy {
			return errors.New("finish or remove incomplete uploads before migration")
		}
		if command == "migration-check" {
			return nil
		}
		if _, e = os.Stat(dir); !os.IsNotExist(e) {
			return errors.New("migration output already exists")
		}
		tmp, e := os.MkdirTemp(filepath.Dir(dir), ".migration-")
		if e != nil {
			return e
		}
		defer os.RemoveAll(tmp)
		key, e := config.LoadMasterKey(c.MasterKeyFile)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(tmp, "master.key"), []byte(base64.StdEncoding.EncodeToString(key)), 0600); e != nil {
			return e
		}
		if e = runCommand(ctx, "pg_dump", "--dbname", c.DatabaseURL, "--format=custom", "--file", filepath.Join(tmp, "database.dump")); e != nil {
			return e
		}
		var schema int
		if e = db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&schema); e != nil {
			return e
		}
		m := transferManifest{schema, 1, time.Now().UTC(), os.Getenv("SOURCE_REVISION"), os.Getenv("TELEGRAM_API_ID"), os.Getenv("TELEGRAM_API_HASH"), c.TelegramChunkSize / 1024 / 1024, int(c.TrashRetention / (24 * time.Hour)), map[string]string{}}
		for _, n := range []string{"master.key", "database.dump"} {
			m.Hashes[n], e = fileHash(filepath.Join(tmp, n))
			if e != nil {
				return e
			}
			os.Chmod(filepath.Join(tmp, n), 0600)
		}
		b, e := json.MarshalIndent(m, "", "  ")
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(tmp, "restore.json"), b, 0600); e != nil {
			return e
		}
		if _, e = readTransfer(tmp); e != nil {
			return e
		}
		return os.Rename(tmp, dir)
	}
	m, e := readTransfer(dir)
	if e != nil {
		return e
	}
	keyRaw, e := os.ReadFile(filepath.Join(dir, "master.key"))
	if e != nil {
		return e
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyRaw)))
	if e != nil || len(key) != 32 {
		return errors.New("invalid migration key")
	}
	if command == "migration-config" {
		public := os.Getenv("PUBLIC_URL")
		u, e := url.Parse(public)
		if e != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || strings.ContainsAny(public, "$\"'\\") {
			return errors.New("PUBLIC_URL must be http://server:port")
		}
		port := os.Getenv("PORT")
		if port == "" {
			port = "8088"
		}
		v, e := strconv.Atoi(port)
		if e != nil || v < 1 || v > 65535 || u.Port() != port {
			return errors.New("PUBLIC_URL port must match PORT (default 8088)")
		}
		for _, value := range []string{m.APIID, m.APIHash} {
			if strings.ContainsAny(value, "\r\n$\"'\\") {
				return errors.New("invalid Telegram settings")
			}
		}
		dest := "/target/.env"
		expected := fmt.Sprintf("TELEGRAM_API_ID=%s\nTELEGRAM_API_HASH=%s\nPENTARACT_PUBLIC_URL=%s\nPENTARACT_HTTP_PORT=%s\nPENTARACT_COOKIE_SECURE=false\nPENTARACT_TRASH_RETENTION_DAYS=%d\nPENTARACT_TELEGRAM_CHUNK_MIB=%d\n", m.APIID, m.APIHash, public, port, m.Retention, m.Chunk)
		if old, e := os.ReadFile(dest); e == nil {
			if !strings.HasPrefix(string(old), expected) {
				return errors.New("existing .env conflicts with migration")
			}
			return nil
		} else if !os.IsNotExist(e) {
			return e
		}
		random, e := cryptostore.RandomKey()
		if e != nil {
			return e
		}
		f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(expected + "POSTGRES_PASSWORD=" + hex.EncodeToString(random) + "\n")
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	if old, e := config.LoadMasterKey(c.MasterKeyFile); e == nil {
		if string(old) != string(key) {
			return errors.New("existing master key conflicts")
		}
	} else if !os.IsNotExist(e) && !strings.Contains(e.Error(), "no such file") {
		return e
	}
	db, e := database.Open(ctx, c.DatabaseURL)
	if e != nil {
		return e
	}
	defer db.Close()
	var nonempty bool
	if e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public')`).Scan(&nonempty); e != nil {
		return e
	}
	if nonempty {
		return errors.New("migration requires an empty database; existing data is never overwritten")
	}
	tmp, e := os.MkdirTemp("", "migration-import-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	sql := filepath.Join(tmp, "restore.sql")
	if e = runCommand(ctx, "pg_restore", "--no-owner", "--file", sql, filepath.Join(dir, "database.dump")); e != nil {
		return e
	}
	f, e := os.OpenFile(sql, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.WriteString("\nDELETE FROM public.sessions;\nDO $$ BEGIN IF to_regclass('public.file_shares') IS NOT NULL THEN UPDATE public.file_shares SET revoked_at=now(); END IF; END $$;\n")
	f.Close()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(c.MasterKeyFile), 0700); e != nil {
		return e
	}
	if e = os.WriteFile(c.MasterKeyFile, key, 0600); e != nil {
		return e
	}
	if e = runCommand(ctx, "psql", "--dbname", c.DatabaseURL, "--single-transaction", "--set", "ON_ERROR_STOP=on", "--file", sql); e != nil {
		return e
	}
	if e = database.Migrate(ctx, db); e != nil {
		return e
	}
	var token []byte
	var chat int64
	if e = db.QueryRow(ctx, `SELECT bot_token_enc,chat_id FROM telegram_settings WHERE id=1`).Scan(&token, &chat); e != nil {
		return e
	}
	plain, e := cryptostore.OpenSecret(key, token)
	if e != nil {
		return errors.New("restored Telegram credentials cannot be decrypted")
	}
	tg := telegram.New(c.TelegramAPIURL)
	if _, e = tg.GetMe(ctx, string(plain)); e != nil {
		return e
	}
	if _, e = tg.GetChat(ctx, string(plain), chat); e != nil {
		return e
	}
	l.Info("migration imported; old sessions cleared and links revoked")
	return nil
}

func migrationOwner(path string) error {
	if os.Getenv("EXPORT_UID") == "" {
		return nil
	}
	uid, e := strconv.Atoi(os.Getenv("EXPORT_UID"))
	if e != nil {
		return e
	}
	gid, e := strconv.Atoi(os.Getenv("EXPORT_GID"))
	if e != nil {
		return e
	}
	return os.Chown(path, uid, gid)
}
