package httpapi

import (
	"context"
	"crypto/sha256"
	"github.com/bryu234/pentaract-v2/internal/auth"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSecurityIntegration(t *testing.T) {
	u := os.Getenv("AUTH_TEST_DATABASE_URL")
	if u == "" {
		t.Skip("isolated database required")
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
	id := uuid.New()
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,totp_secret_enc,totp_enabled) VALUES($1,'fixture@example.test','unused','',true)`, id)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte("FIXTURE-CODE"))
	_, e = db.Exec(ctx, `INSERT INTO recovery_codes(id,user_id,code_hash) VALUES($1,$2,$3)`, uuid.New(), id, h[:])
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.consumeRecoveryCode(ctx, id, "FIXTURE-CODE") {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("recovery accepted %d times", wins.Load())
	}
	r := httptest.NewRequest("POST", "/", nil)
	w := httptest.NewRecorder()
	_, e = s.createSession(w, r, id)
	if e != nil {
		t.Fatal(e)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.MaxAge > 86400 || cookie.Expires.After(time.Now().Add(24*time.Hour+time.Second)) {
		t.Fatal("session exceeds day")
	}
	handler := s.Handler()
	get := func() int {
		r := httptest.NewRequest("GET", "/api/v1/auth/session", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if get() != 200 {
		t.Fatal("new session invalid")
	}
	_, e = db.Exec(ctx, `UPDATE sessions SET created_at=now()-interval '25 hours',expires_at=now()+interval '29 days' WHERE token_hash=$1`, auth.TokenHash(cookie.Value))
	if e != nil {
		t.Fatal(e)
	}
	if get() != 401 {
		t.Fatal("old cookie accepted")
	}
	db.Exec(ctx, `DELETE FROM schema_migrations WHERE version=5`)
	if e = database.Migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	var capped bool
	db.QueryRow(ctx, `SELECT expires_at<=created_at+interval '24 hours' FROM sessions LIMIT 1`).Scan(&capped)
	if !capped {
		t.Fatal("migration did not cap old sessions")
	}
}
