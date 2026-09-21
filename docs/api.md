# API Reference

> **Phase 7B:** Refund endpoints are implemented. See [refunds.md](./refunds.md) for full documentation.
> **Phase 7C:** Settlement import and reconciliation admin endpoints are implemented. See [settlements.md](./settlements.md) and [reconciliation.md](./reconciliation.md).
> **Phase 8:** Dashboard authentication and user management. See [phase-8-dashboard-auth-api.md](./phase-8-dashboard-auth-api.md) and [phase-8-auth-design.md](./phase-8-auth-design.md).
> **Phase 9:** Merchant-scoped Dashboard APIs (Bearer JWT). See [phase-9-dashboard-api.md](./phase-9-dashboard-api.md).

All endpoints follow the standard response envelope:

```json
// Success
{ "success": true, "data": {}, "meta": { "request_id": "req_xxx" } }

// Error
{ "success": false, "error": { "code": "ERROR_CODE", "message": "..." }, "meta": { "request_id": "req_xxx" } }
```

Every request receives an `X-Request-ID` response header.
Supply `X-Request-ID` in the incoming request to use your own trace ID.

## Phase 7C Admin Operations

These endpoints use `X-Admin-Key` from `ADMIN_API_KEY`; merchant API keys are not accepted. When the key is not configured they return `ADMIN_NOT_CONFIGURED`.

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/v1/admin/settlements/import` | Import mock/provider-normalized settlement evidence; replay is idempotent on provider, reference, and canonical payload hash |
| GET | `/api/v1/admin/settlements` | Paginated settlement list |
| GET | `/api/v1/admin/settlements/:id` | Settlement header and items |
| POST | `/api/v1/admin/settlements/:id/reconcile` | Run or rerun reconciliation |
| GET | `/api/v1/admin/settlements/:id/reconciliation` | Paginated results for one settlement |
| GET | `/api/v1/admin/reconciliation/mismatches` | Paginated mismatch/unmatched results |
| GET | `/api/v1/admin/reconciliation/mismatches/:id` | One audit result |
| POST | `/api/v1/admin/merchants/:merchant_id/users` | Bootstrap a dashboard user (Phase 8) |

## Phase 8 Dashboard Auth

Dashboard login uses **email + password**, not merchant API keys. SPA contract: [phase-8-dashboard-auth-api.md](./phase-8-dashboard-auth-api.md).

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST | `/api/v1/auth/login` | — | Issue JWT + HttpOnly refresh cookie |
| POST | `/api/v1/auth/logout` | refresh cookie | Revoke session |
| POST | `/api/v1/auth/refresh` | refresh cookie | Rotate session / new access token |
| GET | `/api/v1/auth/me` | Bearer JWT | Current dashboard user |
| GET | `/api/v1/dashboard/users` | Bearer JWT | List users in caller's merchant |
| PATCH | `/api/v1/dashboard/users/:user_id/status` | Bearer JWT + OWNER | Enable/disable user |

---

## Authentication

Protected merchant API endpoints require the `X-API-Key` header.

### Phase 5C compound API keys (preferred)

```
X-API-Key: pk_<hex>:sk_<hex>
```

Created via `POST /api/v1/merchants/{id}/api-keys`. The plaintext
`secret` is returned **only** on create/rotate. See
[api-key-lifecycle.md](./api-key-lifecycle.md).

### Legacy Phase 1 credentials (still supported)

```
X-API-Key: pk_<hex>
```

Obtained when creating a merchant via `POST /api/v1/merchants`. Bare legacy
keys (no `:sk_` segment) continue to work; they are not invalidated by Phase 5C.

---

## Health

### GET /health

Liveness check — always returns 200 when the server is running.

**Response 200**
```json
{ "success": true, "data": { "status": "ok" }, "meta": { "request_id": "req_xxx" } }
```

---

### GET /health/ready

Readiness check — verifies PostgreSQL connectivity.

**Response 200** — database reachable
```json
{ "success": true, "data": { "status": "ok" }, "meta": { "request_id": "req_xxx" } }
```

**Response 503** — database unreachable
```json
{ "success": false, "error": { "code": "DATABASE_UNAVAILABLE", "message": "Database is not reachable" }, "meta": { "request_id": "req_xxx" } }
```

---

## Merchants

### POST /api/v1/merchants

Register a new merchant. Returns an API key for subsequent calls.
The API secret is **not** returned after this call and is never stored in plain text.

**Request**
```json
{
  "name": "Demo Merchant",
  "code": "DEMO001"
}
```

| Field | Type | Rules |
|-------|------|-------|
| name  | string | required, 2–150 chars |
| code  | string | required, 2–50 chars, unique |

**Response 201**
```json
{
  "success": true,
  "data": {
    "id": "14a49e45-bb31-4abe-98b3-ee38ff002268",
    "name": "Demo Merchant",
    "code": "DEMO001",
    "api_key": "pk_f139fbe7...",
    "status": "ACTIVE",
    "created_at": "2026-09-11T02:56:24Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — validation error
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "details": { "code": "This field is required" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 409** — duplicate code
```json
{
  "success": false,
  "error": { "code": "DUPLICATE_MERCHANT_CODE", "message": "Merchant code already exists" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl example**
```bash
curl -s -X POST http://localhost:8080/api/v1/merchants \
  -H "Content-Type: application/json" \
  -d '{"name":"Demo Merchant","code":"DEMO001"}'
```

---

### GET /api/v1/merchants/:id

Retrieve merchant details. Does **not** return `api_key` or `api_secret`.

**Response 200**
```json
{
  "success": true,
  "data": {
    "id": "14a49e45-bb31-4abe-98b3-ee38ff002268",
    "name": "Demo Merchant",
    "code": "DEMO001",
    "status": "ACTIVE",
    "created_at": "2026-09-11T02:56:24Z",
    "updated_at": "2026-09-11T02:56:24Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 404**
```json
{
  "success": false,
  "error": { "code": "MERCHANT_NOT_FOUND", "message": "Merchant not found" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl example**
```bash
curl -s http://localhost:8080/api/v1/merchants/14a49e45-bb31-4abe-98b3-ee38ff002268
```

---

## Payments

All payment endpoints require `X-API-Key` authentication.
The default provider is **MOCK** — no real payment is processed unless `PAYMENT_PROVIDER=midtrans` is set.

### GET /api/v1/payments

List payments for the authenticated merchant. Results are always scoped to the
merchant identified by the `X-API-Key` header — the merchant identity is **never**
accepted from a query parameter.

**Authentication:** `X-API-Key` required

**Ordering:** Fixed `created_at DESC, id DESC` for deterministic pagination when
multiple transactions share the same timestamp.

**Date range semantics:** Half-open interval `[created_from, created_to)`:
- `created_at >= created_from` (inclusive lower bound)
- `created_at < created_to` (exclusive upper bound)

**Page beyond last page:** Returns an empty `data` array with accurate `total`
and `total_pages`. This is not an error.

**Query Parameters**

| Parameter | Type | Default | Max | Description |
|-----------|------|---------|-----|-------------|
| `page` | integer | 1 | — | Page number, must be ≥ 1 |
| `limit` | integer | 20 | 100 | Items per page, must be 1–100 |
| `status` | string | — | — | Filter by status: `CREATED` \| `PENDING` \| `PAID` \| `FAILED` \| `EXPIRED` \| `CANCELLED` |
| `merchant_order_id` | string | — | — | Exact match on `merchant_order_id` |
| `payment_method` | string | — | — | Filter by payment method (e.g. `QRIS`) |
| `created_from` | RFC3339 | — | — | Lower bound on `created_at` (inclusive) |
| `created_to` | RFC3339 | — | — | Upper bound on `created_at` (exclusive) |

**Response 200** — with results
```json
{
  "success": true,
  "data": [
    {
      "transaction_id": "0199a6c4-7e2a-7abc-8c8f-123456789abc",
      "merchant_order_id": "ORDER-001",
      "amount": 50000,
      "currency": "IDR",
      "payment_method": "QRIS",
      "provider": "MOCK",
      "provider_transaction_id": "MOCK-TXN-a1b2c3d4",
      "payment_url": "https://mock-payment.local/pay/MOCK-TXN-a1b2c3d4",
      "status": "PAID",
      "expired_at": "2026-09-11T10:30:00Z",
      "paid_at": "2026-09-11T10:15:00Z",
      "created_at": "2026-09-11T10:00:00Z",
      "updated_at": "2026-09-11T10:15:00Z"
    }
  ],
  "meta": {
    "request_id": "req_xxx",
    "page": 1,
    "limit": 20,
    "total": 1,
    "total_pages": 1
  }
}
```

**Response 200** — empty result (valid filter, no matching rows)
```json
{
  "success": true,
  "data": [],
  "meta": {
    "request_id": "req_xxx",
    "page": 1,
    "limit": 20,
    "total": 0,
    "total_pages": 0
  }
}
```

**Response 400** — invalid pagination
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "details": { "limit": "must be an integer between 1 and 100" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid status
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "details": { "status": "must be one of: CREATED, PENDING, PAID, FAILED, EXPIRED, CANCELLED" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid date range (`created_from >= created_to`)
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "details": { "created_from": "created_from must be before created_to" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 401** — missing or invalid API key
```json
{
  "success": false,
  "error": { "code": "INVALID_API_KEY", "message": "API key is required" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl examples**
```bash
# Default pagination
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments"

# Page 2, 10 items per page
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?page=2&limit=10"

# Filter by status
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?status=PAID"

# Filter by payment method
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?payment_method=QRIS"

# Filter by order ID (exact match)
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?merchant_order_id=ORDER-001"

# Date range (January 2026)
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?created_from=2026-01-01T00:00:00Z&created_to=2026-02-01T00:00:00Z"

# Combined filters
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments?status=PAID&payment_method=QRIS&page=1&limit=5"
```

---

### POST /api/v1/payments

Create a new payment transaction. On success the transaction moves to `PENDING`
and a `payment_url` is returned for the customer to complete payment.

**Authentication:** `X-API-Key` required

**Idempotency:** `Idempotency-Key` header required (1–255 characters, scoped to authenticated merchant).
Supply the same key with the same payload to replay the stored result without creating a
duplicate transaction or calling the provider again. See the idempotency behaviour table below.

**Idempotency behaviour**

| Scenario | Response |
|----------|----------|
| First request | `201 Created` — transaction created, provider called once |
| Same key + same payload | `201 Created` — stored result replayed, no second provider call |
| Same key + different payload | `409 IDEMPOTENCY_KEY_REUSED` — original record unchanged |
| Same key + concurrent request in flight | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` |
| Key expired (`IDEMPOTENCY_TTL` elapsed) | Treated as a new key — fresh transaction created |
| Provider timed out (FAILED replay) | `504 PAYMENT_PROVIDER_TIMEOUT` — not retried automatically |

**Request**
```json
{
  "merchant_order_id": "ORDER-001",
  "amount": 50000,
  "currency": "IDR",
  "payment_method": "QRIS"
}
```

| Field | Type | Rules |
|-------|------|-------|
| merchant_order_id | string | required, 1–100 chars, unique per merchant |
| amount | integer (int64) | required, > 0, smallest currency unit (e.g. IDR in rupiah) |
| currency | string | required, exactly 3 chars, must be `IDR` |
| payment_method | string | required, must be `QRIS` |

**Response 201**
```json
{
  "success": true,
  "data": {
    "transaction_id": "0199a6c4-7e2a-7abc-8c8f-123456789abc",
    "merchant_order_id": "ORDER-001",
    "amount": 50000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "provider_transaction_id": "MOCK-TXN-a1b2c3d4",
    "status": "PENDING",
    "payment_url": "https://mock-payment.local/pay/MOCK-TXN-a1b2c3d4",
    "expired_at": "2026-09-11T10:30:00Z",
    "created_at": "2026-09-11T10:00:00Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid currency
```json
{
  "success": false,
  "error": { "code": "INVALID_CURRENCY", "message": "Currency not supported. Supported: IDR" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid payment method
```json
{
  "success": false,
  "error": { "code": "INVALID_PAYMENT_METHOD", "message": "Payment method not supported. Supported: QRIS" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — validation error (missing field, zero/negative amount)
```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Validation failed",
    "details": { "amount": "Value is too short (minimum 1 characters)" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 401** — missing or invalid API key
```json
{
  "success": false,
  "error": { "code": "INVALID_API_KEY", "message": "Invalid API key" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 401** — inactive merchant
```json
{
  "success": false,
  "error": { "code": "MERCHANT_INACTIVE", "message": "Merchant account is not active" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 409** — duplicate order ID
```json
{
  "success": false,
  "error": { "code": "DUPLICATE_ORDER", "message": "An order with this merchant_order_id already exists" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 409** — same `Idempotency-Key`, different request payload
```json
{
  "success": false,
  "error": { "code": "IDEMPOTENCY_KEY_REUSED", "message": "Idempotency-Key was already used with a different request" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 409** — same `Idempotency-Key`, original request still in progress
```json
{
  "success": false,
  "error": { "code": "IDEMPOTENCY_REQUEST_IN_PROGRESS", "message": "A request with this Idempotency-Key is already in progress" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 502** — provider error
```json
{
  "success": false,
  "error": { "code": "PAYMENT_PROVIDER_ERROR", "message": "Payment provider failed to process the request" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 504** — provider timeout
```json
{
  "success": false,
  "error": { "code": "PAYMENT_PROVIDER_TIMEOUT", "message": "Payment provider did not respond in time" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl example**
```bash
curl -s -X POST http://localhost:8080/api/v1/payments \
  -H "Content-Type: application/json" \
  -H "X-API-Key: pk_f139fbe7..." \
  -d '{
    "merchant_order_id": "ORDER-001",
    "amount": 50000,
    "currency": "IDR",
    "payment_method": "QRIS"
  }'
```

---

### GET /api/v1/payments/:id

Retrieve payment details including `payment_url` and `provider_transaction_id`.
Returns 404 if the transaction does not exist **or** belongs to a different merchant
(no information leakage via 403).

**Authentication:** `X-API-Key` required

**Response 200**
```json
{
  "success": true,
  "data": {
    "transaction_id": "0199a6c4-7e2a-7abc-8c8f-123456789abc",
    "merchant_order_id": "ORDER-001",
    "amount": 50000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "provider_transaction_id": "MOCK-TXN-a1b2c3d4",
    "status": "PENDING",
    "payment_url": "https://mock-payment.local/pay/MOCK-TXN-a1b2c3d4",
    "expired_at": "2026-09-11T10:30:00Z",
    "created_at": "2026-09-11T10:00:00Z",
    "updated_at": "2026-09-11T10:00:00Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid UUID
```json
{
  "success": false,
  "error": { "code": "INVALID_REQUEST", "message": "Invalid transaction ID format" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 401** — missing or invalid API key
```json
{
  "success": false,
  "error": { "code": "INVALID_API_KEY", "message": "API key is required" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 404** — not found or cross-merchant
```json
{
  "success": false,
  "error": { "code": "TRANSACTION_NOT_FOUND", "message": "Transaction not found" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl example**
```bash
curl -s \
  -H "X-API-Key: pk_f139fbe7..." \
  http://localhost:8080/api/v1/payments/0199a6c4-7e2a-7abc-8c8f-123456789abc
```

---

### POST /api/v1/payments/:id/cancel

Cancel a payment. Only `CREATED` and `PENDING` transactions can be cancelled.

- For `CREATED` transactions: cancelled immediately (no provider call needed).
- For `PENDING` transactions: provider is asked to cancel first. If provider
  refuses, the transaction **remains in its current state** and 502 is returned.

**Authentication:** `X-API-Key` required

**Allowed states:** `CREATED`, `PENDING`

**Response 200**
```json
{
  "success": true,
  "data": {
    "transaction_id": "0199a6c4-7e2a-7abc-8c8f-123456789abc",
    "merchant_order_id": "ORDER-001",
    "amount": 50000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "status": "CANCELLED",
    "created_at": "2026-09-11T10:00:00Z",
    "updated_at": "2026-09-11T10:05:00Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 400** — invalid UUID
```json
{
  "success": false,
  "error": { "code": "INVALID_REQUEST", "message": "Invalid transaction ID format" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 401** — missing or invalid API key
```json
{
  "success": false,
  "error": { "code": "INVALID_API_KEY", "message": "API key is required" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 404** — not found or cross-merchant
```json
{
  "success": false,
  "error": { "code": "TRANSACTION_NOT_FOUND", "message": "Transaction not found" },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 409** — invalid state (PAID, FAILED, EXPIRED, or already CANCELLED)
```json
{
  "success": false,
  "error": {
    "code": "INVALID_TRANSACTION_STATE",
    "message": "Transaction cannot be cancelled in its current state",
    "details": { "requested_action": "CANCEL" }
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Response 502** — provider cancel failure (transaction not changed)
```json
{
  "success": false,
  "error": { "code": "PAYMENT_PROVIDER_ERROR", "message": "Payment provider failed to process the request" },
  "meta": { "request_id": "req_xxx" }
}
```

**curl example**
```bash
curl -s -X POST \
  -H "X-API-Key: pk_f139fbe7..." \
  http://localhost:8080/api/v1/payments/0199a6c4-7e2a-7abc-8c8f-123456789abc/cancel
```

---

## Refunds (Phase 7B)

Full documentation in [refunds.md](./refunds.md).

### POST /api/v1/payments/:id/refunds

Create a partial or full refund for a PAID transaction.

**Auth:** `X-API-Key` required.

**Header:** `Idempotency-Key` required (1–255 chars).

**Request body:**
```json
{
  "amount": 30000,
  "currency": "IDR",
  "reason": "customer_request"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `amount` | int64 | Yes | Must be > 0; in smallest currency unit (e.g. IDR rupiah) |
| `currency` | string | Yes | Must match transaction currency |
| `reason` | string | No | Max 500 chars |

**Response 201:**
```json
{
  "success": true,
  "data": {
    "refund_id": "uuid",
    "transaction_id": "uuid",
    "amount": 30000,
    "currency": "IDR",
    "status": "SUCCEEDED",
    "provider": "MOCK",
    "provider_refund_id": "MOCK-REF-abc123",
    "reason": "customer_request",
    "refunded_amount": 30000,
    "reserved_refund_amount": 0,
    "refundable_amount": 70000,
    "transaction_amount": 100000,
    "requested_at": "2026-09-15T01:00:00Z",
    "succeeded_at": "2026-09-15T01:00:01Z",
    "created_at": "2026-09-15T01:00:00Z",
    "updated_at": "2026-09-15T01:00:01Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

**Error codes specific to refunds:**

| Code | HTTP | When |
|------|------|------|
| `REFUND_TRANSACTION_NOT_PAID` | 400 | Transaction is not PAID |
| `REFUND_AMOUNT_EXCEEDED` | 400 | Amount > refundable balance |
| `REFUND_CURRENCY_MISMATCH` | 400 | Currency ≠ transaction currency |
| `REFUND_PROVIDER_ERROR` | 502 | Provider rejected refund |
| `REFUND_PROVIDER_TIMEOUT` | 504 | Provider timeout; reservation held |

**curl example:**
```bash
curl -s -X POST \
  -H "X-API-Key: pk_f139fbe7..." \
  -H "Idempotency-Key: refund-order-001-attempt-1" \
  -H "Content-Type: application/json" \
  -d '{"amount":30000,"currency":"IDR","reason":"customer_request"}' \
  "http://localhost:8080/api/v1/payments/PAYMENT_UUID/refunds"
```

---

### GET /api/v1/refunds/:id

Retrieve a refund by its UUID.

**Auth:** `X-API-Key` required.

Returns 404 for both missing refunds and refunds belonging to another merchant.

**curl example:**
```bash
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/refunds/REFUND_UUID"
```

---

### GET /api/v1/payments/:id/refunds

List refunds for a payment. Merchant-scoped.

**Auth:** `X-API-Key` required.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `page` | int | 1 | Page number (min 1) |
| `limit` | int | 20 | Items per page (max 100) |
| `status` | string | — | `PENDING` \| `PROCESSING` \| `SUCCEEDED` \| `FAILED` |

**Ordering:** `created_at DESC, id DESC`

**curl example:**
```bash
curl -s -H "X-API-Key: pk_f139fbe7..." \
  "http://localhost:8080/api/v1/payments/PAYMENT_UUID/refunds?status=SUCCEEDED"
```

---

## Standard Error Codes

| Code | HTTP | Description |
|------|------|-------------|
| INVALID_REQUEST | 400 | Malformed request (e.g. invalid UUID format, missing/oversized Idempotency-Key) |
| VALIDATION_ERROR | 400 | Request body fails struct binding validation |
| INVALID_AMOUNT | 400 | Amount ≤ 0 |
| INVALID_CURRENCY | 400 | Unsupported currency (only IDR in Phase 2) |
| INVALID_PAYMENT_METHOD | 400 | Unsupported payment method (only QRIS in Phase 2) |
| UNAUTHORIZED | 401 | Generic authentication failure |
| INVALID_API_KEY | 401 | Missing or invalid X-API-Key header |
| MERCHANT_INACTIVE | 401 | Merchant account is not ACTIVE |
| FORBIDDEN | 403 | Authenticated but not authorised |
| MERCHANT_NOT_FOUND | 404 | No merchant with given ID |
| TRANSACTION_NOT_FOUND | 404 | No transaction, or belongs to another merchant |
| REFUND_NOT_FOUND | 404 | No refund with given ID, or belongs to another merchant |
| DUPLICATE_ORDER | 409 | merchant_order_id already exists for this merchant |
| DUPLICATE_MERCHANT_CODE | 409 | Merchant code already registered |
| INVALID_TRANSACTION_STATE | 409 | State transition not allowed |
| IDEMPOTENCY_KEY_REUSED | 409 | Same Idempotency-Key submitted with a different request payload |
| IDEMPOTENCY_REQUEST_IN_PROGRESS | 409 | A request with this Idempotency-Key is still being processed |
| REFUND_TRANSACTION_NOT_PAID | 400 | Refund attempted on non-PAID transaction |
| REFUND_AMOUNT_EXCEEDED | 400 | Refund amount exceeds refundable balance |
| REFUND_CURRENCY_MISMATCH | 400 | Refund currency does not match transaction currency |
| REFUND_PROVIDER_ERROR | 502 | Provider returned an error for the refund |
| REFUND_PROVIDER_TIMEOUT | 504 | Provider did not respond for the refund |
| PAYMENT_PROVIDER_ERROR | 502 | Provider returned an error |
| PAYMENT_PROVIDER_TIMEOUT | 504 | Provider did not respond in time (result stored; replay returns same 504) |
| INTERNAL_ERROR | 500 | Unexpected server error (details never exposed) |

For Midtrans, configure the notification URL as
`POST /api/v1/webhooks/providers/midtrans`. Its body carries Midtrans'
`signature_key`; no provider credential is exposed by this API.
| INTERNAL_ERROR | 500 | Unexpected server error (details never exposed) |

---

## Idempotency

`POST /api/v1/payments` requires `Idempotency-Key` (1–255 characters). The
key is scoped by authenticated `merchant_id`, and the request hash includes
all payment business fields (`merchant_order_id`, `amount`, `currency`,
`payment_method`). PostgreSQL reserves the key before provider execution
using the unique `(merchant_id, key)` constraint.

| Scenario | Result |
|----------|--------|
| Same key + same payload | `201` — stored result replayed; no second transaction or provider call |
| Same key + different payload | `409 IDEMPOTENCY_KEY_REUSED` |
| Same key, concurrent request in flight | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` |
| Provider failure or timeout | `502`/`504` stored and replayed; never retried automatically |
| Key expired (`IDEMPOTENCY_TTL`, default `24h`) | Treated as a new key; fresh payment created |

See [docs/flow.md §8–11](flow.md) for the full state machine, replay sequence
diagrams, concurrent request flow, and provider timeout semantics.

---

## Merchant Outbound Webhooks (Phase 6)

Distinct from inbound provider webhooks. Full reference:
[merchant-webhooks.md](./merchant-webhooks.md).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/merchants/{id}/webhook` | Yes | Create/replace endpoint; returns signing secret once |
| GET | `/api/v1/merchants/{id}/webhook` | Yes | Get config (never includes secret) |
| POST | `/api/v1/merchants/{id}/webhook/rotate` | Yes | Rotate signing secret |
| DELETE | `/api/v1/merchants/{id}/webhook` | Yes | Soft-disable (`DISABLED`) |
| GET | `/api/v1/merchants/{id}/webhook/deliveries` | Yes | List delivery audit records |
| GET | `/api/v1/merchants/{id}/webhook/deliveries/{delivery_id}` | Yes | Get one delivery |
| POST | `/api/v1/merchants/{id}/webhook/deliveries/{delivery_id}/retry` | Yes | Manual retry |

Cross-merchant access returns **403 FORBIDDEN**.
