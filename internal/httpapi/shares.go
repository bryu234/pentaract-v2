package httpapi

import (
	"github.com/bryu234/pentaract-v2/internal/auth"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const visibleShareNode = `WITH RECURSIVE ancestors AS (SELECT id,parent_id,deleted_at FROM nodes WHERE id=$1 UNION ALL SELECT n.id,n.parent_id,n.deleted_at FROM nodes n JOIN ancestors a ON a.parent_id=n.id) SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND kind='file' AND state='ready' AND NOT recovery_pending) AND NOT EXISTS(SELECT 1 FROM ancestors WHERE deleted_at IS NOT NULL)`

func (s *Server) shareURL(token string) (string, error) {
	u, e := url.Parse(s.Config.PublicURL)
	if e != nil {
		return "", e
	}
	if u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return "", http.ErrNotSupported
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/s/"
	u.RawQuery = ""
	u.Fragment = token
	return u.String(), nil
}
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		problem(w, 400, "invalid file")
		return
	}
	var in struct {
		TTL     int  `json:"ttl_seconds"`
		OneTime bool `json:"one_time"`
	}
	in.OneTime = true
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.TTL != 900 && in.TTL != 3600 && in.TTL != 86400 && in.TTL != 604800 {
		problem(w, 400, "invalid expiration")
		return
	}
	tx, e := s.DB.Begin(r.Context())
	if e != nil {
		problem(w, 503, "database unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `WITH RECURSIVE a AS(SELECT id,parent_id FROM nodes WHERE id=$1 UNION ALL SELECT n.id,n.parent_id FROM nodes n JOIN a ON a.parent_id=n.id) SELECT n.id FROM nodes n JOIN a ON a.id=n.id ORDER BY n.id FOR UPDATE OF n`, id); e != nil {
		problem(w, 503, "file unavailable")
		return
	}
	var visible bool
	if e = tx.QueryRow(r.Context(), visibleShareNode, id).Scan(&visible); e != nil {
		problem(w, 500, "database unavailable")
		return
	}
	if !visible {
		problem(w, 404, "ready file not found")
		return
	}
	raw, hash, e := auth.RandomToken(32)
	if e != nil {
		problem(w, 500, "cannot create link")
		return
	}
	link, e := s.shareURL(raw)
	if e != nil {
		problem(w, 503, "Configure PENTARACT_PUBLIC_URL with the public HTTPS address")
		return
	}
	enc, e := cryptostore.SealSecret(s.MasterKey, []byte(raw))
	if e != nil {
		problem(w, 500, "cannot protect link")
		return
	}
	shareID := uuid.New()
	expires := time.Now().UTC().Add(time.Duration(in.TTL) * time.Second)
	_, e = tx.Exec(r.Context(), `INSERT INTO file_shares(id,node_id,token_hash,token_enc,expires_at,one_time) VALUES($1,$2,$3,$4,$5,$6)`, shareID, id, hash, enc, expires, in.OneTime)
	if e != nil {
		problem(w, 500, "cannot save link")
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		problem(w, 503, "cannot save link")
		return
	}
	writeJSON(w, 201, map[string]any{"id": shareID, "url": link, "expires_at": expires, "one_time": in.OneTime})
}
func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		problem(w, 400, "invalid file")
		return
	}
	rows, e := s.DB.Query(r.Context(), `SELECT id,token_enc,expires_at,one_time,used_at,revoked_at FROM file_shares WHERE node_id=$1 ORDER BY created_at DESC`, id)
	if e != nil {
		problem(w, 500, "cannot list links")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var sid uuid.UUID
		var enc []byte
		var expires time.Time
		var once bool
		var used, revoked *time.Time
		if e = rows.Scan(&sid, &enc, &expires, &once, &used, &revoked); e != nil {
			problem(w, 500, "cannot read links")
			return
		}
		status := "active"
		if !expires.After(time.Now()) {
			status = "expired"
		}
		if once && used != nil {
			status = "used"
		}
		if revoked != nil {
			status = "revoked"
		}
		link := ""
		if status == "active" {
			token, e := cryptostore.OpenSecret(s.MasterKey, enc)
			if e != nil {
				problem(w, 500, "cannot read link")
				return
			}
			link, e = s.shareURL(string(token))
			if e != nil {
				problem(w, 503, "invalid public URL")
				return
			}
		}
		items = append(items, map[string]any{"id": sid, "url": link, "expires_at": expires, "one_time": once, "status": status})
	}
	if rows.Err() != nil {
		problem(w, 500, "cannot read links")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		problem(w, 400, "invalid link")
		return
	}
	if _, e = s.DB.Exec(r.Context(), `UPDATE file_shares SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id); e != nil {
		problem(w, 500, "cannot revoke link")
		return
	}
	w.WriteHeader(204)
}
func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	download := r.Method == "POST"
	if download {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			problem(w, 400, "invalid request")
			return
		}
		token = r.PostForm.Get("token")
	}
	if len(token) != 43 {
		problem(w, 404, "Ссылка недоступна")
		return
	}
	var id uuid.UUID
	var once bool
	var name string
	var size int64
	var expires time.Time
	e := s.DB.QueryRow(r.Context(), `SELECT n.id,s.one_time,n.name,n.size,s.expires_at FROM file_shares s JOIN nodes n ON n.id=s.node_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND (NOT s.one_time OR s.used_at IS NULL)`, auth.TokenHash(token)).Scan(&id, &once, &name, &size, &expires)
	if e != nil {
		problem(w, 410, "Ссылка недоступна или уже использована")
		return
	}
	var visible bool
	if e = s.DB.QueryRow(r.Context(), visibleShareNode, id).Scan(&visible); e != nil || !visible {
		problem(w, 410, "Файл недоступен")
		return
	}
	if !download {
		writeJSON(w, 200, map[string]any{"name": name, "size": size, "one_time": once, "expires_at": expires})
		return
	}
	if once && r.Header.Get("Range") != "" {
		problem(w, 416, "Одноразовая ссылка не поддерживает докачку")
		return
	}
	if _, _, _, e = parseRange(r.Header.Get("Range"), size); e != nil {
		problem(w, 416, "invalid range")
		return
	}
	tag, e := s.DB.Exec(r.Context(), `UPDATE file_shares SET used_at=CASE WHEN one_time THEN now() ELSE used_at END WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now() AND (NOT one_time OR used_at IS NULL)`, auth.TokenHash(token))
	if e != nil {
		problem(w, 503, "Скачивание временно недоступно")
		return
	}
	if tag.RowsAffected() != 1 {
		problem(w, 410, "Ссылка уже использована или отозвана")
		return
	}
	s.downloadNode(w, r, id)
}
