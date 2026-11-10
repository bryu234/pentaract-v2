package httpapi

import (
	"context"
	"encoding/json"
	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPublicShares(t *testing.T) {
	dburl := os.Getenv("SHARES_TEST_DATABASE_URL")
	if dburl == "" {
		t.Skip("isolated database required")
	}
	ctx := context.Background()
	db, e := database.Open(ctx, dburl)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = database.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	key, _ := cryptostore.RandomKey()
	wrapped, _ := cryptostore.SealSecret(key, key)
	token, _ := cryptostore.SealSecret(key, []byte("mock"))
	folder, id := uuid.New(), uuid.New()
	_, e = db.Exec(ctx, `INSERT INTO nodes(id,name,kind) VALUES($1,'folder','folder')`, folder)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO nodes(id,parent_id,name,kind,state,size,wrapped_key) VALUES($1,$2,'empty','file','ready',0,$3)`, id, folder, wrapped)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,'mock',-100,1,'mock',$1)`, token)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{DB: db, MasterKey: key, Config: config.Config{PublicURL: "https://example.test"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	create := func(once bool) string {
		body := `{"ttl_seconds":86400,"one_time":` + map[bool]string{true: "true", false: "false"}[once] + `}`
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id.String())
		r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		s.createShare(w, r)
		if w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		var v struct {
			URL string `json:"url"`
		}
		json.Unmarshal(w.Body.Bytes(), &v)
		u, _ := url.Parse(v.URL)
		return u.Fragment
	}
	call := func(tok, method string) int {
		r := httptest.NewRequest(method, "/", strings.NewReader(url.Values{"token": {tok}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.publicShare(w, r)
		return w.Code
	}
	tok := create(true)
	if call(tok, "GET") != 200 || call(tok, "GET") != 200 {
		t.Fatal("preview consumed link")
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call(tok, "POST") == 200 {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners %d", winners.Load())
	}
	if call(tok, "POST") != 410 {
		t.Fatal("reuse allowed")
	}
	multi := create(false)
	if call(multi, "POST") != 200 || call(multi, "POST") != 200 {
		t.Fatal("multi link failed")
	}
	_, e = db.Exec(ctx, `UPDATE nodes SET deleted_at=now() WHERE id=$1`, folder)
	if e != nil {
		t.Fatal(e)
	}
	if call(multi, "GET") != 410 {
		t.Fatal("folder trash leaked file")
	}
	db.Exec(ctx, `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, folder)
	if call(multi, "GET") != 410 {
		t.Fatal("restore revived link")
	}
	expired := create(false)
	db.Exec(ctx, `UPDATE file_shares SET expires_at=now()-interval '1 second'`)
	if call(expired, "POST") != 410 {
		t.Fatal("expired accepted")
	}
	if call(strings.Repeat("z", 43), "POST") != 410 {
		t.Fatal("invalid accepted")
	}
}
