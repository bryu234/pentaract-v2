package recovery

import (
	"bytes"
	"context"
	"errors"
	"github.com/bryu234/pentaract-v2/internal/cryptostore"
	"github.com/bryu234/pentaract-v2/internal/database"
	"github.com/bryu234/pentaract-v2/internal/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zeebo/blake3"
	"os"
)

// Verify imports the real pinned catalog into an explicitly supplied empty DB,
// then authenticates and hashes at most two files no larger than 5 MiB each.
func Verify(ctx context.Context, source, target *pgxpool.Pool, tg *telegram.Client, key []byte) error {
	var chat int64
	var enc []byte
	if e := source.QueryRow(ctx, `SELECT chat_id,bot_token_enc FROM telegram_settings WHERE id=1`).Scan(&chat, &enc); e != nil {
		return e
	}
	token, e := cryptostore.OpenSecret(key, enc)
	if e != nil {
		return e
	}
	path, e := tg.CatalogFile(ctx, string(token), chat)
	if e != nil {
		return e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	c, e := Decode(b, key)
	if e != nil {
		return e
	}
	if c.ChatID != chat {
		return errors.New("wrong catalog chat")
	}
	if e = database.Migrate(ctx, target); e != nil {
		return e
	}
	if e = Restore(ctx, target, c, key, string(token), tg); e != nil {
		return e
	}
	var before, after int64
	source.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE kind='folder' OR state='ready'`).Scan(&before)
	target.QueryRow(ctx, `SELECT count(*) FROM nodes`).Scan(&after)
	if before != after {
		return errors.New("catalog record count mismatch")
	}
	rows, e := target.Query(ctx, `SELECT id,size,wrapped_key,plain_hash FROM nodes WHERE state='ready' AND size<=5242880 ORDER BY size LIMIT 2`)
	if e != nil {
		return e
	}
	type file struct {
		id        uuid.UUID
		size      int64
		key, hash []byte
	}
	var files []file
	for rows.Next() {
		var f file
		if e = rows.Scan(&f.id, &f.size, &f.key, &f.hash); e != nil {
			rows.Close()
			return e
		}
		files = append(files, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, f := range files {
		plainKey, e := cryptostore.OpenSecret(key, f.key)
		if e != nil {
			return e
		}
		chunks, e := target.Query(ctx, `SELECT position,plain_offset,telegram_file_id,cipher_hash FROM file_chunks WHERE node_id=$1 ORDER BY position`, f.id)
		if e != nil {
			return e
		}
		h := blake3.New()
		for chunks.Next() {
			var position int
			var offset int64
			var id string
			var hash []byte
			if e = chunks.Scan(&position, &offset, &id, &hash); e != nil {
				chunks.Close()
				return e
			}
			path, e := tg.ResolveFile(ctx, string(token), id)
			if e != nil {
				chunks.Close()
				return e
			}
			input, e := os.Open(path)
			if e != nil {
				chunks.Close()
				return e
			}
			e = cryptostore.DecryptChunk(input, h, plainKey, f.id, position, hash, offset, 0, f.size-1)
			input.Close()
			if e != nil {
				chunks.Close()
				return e
			}
		}
		e = chunks.Err()
		chunks.Close()
		if e != nil {
			return e
		}
		if !bytes.Equal(h.Sum(nil), f.hash) {
			return errors.New("restored file checksum mismatch")
		}
	}
	return nil
}
