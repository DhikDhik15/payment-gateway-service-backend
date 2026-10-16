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
| Product image upload / remove | yes | yes | **no** |
| Product images read (public GET) | yes | yes | yes |
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
    "cancelled": 0,
    "unsuccessful_transactions": 0
  },
  "revenue": { "amount": 0, "currency": "IDR" },
  "refunds": { "count": 0, "amount": 0, "currency": "IDR" },
  "recent_payments": []
}
```

**Semantics:**

- Payment counts: `transactions` where `created_at ∈ [from, to)`, grouped by status.
- `payments.unsuccessful_transactions` = `failed + expired + cancelled` for the same window.
- Revenue (financial truth): `SUM(amount)` where `status = PAID` and `paid_at ∈ [from, to)`.
- Refunds: SUCCEEDED refunds where `COALESCE(succeeded_at, created_at) ∈ [from, to)`.
- `recent_payments`: last 10 payments **created in the period** (`created_at ∈ [from, to)`),
  newest first. The list always reflects the selected range; dashboard
  statistics are never derived from these displayed rows.
  Order-backed items include `order_summary` (see above).
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

Order-backed items additionally carry `order_summary` (first product names
plus product-line count, built from immutable `order_items` snapshots — never
the live catalog) for display titles; it is omitted when unavailable and
clients fall back to `merchant_order_id`.

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

### Phase 9A — Order-scoped payments

### GET /api/v1/dashboard/orders/:order_id/payments

**Auth:** Bearer JWT
**Roles:** OWNER, ADMIN, VIEWER (read-only for all roles)

Returns the recorded payment attempts for one merchant-scoped Order, newest
first (`created_at DESC, id DESC`), as `PaymentResponse` items (each with
`transaction_id`, `order_id`, status, method, server-derived amount/currency,
and timestamps, plus `order_summary` product names from immutable order-item
snapshots when the order has items). Missing and foreign order IDs both return `404`. An empty
array means no attempt exists yet.

Strictly read-only: never creates, mutates, reconciles, simulates, or
cancels anything — safe to call for checkout recovery. Clients must not trust
a replayed creation response as latest status; re-read the attempt (or this
history) for the current state.

### POST /api/v1/dashboard/orders/:order_id/payments

**Auth:** Bearer JWT
**Roles:** OWNER, ADMIN (VIEWER → 403)

**Headers:**

| Header | Required |
|---|---|
| `Idempotency-Key` | yes (1–255 chars, merchant-scoped) |
| `Content-Type` | `application/json` |

**Body (only this — unknown fields rejected):**

```json
{ "payment_method": "QRIS" }
```

The request MUST NOT contain `merchant_id`, `amount`, `currency`,
`total_amount`, `subtotal`, `unit_price`, or `merchant_order_id`. Those are
derived server-side from the order (`merchant_id + order_id` lookup; `amount`
← `order.total_amount`, `currency` ← `order.currency`). `transactions.order_id`
is nullable for backward compatibility — legacy non-order payments stay valid.

**Order ≠ Payment ≠ Transaction:** the payment is recorded on the existing
`transactions` row lifecycle (`CREATED → PENDING → PAID/FAILED/EXPIRED/
CANCELLED`, plus `CREATED → FAILED` on definitive provider failure and
`CREATED → CANCELLED`). No second transaction is created on success; the order
itself stays `CREATED` (no `PAID` order status in this phase). No Checkout
table exists.

**`merchant_order_id`:** keeps its existing meaning/uniqueness contract — it is
NOT the internal order UUID. Each order-backed attempt generates a unique
reference (`order-<order-uuid>-<random>`), so retries never collide.

**Retry policy:** one order may have many attempts, but at most one active
(`CREATED`/`PENDING`) and at most one `PAID` payment, enforced by PostgreSQL
partial unique indexes (`uq_transactions_order_active`,
`uq_transactions_order_paid`). New attempts are allowed after `FAILED` /
`CANCELLED` / `EXPIRED`; active or `PAID` attempts return `409`.

**Idempotency:** same order + same payment method + same key replays the
original `201` result; the same key with a different payment method returns
`409 IDEMPOTENCY_KEY_REUSED`. The hash covers merchant, order, method, and the
server-derived amount/currency only.

**Ambiguous provider outcome:** a provider timeout reconciles via the existing
`GetPayment` lookup. If the outcome stays unknown the transaction remains
`CREATED` and the endpoint returns `504` — no blind provider retry, and the
active-payment guard blocks a second attempt until resolved. Definitive
failures record `FAILED` (a valid `CREATED` transition) and allow retry.

**Webhooks (unchanged):** provider webhook → transaction transition → merchant
outbox → delivery. `order_id` is intentionally NOT added to the outbound
merchant payload (backward compatible — existing consumers keep working); the
relation is available to internal persistence only.

**Errors:** `404` missing/foreign order, `400` unsupported payment method
(only `QRIS`), `409` not payable / duplicate active-or-paid /
idempotency conflict, `502` definitive provider failure, `504` ambiguous
provider outcome, `500` unexpected.

**Response `data`:** `CreatePaymentResponse` including `order_id`.

---

### Phase 10C — Product Catalog

Merchant-scoped product and category management. All routes require
`Authorization: Bearer <JWT>`; the merchant derives from the authenticated
user, never from the body. Reads allow OWNER, ADMIN, VIEWER; every mutation
requires OWNER or ADMIN (`403 INSUFFICIENT_ROLE` otherwise). Missing and
foreign IDs both return `404 RESOURCE_NOT_FOUND`.

Price is integer minor units (`≥ 0`); currency must be `IDR`. Amounts are
never floats.

#### Categories

```http
POST   /api/v1/dashboard/categories
GET    /api/v1/dashboard/categories
GET    /api/v1/dashboard/categories/:category_id
PATCH  /api/v1/dashboard/categories/:category_id
DELETE /api/v1/dashboard/categories/:category_id
```

Create body: `{ "name": "Beverages", "description?": "..." }` (name 1–100
chars after trimming; blank → `400 VALIDATION_ERROR` field `name`;
case-insensitive duplicate per merchant → `409 DUPLICATE_MERCHANT_CODE`).
Update accepts `{ name?, description? }` with the same rules. Delete returns
`{"status": "deleted"}`; products in the category keep their rows with a
nulled category link. Category listing is unpaginated (small admin-managed
sets), ordered by name.

#### Products

```http
POST   /api/v1/dashboard/products
GET    /api/v1/dashboard/products?page=&limit=&category_id=&active=&search=
PATCH  /api/v1/dashboard/products/:product_id
DELETE /api/v1/dashboard/products/:product_id
GET    /api/v1/dashboard/products/:product_id
```

Create body:

```json
{
  "name": "Arabica 250g",
  "price": 85000,
  "currency": "IDR",
  "sku?": "AR-250",
  "description?": "...",
  "category_id?": "<uuid, same merchant>",
  "image_url?": "https://cdn.example.com/p/arabica.png",
  "active?": true
}
```

Validation (`400 VALIDATION_ERROR` with field details): blank name, negative
price, non-IDR currency, foreign/missing category (`404`), non-HTTPS,
malformed, credential-bearing, blank, or over-long `image_url`. Duplicate SKU
per merchant → `409 DUPLICATE_MERCHANT_CODE`.

Listing is paginated like orders (`page` default 1, `limit` default 20, max
100; invalid values → `400`); filters `category_id` (UUID), `active`
(boolean), `search` (name/SKU/description substring); ordering `active DESC,
updated_at DESC, name ASC, id DESC`.

`image_url` is URL-only and optional: `null` means no image; the backend
validates syntax (absolute HTTPS, no embedded credentials, ≤2048 chars) at
both the application and database (`chk_products_image_url`) boundaries but
never fetches the URL, so validation says nothing about destination
trustworthiness. PATCH cannot clear an image (omitted means unchanged).

Order snapshots are immutable: price/name edits and product deletion never
rewrite existing `order_items` rows (deletion nulls only the product link);
inactive products are rejected for new orders with `INVALID_REQUEST`.

#### Product images (uploads)

Standalone upload + link flow (replaces manual `image_url` pasting):

```http
POST   /api/v1/dashboard/product-images                 # OWNER/ADMIN, multipart field "image"
GET    /api/v1/product-images/:merchant_id/:filename    # public, no auth
DELETE /api/v1/dashboard/products/:product_id/image     # OWNER/ADMIN
```

**Upload** (`POST`, `Content-Type: multipart/form-data`, field `image`):

- Auth: `Authorization: Bearer <JWT>`; merchant from the JWT user (never the body).
- Accepts JPEG, PNG, and WebP (validated by magic bytes AND full decode, not
  filename or `Content-Type`; WebP via the pure-Go `golang.org/x/image/webp`
  package — no cgo, safe for the static build). GIF, BMP, and SVG/HTML are
  rejected with `400 VALIDATION_ERROR` (field `image`).
- Limits (env-configurable): `PRODUCT_IMAGE_MAX_BYTES` (default 5 MiB),
  `PRODUCT_IMAGE_MAX_DIMENSION` (default 4096 px/side), `PRODUCT_IMAGE_MAX_PIXELS`
  (default 16777216). Oversize → `413 REQUEST_TOO_LARGE`.
- Filenames are server-generated UUIDs; the supplied filename is ignored
  (no traversal, no overwrite, no execution — files are served as
  `image/jpeg`/`image/png`/`image/webp` with `X-Content-Type-Options: nosniff`).
- Success `201`:

```json
{
  "success": true,
  "data": {
    "image_url": "/api/v1/product-images/<merchant-uuid>/<file-uuid>.png",
    "format": "png",
    "width": 800,
    "height": 600,
    "size_bytes": 12345,
    "content_type": "image/png"
  },
  "meta": { "request_id": "req_..." }
}
```

The upload alone never modifies a product. Link it with the existing contract:

```http
POST  /api/v1/dashboard/products            { ..., "image_url": "<returned>" }
PATCH /api/v1/dashboard/products/:product_id { "image_url": "<returned>" }
```

`image_url` accepts external `https://` URLs (unchanged, never fetched) and
upload references (strict grammar, enforced in app + DB CHECK after migration
`000024`). PATCH still cannot clear an image (omitted = unchanged).

**Serving** (`GET`, public): no auth — product images are public catalog assets
like the external URLs they replace. Unknown/malformed references → bare `404`
(no storage detail). `Cache-Control: public, max-age=86400, immutable`.
Website/Flutter clients prepend the backend API origin to the stored
`image_url` (e.g. `http://localhost:8081` in Compose dev).

**Removal** (`DELETE .../products/:product_id/image`): sets `image_url` NULL
and returns the product. The backing file is deleted only when no product
references its URL anymore (global reference count — a URL reused by another
product, same or foreign merchant, protects the file). External HTTPS
references are unlinked without file operations. No image → `404`.

**Lifecycle rules:**

- Create without an image: `image_url` stays `null` (unchanged behavior).
- Upload before product creation: supported — upload returns the reference,
  then create with it. If the create fails, the file remains and the SAME
  reference retries cleanly (no re-upload, no silent orphan).
- Replacement (PATCH with a new URL, or product delete): the old backing file
  is released once unreferenced; still-referenced files survive.
- Upload success + failed link: file persists as an unlinked upload; relink
  with the returned reference. Standalone uploads that are never linked are
  retained (no background sweeper yet — operator cleanup, see limitations).

**Ops prerequisites:** migrations `000023` then `000024` (both manual — never
auto-run); `PRODUCT_IMAGE_DIR` on a persistent volume (`product-images` named
volume in Compose; `./storage/product-images` for local runs); keep
`HTTP_MAX_BODY_BYTES` comfortably above `PRODUCT_IMAGE_MAX_BYTES` (Compose
default 6 MiB vs 5 MiB image limit — the global body cap rejects large uploads
with `413` before image validation, and the server logs a startup warning when
misconfigured).

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
