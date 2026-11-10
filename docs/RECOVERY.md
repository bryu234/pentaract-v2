# Recovery and migration

Keep `data/secrets/master.key` outside the server. The key alone is insufficient for
old installations: publish a catalog before losing the original database.
The existing bot and channel must remain accessible. Telegram credentials, this
key, backups and backup passwords must never be committed.

## Catalog

The worker publishes an encrypted snapshot after metadata changes and pins it in
the storage channel. Grant the bot permission to edit/pin channel messages.
`syncing` means the file is uploaded but its catalog has not been acknowledged.
Settings displays publication status and retries. Older catalog documents remain
in the channel. A full catalog is published each time; its cost grows with metadata.
Automatic trash purging is currently disabled pending safe deletion review.

Key source: `PENTARACT_MASTER_KEY` (base64) or `PENTARACT_MASTER_KEY_FILE` (raw or
base64). Conflicting values fail startup. `make init` must not generate a new key
for recovery: install the saved key first.

```sh
make catalog-publish
make export-key KEY_OUTPUT=/data/backups/recovery-key.txt
```

Store the exported key separately from the channel. Export refuses to overwrite.

## Empty installation

Install the saved key in `data/secrets/master.key`, configure Telegram API ID/hash
and PostgreSQL in `.env`, and build the same or newer compatible release. Keep the
application stopped during import. Supply `RECOVERY_BOT_TOKEN` and
`RECOVERY_CHAT_ID` through the environment (do not put credentials in shell history).

```sh
make catalog-restore
```

If the latest pinned message is not a catalog, download a catalog document manually,
put it in `data/backups`, and set `RECOVERY_FILE=/data/backups/<filename>`.
Import authenticates the catalog and checks the bot and channel. It refuses a
nonempty database. Start the application, create a new owner and verify downloads.
The recovered storage is read-only until `make recovery-resume`.
Incomplete uploads and permanently deleted data are not recoverable.

## Ordinary migration

Use the encrypted full backup to preserve the owner, TOTP and settings. Complete
uploads, stop writes, publish the catalog, create a full backup and keep an off-host
copy of it and its password. Stop the old app, call `pentaract bot-logout`
against its Local Bot API, then stop the old Bot API before starting the new one.
This follows the upstream local-server migration procedure:
https://github.com/tdlib/telegram-bot-api#moving-a-bot-from-one-local-server-to-another Preserve the old PostgreSQL volumes and key for rollback.

On homeserver-out select the server overlay once; standard `make` commands then
operate on the correct server project:

```sh
ln -s docker-compose.server.yml docker-compose.override.yml
make doctor
make up
```

The server overlay uses the separate project `pentaract-v2-home` and its own
persistent volumes; pre-existing `pentaract-v2` volumes remain untouched.
The server overlay publishes no host ports. Only app joins the external `npm`
network. Configure NPM upstream HTTP `pentaract-v2-app:8080`, HTTPS, force SSL,
and an upload size/timeout suitable for large files. Secure cookies are enabled.
Set `PENTARACT_PUBLIC_URL` to the actual HTTPS URL. Check port availability before
starting any alternative local setup. Do not start the Caddy production profile.

For a verification import into a disposable empty PostgreSQL database, use
`pentaract catalog-verify` with `RECOVERY_VERIFY_DATABASE_URL`. It downloads only
the two smallest files of at most 5 MiB each and compares their checksums.

## Shared download links

Ready files support expiring links (15 minutes, 1 hour, 24 hours or 7 days).
One-time links are consumed atomically when a download starts; interrupted or failed
transfers require a new link. Previews do not consume links. The owner can copy or
revoke links in the Share dialog. Trashing a file or ancestor folder revokes its
links permanently. Active transfers are not interrupted by expiration/revocation.
The public URL uses a fragment token so NPM request URLs do not contain it.
Set `PENTARACT_PUBLIC_URL` to the actual HTTPS origin.
Links are excluded from Telegram recovery catalogs. Full database restoration
revokes all restored links; normal application updates preserve them.

## Authentication lifetime and limits

Owner sessions expire 24 hours after login; activity does not extend them. Existing
sessions are capped at their creation time plus 24 hours on upgrade. Full backup
restoration clears sessions and revokes links in the same database transaction.
Passwords, TOTP secrets and file keys remain unchanged.

Authentication endpoints allow 10 attempts per source per minute and at most two
simultaneous password checks. Five failed logins for an account block attempts
for the remainder of a 15-minute window; rejected requests do not extend it.
Limits return HTTP 429 with Retry-After. Counters are bounded in process memory
and reset on application restart.

Set PENTARACT_TRUSTED_PROXY_CIDRS to the exact NPM address (e.g. 172.18.0.2/32).
Only that peer may provide X-Real-IP. Other forwarded headers are ignored. Recheck
this address if NPM is recreated. Empty configuration trusts no proxy.
