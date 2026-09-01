package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bryu234/pentaract-v2/internal/auth"
	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB        *pgxpool.Pool
	Config    config.Config
	MasterKey []byte
	Telegram  *telegram.Client
	Logger    *slog.Logger
}

type contextKey string

const sessionKey contextKey = "session"

type session struct {
	UserID uuid.UUID
	CSRF   string
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer(s.Logger), securityHeaders)
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"status": "ok"}) })
	r.Get("/health/ready", s.ready)
	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/setup/status", s.setupStatus)
		api.Post("/setup", s.setup)
		api.Post("/setup/verify", s.verifySetup)
		api.Post("/auth/login", s.login)
		api.Group(func(private chi.Router) {
			private.Use(s.authenticate, s.csrf)
			private.Get("/auth/session", s.currentSession)
			private.Delete("/auth/session", s.logout)
			private.Get("/settings/telegram", s.getTelegramSettings)
			private.Put("/settings/telegram", s.putTelegramSettings)
			private.Get("/files", s.listFiles)
			private.Post("/folders", s.createFolder)
			private.Patch("/files/{id}", s.renameNode)
			private.Delete("/files/{id}", s.trashNode)
			private.Post("/files/{id}/restore", s.restoreNode)
			private.Get("/files/{id}/download", s.download)
			private.Get("/trash", s.listTrash)
			private.Post("/uploads", s.createUpload)
			private.Head("/uploads/{id}", s.uploadStatus)
			private.Patch("/uploads/{id}", s.appendUpload)
			private.Post("/uploads/{id}/complete", s.completeUpload)
			private.Post("/uploads/{id}/retry", s.retryUpload)
		})
	})
	return r
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		problem(w, 503, "database unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ready"})
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	var exists bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE totp_enabled)`).Scan(&exists); err != nil {
		problem(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]bool{"configured": exists})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	var count int
	if err := s.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count > 0 {
		problem(w, 409, "owner setup is already initialized")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	secret, uri, err := auth.GenerateTOTP(strings.ToLower(strings.TrimSpace(in.Email)), "Pentaract V2")
	if err != nil {
		problem(w, 500, "cannot generate TOTP")
		return
	}
	sealed, err := cryptostore.SealSecret(s.MasterKey, []byte(secret))
	if err != nil {
		problem(w, 500, "cannot protect TOTP secret")
		return
	}
	_, err = s.DB.Exec(r.Context(), `INSERT INTO users(id,email,password_hash,totp_secret_enc) VALUES($1,$2,$3,$4)`, uuid.New(), strings.ToLower(strings.TrimSpace(in.Email)), hash, sealed)
	if err != nil {
		problem(w, 409, "owner already exists")
		return
	}
	writeJSON(w, 201, map[string]string{"otpauth_uri": uri, "manual_secret": secret})
}

func (s *Server) verifySetup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if !decodeJSON(w, r, &in) {
		return
	}
	var userID uuid.UUID
	var sealed []byte
	if err := s.DB.QueryRow(r.Context(), `SELECT id,totp_secret_enc FROM users WHERE NOT totp_enabled LIMIT 1`).Scan(&userID, &sealed); err != nil {
		problem(w, 409, "no pending setup")
		return
	}
	secret, err := cryptostore.OpenSecret(s.MasterKey, sealed)
	if err != nil || !auth.ValidateTOTP(in.Code, string(secret)) {
		problem(w, 401, "invalid TOTP code")
		return
	}
	codes, hashes, err := auth.GenerateRecoveryCodes(10)
	if err != nil {
		problem(w, 500, "cannot generate recovery codes")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE users SET totp_enabled=true WHERE id=$1`, userID); err == nil {
		for _, hash := range hashes {
			_, err = tx.Exec(r.Context(), `INSERT INTO recovery_codes(id,user_id,code_hash) VALUES($1,$2,$3)`, uuid.New(), userID, hash)
			if err != nil {
				break
			}
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		problem(w, 500, "cannot complete setup")
		return
	}
	csrf, err := s.createSession(w, r, userID)
	if err != nil {
		problem(w, 500, "cannot create session")
		return
	}
	writeJSON(w, 200, map[string]any{"csrf_token": csrf, "recovery_codes": codes})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password, Code string }
	if !decodeJSON(w, r, &in) {
		return
	}
	var userID uuid.UUID
	var passwordHash string
	var sealed []byte
	var enabled bool
	err := s.DB.QueryRow(r.Context(), `SELECT id,password_hash,totp_secret_enc,totp_enabled FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&userID, &passwordHash, &sealed, &enabled)
	if err != nil || !enabled || !auth.VerifyPassword(passwordHash, in.Password) {
		time.Sleep(300 * time.Millisecond)
		problem(w, 401, "invalid credentials")
		return
	}
	secret, err := cryptostore.OpenSecret(s.MasterKey, sealed)
	valid := err == nil && auth.ValidateTOTP(in.Code, string(secret))
	if !valid {
		hash := sha256.Sum256([]byte(strings.ToUpper(in.Code)))
		valid = s.DB.QueryRow(r.Context(), `UPDATE recovery_codes SET used_at=now() WHERE id=(SELECT id FROM recovery_codes WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL LIMIT 1) RETURNING true`, userID, hash[:]).Scan(&valid) == nil && valid
	}
	if !valid {
		problem(w, 401, "invalid second factor")
		return
	}
	csrf, err := s.createSession(w, r, userID)
	if err != nil {
		problem(w, 500, "cannot create session")
		return
	}
	writeJSON(w, 200, map[string]string{"csrf_token": csrf})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) (string, error) {
	raw, hash, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	csrf, _, err := auth.RandomToken(24)
	if err != nil {
		return "", err
	}
	expires := auth.SessionExpiry()
	_, err = s.DB.Exec(r.Context(), `INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,$3,$4,$5)`, uuid.New(), userID, hash, csrf, expires)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: "pv2_session", Value: raw, Path: "/", HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	return csrf, nil
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("pv2_session")
		if err != nil {
			problem(w, 401, "authentication required")
			return
		}
		var sess session
		err = s.DB.QueryRow(r.Context(), `SELECT user_id,csrf_token FROM sessions WHERE token_hash=$1 AND expires_at>now()`, auth.TokenHash(cookie.Value)).Scan(&sess.UserID, &sess.CSRF)
		if err != nil {
			problem(w, 401, "session expired")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			sess := r.Context().Value(sessionKey).(session)
			if r.Header.Get("X-CSRF-Token") != sess.CSRF {
				problem(w, 403, "invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	sess := r.Context().Value(sessionKey).(session)
	var email string
	_ = s.DB.QueryRow(r.Context(), `SELECT email FROM users WHERE id=$1`, sess.UserID).Scan(&email)
	writeJSON(w, 200, map[string]string{"email": email, "csrf_token": sess.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("pv2_session")
	if cookie != nil {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, auth.TokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "pv2_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(204)
}

func (s *Server) getTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var name, username string
	var chatID, botID int64
	err := s.DB.QueryRow(r.Context(), `SELECT storage_name,chat_id,bot_user_id,bot_username FROM telegram_settings WHERE id=1`).Scan(&name, &chatID, &botID, &username)
	if err == pgx.ErrNoRows {
		writeJSON(w, 200, map[string]bool{"configured": false})
		return
	}
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"configured": true, "storage_name": name, "chat_id": chatID, "bot_id": botID, "bot_username": username})
}

func (s *Server) putTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StorageName string `json:"storage_name"`
		ChatID      int64  `json:"chat_id"`
		BotToken    string `json:"bot_token"`
		LogoutCloud bool   `json:"logout_cloud"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ChatID >= 0 || in.BotToken == "" {
		problem(w, 400, "negative chat_id and bot_token are required")
		return
	}
	if in.LogoutCloud {
		if err := telegram.CloudLogout(r.Context(), in.BotToken); err != nil {
			problem(w, 502, err.Error())
			return
		}
	}
	bot, err := s.Telegram.GetMe(r.Context(), in.BotToken)
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	chat, err := s.Telegram.GetChat(r.Context(), in.BotToken, in.ChatID)
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	sealed, err := cryptostore.SealSecret(s.MasterKey, []byte(in.BotToken))
	if err != nil {
		problem(w, 500, "cannot encrypt bot token")
		return
	}
	name := strings.TrimSpace(in.StorageName)
	if name == "" {
		name = chat.Title
	}
	_, err = s.DB.Exec(r.Context(), `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,$1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET storage_name=excluded.storage_name,chat_id=excluded.chat_id,bot_user_id=excluded.bot_user_id,bot_username=excluded.bot_username,bot_token_enc=excluded.bot_token_enc,updated_at=now()`, name, chat.ID, bot.ID, bot.Username, sealed)
	if err != nil {
		problem(w, 500, "cannot save Telegram settings")
		return
	}
	writeJSON(w, 200, map[string]any{"storage_name": name, "chat_id": chat.ID, "bot_username": bot.Username})
}

type nodeDTO struct {
	ID         uuid.UUID  `json:"id"`
	ParentID   *uuid.UUID `json:"parent_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Size       int64      `json:"size"`
	State      *string    `json:"state"`
	Error      *string    `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	PurgeAfter *time.Time `json:"purge_after,omitempty"`
}

func scanNode(row pgx.Row) (nodeDTO, error) {
	var n nodeDTO
	err := row.Scan(&n.ID, &n.ParentID, &n.Name, &n.Kind, &n.Size, &n.State, &n.Error, &n.CreatedAt, &n.DeletedAt, &n.PurgeAfter)
	return n, err
}

const nodeColumns = `id,parent_id,name,kind::text,size,state::text,error_message,created_at,deleted_at,purge_after`

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	parent := r.URL.Query().Get("parent_id")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var rows pgx.Rows
	var err error
	if q != "" {
		rows, err = s.DB.Query(r.Context(), `SELECT `+nodeColumns+` FROM nodes WHERE deleted_at IS NULL AND name ILIKE '%'||$1||'%' ORDER BY kind DESC,lower(name) LIMIT 200`, q)
	} else if parent == "" {
		rows, err = s.DB.Query(r.Context(), `SELECT `+nodeColumns+` FROM nodes WHERE parent_id IS NULL AND deleted_at IS NULL ORDER BY kind DESC,lower(name)`)
	} else {
		id, parseErr := uuid.Parse(parent)
		if parseErr != nil {
			problem(w, 400, "invalid parent_id")
			return
		}
		rows, err = s.DB.Query(r.Context(), `SELECT `+nodeColumns+` FROM nodes WHERE parent_id=$1 AND deleted_at IS NULL ORDER BY kind DESC,lower(name)`, id)
	}
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []nodeDTO{}
	for rows.Next() {
		n, e := scanNode(rows)
		if e == nil {
			items = append(items, n)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string
		ParentID *uuid.UUID `json:"parent_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.validateParent(r.Context(), in.ParentID); err != nil {
		problem(w, 400, err.Error())
		return
	}
	name, err := s.uniqueName(r.Context(), in.ParentID, in.Name)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	n, err := scanNode(s.DB.QueryRow(r.Context(), `INSERT INTO nodes(id,parent_id,name,kind) VALUES($1,$2,$3,'folder') RETURNING `+nodeColumns, uuid.New(), in.ParentID, name))
	if err != nil {
		problem(w, 500, "cannot create folder")
		return
	}
	writeJSON(w, 201, n)
}

func (s *Server) renameNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid id")
		return
	}
	var in struct{ Name string }
	if !decodeJSON(w, r, &in) {
		return
	}
	var parent *uuid.UUID
	if err := s.DB.QueryRow(r.Context(), `SELECT parent_id FROM nodes WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&parent); err != nil {
		problem(w, 404, "file not found")
		return
	}
	name, err := s.uniqueNameExcluding(r.Context(), parent, in.Name, id)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	n, err := scanNode(s.DB.QueryRow(r.Context(), `UPDATE nodes SET name=$2,updated_at=now() WHERE id=$1 RETURNING `+nodeColumns, id, name))
	if err != nil {
		problem(w, 500, "cannot rename")
		return
	}
	writeJSON(w, 200, n)
}

func (s *Server) trashNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid id")
		return
	}
	tag, err := s.DB.Exec(r.Context(), `UPDATE nodes SET deleted_at=now(),purge_after=now()+$2::interval,updated_at=now() WHERE id=$1 AND deleted_at IS NULL`, id, fmt.Sprintf("%d days", int(s.Config.TrashRetention/(24*time.Hour))))
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, 404, "file not found")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) restoreNode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid id")
		return
	}
	var parent *uuid.UUID
	var oldName string
	if err := s.DB.QueryRow(r.Context(), `SELECT parent_id,name FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL`, id).Scan(&parent, &oldName); err != nil {
		problem(w, 404, "trash item not found")
		return
	}
	name, _ := s.uniqueNameExcluding(r.Context(), parent, oldName, id)
	n, err := scanNode(s.DB.QueryRow(r.Context(), `UPDATE nodes SET name=$2,deleted_at=NULL,purge_after=NULL,updated_at=now() WHERE id=$1 RETURNING `+nodeColumns, id, name))
	if err != nil {
		problem(w, 500, "cannot restore")
		return
	}
	writeJSON(w, 200, n)
}
func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Query(r.Context(), `SELECT `+nodeColumns+` FROM nodes WHERE deleted_at IS NOT NULL AND (parent_id IS NULL OR NOT EXISTS(SELECT 1 FROM nodes p WHERE p.id=nodes.parent_id AND p.deleted_at IS NOT NULL)) ORDER BY deleted_at DESC`)
	if err != nil {
		problem(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []nodeDTO{}
	for rows.Next() {
		n, e := scanNode(rows)
		if e == nil {
			items = append(items, n)
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string
		ParentID *uuid.UUID `json:"parent_id"`
		Size     int64
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Size < 0 {
		problem(w, 400, "size must not be negative")
		return
	}
	if err := s.validateParent(r.Context(), in.ParentID); err != nil {
		problem(w, 400, err.Error())
		return
	}
	name, err := s.uniqueName(r.Context(), in.ParentID, in.Name)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	nodeID, uploadID := uuid.New(), uuid.New()
	key, err := cryptostore.RandomKey()
	if err != nil {
		problem(w, 500, "cannot create encryption key")
		return
	}
	wrapped, err := cryptostore.SealSecret(s.MasterKey, key)
	if err != nil {
		problem(w, 500, "cannot protect encryption key")
		return
	}
	tempPath := filepath.Join(s.Config.DataDir, "uploads", uploadID.String()+".upload")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		problem(w, 500, "cannot create temporary upload")
		return
	}
	file.Close()
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		_ = os.Remove(tempPath)
		problem(w, 500, "database error")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO nodes(id,parent_id,name,kind,size,state,wrapped_key) VALUES($1,$2,$3,'file',$4,'uploading',$5)`, nodeID, in.ParentID, name, in.Size, wrapped)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO upload_sessions(id,node_id,temp_path,expected_size,expires_at) VALUES($1,$2,$3,$4,now()+interval '7 days')`, uploadID, nodeID, tempPath, in.Size)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		_ = os.Remove(tempPath)
		problem(w, 500, "cannot create upload")
		return
	}
	w.Header().Set("Location", "/api/v1/uploads/"+uploadID.String())
	writeJSON(w, 201, map[string]any{"upload_id": uploadID, "node_id": nodeID, "name": name, "offset": 0, "size": in.Size})
}

func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid upload id")
		return
	}
	var offset, size int64
	var state string
	err = s.DB.QueryRow(r.Context(), `SELECT u.received_size,u.expected_size,n.state::text FROM upload_sessions u JOIN nodes n ON n.id=u.node_id WHERE u.id=$1`, id).Scan(&offset, &size, &state)
	if err != nil {
		problem(w, 404, "upload not found")
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Upload-State", state)
	w.WriteHeader(204)
}

func (s *Server) appendUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid upload id")
		return
	}
	provided, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		problem(w, 400, "Upload-Offset header is required")
		return
	}
	var path string
	var current, total int64
	err = s.DB.QueryRow(r.Context(), `SELECT temp_path,received_size,expected_size FROM upload_sessions WHERE id=$1 AND expires_at>now()`, id).Scan(&path, &current, &total)
	if err != nil {
		problem(w, 404, "upload not found")
		return
	}
	if provided != current {
		w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
		problem(w, 409, "upload offset mismatch")
		return
	}
	remaining := total - current
	reader := http.MaxBytesReader(w, r.Body, remaining)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		problem(w, 500, "cannot open upload")
		return
	}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		problem(w, 400, "cannot append upload data")
		return
	}
	current += written
	_, err = s.DB.Exec(r.Context(), `UPDATE upload_sessions SET received_size=$2,updated_at=now(),expires_at=now()+interval '7 days' WHERE id=$1`, id, current)
	if err != nil {
		problem(w, 500, "cannot update upload")
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(current, 10))
	w.WriteHeader(204)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid upload id")
		return
	}
	tag, err := s.DB.Exec(r.Context(), `UPDATE nodes SET state='queued',updated_at=now() WHERE id=(SELECT node_id FROM upload_sessions WHERE id=$1 AND received_size=expected_size) AND state='uploading'`, id)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, 409, "upload is incomplete or already finalized")
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}
func (s *Server) retryUpload(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid upload id")
		return
	}
	tag, err := s.DB.Exec(r.Context(), `UPDATE nodes SET state='queued',error_message=NULL,updated_at=now() WHERE id=(SELECT node_id FROM upload_sessions WHERE id=$1) AND state='failed'`, id)
	if err != nil || tag.RowsAffected() == 0 {
		problem(w, 409, "upload cannot be retried")
		return
	}
	writeJSON(w, 202, map[string]string{"status": "queued"})
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	nodeID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid file id")
		return
	}
	var name string
	var size int64
	var wrapped []byte
	err = s.DB.QueryRow(r.Context(), `SELECT name,size,wrapped_key FROM nodes WHERE id=$1 AND kind='file' AND state='ready' AND deleted_at IS NULL`, nodeID).Scan(&name, &size, &wrapped)
	if err != nil {
		problem(w, 404, "ready file not found")
		return
	}
	key, err := cryptostore.OpenSecret(s.MasterKey, wrapped)
	if err != nil {
		problem(w, 500, "cannot decrypt file key")
		return
	}
	start, end, partial, err := parseRange(r.Header.Get("Range"), size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		problem(w, 416, err.Error())
		return
	}
	var tokenEnc []byte
	if err := s.DB.QueryRow(r.Context(), `SELECT bot_token_enc FROM telegram_settings WHERE id=1`).Scan(&tokenEnc); err != nil {
		problem(w, 503, "Telegram is not configured")
		return
	}
	tokenBytes, err := cryptostore.OpenSecret(s.MasterKey, tokenEnc)
	if err != nil {
		problem(w, 500, "cannot decrypt bot token")
		return
	}
	rows, err := s.DB.Query(r.Context(), `SELECT position,plain_offset,telegram_file_id,cipher_hash FROM file_chunks WHERE node_id=$1 AND plain_offset+plain_size>$2 AND plain_offset<=$3 ORDER BY position`, nodeID, start, end)
	if err != nil {
		problem(w, 500, "cannot list chunks")
		return
	}
	defer rows.Close()
	type chunk struct {
		position int
		offset   int64
		fileID   string
		hash     []byte
	}
	var chunks []chunk
	for rows.Next() {
		var c chunk
		if rows.Scan(&c.position, &c.offset, &c.fileID, &c.hash) == nil {
			chunks = append(chunks, c)
		}
	}
	if len(chunks) == 0 && size > 0 {
		problem(w, 500, "file chunks are missing")
		return
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.WriteHeader(206)
	}
	for _, c := range chunks {
		path, err := s.Telegram.ResolveFile(r.Context(), string(tokenBytes), c.fileID)
		if err != nil {
			s.Logger.Error("resolve Telegram file", "error", err)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			s.Logger.Error("open Telegram file", "error", err)
			return
		}
		err = cryptostore.DecryptChunk(file, w, key, nodeID, c.position, c.hash, c.offset, start, end)
		file.Close()
		if err != nil {
			s.Logger.Error("decrypt Telegram chunk", "error", err)
			return
		}
	}
}

func (s *Server) uniqueName(ctx context.Context, parent *uuid.UUID, name string) (string, error) {
	return s.uniqueNameExcluding(ctx, parent, name, uuid.Nil)
}

func (s *Server) validateParent(ctx context.Context, parent *uuid.UUID) error {
	if parent == nil {
		return nil
	}
	var valid bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND kind='folder' AND deleted_at IS NULL)`, parent).Scan(&valid); err != nil {
		return errors.New("cannot validate parent folder")
	}
	if !valid {
		return errors.New("parent folder does not exist")
	}
	return nil
}

func (s *Server) uniqueNameExcluding(ctx context.Context, parent *uuid.UUID, name string, exclude uuid.UUID) (string, error) {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", errors.New("invalid file name")
	}
	stem, ext := name, ""
	if dot := strings.LastIndex(name, "."); dot > 0 {
		stem, ext = name[:dot], name[dot:]
	}
	candidate := name
	for i := 0; i < 10000; i++ {
		var exists bool
		err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE parent_id IS NOT DISTINCT FROM $1 AND lower(name)=lower($2) AND deleted_at IS NULL AND id<>$3)`, parent, candidate, exclude).Scan(&exists)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s (%d)%s", stem, i+1, ext)
	}
	return "", errors.New("too many files with the same name")
}

func parseRange(header string, size int64) (start, end int64, partial bool, err error) {
	if size == 0 {
		return 0, -1, false, nil
	}
	start, end = 0, size-1
	if header == "" {
		return start, end, false, nil
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false, errors.New("only one byte range is supported")
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, errors.New("invalid byte range")
	}
	if parts[0] == "" {
		suffix, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || suffix <= 0 {
			return 0, 0, false, errors.New("invalid byte range")
		}
		if suffix > size {
			suffix = size
		}
		start = size - suffix
	} else {
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, false, errors.New("invalid byte range")
		}
		if parts[1] != "" {
			end, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil || end < start {
				return 0, 0, false, errors.New("invalid byte range")
			}
			if end >= size {
				end = size - 1
			}
		}
	}
	return start, end, true, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		problem(w, 400, "invalid JSON body")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message, "status": status})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if value := recover(); value != nil {
					logger.Error("HTTP panic", "panic", value)
					problem(w, 500, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
