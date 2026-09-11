# Preferred Telegram MTProto endpoint

Pentaract talks to the local `telegram-bot-api` container over HTTP. That container
uses TDLib to connect to Telegram over MTProto. File transfers do not go through
`api.telegram.org`. An address shown under **Production configuration** on
[my.telegram.org/apps](https://my.telegram.org/apps) can be configured as a
preferred endpoint for its DC (data center).

## Configuration

Keep `PENTARACT_TELEGRAM_API_URL=http://telegram-bot-api:8081`. In `.env`, set:

```dotenv
PENTARACT_MTPROTO_DC_ID=2
PENTARACT_MTPROTO_SERVER=<PRODUCTION_IP>
PENTARACT_MTPROTO_PORT=<PRODUCTION_PORT>
```

Use the actual DC number, IP and port from **Production configuration**, not the
example placeholders. IPv4 and unbracketed IPv6 literals are supported. Set all
three values, or leave all three empty for standard TDLib routing. Existing
`TELEGRAM_API_ID` and `TELEGRAM_API_HASH` are still required. Restart the Telegram
container after changing the configuration; rebuilding is only needed when the
code changes. `make telegram-restart` handles both using the Docker build cache.

```bash
make telegram-build
make telegram-check
make telegram-restart
make telegram-logs
```

`telegram-check` validates the configuration without connecting to Telegram.
`telegram-restart` recreates only the Telegram container and preserves its
volumes. Its health check confirms the local HTTP listener, not Telegram access.

The address is preferred within the corresponding DC, with standard TDLib
health-based fallback. Media-specific endpoints and Telegram DC migrations
continue to work normally. Existing sessions retain their home DC; a new session
starts at the configured DC. Telegram can require access to additional DCs, so a
successful TCP connection to one IP does not guarantee authorization or file
transfers. See [Telegram's DC documentation](https://core.telegram.org/api/datacenter).

The public RSA keys supplied by Telegram are already bundled in TDLib. No manual
key entry, MTProxy secret, proxy server, or user-account login is required.
**Test configuration is a separate Telegram environment** and cannot be used with
your existing production bot/channel. This feature configures Production only.
[Telegram's test-environment documentation](https://core.telegram.org/bots/features#creating-a-bot-in-the-test-environment).

## Server trial with an existing bot and channel

1. Use a fresh checkout of `improvements` on the server, with separate empty
   database/Telegram volumes and a fresh `data/` directory. Do not restore or copy
   the laptop database, key or Telegram session for this trial. If this Compose
   project already exists on the server, use a separate checkout and a unique
   `COMPOSE_PROJECT_NAME` for **all** commands; never run `down -v` against it.
2. Run `make init`, edit `.env` with the API credentials and Production endpoint,
   and set a free `PENTARACT_HTTP_PORT` and matching `PENTARACT_PUBLIC_URL`.
   Run `make telegram-build` and `make telegram-check`.
3. Immediately before connecting the same bot, on the laptop run
   `docker compose stop app telegram-bot-api`. Keep its database, key and volumes.
   Do not run two Pentaract instances against the same bot during the trial.
4. On the server, run `make up` (checks the host port), register a local Pentaract
   account, and configure the existing bot token and channel ID. Leave **Cloud
   logOut** disabled: this bot already uses a local server. A bot still registered
   with the cloud Bot API must first be logged out from a network that can reach
   `api.telegram.org`; entering a DC address does not perform that operation.
5. Upload a new disposable file, wait until processing completes, download it and
   compare SHA-256 (`sha256sum` on Linux, `shasum -a 256` on macOS). Also test a file
   larger than the configured chunk size, then restart only the Telegram service
   and repeat the download. A repeated download may use the local Telegram cache
   and alone is not proof of DC access; a completed new upload also verifies
   communication with Telegram.
6. Read `make telegram-logs`: `route=preferred` identifies an attempt to the
   configured endpoint, `fallback` another address in the same DC, and `other-dc`
   a connection to a different DC. These are connection **attempts**, not proof of
   a successful MTProto handshake. Combine them with completed uploads/downloads;
   a run with fallback attempts is not proof that the configured IP worked.
7. Stop the server's app and Telegram containers after the trial. Only then resume
   the laptop with `make up`. Retain the trial database/key until you no longer
   need its uploaded files. The laptop catalog will not list trial uploads made
   with the separate database. Existing channel files are not imported or deleted.

Moving the current storage is a later operation requiring the original database
and encryption key. Keeping ordinary file copies on your computer does not make
the trial database a replacement for that catalog.

## Implementation and verification

The image applies `mtproto-endpoint.patch` to TDLib commit
`bc9c263e2bfee06aaab41e82db51a103376030bc`, used by the pinned Bot API commit. A
version mismatch or patch failure stops the build. The patch prepends the runtime
endpoint after defaults and cached/server DC options; it does not persist the
override into TDLib's DC configuration. Removing the three variables restores
standard address selection after restart (an existing session keeps its home DC).

The build runs tests against TDLib's real `DcOptionsSet` for priority, failures,
DC updates and other-DC routing, plus input validation and initial-DC selection.
The final image runs binary/library smoke checks. Diagnostics intentionally avoid
upstream verbose logging, which can contain bot tokens. `--proxy` in the upstream
Bot API is an outgoing webhook proxy and is unrelated to this feature.
