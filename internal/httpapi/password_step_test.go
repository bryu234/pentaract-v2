package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/bryu234/pentaract-v2/internal/auth"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestTwoStepLogin(t *testing.T) {
	u := os.Getenv("LOGIN_STEP_TEST_DATABASE_URL")
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
	key, _ := cryptostore.RandomKey()
	secret, _, _ := auth.GenerateTOTP("steps@example.test", "test")
	sealed, _ := cryptostore.SealSecret(key, []byte(secret))
	hash, _ := auth.HashPassword("fixture-password-123")
	id := uuid.New()
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,password_hash,totp_secret_enc,totp_enabled) VALUES($1,'steps@example.test',$2,$3,true)`, id, hash, sealed)
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{DB: db, MasterKey: key, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Handler()
	call := func(path, password, code string) *httptest.ResponseRecorder {
		data := map[string]string{"email": "steps@example.test", "password": password}
		if path == "/api/v1/auth/login" {
			data["code"] = code
		}
		b, _ := json.Marshal(data)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/api/v1/auth/password", "wrong", ""); w.Code != 401 {
		t.Fatal("bad password accepted")
	}
	w := call("/api/v1/auth/password", "fixture-password-123", "")
	if w.Code != 200 || len(w.Result().Cookies()) != 0 {
		t.Fatal("password step issued session")
	}
	var count int
	db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, id).Scan(&count)
	if count != 0 {
		t.Fatal("password step persisted session")
	}
	if w = call("/api/v1/auth/login", "fixture-password-123", "invalid"); w.Code != 401 {
		t.Fatal("invalid OTP accepted")
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if w = call("/api/v1/auth/login", "fixture-password-123", code); w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("valid OTP rejected: %d", w.Code)
	}
}
