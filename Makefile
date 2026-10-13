# ──────────────────────────────────────────────────────────────────────────────
# Payment Gateway — Makefile
# ──────────────────────────────────────────────────────────────────────────────

.PHONY: all run build test test-integration fmt fmt-check vet \
        docker-up docker-down docker-build docker-logs docker-validate \
        elk-up elk-down elk-provision elk-provision-dashboard elk-dashboard elk-certs elk-auth-setup elk-tls-check \
        migrate-up migrate-down migrate-status migrate-create \
        swagger lint clean help

# Binary output path.
BINARY        := bin/server
# Entry point.
CMD           := ./cmd/server
# Module name (keep in sync with go.mod).
MODULE        := github.com/dhikaarta/pay-gate-backend
# Migration directory.
MIGRATIONS    := migrations
# Database URL — explicitly supplied by the operator. Prefer DATABASE_URL for
# deployments or TEST_DATABASE_URL for disposable integration tests; no
# credential or port is embedded in the Makefile.
DATABASE_URL  ?=
TEST_DATABASE_URL ?=
DB_URL        ?= $(if $(DATABASE_URL),$(DATABASE_URL),$(TEST_DATABASE_URL))
# Docker Compose project name.
COMPOSE_FILE  := docker-compose.yml
# ELK stack compose file and Kibana URL.
ELK_COMPOSE_FILE := docker-compose.elk.yml
KIBANA_URL    ?= http://localhost:5601

## all: format check + vet + test + build (read-only for the source tree)
all: fmt-check vet test build

# ── Local development ─────────────────────────────────────────────────────────

## run: run the server locally (requires .env with DB_HOST=localhost)
run:
	go run $(CMD)

## build: compile the server binary to ./bin/server
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="-w -s" -o $(BINARY) $(CMD)
	@echo "Binary: $(BINARY)"

## build-no-vcs: compile without VCS stamping (use when not in a git repo)
build-no-vcs:
	@mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -ldflags="-w -s" -o $(BINARY) $(CMD)
	@echo "Binary: $(BINARY)"

## test: run all unit/in-memory tests with the race detector
test:
	go test -v -race -count=1 ./...

## test-integration: run the PostgreSQL-backed suite against a disposable DB
test-integration:
	@test -n "$(TEST_DATABASE_URL)" || (echo "TEST_DATABASE_URL is required"; exit 1)
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -v -race -count=1 -p=1 ./...

## fmt: format all Go source files
fmt:
	gofmt -w .

## fmt-check: fail when Go source files need formatting
fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Go files need gofmt; run make fmt"; gofmt -l .; exit 1)

## vet: run go vet on all packages
vet:
	go vet ./...

## lint: run golangci-lint (must be installed separately)
lint:
	golangci-lint run ./...

## clean: remove build artefacts
clean:
	rm -rf bin/

# ── Docker ────────────────────────────────────────────────────────────────────

## docker-validate: validate the Compose model without starting services
docker-validate:
	docker compose -f $(COMPOSE_FILE) config --quiet

## docker-build: build the Docker image
docker-build:
	docker compose -f $(COMPOSE_FILE) build

## docker-up: start the app and one-shot migration job (external PostgreSQL)
docker-up:
	docker compose -f $(COMPOSE_FILE) up -d --build

## docker-down: stop and remove Compose containers (external DB is unaffected)
docker-down:
	docker compose -f $(COMPOSE_FILE) down

## docker-logs: tail logs for all services
docker-logs:
	docker compose -f $(COMPOSE_FILE) logs -f

# ── ELK observability ─────────────────────────────────────────────────────────

## elk-up: start the ELK stack (Elasticsearch, Logstash, Kibana)
elk-up:
	docker compose -f $(ELK_COMPOSE_FILE) up -d

## elk-provision: wait for Kibana readiness and provision Data View + Dashboard (idempotent)
elk-provision:
	@if [ -f .env.elk ]; then set -a; . ./.env.elk; set +a; \
	  KIBANA_USER=elastic KIBANA_PASSWORD="$${ELASTIC_PASSWORD}" KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dataview.sh; \
	  KIBANA_USER=elastic KIBANA_PASSWORD="$${ELASTIC_PASSWORD}" KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dashboard.sh; \
	else \
	  KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dataview.sh; \
	  KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dashboard.sh; \
	fi

## elk-provision-dashboard: provision only the dashboard (idempotent)
elk-provision-dashboard:
	@if [ -f .env.elk ]; then set -a; . ./.env.elk; set +a; \
	  KIBANA_USER=elastic KIBANA_PASSWORD="$${ELASTIC_PASSWORD}" KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dashboard.sh; \
	else \
	  KIBANA_URL=$(KIBANA_URL) ./elk/scripts/provision-kibana-dashboard.sh; \
	fi

elk-dashboard: elk-provision-dashboard

## elk-certs: generate development TLS certificates for ELK stack
elk-certs:
	./elk/scripts/generate-dev-certs.sh

## elk-auth-setup: bootstrap the elastic password + Kibana service token into .env.elk (gitignored, mode 600)
elk-auth-setup:
	./elk/scripts/setup-elk-auth.sh

## elk-tls-check: verify ELK TLS connectivity (Elasticsearch HTTPS with CA + auth, Kibana status)
elk-tls-check:
	@test -f .env.elk || (echo "Missing .env.elk; run 'make elk-auth-setup' first"; exit 1)
	@echo "=== Elasticsearch HTTPS check (CA verification + auth) ==="
	@set -a; . ./.env.elk; set +a; \
	curl --fail --silent --show-error --cacert ./elk/certs/ca/ca.crt \
	  -u "elastic:$${ELASTIC_PASSWORD}" https://localhost:9200 >/dev/null && echo "ES HTTPS OK (200, CA verified)"
	@echo "=== Elasticsearch cluster health ==="
	@set -a; . ./.env.elk; set +a; \
	curl --fail --silent --show-error --cacert ./elk/certs/ca/ca.crt \
	  -u "elastic:$${ELASTIC_PASSWORD}" 'https://localhost:9200/_cluster/health?pretty'
	@echo "=== Kibana status check ==="
	@curl --fail --silent --show-error http://localhost:5601/api/status && echo

## elk-down: stop and remove the ELK stack
elk-down:
	docker compose -f $(ELK_COMPOSE_FILE) down

# ── Database migrations ───────────────────────────────────────────────────────

## migrate-up: apply all pending migrations (requires an explicit DB_URL)
migrate-up:
	@test -n "$(DB_URL)" || (echo "DB_URL or DATABASE_URL is required"; exit 1)
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" up

## migrate-status: show the current migration version and dirty state
migrate-status:
	@test -n "$(DB_URL)" || (echo "DB_URL or DATABASE_URL is required"; exit 1)
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" version

## migrate-down: explicitly roll back one migration (destructive; guarded)
migrate-down:
	@test "$(ALLOW_DESTRUCTIVE_DOWN)" = "true" || (echo "Refusing destructive migration rollback; set ALLOW_DESTRUCTIVE_DOWN=true after approval"; exit 1)
	@test -n "$(DB_URL)" || (echo "DB_URL or DATABASE_URL is required"; exit 1)
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" down 1

## migrate-create name=<migration_name>: create a new migration pair
## Example: make migrate-create name=add_users
migrate-create:
	@[ -n "$(name)" ] || (echo "Usage: make migrate-create name=<migration_name>" && exit 1)
	migrate create -ext sql -dir $(MIGRATIONS) -seq $(name)

# ── Swagger ───────────────────────────────────────────────────────────────────

## swagger: regenerate Swagger docs from annotations
swagger:
	swag init -g $(CMD)/main.go -o ./swagger --parseDependency --parseInternal

# ── Help ──────────────────────────────────────────────────────────────────────

## help: list all targets with descriptions
help:
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /' | column -t -s ':'
	@echo ""
