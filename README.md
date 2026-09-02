# Pentaract V2

[![CI](https://github.com/bryu234/pentaract-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/bryu234/pentaract-v2/actions/workflows/ci.yml)
[![Go 1.24](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![React 19](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=111827)](https://react.dev/)
[![Docker Compose](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![License MIT](https://img.shields.io/badge/License-MIT-22c55e.svg)](LICENSE)

[Русская версия](README.ru.md)

Encrypted personal file storage backed by a private Telegram channel and a self-hosted Local Bot API Server.

## Features

- Resumable uploads with no application-level file size limit
- XChaCha20-Poly1305 encryption before Telegram upload
- 256 MiB Telegram chunks, configurable up to 1900 MiB
- Password, TOTP, recovery codes and server-side sessions
- Folders, search, byte-range downloads and 30-day trash
- Encrypted portable backup of PostgreSQL and the master key

## Requirements

- Docker Desktop or Docker Engine with Compose v2
- A private Telegram channel
- A Telegram bot with channel administrator access
- Telegram `api_id` and `api_hash`

## Telegram setup

1. Create a private channel.
2. Create a bot with [@BotFather](https://t.me/BotFather) using `/newbot`.
3. Add the bot to the channel as an administrator. Enable **Post Messages** and **Delete Messages**.
4. Publish one message in the channel and call the official Bot API [`getUpdates`](https://core.telegram.org/bots/api#getupdates). Copy `channel_post.chat.id`; private channel IDs normally start with `-100`.
5. Sign in at [my.telegram.org/apps](https://my.telegram.org/apps), open **API development tools**, create an application and copy its `api_id` and `api_hash`.

Keep the bot token, `api_hash`, `.env`, master key and backups private.

## Quick start

```bash
git clone https://github.com/bryu234/pentaract-v2.git
cd pentaract-v2
make init
nano .env
make doctor
make up
```

Set these values in `.env`:

```dotenv
POSTGRES_PASSWORD=<LONG_URL_SAFE_RANDOM_PASSWORD>
TELEGRAM_API_ID=<API_ID>
TELEGRAM_API_HASH=<API_HASH>
```

Open <http://localhost:8088>. On first launch:

1. Create the owner account and register TOTP.
2. Store the recovery codes outside the server.
3. Open **Settings** and enter the channel ID and bot token.
4. Enable **Cloud logOut** if the token was used with `api.telegram.org` during channel setup.

The first Local Bot API build compiles TDLib and can take several minutes.

## Commands

```bash
make help
make up
make ps
make logs
make test
make lint
make backup BACKUP_PASSPHRASE='<PASSPHRASE>'
make down
```

`make up` checks the host port before starting. `make down` preserves all volumes and files.

## Backup and migration

```bash
make backup BACKUP_PASSPHRASE='<PASSPHRASE>'
make down
make restore BACKUP_FILE='pentaract-v2-....pv2backup' BACKUP_PASSPHRASE='<PASSPHRASE>'
```

The encrypted backup contains the PostgreSQL catalog, master key and restore metadata. Telegram retains the encrypted file payloads. Both the catalog and master key are required to access existing files.

## Security

Telegram receives only authenticated encrypted chunks. The bot token is encrypted in PostgreSQL; the master key is mounted separately. PostgreSQL and Local Bot API are not published to the host network.

Use HTTPS or a private VPN outside a trusted LAN. See [SECURITY.md](SECURITY.md) for vulnerability reports.

## Development

```bash
make test
make lint
docker compose config -q
```

Architecture details are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## License

[MIT](LICENSE)
