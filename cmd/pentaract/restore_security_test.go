package main

import (
	"context"
	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreSecurity(t *testing.T) {
	u := os.Getenv("RESTORE_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("isolated restore DB required")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = database.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "backups"), 0700)
	key := filepath.Join(root, "master.key")
	os.WriteFile(key, make([]byte, 32), 0600)
	cfg := config.Config{DatabaseURL: u, MasterKeyFile: key, DataDir: root}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	user, node := uuid.New(), uuid.New()
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,totp_secret_enc) VALUES($1,'restore@example.test','unused','')`, user)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO nodes(id,name,kind) VALUES($1,'fixture','folder')`, node)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,'fixture','fixture',now()+interval '1 day');`, uuid.New(), user)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO file_shares(id,node_id,token_hash,token_enc,expires_at,one_time) VALUES($1,$2,'fixture','fixture',now()+interval '1 day',true)`, uuid.New(), node)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("BACKUP_PASSPHRASE", "fixture-only-backup-passphrase")
	t.Setenv("PENTARACT_MASTER_KEY", "")
	save := func() string {
		if e := backup(ctx, cfg, logger); e != nil {
			t.Fatal(e)
		}
		files, _ := filepath.Glob(filepath.Join(root, "backups", "*.pv2backup"))
		if len(files) != 1 {
			t.Fatal("unexpected backup count")
		}
		return files[0]
	}
	file := save()
	t.Setenv("BACKUP_FILE", file)
	if e = restore(ctx, cfg, logger); e != nil {
		t.Fatal(e)
	}
	var count int
	db.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&count)
	if count != 0 {
		t.Fatal("sessions revived")
	}
	db.QueryRow(ctx, `SELECT count(*) FROM file_shares WHERE revoked_at IS NULL`).Scan(&count)
	if count != 0 {
		t.Fatal("links revived")
	}
	db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count)
	if count != 1 {
		t.Fatal("owner lost")
	}
	os.Rename(file, filepath.Join(root, "first.backup"))
	_, e = db.Exec(ctx, `CREATE TABLE rollback_marker(value integer); INSERT INTO rollback_marker VALUES(1); CREATE FUNCTION fail_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture cleanup failure'; END $$; CREATE TRIGGER fail_cleanup BEFORE UPDATE ON file_shares FOR EACH ROW EXECUTE FUNCTION fail_cleanup()`)
	if e != nil {
		t.Fatal(e)
	}
	file = save()
	db.Exec(ctx, `UPDATE rollback_marker SET value=2`)
	t.Setenv("BACKUP_FILE", file)
	if e = restore(ctx, cfg, logger); e == nil {
		t.Fatal("expected cleanup failure")
	}
	db.QueryRow(ctx, `SELECT value FROM rollback_marker`).Scan(&count)
	if count != 2 {
		t.Fatal("restore did not roll back")
	}
}
