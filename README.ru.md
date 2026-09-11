# Pentaract V2

[![CI](https://github.com/bryu234/pentaract-v2/actions/workflows/ci.yml/badge.svg)](https://github.com/bryu234/pentaract-v2/actions/workflows/ci.yml)
[![Go 1.24](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![React 19](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=111827)](https://react.dev/)
[![Docker Compose](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](https://docs.docker.com/compose/)
[![License MIT](https://img.shields.io/badge/License-MIT-22c55e.svg)](LICENSE)

[English](README.md)

Персональное зашифрованное файловое хранилище на базе приватного Telegram-канала и собственного Local Bot API Server.

## Возможности

- Возобновляемая загрузка без ограничения размера файла на уровне приложения
- Шифрование XChaCha20-Poly1305 до отправки в Telegram
- Части по 256 MiB с настройкой до 1900 MiB
- Пароль, TOTP, recovery-коды и серверные сессии
- Папки, поиск, Range-загрузка и корзина на 30 дней
- Зашифрованный переносимый backup PostgreSQL и master key

## Требования

- Docker Desktop или Docker Engine с Compose v2
- Приватный Telegram-канал
- Telegram-бот с правами администратора канала
- Telegram `api_id` и `api_hash`

## Настройка Telegram

1. Создайте приватный канал.
2. Создайте бота через [@BotFather](https://t.me/BotFather) командой `/newbot`.
3. Добавьте бота администратором канала. Разрешите **Post Messages** и **Delete Messages**.
4. Опубликуйте сообщение в канале и вызовите официальный метод Bot API [`getUpdates`](https://core.telegram.org/bots/api#getupdates). Скопируйте `channel_post.chat.id`; ID приватного канала обычно начинается с `-100`.
5. Войдите на [my.telegram.org/apps](https://my.telegram.org/apps), откройте **API development tools**, создайте приложение и скопируйте `api_id` и `api_hash`.

Не публикуйте bot token, `api_hash`, `.env`, master key и backups.

## Быстрый запуск

```bash
git clone https://github.com/bryu234/pentaract-v2.git
cd pentaract-v2
make init
nano .env
make doctor
make up
```

Заполните `.env`:

```dotenv
POSTGRES_PASSWORD=<LONG_URL_SAFE_RANDOM_PASSWORD>
TELEGRAM_API_ID=<API_ID>
TELEGRAM_API_HASH=<API_HASH>
```

Откройте <http://localhost:8088>. При первом запуске:

1. Создайте владельца и подключите TOTP.
2. Сохраните recovery-коды вне сервера.
3. В **Settings** укажите ID канала и bot token.
4. Включите **Cloud logOut**, если токен использовался через `api.telegram.org` для получения ID канала.

Первая сборка Local Bot API компилирует TDLib и может занять несколько минут.

## Команды

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

`make up` проверяет порт перед запуском. `make down` сохраняет volumes и файлы.

## MTProto server

Если `api.telegram.org` недоступен, передача файлов уже работает по MTProto через
локальный Bot API. Дополнительно можно указать приоритетный Production DC/IP/порт
со штатным резервным выбором адресов TDLib. См. [настройку и проверку на сервере](docs/MTPROTO.ru.md).

## Backup и перенос

```bash
make backup BACKUP_PASSPHRASE='<PASSPHRASE>'
make down
make restore BACKUP_FILE='pentaract-v2-....pv2backup' BACKUP_PASSPHRASE='<PASSPHRASE>'
```

Зашифрованный backup содержит каталог PostgreSQL, master key и данные восстановления. Зашифрованные файлы остаются в Telegram. Для доступа к ним нужны каталог и master key.

## Безопасность

Telegram получает только зашифрованные части с проверкой целостности. Bot token зашифрован в PostgreSQL, а master key подключается отдельно. PostgreSQL и Local Bot API не публикуются в сеть хоста.

За пределами доверенной локальной сети используйте HTTPS или приватный VPN. Уязвимости описаны в [SECURITY.md](SECURITY.md).

## Разработка

```bash
make test
make lint
docker compose config -q
```

Архитектура описана в [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Лицензия

[MIT](LICENSE)
