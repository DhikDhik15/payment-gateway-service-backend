# Payment Gateway Backend

A production-oriented Payment Gateway backend built with **Go**, exposing a REST API that sits between merchants and payment providers.

---

## Features

- Merchant registration with auto-generated API key
- API key authentication middleware
- Payment creation, retrieval, and cancellation
- Strict transaction status state machine (CREATED → PENDING → PAID/FAILED/EXPIRED/CANCELLED)
- Mock Payment Provider (pluggable interface — ready for real providers in Phase 2)
- Full audit trail via `payment_attempts` table
- Structured JSON logging
- Docker + Docker Compose (single command start)
- Reversible database migrations (`golang-migrate`)
- Swagger / OpenAPI documentation
- Unit tests (no external dependencies required)

---

## Architecture

```
HTTP Request
     │
     ▼
 Middleware       ← authentication, logging, recovery
     │
     ▼
 Handler          ← request parsing, validation, HTTP response
     │
     ▼
 Service          ← business logic, state machine, orchestration
     │
     ▼
 Repository       ← database queries (pgx)
     │
     ▼
 PostgreSQL
```

See [docs/architecture.md](docs/architecture.md) for a detailed explanation of each layer.

---

## Technology Stack

| Component       | Choice                          |
|-----------------|---------------------------------|
| Language        | Go 1.24                         |
| HTTP framework  | Gin v1.10                       |
| Database        | PostgreSQL 16                   |
| DB driver       | pgx v5                          |
| Migrations      | golang-migrate v4               |
| Containerisation| Docker + Docker Compose         |
| API docs        | Swagger (swaggo/swag)           |
| Config          | Environment variables + godotenv|

---

## Requirements

- Go 1.24+
- Docker & Docker Compose v2
- `golang-migrate` CLI (for local migrations)
- `swag` CLI (for Swagger regeneration, optional)

Install `golang-migrate`:

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

Install `swag`:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
```

---

## Quick Start (Docker Compose)

```bash
# Clone and enter the project
git clone <repo-url>
cd pay-gate-backend

# Start services
docker compose up -d --build

# Verify
curl http://localhost:8080/health
# {"status":"ok"}
```

PostgreSQL data is persisted in the `pay-gate-postgres-data` Docker volume.

---

## Environment Configuration

Copy `.env.example` and adjust values:

```bash
cp .env.example .env
```

| Variable      | Default            | Description                          |
|---------------|--------------------|--------------------------------------|
| `APP_NAME`    | `payment-gateway`  | Application name (used in logs)      |
| `APP_ENV`     | `development`      | `development` or `production`        |
| `APP_PORT`    | `8080`             | HTTP listen port                     |
| `DB_HOST`     | `localhost`        | PostgreSQL host                      |
| `DB_PORT`     | `5432`             | PostgreSQL port                      |
| `DB_USER`     | `payment`          | PostgreSQL user                      |
| `DB_PASSWORD` | `payment`          | PostgreSQL password                  |
| `DB_NAME`     | `payment_gateway`  | Database name                        |
| `DB_SSLMODE`  | `disable`          | PostgreSQL SSL mode                  |

> **Never commit `.env` to version control.**

---

## Running Locally (without Docker)

```bash
# Ensure PostgreSQL is running and .env is configured with DB_HOST=localhost

go run ./cmd/server
```

---

## Database Migration

```bash
# Apply all migrations
make migrate-up

# Roll back last migration
make migrate-down

# Create a new migration
make migrate-create name=add_users
```

Migrations live in `./migrations/`.

---

## Makefile Targets

```
make run            Run server locally
make build          Build binary to ./bin/server
make test           Run all unit tests
make fmt            Format Go source files
make vet            Run go vet

make docker-up      Build & start all Docker services
make docker-down    Stop containers (volumes preserved)
make docker-build   Build Docker image only
make docker-logs    Tail all container logs

make migrate-up     Apply pending migrations
make migrate-down   Roll back last migration
make migrate-create name=<name>   Create new migration pair

make swagger        Regenerate Swagger docs
make help           List all targets
```

---

## API Overview

All merchant API endpoints require the `X-API-Key` header.

| Method | Path                           | Auth | Description                |
|--------|--------------------------------|------|----------------------------|
| GET    | /health                        | No   | Liveness check             |
| GET    | /health/ready                  | No   | Readiness check (DB ping)  |
| POST   | /api/v1/merchants              | No   | Register a merchant        |
| GET    | /api/v1/merchants/:id          | No   | Get merchant details       |
| POST   | /api/v1/payments               | Yes  | Create a payment           |
| GET    | /api/v1/payments/:id           | Yes  | Get payment details        |
| POST   | /api/v1/payments/:id/cancel    | Yes  | Cancel a payment           |

See [docs/api.md](docs/api.md) for full request/response documentation.

---

## Swagger UI

Once the server is running, open:

```
http://localhost:8080/swagger/index.html
```

---

## Testing

```bash
# Run all tests
make test

# Or directly
go test ./...
```

---

## Project Structure

```
pay-gate-backend/
├── cmd/server/           Entry point (main.go)
├── internal/
│   ├── config/           Environment configuration
│   ├── handler/          HTTP handlers (thin layer)
│   ├── middleware/        Auth, logging
│   ├── model/            Domain models
│   ├── repository/       Database access layer
│   └── service/          Business logic layer
├── pkg/
│   ├── database/         pgxpool setup and helpers
│   └── response/         Standardised JSON response helpers
├── migrations/           SQL migration files
├── docs/                 Markdown documentation
├── swagger/              Generated Swagger artefacts
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── README.md
```

---

## Development Roadmap

### Phase 1 (current) — Core Payment Gateway
- Merchant management
- Payment lifecycle (create / get / cancel)
- Mock Payment Provider
- API key authentication
- Structured logging
- Docker + migrations + Swagger

### Phase 2 (planned)
- Real QRIS provider integration
- Virtual Account provider
- E-Wallet provider
- Webhook notifications
- Retry queue

### Phase 3 (planned)
- Settlement
- Refund
- Redis caching
- Message queue (RabbitMQ / Kafka)
- Kubernetes deployment

---

## Documentation

| Document                    | Description                        |
|-----------------------------|------------------------------------|
| [docs/architecture.md](docs/architecture.md) | Layer responsibilities & design decisions |
| [docs/database.md](docs/database.md)         | ERD, table schemas, constraints           |
| [docs/api.md](docs/api.md)                   | Full API reference with curl examples     |
| [docs/flow.md](docs/flow.md)                 | Payment flow diagrams (Mermaid)           |
