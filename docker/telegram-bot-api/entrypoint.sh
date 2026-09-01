#!/usr/bin/env bash
set -euo pipefail

: "${TELEGRAM_API_ID:?TELEGRAM_API_ID is required}"
: "${TELEGRAM_API_HASH:?TELEGRAM_API_HASH is required}"

exec /opt/telegram-bot-api/bin/telegram-bot-api \
  --api-id="${TELEGRAM_API_ID}" \
  --api-hash="${TELEGRAM_API_HASH}" \
  --local \
  --http-port=8081 \
  --dir=/var/lib/telegram-bot-api \
  --temp-dir=/shared/tmp

