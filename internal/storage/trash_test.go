package storage

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/google/uuid"
)

func TestTrashExpiryIntegration(t *testing.T) {
	u := os.Getenv("TRASH_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("isolated database required")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	expired, child, recent, active := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{expired, recent, active} {
		if _, err = db.Exec(ctx, `INSERT INTO nodes(id,name,kind) VALUES($1,$2,'folder')`, id, id.String()); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(ctx, `INSERT INTO nodes(id,parent_id,name,kind,state) VALUES($1,$2,'pending','file','uploading')`, child, expired)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(ctx, `UPDATE nodes SET deleted_at=now()-interval '4 days',purge_after=now()+interval '26 days' WHERE id=$1`, expired)
	db.Exec(ctx, `UPDATE nodes SET deleted_at=now()-interval '1 day',purge_after=now()+interval '29 days' WHERE id=$1`, recent)
	// Reapply just the new migration against old trash deadlines.
	db.Exec(ctx, `DELETE FROM schema_migrations WHERE version=6`)
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var capped bool
	if err = db.QueryRow(ctx, `SELECT bool_and(purge_after<=deleted_at+interval '3 days') FROM nodes WHERE deleted_at IS NOT NULL`).Scan(&capped); err != nil || !capped {
		t.Fatal("old trash deadlines not capped", err)
	}
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	os.Mkdir(uploads, 0700)
	path := filepath.Join(uploads, "fixture")
	os.WriteFile(path, []byte("fixture"), 0600)
	_, err = db.Exec(ctx, `INSERT INTO upload_sessions(id,node_id,temp_path,expected_size,expires_at) VALUES($1,$2,$3,7,now()+interval '1 day')`, uuid.New(), child, path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Processor{DB: db, Config: config.Config{DataDir: dir}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	db.Exec(ctx, `UPDATE recovery_status SET paused=true WHERE id=1`)
	p.purgeTrash(ctx)
	var count int
	db.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&count)
	if count != 4 {
		t.Fatal("paused storage was purged", count)
	}
	db.Exec(ctx, `UPDATE recovery_status SET paused=false WHERE id=1`)
	p.purgeTrash(ctx)
	db.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&count)
	if count != 2 {
		t.Fatal("expired subtree was not removed", count)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary upload retained", err)
	}
	var pending bool
	db.QueryRow(ctx, `SELECT revision>published_revision FROM recovery_status`).Scan(&pending)
	if !pending {
		t.Fatal("purge not scheduled for recovery catalog publication")
	}
	var exists bool
	db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, active).Scan(&exists)
	if !exists {
		t.Fatal("active folder removed")
	}
	// Telegram is nil: cleanup must never attempt remote deletion.
	p.purgeTrash(ctx)
}
