# Payment Gateway Backend

A production-oriented Payment Gateway backend built with **Go**, exposing a REST API that sits between merchants and payment providers.

---

## Features

- Merchant registration with auto-generated API key
- API key authentication middleware (legacy + Phase 5C Argon2id lifecycle)
- Payment creation, retrieval, cancellation, and paginated listing
- Idempotent payment creation with merchant-scoped replay protection
- Strict transaction status state machine (CREATED → PENDING → PAID/FAILED/EXPIRED/CANCELLED)
- Partial / full / multiple refunds with over-refund protection (Phase 7B)
- Settlement import and reconciliation (Phase 7C)
- Dashboard authentication and user management with JWT + HttpOnly refresh sessions (Phase 8)
- Inbound provider webhooks + payment expiry worker
- Outbound merchant webhooks with HMAC signing, transactional outbox, and retries (Phase 6)
- Mock / Midtrans payment providers; Mock refund provider
- Full audit trail via `payment_attempts` / `refund_attempts`
- Merchant isolation (cross-merchant transaction access returns 404, not 403)
- `payment_url` and `provider_transaction_id` persisted on transaction for fast retrieval
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
     ├──────────► PaymentProvider (interface)
     │                   │
     │             MockPaymentProvider
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
# {"success":true,"data":{"status":"ok"},"meta":{"request_id":"req_xxx"}}
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
| `IDEMPOTENCY_TTL` | `24h`           | How long a payment idempotency key can be replayed |
| `ADMIN_API_KEY` | _(empty)_ | Ops/admin bootstrap key (`X-Admin-Key`); empty disables admin routes |
| `AUTH_JWT_SECRET` | _(dev default)_ | JWT signing secret for dashboard auth; **required in production** |
| `AUTH_ACCESS_TOKEN_TTL` | `15m` | Short-lived dashboard access token lifetime |
| `AUTH_REFRESH_TOKEN_TTL` | `168h` | Refresh session / cookie lifetime |
| `CORS_ALLOWED_ORIGINS` | _(empty)_ | Comma-separated SPA origins (e.g. `http://localhost:5173`) |

> **Never commit `.env` to version control.**

Dashboard auth is separate from merchant API keys. See [docs/phase-8-auth-design.md](docs/phase-8-auth-design.md) and [docs/phase-8-dashboard-auth-api.md](docs/phase-8-dashboard-auth-api.md).

### Payment idempotency

`POST /api/v1/payments` requires an `Idempotency-Key` header. The key is scoped
to the authenticated merchant and hashed together with the business payload.
The first request reserves the key in PostgreSQL before calling the provider.
The same key and payload replays the stored result without creating another
transaction or provider attempt. A different payload returns `409
IDEMPOTENCY_KEY_REUSED`; an active request returns
`409 IDEMPOTENCY_REQUEST_IN_PROGRESS`.

Completed and failed results are replayed until `IDEMPOTENCY_TTL` expires.
Expired keys can be reserved again. A provider timeout is not retried
automatically: the external result may be ambiguous, so the failed result is
replayed rather than risking a second external payment.

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

All merchant payment endpoints require the `X-API-Key` header.

| Method | Path                           | Auth | Description                |
|--------|--------------------------------|------|----------------------------|
| GET    | /health                        | No   | Liveness check             |
| GET    | /health/ready                  | No   | Readiness check (DB ping)  |
| POST   | /api/v1/merchants              | No   | Register a merchant        |
| GET    | /api/v1/merchants/:id          | No   | Get merchant details       |
| POST   | /api/v1/merchants/:id/webhook  | Yes  | Configure outbound webhook |
| GET    | /api/v1/merchants/:id/webhook  | Yes  | Get webhook config         |
| POST   | /api/v1/merchants/:id/webhook/rotate | Yes | Rotate webhook secret |
| DELETE | /api/v1/merchants/:id/webhook  | Yes  | Disable webhook            |
| GET    | /api/v1/merchants/:id/webhook/deliveries | Yes | List deliveries |
| POST   | /api/v1/merchants/:id/webhook/deliveries/:id/retry | Yes | Manual retry |
| GET    | /api/v1/payments               | Yes  | List payments (paginated, filtered) |
| POST   | /api/v1/payments               | Yes  | Create a payment (`Idempotency-Key` required) |
| GET    | /api/v1/payments/:id           | Yes  | Get payment details        |
| POST   | /api/v1/payments/:id/cancel    | Yes  | Cancel a payment           |
| POST   | /api/v1/payments/:id/refunds   | Yes  | Create a refund (`Idempotency-Key` required) |
| GET    | /api/v1/payments/:id/refunds   | Yes  | List refunds for a payment |
| GET    | /api/v1/refunds/:id            | Yes  | Get refund details         |

See [docs/api.md](docs/api.md) for full request/response documentation.

---

## Swagger UI

Once the server is running, open:

```
http://localhost:8081/swagger/index.html
```

---

## Testing

```bash
# Run all tests
make test

# Or directly
go test ./...

# With race detector
go test -race ./...
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
│   ├── model/            Domain models + DTOs + state machine
│   ├── repository/       Database access layer
│   └── service/          Business logic + PaymentProvider interface + Mock
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

### Phase 1 — Core Infrastructure ✅
- Merchant management (register, get)
- API key authentication
- PostgreSQL migrations
- Docker + Docker Compose
- Structured logging
- Swagger setup

### Phase 2 — Payment Core Flow ✅
- Payment creation, retrieval, cancellation
- Mock Payment Provider (pluggable interface)
- Transaction state machine
- Full audit trail (`payment_attempts`)
- Merchant isolation
- `payment_url` and `provider_transaction_id` stored on transaction

### Phase 3 — Provider & Webhooks ✅
- Midtrans Snap adapter
- Inbound webhook processing (PAID / FAILED events)
- Automatic EXPIRED transitions via background expiry worker

### Phase 4 — Reliability & Operations ✅
- Payment expiry worker with configurable interval and batch size
- Webhook event deduplication
- Provider timeout handling (no silent retry)

### Phase 5A — API Idempotency ✅
- `POST /api/v1/payments` requires `Idempotency-Key` header
- Merchant-scoped PostgreSQL reservation (`UNIQUE(merchant_id, key)`)
- Atomic `PROCESSING` reservation prevents duplicate provider calls
- `COMPLETED` replay — stored JSONB response returned without a second transaction or provider call
- `FAILED` replay — provider errors and timeouts replayed, never retried automatically
- `409 IDEMPOTENCY_KEY_REUSED` for same key + different payload
- `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` for concurrent in-flight requests
- TTL-based expiry (`IDEMPOTENCY_TTL`, default `24h`) — expired keys re-reservable
- Thread-safe `MockPaymentProvider` call counter for concurrency tests
- Full handler + service + concurrency test coverage

### Phase 5B — Payment Listing ✅
- `GET /api/v1/payments` — paginated, filtered listing with merchant isolation
- Deterministic ordering (`created_at DESC, id DESC`)
- Filters: `status`, `merchant_order_id`, `payment_method`, `created_from`, `created_to`
- Half-open date range semantics `[created_from, created_to)`
- Safe DTO response (no API secrets or provider credentials)
- Count query for accurate `total` and `total_pages` pagination metadata
- Indexes: composite `(merchant_id, created_at DESC, id DESC)` and `(merchant_id, status, created_at DESC, id DESC)`

### Phase 7A — Financial design (documentation only) 📋
- Refund, settlement, and reconciliation **design** — no runtime APIs yet
- See [docs/phase-7a-financial-design.md](docs/phase-7a-financial-design.md)
- Next implementation slice: **Phase 7B — Refund Core** (migrations from `000012`)

### Phase 7B — Refund Core ✅
- Full, partial, and multiple refunds against PAID transactions
- Over-refund protection: `SELECT … FOR UPDATE` + denormalized counters + CHECK constraints
- `refunded_amount` and `reserved_refund_amount` on transactions (PENDING/PROCESSING both reserve)
- New tables: `refunds`, `refund_attempts`; extended `idempotency_keys` with `refund_id`
- `RefundProvider` interface + `MockRefundProvider` (thread-safe, atomic call counter)
- `RefundService` with idempotency, concurrency protection, outbox events
- APIs: `POST /api/v1/payments/:id/refunds`, `GET /api/v1/refunds/:id`, `GET /api/v1/payments/:id/refunds`
- Inbound provider refund webhooks (`REFUND_SUCCEEDED`, `REFUND_FAILED`) via existing webhook endpoint
- Merchant outbound webhook events: `refund.created`, `refund.processing`, `refund.succeeded`, `refund.failed`
- Idempotent refund creation (reuses Phase 5A infrastructure)
- Provider timeout: refund stays PENDING (reservation held; no silent FAILED)
- See [docs/refunds.md](docs/refunds.md)

### Phase 7C — Settlement & Reconciliation ✅
- Atomic provider settlement import with canonical payload hash and idempotent replay/conflict detection
- External-evidence model: `settlements`, `settlement_items`, `reconciliation_runs`, `reconciliation_results`
- Deterministic payment/refund matching with amount, currency, and lifecycle checks
- Safe reconciliation rerun with PostgreSQL claim/update and unique current result per settlement item
- Admin/ops APIs protected by dedicated `X-Admin-Key`; merchant API keys are not accepted
- Mock settlement importer for tests and development; no unverified live Midtrans settlement contract
- See [docs/settlements.md](docs/settlements.md) and [docs/reconciliation.md](docs/reconciliation.md)

### Phase 6 — Merchant Outbound Webhooks ✅
- One webhook endpoint per merchant (`merchant_webhook_configs`)
- Signing secret (`whsec_…`) returned once on create/rotate; AES-256-GCM encrypted at rest
- Transactional outbox (`merchant_webhook_deliveries`) — status change + delivery row in one DB tx
- Event types: `payment.created`, `payment.pending`, `payment.paid`, `payment.failed`, `payment.expired`, `payment.cancelled`
- Background worker with `FOR UPDATE SKIP LOCKED` claiming, exponential backoff, dead-letter (`DEAD`)
- HMAC-SHA256 signing (`X-PayGate-Event-ID`, `X-PayGate-Signature`, …)
- Manual retry API + delivery audit listing
- Merchant isolation (403 on cross-merchant access)
- See [docs/merchant-webhooks.md](docs/merchant-webhooks.md)

---

## Documentation

| Document                    | Description                        |
|-----------------------------|------------------------------------|
| [docs/architecture.md](docs/architecture.md) | Layer responsibilities & design decisions |
| [docs/database.md](docs/database.md)         | ERD, table schemas, constraints           |
| [docs/api.md](docs/api.md)                   | Full API reference with curl examples     |
| [docs/flow.md](docs/flow.md)                 | Payment flow diagrams (Mermaid)           |
| [docs/merchant-webhooks.md](docs/merchant-webhooks.md) | Outbound merchant webhooks (Phase 6) |
| [docs/phase-7a-financial-design.md](docs/phase-7a-financial-design.md) | Financial model & refund design (Phase 7A) |
| [docs/refunds.md](docs/refunds.md) | Refund lifecycle, API, idempotency, and concurrency (Phase 7B) |
| [docs/settlements.md](docs/settlements.md) | Settlement import, identity, security, and operations (Phase 7C) |
| [docs/reconciliation.md](docs/reconciliation.md) | Matching rules, mismatch reasons, rerun, and recovery (Phase 7C) |
| [docs/api-key-lifecycle.md](docs/api-key-lifecycle.md) | Merchant API key lifecycle (Phase 5C) |
