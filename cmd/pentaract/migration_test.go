package main

import (
	"context"
	"encoding/json"
	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlainMigration(t *testing.T) {
	source, target := os.Getenv("MIGRATION_TEST_SOURCE"), os.Getenv("MIGRATION_TEST_TARGET")
	if source == "" || target == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = database.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	key, _ := cryptostore.RandomKey()
	token, _ := cryptostore.SealSecret(key, []byte("fixture"))
	_, e = db.Exec(ctx, `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,'test',-100,123,'test',$1)`, token)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,totp_secret_enc) VALUES(gen_random_uuid(),'fixture@example.test','fixture','');INSERT INTO nodes(id,name,kind) VALUES(gen_random_uuid(),'Books','folder')`)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "source.key"), key, 0600)
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 123, "username": "test"}})
	}))
	defer tg.Close()
	c := config.Config{DatabaseURL: source, MasterKeyFile: filepath.Join(root, "source.key"), DataDir: root, TelegramAPIURL: tg.URL, TrashRetention: 72 * time.Hour, TelegramChunkSize: 256 * 1024 * 1024}
	t.Setenv("MIGRATION_DIR", filepath.Join(root, "transfer"))
	t.Setenv("TELEGRAM_API_ID", "123")
	t.Setenv("TELEGRAM_API_HASH", "fixture")
	t.Setenv("PENTARACT_MASTER_KEY", "")
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	if e = migrationCommand(ctx, c, l, "migration-export"); e != nil {
		t.Fatal(e)
	}
	if e = migrationCommand(ctx, c, l, "migration-export"); e == nil {
		t.Fatal("overwrote export")
	}
	c.DatabaseURL = target
	c.MasterKeyFile = filepath.Join(root, "target.key")
	if e = migrationCommand(ctx, c, l, "migration-import"); e != nil {
		t.Fatal(e)
	}
	if e = migrationCommand(ctx, c, l, "migration-import"); e == nil {
		t.Fatal("overwrote target DB")
	}
	targetDB, e := database.Open(ctx, target)
	if e != nil {
		t.Fatal(e)
	}
	defer targetDB.Close()
	var count int
	targetDB.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	if count != 1 {
		t.Fatal("owner missing")
	}
	path := filepath.Join(root, "transfer", "master.key")
	os.WriteFile(path, []byte("wrong"), 0600)
	if _, e = readTransfer(filepath.Dir(path)); e == nil {
		t.Fatal("accepted tampered key")
	}
}
