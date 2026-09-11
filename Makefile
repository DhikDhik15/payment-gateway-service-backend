# ──────────────────────────────────────────────────────────────────────────────
# Payment Gateway — Makefile
# ──────────────────────────────────────────────────────────────────────────────

.PHONY: all run build test fmt vet \
        docker-up docker-down docker-build docker-logs \
        migrate-up migrate-down migrate-create \
        swagger lint clean help

# Binary output path.
BINARY        := bin/server
# Entry point.
CMD           := ./cmd/server
# Module name (keep in sync with go.mod).
MODULE        := github.com/dhikaarta/pay-gate-backend
# Migration directory.
MIGRATIONS    := migrations
# Database URL — mirrors docker-compose env for local use.
DB_URL        ?= postgres://payment:payment@localhost:5432/payment_gateway?sslmode=disable
# Docker Compose project name.
COMPOSE_FILE  := docker-compose.yml

## all: fmt + vet + test + build
all: fmt vet test build

# ── Local development ─────────────────────────────────────────────────────────

## run: run the server locally (requires .env with DB_HOST=localhost)
run:
	go run $(CMD)

## build: compile the server binary to ./bin/server
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="-w -s" -o $(BINARY) $(CMD)
	@echo "Binary: $(BINARY)"

## test: run all unit tests
test:
	go test -v -race -count=1 ./...

## fmt: format all Go source files
fmt:
	gofmt -w .

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

## docker-build: build the Docker image
docker-build:
	docker compose -f $(COMPOSE_FILE) build

## docker-up: start all services (app + postgres) in detached mode
docker-up:
	docker compose -f $(COMPOSE_FILE) up -d --build

## docker-down: stop and remove containers (volumes are preserved)
docker-down:
	docker compose -f $(COMPOSE_FILE) down

## docker-logs: tail logs for all services
docker-logs:
	docker compose -f $(COMPOSE_FILE) logs -f

# ── Database migrations ───────────────────────────────────────────────────────

## migrate-up: apply all pending migrations
migrate-up:
	migrate -path $(MIGRATIONS) -database "$(DB_URL)" up

## migrate-down: roll back the last applied migration
migrate-down:
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
