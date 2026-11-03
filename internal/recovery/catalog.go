// Package recovery stores an authenticated portable catalog independently of the server.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"time"
)

const Marker = "pentaract-v2:recovery:1"

type Catalog struct {
	Format    int             `json:"format"`
	StorageID string          `json:"storage_id"`
	Revision  int64           `json:"revision"`
	ChatID    int64           `json:"chat_id"`
	BotID     int64           `json:"bot_id"`
	CreatedAt time.Time       `json:"created_at"`
	Nodes     json.RawMessage `json:"nodes"`
	Chunks    json.RawMessage `json:"chunks"`
}

func Encode(c Catalog, key []byte) ([]byte, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return nil, e
	}
	return cryptostore.SealSecret(key, b)
}
func Decode(b, key []byte) (Catalog, error) {
	var c Catalog
	p, e := cryptostore.OpenSecret(key, b)
	if e != nil {
		return c, errors.New("catalog authentication failed")
	}
	if e = json.Unmarshal(p, &c); e != nil {
		return c, e
	}
	if c.Format != 1 || c.StorageID == "" || c.Revision < 1 {
		return c, errors.New("unsupported catalog")
	}
	return c, nil
}
func Publish(ctx context.Context, db *pgxpool.Pool, tg *telegram.Client, key []byte, shared string) error {
	conn, e := db.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(72620421)`).Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(72620421)`)
	tx, e := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	c := Catalog{Format: 1, CreatedAt: time.Now().UTC()}
	var published int64
	var paused bool
	if e = tx.QueryRow(ctx, `SELECT storage_id::text,revision,published_revision,paused FROM recovery_status WHERE id=1`).Scan(&c.StorageID, &c.Revision, &published, &paused); e != nil {
		return e
	}
	if paused || published == c.Revision {
		return nil
	}
	var tokenEnc []byte
	if e = tx.QueryRow(ctx, `SELECT chat_id,bot_user_id,bot_token_enc FROM telegram_settings WHERE id=1`).Scan(&c.ChatID, &c.BotID, &tokenEnc); e != nil {
		return e
	}
	token, e := cryptostore.OpenSecret(key, tokenEnc)
	if e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(n) || '{"recovery_pending":false}'::jsonb),'[]') FROM nodes n WHERE kind='folder' OR state='ready'`).Scan(&c.Nodes); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(c)),'[]') FROM file_chunks c JOIN nodes n ON n.id=c.node_id WHERE n.state='ready'`).Scan(&c.Chunks); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	data, e := Encode(c, key)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Join(shared, "outbox"), "catalog-*.pv2catalog")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	sent, e := tg.SendDocument(ctx, string(token), c.ChatID, f.Name(), Marker)
	if e != nil {
		return e
	}
	if e = tg.Pin(ctx, string(token), c.ChatID, sent.MessageID); e != nil {
		return e
	}
	// Only clear pending flags when no mutations happened during publication.
	tx, e = conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `UPDATE nodes SET recovery_pending=false WHERE recovery_pending AND (SELECT revision FROM recovery_status WHERE id=1)=$1`, c.Revision); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE recovery_status SET published_revision=$1,published_at=now(),error=NULL WHERE id=1`, c.Revision); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func Restore(ctx context.Context, db *pgxpool.Pool, c Catalog, key []byte, token string, tg *telegram.Client) error {
	bot, e := tg.GetMe(ctx, token)
	if e != nil {
		return e
	}
	if bot.ID != c.BotID {
		return errors.New("catalog belongs to another bot")
	}
	if _, e = tg.GetChat(ctx, token, c.ChatID); e != nil {
		return e
	}
	tx, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `LOCK TABLE nodes,users,telegram_settings IN ACCESS EXCLUSIVE MODE`); e != nil {
		return e
	}
	var empty bool
	if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM nodes) AND NOT EXISTS(SELECT 1 FROM users) AND NOT EXISTS(SELECT 1 FROM telegram_settings)`).Scan(&empty); e != nil {
		return e
	}
	if !empty {
		return errors.New("recovery requires an empty database")
	}
	// Deferred self-reference permits arbitrary ordering of folders.
	if _, e = tx.Exec(ctx, `ALTER TABLE nodes ALTER CONSTRAINT nodes_parent_id_fkey DEFERRABLE INITIALLY DEFERRED`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO nodes SELECT * FROM jsonb_populate_recordset(NULL::nodes,$1::jsonb || '[]'::jsonb)`, string(c.Nodes)); e != nil {
		return fmt.Errorf("import nodes: %w", e)
	}
	if _, e = tx.Exec(ctx, `UPDATE nodes SET recovery_pending=false`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO file_chunks SELECT * FROM jsonb_populate_recordset(NULL::file_chunks,$1::jsonb)`, string(c.Chunks)); e != nil {
		return e
	}
	sealed, e := cryptostore.SealSecret(key, []byte(token))
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO telegram_settings(id,storage_name,chat_id,bot_user_id,bot_username,bot_token_enc) VALUES(1,'Recovered storage',$1,$2,$3,$4)`, c.ChatID, bot.ID, bot.Username, sealed); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE recovery_status SET storage_id=$1,revision=$2,published_revision=$2,published_at=$3,paused=true WHERE id=1`, c.StorageID, c.Revision, c.CreatedAt); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
