package recovery

import (
	"context"
	"encoding/json"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationAndImport(t *testing.T) {
	url := os.Getenv("RECOVERY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = database.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	key, _ := cryptostore.RandomKey()
	sealed, _ := cryptostore.SealSecret(key, []byte("test"))
	_, e = db.Exec(ctx, `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,'Test',-100,123,'test',$1);`, sealed)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO nodes(id,name,kind,state,size,recovery_pending) VALUES('00000000-0000-0000-0000-000000000001','empty','file','ready',0,true)`)
	if e != nil {
		t.Fatal(e)
	}
	shared := t.TempDir()
	os.Mkdir(filepath.Join(shared, "outbox"), 0700)
	failPin := true
	var data []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		result := any(true)
		ok := true
		switch {
		case strings.HasSuffix(r.URL.Path, "sendDocument"):
			data, e = os.ReadFile(strings.TrimPrefix(r.Form.Get("document"), "file://"))
			if e != nil {
				t.Error(e)
			}
			result = map[string]any{"message_id": 1, "document": map[string]any{"file_id": "test"}}
		case strings.HasSuffix(r.URL.Path, "pinChatMessage"):
			ok = !failPin
		case strings.HasSuffix(r.URL.Path, "getMe"):
			result = map[string]any{"id": 123, "username": "test"}
		case strings.HasSuffix(r.URL.Path, "getChat"):
			result = map[string]any{"id": -100}
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": ok, "result": result, "error_code": 400, "description": "test"})
	}))
	defer srv.Close()
	tg := telegram.New(srv.URL)
	if e = Publish(ctx, db, tg, key, shared); e == nil {
		t.Fatal("expected pin failure")
	}
	var pending bool
	db.QueryRow(ctx, `SELECT recovery_pending FROM nodes`).Scan(&pending)
	if !pending {
		t.Fatal("premature ready")
	}
	failPin = false
	if e = Publish(ctx, db, tg, key, shared); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(ctx, `SELECT recovery_pending FROM nodes`).Scan(&pending)
	if pending {
		t.Fatal("pending after publication")
	}
	c, e := Decode(data, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `CREATE DATABASE recovery_import`); e != nil {
		t.Fatal(e)
	}
	target, e := database.Open(ctx, strings.TrimSuffix(url, "/postgres")+"/recovery_import")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	if e = database.Migrate(ctx, target); e != nil {
		t.Fatal(e)
	}
	if e = Restore(ctx, target, c, key, "test", tg); e != nil {
		t.Fatal(e)
	}
	var paused bool
	target.QueryRow(ctx, `SELECT paused FROM recovery_status`).Scan(&paused)
	if !paused {
		t.Fatal("not paused")
	}
	if e = Restore(ctx, target, c, key, "test", tg); e == nil {
		t.Fatal("import overwrote database")
	}
}
