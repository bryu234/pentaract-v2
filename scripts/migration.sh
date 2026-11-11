#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077
mode=$1
case "$mode" in
export|handoff)
 docker compose config -q
 running=$(docker compose ps --status running --services | sed -n '/^app$/p')
 restore_app() { if [ -n "$running" ]; then docker compose start app; fi; }
 trap restore_app EXIT
 if [ -n "$running" ]; then docker compose stop app; fi
 stamp=$(date -u +%Y%m%dT%H%M%SZ)
 mkdir -p data/backups
 output="migration-$stamp"
 docker compose run --rm --no-deps -e MIGRATION_DIR="/data/backups/$output" -e SOURCE_REVISION="$(git rev-parse HEAD)" app migration-export
 docker compose run --rm --no-deps --entrypoint sh app -c 'chown -R "$1:$2" "$3"' sh "$(id -u)" "$(id -g)" "/data/backups/$output"
 printf 'Migration files: %s/data/backups/%s\n' "$PWD" "$output"
 if [ "$mode" = handoff ]; then
  docker compose run --rm --no-deps app bot-logout
  docker compose stop telegram-bot-api
  running=''
  printf 'Old application and Bot API stopped. Preserve the database and volumes.\n'
 fi
 ;;
restore)
 input=${MIGRATION_DIR:-$PWD}
 input=$(cd "$input" && pwd)
 for f in master.key database.dump restore.json; do test -f "$input/$f" || { echo "Missing $input/$f"; exit 1; }; done
 test ! -e docker-compose.override.yml || { echo 'Restore through port requires a fresh clone without Compose override'; exit 1; }
 test -n "${PUBLIC_URL:-}" || { echo 'Set PUBLIC_URL=http://SERVER_IP:8088'; exit 1; }
 port=${PORT:-8088}
 case "$port" in ''|*[!0-9]*) echo 'Invalid PORT'; exit 1;; esac
 if command -v lsof >/dev/null; then
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then echo 'PORT occupied'; exit 1; fi
 elif command -v ss >/dev/null; then
  if ss -ltnH | awk '{print $4}' | grep -Eq "(^|:)$port$"; then echo 'PORT occupied'; exit 1; fi
 else echo 'Install lsof or ss for port verification'; exit 1; fi
 test -z "$(docker compose ps --status running --services | sed -n '/^app$/p')" || { echo 'Application must be stopped'; exit 1; }
 docker compose build app telegram-bot-api
 docker compose run --rm --no-deps -v "$input:/migration:ro" -v "$PWD:/target" -e PUBLIC_URL -e PORT="$port" app migration-config
 docker compose run --rm --no-deps --entrypoint sh -v "$PWD:/target" app -c 'chown "$1:$2" /target/.env' sh "$(id -u)" "$(id -g)"
 docker compose config -q
 docker compose up -d --wait postgres telegram-bot-api
 mkdir -p data/secrets
 if ! docker compose run --rm --no-deps -v "$input:/migration:ro" app migration-import; then
  echo 'Import or verification failed. Application remains stopped. Do not delete the database. Inspect logs; if import committed, fix the cause and use make up.'
  exit 1
 fi
 docker compose up -d --no-deps --wait app
 printf 'Restored: %s\n' "$PUBLIC_URL"
 ;;
esac
