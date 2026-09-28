# Architecture Documentation

## Overview

Payment Gateway Backend uses a **layered architecture** that separates HTTP
concerns, business logic, and data access into distinct layers with explicit
boundaries.

```
┌────────────────────────────────────────┐
│              HTTP Client               │
└─────────────────┬──────────────────────┘
                  │ HTTP Request
┌─────────────────▼──────────────────────┐
│             Middleware Layer           │
│  RequestID · Auth · Recovery · Logger  │
└─────────────────┬──────────────────────┘
                  │ *gin.Context + Merchant
┌─────────────────▼──────────────────────┐
│              Handler Layer             │
│   Parse request · Validate format ·    │
│   Call service · Write response        │
└─────────────────┬──────────────────────┘
                  │ Domain types
┌─────────────────▼──────────────────────┐
│              Service Layer             │
│  Business logic · State machine ·      │
│  Orchestrate repo + provider calls     │
└──────┬──────────────────────┬──────────┘
       │                      │
┌──────▼──────┐     ┌─────────▼────────┐
│ Repository  │     │  Payment Provider │
│   Layer     │     │  (interface)      │
└──────┬──────┘     └─────────┬────────┘
       │                      │
┌──────▼──────┐     ┌─────────▼────────┐
│ PostgreSQL  │     │  MockProvider    │
│  (pgxpool)  │     │  (Phase 2)       │
└─────────────┘     └──────────────────┘
```

---

## Layer Responsibilities

### Middleware (`internal/middleware/`)

| Middleware | Responsibility |
|------------|----------------|
| `RequestID` | Reads or generates `X-Request-ID`, stores in context, echoes in response header |
| `CORS` | Applies explicit-origin browser policy; preflight still carries the request ID |
| `Auth` | Reads `X-API-Key`, looks up merchant, validates ACTIVE status, stores merchant in context |
| `gin.Recovery` | Catches panics, returns 500 without crashing the server |
| `requestLogger` | Structured JSON log line per request after all handlers run; logs route templates, not raw token paths |
| `MaxBodyBytes` | Bounds declared and chunked request bodies before handlers/readers |

Middleware runs in this order:
1. Recovery (catches panics from subsequent handlers)
2. RequestID (stamps every request, including CORS preflight)
3. CORS (applies the explicit browser-origin policy)
4. Logger (runs after request completes so it can log the final status code)
5. BodyLimit (caps bodies before handlers or route middleware read them)
6. Auth (route-group level — only on protected endpoints)

### Handler (`internal/handler/`)

Handlers are **thin adapters** between HTTP and the service layer.

Responsibilities:
- Parse and bind JSON request body (`ShouldBindJSON`)
- Validate request **format** (UUID format, required fields via struct tags)
- Extract authenticated merchant from Gin context via `MerchantFromContext`
- Call exactly one service method
- Map service errors to HTTP status codes + error envelopes
- Write the HTTP response using `pkg/response` helpers

Handlers must **not** contain:
- SQL queries
- Business rules
- State transition logic
- Payment provider calls

### Service (`internal/service/`)

The service layer owns all **business logic**.

Responsibilities:
- Business rule validation (supported currency, supported payment method)
- Duplicate detection (`merchant_order_id` uniqueness)
- Transaction state machine enforcement (`CanTransitionTo`, `IsCancellable`)
- Orchestrating repository writes and payment provider calls
- Atomicity strategy (see `docs/flow.md`)
- Translating domain errors into service-level sentinels

Services depend on **interfaces** (not concrete types) for both repositories
and payment providers, enabling unit testing without a real database.

### Repository (`internal/repository/`)

Repositories are **data access objects** — thin wrappers around SQL.

Responsibilities:
- Parameterised SQL queries (INSERT / SELECT / UPDATE)
- Scanning result rows into domain models
- Returning sentinel errors (`ErrTransactionNotFound`, `ErrDuplicateOrder`)
- Enforcing merchant isolation at the SQL level (`WHERE merchant_id = $2`)

Repositories must **not** contain:
- Business logic
- State transition validation
- Payment provider calls

### Security audit (`internal/audit/` and `internal/service/audit_service.go`)

Phase 8D.5 uses one append-only `AuditService`/`AuditLogRepository` pair.
Services attach an explicit, validated event to the request context; repositories
that own a PostgreSQL transaction insert that event before `COMMIT`. This keeps
the Phase 8D.4 merchant/OWNER lock and Phase 8D.3 credential transitions
transactionally consistent. The repository has no update/delete operation, and
tenant-scoped reads always bind `merchant_id`. Failed shared-key admin
authentication uses the separate best-effort path and never changes the auth
response. See `docs/phase-8d5-security-audit.md` for the event, metadata, and
retention policies.

#### Payment Listing SQL Design

The listing query is constructed by `buildListQuery` in `transaction_repository.go`.
Key safety properties:

1. `merchant_id` is always bound as `$1` — never derived from client input.
2. Each optional filter is appended as a positional parameter (`$2`, `$3`, …) — no string interpolation of user values.
3. `ORDER BY` is fixed to `created_at DESC, id DESC` — not client-controlled. The `id DESC` tie-breaker ensures deterministic results when multiple transactions share the same `created_at`.
4. `LIMIT` and `OFFSET` are the last two bound parameters.
5. A separate `COUNT(*)` query reuses the exact same `WHERE` clause so `total` and `total_pages` are always accurate.
6. Composite indexes from migration `000009` directly support both the unfiltered and status-filtered listing paths.

### Payment Provider Abstraction (`internal/service/payment_provider.go`)

The `PaymentProvider` interface abstracts external payment processors.

The runtime factory selects either `MockPaymentProvider` or `MidtransProvider`.
The latter owns all Snap HTTP, Basic-auth, response translation, and timeout
handling; `PaymentService` remains provider-neutral.

**Why does this abstraction exist?**

Without the interface, `PaymentService` would be directly coupled to `MockPaymentProvider`.
Adding a real QRIS provider in Phase 3 would require modifying `PaymentService` itself — 
violating the Open/Closed Principle and making tests harder.

With the interface:
- `PaymentService` depends on the **contract**, not the implementation
- Each new provider (QRIS, VA, e-wallet) is a new type that implements the interface
- `PaymentService` requires **zero changes** when adding providers
- Tests can inject any implementation including failure scenarios

```go
type PaymentProvider interface {
    Name() string
    CreatePayment(ctx, ProviderCreateRequest) (*ProviderPaymentResponse, error)
    GetPayment(ctx, providerTransactionID) (*ProviderPaymentResponse, error)
    CancelPayment(ctx, providerTransactionID) error
}
```

### Mock Payment Provider (`internal/service/mock_payment_provider.go`)

`MockPaymentProvider` implements `PaymentProvider` for Phase 2 development and testing.

- **Never makes external HTTP calls**
- Generates `MOCK-TXN-{hex8}` provider transaction IDs
- Generates `{FRONTEND_PUBLIC_URL}/pay/MOCK-TXN-{hex32}` payment URLs; the
  backend config supplies the browser-facing frontend origin, not its API port
- Sets `expired_at` to 30 minutes in the future
- Configurable failure modes via struct fields:
  - `ShouldFailCreate` — makes `CreatePayment` return `ErrProviderFailure`
  - `ShouldFailCancel` — makes `CancelPayment` return `ErrProviderFailure`
  - `ShouldTimeout` — makes all methods return `ErrProviderTimeout`
- No global state — each test creates its own instance

---

## Dependency Injection

All dependencies are wired in `cmd/server/main.go`:

```
pgxpool.Pool
    └─► MerchantRepository  (interface)
    │       └─► MerchantService  (interface)
    │               └─► MerchantHandler
    │               └─► Auth Middleware
    │
    └─► TransactionRepository       (interface)
    └─► PaymentAttemptRepository    (interface)
    └─► IdempotencyKeyRepository    (interface)

MockPaymentProvider  (implements PaymentProvider interface)
    │
    ▼
PaymentService(TransactionRepository, PaymentAttemptRepository, IdempotencyKeyRepository, PaymentProvider)
    │
    ▼
PaymentHandler
```

No global state. No `init()` side effects. Every dependency is explicit.

### Payment idempotency boundary

The idempotency repository uses `merchant_id + key` for every lookup. Its
atomic `Reserve` operation inserts a `PROCESSING` row or replaces an expired
row; an active unique conflict is read back and interpreted by the service.
`COMPLETED` stores the business response as JSONB. `FAILED` stores a stable
error code and is replayed, including timeout failures, because the provider
may have created an external payment before the gateway timed out.

---

## Package Structure

```
pay-gate-backend/
├── cmd/server/           main.go — wiring, HTTP server, graceful shutdown
├── internal/
│   ├── config/           env-based configuration
│   ├── handler/          HTTP handlers + binding helpers
│   ├── middleware/        RequestID, Auth
│   ├── model/            domain models + DTOs + state machine
│   ├── repository/        DB access interfaces + pgx implementations
│   └── service/           business logic + PaymentProvider interface + Mock
├── pkg/
│   ├── database/          pgxpool setup + Ping helper
│   └── response/          JSON envelope helpers + error codes
├── migrations/            golang-migrate SQL files
├── swagger/               generated OpenAPI docs (docs.go, swagger.json, swagger.yaml)
└── docs/                  Markdown documentation
```

---

## Design Decisions

### Why not use an ORM?

Raw SQL with `pgx` gives full control over queries, avoids N+1 issues,
and makes the merchant-isolation `WHERE merchant_id = $2` constraint explicit.

### Why no global DB transaction across the provider call?

External HTTP calls can be slow (seconds). Holding a DB connection/transaction
open for that duration would exhaust the pool under load.
The chosen approach (persist → call → persist result → update) is documented
in `docs/flow.md` with compensation logic for failure cases.

### Why conditional UPDATE (`WHERE status = $from`)?

Acts as an optimistic lock. Prevents concurrent requests from both succeeding
on the same state transition without requiring `SELECT FOR UPDATE` overhead
on every read.

### Why 404 for cross-merchant access instead of 403?

Returning 403 would confirm that the resource exists — leaking information
(Merchant B learns that Merchant A's transaction ID is valid).
404 is always returned for "not found or not yours".

### Why store payment_url and provider_transaction_id on the transaction row?

`GET /api/v1/payments/:id` must return these fields. Storing them directly on
the transaction row means a single indexed lookup — no join to `payment_attempts`.

---

## Future Extension Points (Phase 3+)

| Feature | Extension point |
|---------|----------------|
| Real QRIS provider | Implement `PaymentProvider`, register in `main.go` |
| Virtual Account | Implement `PaymentProvider` |
| E-Wallet | Implement `PaymentProvider` |
| Webhook callbacks | Add `WebhookHandler` + `UpdateTransactionStatus` repo method |
| PAID/EXPIRED transitions | Add polling job or webhook receiver |
| Retry queue | Wrap `PaymentProvider` calls in a retry decorator |
| Redis caching | Add cache layer between `Auth` middleware and `MerchantRepository` |
| Rate limiting | Add Gin middleware before `Auth` |
| Settlement | Admin import service + evidence tables; reconciliation is explicit and never changes payment/refund truth |
| Refund | New service + DB table, from PAID state |
| Transactional email | `EmailSender` interface with SMTP + no-op implementations (Phase 8C.1); invitation creation renders the email and commits it into the `email_outbox` transactional outbox **in the same transaction** as the invitation row (Phase 8C.3A) — invitation exists ⇔ queued email exists, and **SMTP is not on the request critical path**. Two independent in-process workers now own `email_outbox`, with **disjoint state machines**: `EmailOutboxWorker` + dispatcher (Phase 8C.3B, structural mirror of the merchant webhook worker) owns `PENDING`/`PROCESSING` — claims due rows with `FOR UPDATE SKIP LOCKED`, recovers stale PROCESSING rows, classifies permanent (SMTP 5xx / invalid message) vs retryable failures, retries on the shared webhook backoff+jitter schedule, and dead-letters at max attempts — **at-least-once** delivery (duplicate SMTP submissions possible after a crash); `EmailOutboxCleanupWorker` (Phase 8C.3C, `ExpiryWorker` pattern, `EMAIL_CLEANUP_ENABLED` opt-in, default off) owns **terminal retention only** — a bounded batched full-row `DELETE` (never a giant scan: `EMAIL_CLEANUP_BATCH_SIZE` per call, drained to zero with a hard per-run batch cap) of `SENT` rows older than **7 days** (`sent_at`, strict `<`) and `DEAD` rows older than **30 days** (`updated_at`, strict `<`), on an `EMAIL_CLEANUP_INTERVAL` ticker with an immediate first run. Cleanup **never** touches `PENDING` or `PROCESSING` regardless of age (stale recovery stays exclusively with the delivery worker) and **never** touches `merchant_user_invitations` — the invitation table remains the sole authority for invitation validity, and `email_outbox.reference_id` is a deliberate non-FK correlation id. Run logs carry counts/duration only (no recipient/subject/body/token) |
