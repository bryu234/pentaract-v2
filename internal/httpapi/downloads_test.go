package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestArchivePath(t *testing.T) {
	for _, parts := range [][]string{{"..", "file"}, {"folder", "../file"}, {"a\\b"}, {"/absolute"}, {"C:evil"}, {"."}, {""}} {
		if _, err := archivePath(parts); err == nil {
			t.Fatalf("unsafe path accepted: %v", parts)
		}
	}
	if path, err := archivePath([]string{"Папка", "nested", "file.txt"}); err != nil || path != "Папка/nested/file.txt" {
		t.Fatal(path, err)
	}
}

func TestFolderDownloadIntegration(t *testing.T) {
	url := os.Getenv("FILES_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("isolated database required")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	key, _ := cryptostore.RandomKey()
	wrapped, _ := cryptostore.SealSecret(key, key)
	token, _ := cryptostore.SealSecret(key, []byte("fixture"))
	if _, err = db.Exec(ctx, `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,'fixture',-100,1,'fixture',$1)`, token); err != nil {
		t.Fatal(err)
	}
	root, nested, file, removed, empty := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, f := range []struct {
		id     uuid.UUID
		parent *uuid.UUID
		name   string
	}{{root, nil, "Папка"}, {nested, &root, "nested"}, {empty, &root, "empty"}} {
		if _, err = db.Exec(ctx, `INSERT INTO nodes(id,parent_id,name,kind) VALUES($1,$2,$3,'folder')`, f.id, f.parent, f.name); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("small fixture content")
	for _, f := range []struct {
		id   uuid.UUID
		name string
	}{{file, "test.txt"}, {removed, "removed.txt"}} {
		if _, err = db.Exec(ctx, `INSERT INTO nodes(id,parent_id,name,kind,state,size,wrapped_key) VALUES($1,$2,$3,'file','ready',$4,$5)`, f.id, nested, f.name, len(data), wrapped); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(ctx, `UPDATE nodes SET deleted_at=now(),purge_after=now()+interval '2 days' WHERE id=$1`, removed)
	// ResolveFile intentionally accepts only this shared-container directory.
	if err = os.MkdirAll("/var/lib/telegram-bot-api", 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/var/lib/telegram-bot-api", "fixture-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "chunk")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := cryptostore.EncryptChunk(bytes.NewReader(data), out, key, file, 0, int64(len(data)))
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO file_chunks(id,node_id,position,plain_offset,plain_size,cipher_size,nonce_prefix,plain_hash,cipher_hash,telegram_file_id,telegram_file_unique_id,telegram_message_id) VALUES($1,$2,0,0,$3,$4,$5,$6,$7,'fixture','fixture',1)`, uuid.New(), file, meta.PlainSize, meta.CipherSize, meta.NoncePrefix, meta.PlainHash, meta.CipherHash)
	if err != nil {
		t.Fatal(err)
	}
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]string{"file_path": path}})
	}))
	defer tg.Close()
	s := &Server{DB: db, MasterKey: key, Telegram: telegram.New(tg.URL), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	call := func(id uuid.UUID, rangeHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Range", rangeHeader)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id.String())
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		s.download(w, r)
		return w
	}
	w := call(root, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range z.File {
		seen[f.Name] = true
		if strings.HasSuffix(f.Name, "test.txt") {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			got, e := io.ReadAll(r)
			r.Close()
			if e != nil || !bytes.Equal(got, data) {
				t.Fatal("archive content differs", e)
			}
		}
	}
	for _, name := range []string{"Папка/", "Папка/nested/", "Папка/empty/", "Папка/nested/test.txt"} {
		if !seen[name] {
			t.Fatal("missing", name)
		}
	}
	if len(seen) != 4 {
		t.Fatal("unexpected archive entries", seen)
	}
	w = call(file, "bytes=1-4")
	if w.Code != 206 || w.Body.String() != string(data[1:5]) {
		t.Fatal("range regression", w.Code, w.Body.String())
	}
	db.Exec(ctx, `UPDATE nodes SET recovery_pending=true WHERE id=$1`, file)
	if w = call(root, ""); w.Code != 409 {
		t.Fatal("unfinished content not rejected", w.Code)
	}
	db.Exec(ctx, `UPDATE nodes SET recovery_pending=false WHERE id=$1`, file)
	db.Exec(ctx, `UPDATE nodes SET deleted_at=now(),purge_after=now()+interval '2 days' WHERE id=$1`, root)
	if w = call(file, ""); w.Code != 404 {
		t.Fatal("trashed ancestor file was exposed", w.Code)
	}
	if w = call(nested, ""); w.Code != 404 {
		t.Fatal("trashed ancestor folder was exposed", w.Code)
	}
}
