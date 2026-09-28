# Phase 9 — Dashboard API Contract

This document is the authoritative contract for the React TypeScript dashboard
resource APIs (Phase 9). Auth endpoints remain documented in
[`phase-8-dashboard-auth-api.md`](./phase-8-dashboard-auth-api.md).

**Base URL:** `http://localhost:8081` when using the repository's Docker Compose
bridge (`8081` maps to the container's `8080`); local non-Docker runs use
`http://localhost:8080`. Production uses the configured HTTPS public origin.

---

## Authentication

All `/api/v1/dashboard/*` endpoints require:

```http
Authorization: Bearer <access-token>
```

- **Do not** use Merchant API Keys (`X-API-Key`) for dashboard calls.
- On `401` (expired access token), call `POST /api/v1/auth/refresh` with
  `credentials: 'include'`, then retry the original request with the new access token.
- Refresh token remains an **HttpOnly cookie** — never send it in `Authorization`.

### Merchant isolation

`merchant_id` is always taken from the authenticated dashboard user (JWT → DB user).

Never send merchant identity via:

- query parameter
- request body
- custom header
- path parameter (except resource IDs that are then ownership-checked)

Cross-merchant resource access returns **404** (not 403) to avoid existence leaks.

### Response envelope

```json
{
  "success": true,
  "data": {},
  "meta": {
    "request_id": "req_...",
    "page": 1,
    "limit": 20,
    "total": 0,
    "total_pages": 0
  }
}
```

Pagination fields appear only on list endpoints (`response.List`).

Amounts are **integer minor units** (e.g. IDR rupiah). Never floats.

Timestamps are **UTC RFC3339**.

---

## Role matrix

| Operation | OWNER | ADMIN | VIEWER |
|---|---|---|---|
| Overview | yes | yes | yes |
| Payments read / detail | yes | yes | yes |
| Create payment | yes | yes | **no** |
| Refunds read | yes | yes | yes |
| Create refund | yes | yes | **no** |
| Webhook deliveries read | yes | yes | yes |
| Webhook config mutate / retry | yes | yes | **no** |
| Reconciliation mismatches read | yes | yes | yes |
| API keys read | yes | yes | yes |
| API keys create / revoke | yes | yes | **no** |
| Users list | yes | yes | yes |
| User status mutation | yes | **no** | **no** |
| Merchant settings read | yes | yes | yes |

Insufficient role → `403 INSUFFICIENT_ROLE`.

---

## Endpoints

### GET /api/v1/dashboard/overview

**Auth:** Bearer JWT  
**Roles:** OWNER, ADMIN, VIEWER

**Query:**

| Param | Required | Description |
|---|---|---|
| `from` | no | Period start (RFC3339 or `YYYY-MM-DD` UTC). Must be paired with `to`. |
| `to` | no | Period end (exclusive). Half-open `[from, to)`. |

**Default period:** current UTC calendar month `[month_start, next_month)`.

**Response `data`:**

```json
{
  "period": { "from": "2026-09-01T00:00:00Z", "to": "2026-10-01T00:00:00Z" },
  "payments": {
    "total": 0,
    "created": 0,
    "pending": 0,
    "paid": 0,
    "failed": 0,
    "expired": 0,
    "cancelled": 0
  },
  "revenue": { "amount": 0, "currency": "IDR" },
  "refunds": { "count": 0, "amount": 0, "currency": "IDR" },
  "recent_payments": []
}
```

**Semantics:**

- Payment counts: `transactions` where `created_at ∈ [from, to)`, grouped by status.
- Revenue (financial truth): `SUM(amount)` where `status = PAID` and `paid_at ∈ [from, to)`.
- Refunds: SUCCEEDED refunds where `COALESCE(succeeded_at, created_at) ∈ [from, to)`.
- `recent_payments`: last 10 payments for the merchant (not limited to period).
- Empty merchant → zeros / empty arrays (never fake demo numbers).

**Errors:** `400` invalid period, `401`, `500`.

---

### GET /api/v1/dashboard/payments

**Auth:** Bearer JWT  
**Roles:** OWNER, ADMIN, VIEWER

**Query:**

| Param | Default | Notes |
|---|---|---|
| `page` | 1 | ≥ 1 |
| `limit` | 20 | 1–100 |
| `status` | — | `CREATED\|PENDING\|PAID\|FAILED\|EXPIRED\|CANCELLED` |
| `merchant_order_id` | — | exact match |
| `payment_method` | — | e.g. `QRIS` |
| `search` | — | ILIKE on order ID / provider tx ID, or exact payment UUID |
| `created_from` | — | RFC3339 inclusive |
| `created_to` | — | RFC3339 exclusive |

**Ordering:** `created_at DESC, id DESC`.

**Response:** paginated `PaymentResponse[]` with `meta.page/limit/total/total_pages`.

---

### POST /api/v1/dashboard/payments

**Auth:** Bearer JWT
**Roles:** OWNER, ADMIN (VIEWER → 403)

**Headers:**

| Header | Required |
|---|---|
| `Idempotency-Key` | yes (1–255 chars) |
| `Content-Type` | `application/json` |

Creates a payment for the authenticated user's merchant. The body and
provider/idempotency semantics match the integration payment endpoint. In a
production-mode deployment the configured non-mock provider is used; mock
provider flows belong only to development/test environments. No provider
credential or raw provider response is returned.

---

### GET /api/v1/dashboard/payments/:payment_id

**Auth:** Bearer JWT  
**Roles:** OWNER, ADMIN, VIEWER

**Response `data`:** `PaymentResponse` plus:

```json
{
  "timeline": [
    { "type": "CREATED", "at": "..." },
    { "type": "PAID", "at": "..." }
  ]
}
```

Timeline uses **persisted timestamps only**:

- `CREATED` ← `created_at` (always)
- `PAID` ← `paid_at` (only when set)

No fabricated FAILED/EXPIRED/CANCELLED events (no event-history table yet).

Cross-merchant → `404 TRANSACTION_NOT_FOUND`.

---

### GET /api/v1/dashboard/refunds

**Auth:** Bearer JWT  
**Roles:** OWNER, ADMIN, VIEWER

**Query:** `page`, `limit`, `status` (`PENDING|PROCESSING|SUCCEEDED|FAILED`),
`payment_id`, `created_from`, `created_to`.

Merchant-wide list; optional `payment_id` filter.

---

### GET /api/v1/dashboard/refunds/:refund_id

**Auth:** Bearer JWT  
**Roles:** OWNER, ADMIN, VIEWER

Cross-merchant → `404 REFUND_NOT_FOUND`.

---

### POST /api/v1/dashboard/payments/:payment_id/refunds

**Auth:** Bearer JWT
**Roles:** OWNER, ADMIN (VIEWER → 403)

**Headers:**

| Header | Required |
|---|---|
| `Idempotency-Key` | yes (1–255 chars) |
| `Content-Type` | `application/json` |

**Body:** same as integration API:

```json
{ "amount": 10000, "currency": "IDR", "reason": "optional" }
```

Reuses existing `RefundService` (over-refund protection, FOR UPDATE, idempotency,
provider call outside DB tx, finalize + outbox). For a non-mock provider this
route currently returns `503` before reserving a local refund because a real
provider-specific refund adapter is not implemented. Do not duplicate the
client-side flow or treat this endpoint as production-complete for Midtrans.

---

### GET /api/v1/dashboard/webhooks

List outbound webhook deliveries (outbox audit). Paginated (`page`, `limit`).

Never includes signing secrets.

### GET /api/v1/dashboard/webhooks/:id

Delivery detail. Cross-merchant → 404.

### POST /api/v1/dashboard/webhooks/:id/retry

**Roles:** OWNER, ADMIN. Requeues FAILED/DEAD/PENDING via existing outbox logic.

---

### GET /api/v1/dashboard/webhook-config

Safe webhook configuration (URL, status, timestamps). **No secret.**

### PUT /api/v1/dashboard/webhook-config

**Roles:** OWNER, ADMIN. Upserts endpoint; returns plaintext signing secret **once**.

### POST /api/v1/dashboard/webhook-config/rotate

**Roles:** OWNER, ADMIN. New secret returned once.

### DELETE /api/v1/dashboard/webhook-config

**Roles:** OWNER, ADMIN. Soft-disable.

---

### GET /api/v1/dashboard/api-keys

Safe metadata only (`id`, `name`, `key_id`, `status`, timestamps). **No secret.**

### POST /api/v1/dashboard/api-keys

**Roles:** OWNER, ADMIN.

```json
{ "name": "Production", "expires_at": null }
```

Response includes `secret` **once only**. Store client-side immediately.

Credential format for integration APIs: `key_id:secret`.

### POST /api/v1/dashboard/api-keys/:id/revoke

**Roles:** OWNER, ADMIN.

---

### GET /api/v1/dashboard/reconciliation/mismatches

Merchant-scoped mismatches. `merchant_id` is forced from JWT (query `merchant_id` ignored).

### GET /api/v1/dashboard/reconciliation/mismatches/:id

Ownership-checked; cross-merchant → 404.

---

### GET /api/v1/dashboard/settings

Safe merchant profile: `id`, `name`, `code`, `status`, `created_at`, `updated_at`.

**No** API secrets, webhook secrets, passwords, or credentials.

`PATCH /settings` is **not implemented** (no merchant profile update service yet).

---

### Phase 8 user management (unchanged)

```http
GET   /api/v1/dashboard/users
PATCH /api/v1/dashboard/users/:user_id/status   # OWNER only
```

---

## Deferred / not implemented

| Endpoint | Reason |
|---|---|
| `GET /api/v1/dashboard/settlements` | Settlement batches have **no `merchant_id`** (provider-level). Exposing them would leak cross-merchant accounting evidence. Use reconciliation mismatches for merchant-scoped views. Import/reconcile remain admin-only. |
| `GET /api/v1/dashboard/settlements/:id` | Same as above. |
| `GET /api/v1/dashboard/reconciliation` (runs list) | Merchant-scoped runs not modelled; mismatches are the merchant-facing surface. |
| `PATCH /api/v1/dashboard/settings` | No merchant profile mutation service exists. |
| Rich payment timeline beyond CREATED/PAID | No event-history table; only real timestamps. |

---

## Frontend priority (React Phase 2)

Stable and ready:

```http
GET /api/v1/dashboard/overview
GET /api/v1/dashboard/payments
GET /api/v1/dashboard/payments/:payment_id
```

---

## Error codes (common)

| HTTP | Code | Meaning |
|---|---|---|
| 401 | `INVALID_CREDENTIALS` / `USER_DISABLED` | Missing/invalid/expired JWT or disabled user |
| 403 | `INSUFFICIENT_ROLE` | Role cannot perform mutation |
| 404 | `TRANSACTION_NOT_FOUND` / `REFUND_NOT_FOUND` / `RESOURCE_NOT_FOUND` | Missing or cross-merchant |
| 400 | `INVALID_REQUEST` / `VALIDATION_ERROR` | Bad query/body |
| 409 | Idempotency / conflict codes | Same as integration APIs |
| 500 | `INTERNAL_ERROR` | Unexpected |

Every response includes `meta.request_id` (also echoed as `X-Request-Id`).

---

## CORS

Dashboard SPA origin (dev): `http://localhost:5173` via `CORS_ALLOWED_ORIGINS`.

Credentials allowed with explicit origin match — never `*` with credentials.
