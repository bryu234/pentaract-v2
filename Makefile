SHELL := /bin/sh
COMPOSE := docker compose
HTTP_PORT := $(shell awk -F= '$$1 == "PENTARACT_HTTP_PORT" {print $$2; exit}' .env 2>/dev/null || printf '8088')

.DEFAULT_GOAL := help

.PHONY: help doctor check-ports check-production-ports init up prod-up down restart ps logs build test lint migrate backup restore clean

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*## "; print "Pentaract V2 commands:"} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

doctor: ## Validate Docker, configuration and required files
	@command -v docker >/dev/null || { echo "docker is required"; exit 1; }
	@docker compose version >/dev/null
	@test -f .env || { echo "Run: make init"; exit 1; }
	@$(COMPOSE) config -q
	@echo "Environment is ready"

check-ports: ## Ensure host ports are available before first start
	@port="$(HTTP_PORT)"; \
	case "$$port" in ''|*[!0-9]*) echo "Invalid PENTARACT_HTTP_PORT"; exit 1;; esac; \
	if docker compose ps --status running --services 2>/dev/null | grep -qx app; then \
	  echo "Port $$port belongs to the running Pentaract V2 stack"; \
	elif command -v lsof >/dev/null && lsof -nP -iTCP:$$port -sTCP:LISTEN >/dev/null 2>&1; then \
	  echo "Port $$port is already occupied"; exit 1; \
	elif command -v ss >/dev/null && ss -ltn | awk '{print $$4}' | grep -Eq "(^|:)$$port$$"; then \
	  echo "Port $$port is already occupied"; exit 1; \
	else echo "Port $$port is free"; fi

check-production-ports: ## Ensure HTTPS ports are free before production start
	@for port in 80 443; do \
	  if command -v lsof >/dev/null && lsof -nP -iTCP:$$port -sTCP:LISTEN >/dev/null 2>&1; then echo "Port $$port is occupied"; exit 1; fi; \
	done
	@echo "Ports 80 and 443 are free"

init: ## Create local config and generate the encryption master key
	@test -f .env || cp .env.example .env
	@mkdir -p data/secrets data/uploads data/backups
	@test -s data/secrets/master.key || docker run --rm alpine:3.22 sh -c 'head -c 32 /dev/urandom' > data/secrets/master.key
	@chmod 600 data/secrets/master.key 2>/dev/null || true
	@echo "Initialized .env and data/secrets/master.key; set TELEGRAM_API_ID and TELEGRAM_API_HASH"

up: doctor check-ports ## Build and start the complete stack
	@$(COMPOSE) up -d --build --remove-orphans

prod-up: doctor check-ports check-production-ports ## Start with Caddy and automatic HTTPS
	@$(COMPOSE) --profile production up -d --build --remove-orphans

down: ## Stop containers without deleting persistent data
	@$(COMPOSE) down

restart: doctor ## Restart only the application container
	@$(COMPOSE) up -d --build --no-deps app

ps: ## Show service status
	@$(COMPOSE) ps

logs: ## Follow application and Telegram API logs
	@$(COMPOSE) logs -f --tail=100 app telegram-bot-api

build: ## Build all production images
	@$(COMPOSE) build

test: ## Run backend and frontend tests
	@$(COMPOSE) run --rm --no-deps app-test
	@$(COMPOSE) run --rm --no-deps web-test

lint: ## Run static checks
	@$(COMPOSE) run --rm --no-deps app-test sh -c 'gofmt -d . && go vet ./...'
	@$(COMPOSE) run --rm --no-deps web-test npm run lint

migrate: ## Apply pending database migrations
	@$(COMPOSE) run --rm app migrate

backup: doctor ## Create an encrypted portable backup (BACKUP_PASSPHRASE required)
	@test -n "$(BACKUP_PASSPHRASE)" || { echo "Set BACKUP_PASSPHRASE"; exit 1; }
	@$(COMPOSE) run --rm -e BACKUP_PASSPHRASE="$(BACKUP_PASSPHRASE)" app backup

restore: doctor ## Restore BACKUP_FILE using BACKUP_PASSPHRASE
	@test -n "$(BACKUP_FILE)" -a -n "$(BACKUP_PASSPHRASE)" || { echo "Set BACKUP_FILE and BACKUP_PASSPHRASE"; exit 1; }
	@if $(COMPOSE) ps --status running --services | grep -qx app; then echo "Stop the application with 'make down' before restore"; exit 1; fi
	@$(COMPOSE) run --rm -e BACKUP_FILE="$(BACKUP_FILE)" -e BACKUP_PASSPHRASE="$(BACKUP_PASSPHRASE)" app restore

clean: ## Remove rebuildable local build output only; persistent volumes remain
	@$(COMPOSE) down --remove-orphans
	@rm -rf web/dist coverage
