# Pentaract V2

Pentaract V2 is a personal, encrypted file manager backed by a Telegram channel. It uses a self-hosted [Telegram Bot API Server](https://github.com/tdlib/telegram-bot-api) in local mode, so individual Telegram objects can be larger than the cloud Bot API's 20 MB download limit.

This is a clean implementation. It does not reuse the original Pentaract database or silently import unencrypted V1 chunks.

## What is implemented

- One owner protected by Argon2id password hashing, mandatory TOTP and recovery codes.
- One Telegram channel and one dedicated bot, verified during setup.
- Resumable browser uploads without an application-level file-size limit. After a reload, reselecting the same local file continues from the server-confirmed offset.
- 256 MiB Telegram objects by default, configurable from 1 to 1900 MiB.
- XChaCha20-Poly1305 authenticated encryption in 4 MiB streaming frames.
- Per-file random keys wrapped by a portable master key.
- Streaming downloads and single HTTP byte ranges without loading a whole file into RAM.
- Folders, search, rename, collision-safe automatic names and a 30-day trash.
- Telegram 429 `retry_after` handling, retryable background processing and one-send-per-second pacing.
- Encrypted portable PostgreSQL/master-key backups.
- Multi-architecture Docker builds for Docker Desktop and Linux Docker Engine.

## Requirements

- Docker Desktop on macOS/Windows, or Docker Engine with Compose v2 on Linux.
- At least enough temporary disk for the largest in-progress upload plus one encrypted Telegram chunk.
- A private Telegram channel and a bot that is an administrator in that channel.
- Telegram `api_id` and `api_hash` from [my.telegram.org/apps](https://my.telegram.org/apps).

The Local Bot API build is based on the official TDLib source and can take several minutes on its first build.

## Quick start

```bash
make init
nano .env
make doctor
make up
```

Before starting, `make up` checks whether the configured host port is occupied. The default site is <http://localhost:8088>. PostgreSQL and Telegram Bot API ports are not exposed to the host.

Set at minimum:

```dotenv
POSTGRES_PASSWORD=<LONG_URL_SAFE_RANDOM_PASSWORD>
TELEGRAM_API_ID=<API_ID_FROM_MY_TELEGRAM_ORG>
TELEGRAM_API_HASH=<API_HASH_FROM_MY_TELEGRAM_ORG>
```

On the first page:

1. Create the owner and register the displayed TOTP secret.
2. Save all recovery codes outside the server.
3. Open **Settings** and provide the channel ID and bot token.
4. If the bot token has already been used through `api.telegram.org`, enable **Cloud logOut** once. Telegram allows immediate local login but blocks return to the cloud Bot API for ten minutes.

The bot token is encrypted before it is written to PostgreSQL. It is never returned by the API or UI.

## Commands

```bash
make help          # documented commands
make doctor        # configuration and Docker validation
make check-ports   # local port validation
make up            # local HTTP stack
make prod-up       # Caddy profile with ports 80/443 and automatic HTTPS
make ps
make logs
make restart       # rebuild/restart only the app
make test
make lint
make backup BACKUP_PASSPHRASE='<RECOVERY_PASSPHRASE>'
make restore BACKUP_FILE='pentaract-v2-....pv2backup' BACKUP_PASSPHRASE='<RECOVERY_PASSPHRASE>'
make down           # volumes and user data are preserved
```

There is deliberately no Make target that deletes Docker volumes.

## Production

Set a DNS A/AAAA record, then configure:

```dotenv
PENTARACT_DOMAIN=storage.example.com
PENTARACT_PUBLIC_URL=https://storage.example.com
PENTARACT_COOKIE_SECURE=true
```

Run `make prod-up`. Caddy obtains and renews TLS certificates. Keep the direct `8088` host port firewalled on a production server, or remove its mapping in a local Compose override.

## Backup and moving to another server

Files are stored as encrypted documents in Telegram. PostgreSQL is the catalog that maps names and chunk order to Telegram `file_id` values, and `master.key` is required to decrypt them. Losing either PostgreSQL or the master key makes the Telegram documents unusable in Pentaract.

`make backup` creates an age/scrypt passphrase-encrypted `.pv2backup` containing:

- a PostgreSQL custom-format logical dump;
- the 32-byte encryption master key;
- a versioned restore manifest and Telegram API configuration metadata.

To move:

1. Create and verify a backup while the old server is healthy.
2. Copy the `.pv2backup` to `data/backups/` on the new server.
3. Run `make init`, configure the new PostgreSQL password, run `make down`, then restore the bundle. Restore deliberately refuses to run while the application worker is active.
4. Move the bot cleanly using Telegram's `close`/`logOut` procedure if the old Local Bot API instance is still running.
5. Start the new stack and verify an old file's BLAKE3 integrity by downloading it.
6. Keep the old server stopped but intact until the verification succeeds.

The backup does not contain the encrypted file payloads because Telegram already stores them. The backup passphrase must be stored separately.

## Security model

- Telegram sees only independently authenticated encrypted objects with opaque names.
- A fresh data-encryption key is generated per file.
- The master key is mounted from `data/secrets/master.key`, not stored in PostgreSQL.
- Authentication uses server-side revocable sessions, SameSite cookies and CSRF tokens.
- Local Bot API and PostgreSQL are reachable only on the internal Compose network.
- Decryption validates the AEAD tag and encrypted-chunk BLAKE3 checksum before accepting a chunk.

Server-side encryption protects data from Telegram and channel viewers. It does not protect against an attacker who controls both the running application container and its master key.

## Development

Backend packages live under `internal/`; the React application is in `web/`. Database migrations are forward-only files in `migrations/` and are embedded in the server binary.

The normal verification gate is:

```bash
make test
make lint
docker compose config -q
```

Real Telegram smoke testing is intentionally not part of CI because it sends documents to an external channel. Test small, >20 MiB, multi-chunk, interrupted/resumed, Range download, trash/restore and backup/restore flows before a release.
