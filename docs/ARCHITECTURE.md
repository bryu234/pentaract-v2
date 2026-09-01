# Architecture

## Data flow

1. The browser creates an upload session and sends ordered 8 MiB HTTP PATCH requests with `Upload-Offset`.
2. The backend appends bytes to a private temporary file and persists the offset in PostgreSQL.
3. Completion queues a persisted background job represented by the file state.
4. The processor reads at most one configurable Telegram chunk at a time. Each chunk consists of independently authenticated 4 MiB XChaCha20-Poly1305 frames.
5. The encrypted object is placed in the shared volume and handed to Local Bot API using a `file://` URI.
6. PostgreSQL stores the exact bot, Telegram file/message identifiers, offsets, hashes and encryption metadata.
7. Downloads resolve each `file_id` through the same bot, open Local Bot API's absolute shared path, authenticate/decrypt frames and stream selected bytes to the client.

## Failure behavior

- Browser interruption: the client obtains `Upload-Offset` with HEAD and continues from the confirmed byte.
- Application restart: `uploading`, `queued`, `processing` and `failed` states remain in PostgreSQL. Processing resumes after the last committed Telegram chunk.
- Telegram 429: the exact `retry_after` delay is honored.
- Network/5xx error: bounded retries are used. A response lost after Telegram accepts a document can leave an orphan channel message; captions contain an opaque chunk UUID for audit.
- Integrity failure: download stops; corrupted plaintext is never emitted for the affected frame.
- Delete: metadata remains recoverable for 30 days. Remote Telegram deletion at purge time is best effort.

## Deliberate V1 boundaries

- One owner, one channel and one bot.
- No V1 import, public sharing, version history, deduplication or multi-storage scheduler.
- Name collisions create `name (1).ext`, `name (2).ext`, and so on.
- Total file size has no configured cap; temporary disk and HTTP infrastructure provide the practical limit.

