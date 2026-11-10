package httpapi

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		problem(w, 400, "invalid file id")
		return
	}
	var kind string
	if err = s.DB.QueryRow(r.Context(), `SELECT kind FROM nodes WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&kind); err != nil {
		problem(w, 404, "file not found")
		return
	}
	if kind == "folder" {
		s.downloadFolder(w, r, id)
		return
	}
	s.downloadNode(w, r, id)
}

type downloadChunk struct {
	position int
	offset   int64
	size     int64
	fileID   string
	hash     []byte
}
type fileDownload struct {
	id     uuid.UUID
	name   string
	size   int64
	key    []byte
	token  string
	chunks []downloadChunk
}

func (s *Server) prepareDownload(ctx context.Context, id uuid.UUID) (*fileDownload, error) {
	d := &fileDownload{id: id}
	var wrapped, tokenEnc []byte
	// A file in a trashed ancestor is unavailable even if its own deleted_at is NULL.
	err := s.DB.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT id,parent_id,deleted_at FROM nodes WHERE id=$1
 UNION ALL SELECT n.id,n.parent_id,n.deleted_at FROM nodes n JOIN ancestors a ON n.id=a.parent_id
 ) SELECT name,size,wrapped_key FROM nodes WHERE id=$1 AND kind='file' AND state='ready' AND NOT recovery_pending AND NOT EXISTS(SELECT 1 FROM ancestors WHERE deleted_at IS NOT NULL)`, id).Scan(&d.name, &d.size, &wrapped)
	if err != nil {
		return nil, err
	}
	if d.key, err = cryptostore.OpenSecret(s.MasterKey, wrapped); err != nil {
		return nil, err
	}
	if err = s.DB.QueryRow(ctx, `SELECT bot_token_enc FROM telegram_settings WHERE id=1`).Scan(&tokenEnc); err != nil {
		return nil, err
	}
	token, err := cryptostore.OpenSecret(s.MasterKey, tokenEnc)
	if err != nil {
		return nil, err
	}
	d.token = string(token)
	rows, err := s.DB.Query(ctx, `SELECT position,plain_offset,plain_size,telegram_file_id,cipher_hash FROM file_chunks WHERE node_id=$1 ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var c downloadChunk
		if err = rows.Scan(&c.position, &c.offset, &c.size, &c.fileID, &c.hash); err != nil {
			return nil, err
		}
		if c.position != len(d.chunks) || c.offset != total || c.size <= 0 || c.size > d.size-total {
			return nil, errors.New("incomplete file chunks")
		}
		total += c.size
		d.chunks = append(d.chunks, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if total != d.size {
		return nil, errors.New("file chunks missing")
	}
	return d, nil
}

func (s *Server) streamDownload(ctx context.Context, w io.Writer, d *fileDownload, start, end int64) error {
	for i, c := range d.chunks {
		limit := d.size
		if i+1 < len(d.chunks) {
			limit = d.chunks[i+1].offset
		}
		if limit <= start || c.offset > end {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := s.Telegram.ResolveFile(ctx, d.token, c.fileID)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = cryptostore.DecryptChunk(f, w, d.key, d.id, c.position, c.hash, c.offset, start, end)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) downloadNode(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	d, err := s.prepareDownload(r.Context(), id)
	if err != nil {
		problem(w, 404, "ready file unavailable")
		return
	}
	start, end, partial, err := parseRange(r.Header.Get("Range"), d.size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", d.size))
		problem(w, 416, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, d.size))
		w.WriteHeader(206)
	}
	if err = s.streamDownload(r.Context(), w, d, start, end); err != nil {
		s.Logger.Warn("file download interrupted")
		panic(http.ErrAbortHandler)
	}
}

type archiveEntry struct {
	id         uuid.UUID
	path, kind string
	modified   time.Time
}

func archivePath(parts []string) (string, error) {
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "/\\\x00:") {
			return "", errors.New("unsafe archive entry name")
		}
	}
	return strings.Join(parts, "/"), nil
}

func (s *Server) downloadFolder(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	rows, err := s.DB.Query(r.Context(), `WITH RECURSIVE tree AS (
 SELECT id,name,kind,state,recovery_pending,updated_at,ARRAY[name] AS parts FROM nodes WHERE id=$1 AND kind='folder' AND deleted_at IS NULL
 AND NOT EXISTS (WITH RECURSIVE ancestors AS (SELECT parent_id,deleted_at FROM nodes WHERE id=$1 UNION ALL SELECT n.parent_id,n.deleted_at FROM nodes n JOIN ancestors a ON n.id=a.parent_id) SELECT 1 FROM ancestors WHERE deleted_at IS NOT NULL)
 UNION ALL SELECT n.id,n.name,n.kind,n.state,n.recovery_pending,n.updated_at,t.parts||n.name FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
 ) SELECT id,kind,COALESCE(state::text,''),recovery_pending,updated_at,parts FROM tree ORDER BY parts`, id)
	if err != nil {
		problem(w, 500, "cannot list folder")
		return
	}
	var entries []archiveEntry
	var folder string
	for rows.Next() {
		var e archiveEntry
		var state string
		var pending bool
		var parts []string
		if err = rows.Scan(&e.id, &e.kind, &state, &pending, &e.modified, &parts); err != nil {
			break
		}
		e.path, err = archivePath(parts)
		if err != nil {
			break
		}
		if e.id == id {
			folder = parts[0]
		}
		if e.kind == "file" && (state != "ready" || pending) {
			rows.Close()
			problem(w, 409, "Папка содержит файлы, загрузка которых ещё не завершена.")
			return
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		problem(w, 500, "cannot prepare folder archive")
		return
	}
	if folder == "" {
		problem(w, 404, "folder not found")
		return
	}
	// Stream directly to the client; the archive never accumulates in RAM or on disk.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": folder + ".zip"}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	z := zip.NewWriter(w)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.path, Method: zip.Store, Modified: e.modified}
		if e.kind == "folder" {
			h.Name += "/"
			h.SetMode(os.ModeDir | 0755)
		} else {
			h.SetMode(0644)
		}
		dest, err := z.CreateHeader(h)
		if err == nil && e.kind == "file" {
			var d *fileDownload
			d, err = s.prepareDownload(r.Context(), e.id)
			if err == nil {
				err = s.streamDownload(r.Context(), dest, d, 0, d.size-1)
			}
		}
		if err != nil {
			s.Logger.Warn("folder download interrupted")
			panic(http.ErrAbortHandler)
		}
	}
	if err = z.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
}
