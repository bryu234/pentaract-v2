package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bryu234/pentaract-v2/internal/config"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zeebo/blake3"
)

type Processor struct {
	DB        *pgxpool.Pool
	Config    config.Config
	MasterKey []byte
	Telegram  *telegram.Client
	Logger    *slog.Logger
}

type telegramConfig struct {
	ChatID int64
	Token  string
}

func (p *Processor) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	trashTicker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	defer trashTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.processNext(ctx)
		case <-trashTicker.C:
			p.purgeTrash(ctx)
			p.cleanupExpiredUploads(ctx)
		}
	}
}

func (p *Processor) cleanupExpiredUploads(ctx context.Context) {
	rows, err := p.DB.Query(ctx, `SELECT u.node_id,u.temp_path FROM upload_sessions u JOIN nodes n ON n.id=u.node_id WHERE u.expires_at<=now() AND n.state='uploading' LIMIT 100`)
	if err != nil {
		p.Logger.Error("list expired uploads", "error", err)
		return
	}
	type expired struct {
		nodeID uuid.UUID
		path   string
	}
	var uploads []expired
	for rows.Next() {
		var upload expired
		if rows.Scan(&upload.nodeID, &upload.path) == nil {
			uploads = append(uploads, upload)
		}
	}
	rows.Close()
	for _, upload := range uploads {
		if _, err := p.DB.Exec(ctx, `DELETE FROM nodes WHERE id=$1 AND state='uploading'`, upload.nodeID); err == nil {
			if err := os.Remove(upload.path); err != nil && !os.IsNotExist(err) {
				p.Logger.Warn("remove expired upload", "error", err)
			}
		}
	}
}

func (p *Processor) processNext(ctx context.Context) {
	var nodeID uuid.UUID
	err := p.DB.QueryRow(ctx, `
		UPDATE nodes SET state='processing', updated_at=now(), error_message=NULL
		WHERE id=(SELECT id FROM nodes WHERE kind='file' AND state IN ('queued','processing') AND deleted_at IS NULL ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id`).Scan(&nodeID)
	if err == pgx.ErrNoRows {
		return
	}
	if err != nil {
		p.Logger.Error("claim upload", "error", err)
		return
	}
	if err := p.processFile(ctx, nodeID); err != nil {
		p.Logger.Error("process upload", "node_id", nodeID, "error", err)
		_, _ = p.DB.Exec(ctx, `UPDATE nodes SET state='failed', error_message=$2, updated_at=now() WHERE id=$1`, nodeID, safeError(err))
	}
}

func (p *Processor) processFile(ctx context.Context, nodeID uuid.UUID) error {
	var tempPath string
	var expectedSize int64
	var wrappedKey []byte
	if err := p.DB.QueryRow(ctx, `SELECT u.temp_path,u.expected_size,n.wrapped_key FROM upload_sessions u JOIN nodes n ON n.id=u.node_id WHERE n.id=$1`, nodeID).Scan(&tempPath, &expectedSize, &wrappedKey); err != nil {
		return err
	}
	key, err := cryptostore.OpenSecret(p.MasterKey, wrappedKey)
	if err != nil {
		return fmt.Errorf("unwrap file key: %w", err)
	}
	settings, err := p.telegramSettings(ctx)
	if err != nil {
		return err
	}
	input, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	defer input.Close()
	var nextPosition int
	var offset int64
	if err := p.DB.QueryRow(ctx, `SELECT COALESCE(MAX(position)+1,0),COALESCE(SUM(plain_size),0) FROM file_chunks WHERE node_id=$1`, nodeID).Scan(&nextPosition, &offset); err != nil {
		return err
	}
	if _, err := input.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	for offset < expectedSize {
		chunkID := uuid.New()
		outboxPath := filepath.Join(p.Config.SharedDir, "outbox", chunkID.String()+".pv2")
		out, err := os.OpenFile(outboxPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		meta, encryptErr := cryptostore.EncryptChunk(input, out, key, nodeID, nextPosition, p.Config.TelegramChunkSize)
		closeErr := out.Close()
		if encryptErr != nil {
			_ = os.Remove(outboxPath)
			return encryptErr
		}
		if closeErr != nil {
			_ = os.Remove(outboxPath)
			return closeErr
		}
		if meta.PlainSize == 0 {
			_ = os.Remove(outboxPath)
			break
		}
		caption := fmt.Sprintf("pentaract-v2:%s:%d", nodeID, nextPosition)
		sent, err := p.Telegram.SendDocument(ctx, settings.Token, settings.ChatID, outboxPath, caption)
		if err != nil {
			_ = os.Remove(outboxPath)
			return err
		}
		_, err = p.DB.Exec(ctx, `INSERT INTO file_chunks(id,node_id,position,plain_offset,plain_size,cipher_size,nonce_prefix,plain_hash,cipher_hash,telegram_file_id,telegram_file_unique_id,telegram_message_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			chunkID, nodeID, nextPosition, offset, meta.PlainSize, meta.CipherSize, meta.NoncePrefix, meta.PlainHash, meta.CipherHash, sent.Document.FileID, sent.Document.FileUniqueID, sent.MessageID)
		_ = os.Remove(outboxPath)
		if err != nil {
			return err
		}
		offset += meta.PlainSize
		nextPosition++
	}
	if offset != expectedSize {
		return fmt.Errorf("processed %d of %d bytes", offset, expectedSize)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hasher := blake3.New()
	if _, err := io.Copy(hasher, input); err != nil {
		return err
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE nodes SET state='ready',plain_hash=$2,error_message=NULL,updated_at=now() WHERE id=$1`, nodeID, hasher.Sum(nil)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_sessions WHERE node_id=$1`, nodeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		p.Logger.Warn("remove completed upload", "error", err)
	}
	p.Logger.Info("file stored", "node_id", nodeID, "chunks", nextPosition, "bytes", expectedSize)
	return nil
}

func (p *Processor) telegramSettings(ctx context.Context) (telegramConfig, error) {
	var chatID int64
	var encrypted []byte
	if err := p.DB.QueryRow(ctx, `SELECT chat_id,bot_token_enc FROM telegram_settings WHERE id=1`).Scan(&chatID, &encrypted); err != nil {
		return telegramConfig{}, fmt.Errorf("Telegram storage is not configured: %w", err)
	}
	token, err := cryptostore.OpenSecret(p.MasterKey, encrypted)
	if err != nil {
		return telegramConfig{}, err
	}
	return telegramConfig{ChatID: chatID, Token: string(token)}, nil
}

func (p *Processor) purgeTrash(ctx context.Context) {
	settings, settingsErr := p.telegramSettings(ctx)
	rows, err := p.DB.Query(ctx, `SELECT id FROM nodes WHERE deleted_at IS NOT NULL AND purge_after <= now() ORDER BY purge_after LIMIT 100`)
	if err != nil {
		p.Logger.Error("list trash", "error", err)
		return
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if settingsErr == nil {
			messageRows, _ := p.DB.Query(ctx, `WITH RECURSIVE tree AS (SELECT id FROM nodes WHERE id=$1 UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id) SELECT telegram_message_id FROM file_chunks WHERE node_id IN (SELECT id FROM tree)`, id)
			if messageRows != nil {
				for messageRows.Next() {
					var messageID int64
					if messageRows.Scan(&messageID) == nil {
						_ = p.Telegram.DeleteMessage(ctx, settings.Token, settings.ChatID, messageID)
					}
				}
				messageRows.Close()
			}
		}
		_, _ = p.DB.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, id)
	}
}

func safeError(err error) string {
	message := err.Error()
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
