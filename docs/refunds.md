# Refunds — Phase 7B

> **Status:** IMPLEMENTED. Phase 7B adds partial/full/multiple refund support to PAID transactions.
>
> **Settlement and Reconciliation:** Implemented in Phase 7C as external evidence and audit-only reconciliation. See [settlements.md](./settlements.md) and [reconciliation.md](./reconciliation.md).

---

## 1. Overview

A refund reverses part or all of a captured payment. The gateway supports:

- **Partial refunds** — any `amount < transaction.amount`
- **Full refunds** — `amount = transaction.amount`
- **Multiple refunds** — several refund rows against the same transaction, as long as the combined total does not exceed `transaction.amount`

---

## 2. Refund Lifecycle

```
PENDING
   │
   ├──► PROCESSING   (provider accepted; async completion pending)
   │         │
   │         ├──► SUCCEEDED  (provider confirmed — terminal)
   │         └──► FAILED     (provider rejected — terminal)
   │
   └──► FAILED       (validation or provider hard-fail before PROCESSING)
```

| Status | Meaning | Terminal? |
|--------|---------|-----------|
| `PENDING` | Reserved; provider call not yet confirmed | No |
| `PROCESSING` | Provider accepted; awaiting confirmation | No |
| `SUCCEEDED` | Provider confirmed success; amount immutable | **Yes** |
| `FAILED` | Terminal failure | **Yes** |

**`SUCCEEDED` is terminal.** A succeeded refund can never be changed. Corrections require a new refund (which will be rejected if the refundable balance is zero).

---

## 3. Financial Model

The `transactions` table carries two denormalized counters:

| Column | Meaning |
|--------|---------|
| `refunded_amount` | Sum of amounts for SUCCEEDED refunds |
| `reserved_refund_amount` | Sum of amounts for PENDING + PROCESSING refunds |

**Refundable balance:**
```
refundable = transaction.amount - refunded_amount - reserved_refund_amount
```

Both PENDING and PROCESSING refunds count against the cap. This prevents double-spend across concurrent requests before the provider confirms.

**Over-refund protection:**
- All concurrent refund creates lock the transaction row (`SELECT … FOR UPDATE`)
- Database `CHECK` constraints enforce `refunded_amount + reserved_refund_amount <= amount`
- Both guards are required; neither alone is sufficient

**Example:**

```
Transaction = 100,000 IDR

Refund A PROCESSING = 40,000
Refund B SUCCEEDED  = 20,000

refunded_amount         = 20,000
reserved_refund_amount  = 40,000
refundable              = 40,000

New request for 50,000 → REFUND_AMOUNT_EXCEEDED
```

When Refund A succeeds:
```
refunded_amount         = 60,000
reserved_refund_amount  = 0
refundable              = 40,000
```

When a FAILED or CANCELLED refund is released:
```
reserved_refund_amount decreases by the refund amount
refunded_amount stays the same
```

---

## 4. API Endpoints

### POST /api/v1/payments/:id/refunds

Create a refund for a PAID transaction.

**Authentication:** `X-API-Key` (required)

**Idempotency:** `Idempotency-Key` header (required, 1–255 chars)

**Request:**
```json
{
  "amount": 30000,
  "currency": "IDR",
  "reason": "customer_request"
}
```

**Response (201):**
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

**Error codes:**

| Code | HTTP | When |
|------|------|------|
| `REFUND_TRANSACTION_NOT_PAID` | 400 | Transaction is not in PAID status |
| `REFUND_AMOUNT_EXCEEDED` | 400 | Amount exceeds refundable balance |
| `REFUND_CURRENCY_MISMATCH` | 400 | Currency does not match transaction |
| `TRANSACTION_NOT_FOUND` | 404 | Transaction not found or belongs to another merchant |
| `IDEMPOTENCY_KEY_REUSED` | 409 | Same key used with different payload |
| `IDEMPOTENCY_REQUEST_IN_PROGRESS` | 409 | Concurrent request with same key |
| `REFUND_PROVIDER_ERROR` | 502 | Provider rejected the refund |
| `REFUND_PROVIDER_TIMEOUT` | 504 | Provider did not respond; refund stays PENDING and its reservation is held |

---

### GET /api/v1/refunds/:id

Get a refund by ID.

**Authentication:** `X-API-Key` (required)

Returns 404 for both non-existent refunds and refunds belonging to another merchant (no information leakage via 403).

---

### GET /api/v1/payments/:id/refunds

List refunds for a payment.

**Authentication:** `X-API-Key` (required)

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `page` | int | 1 | Page number |
| `limit` | int | 20 | Items per page (max 100) |
| `status` | string | — | Filter: `PENDING` \| `PROCESSING` \| `SUCCEEDED` \| `FAILED` |

**Ordering:** fixed `created_at DESC, id DESC`

---

## 5. Idempotency

Refund creation reuses the Phase 5A idempotency infrastructure.

**Scope:** `(merchant_id, idempotency_key)` — merchant-scoped.

**Request hash:** SHA-256 over `transaction_id + amount + currency + reason` (length-delimited to prevent separator collisions).

**Behaviour:**

| Scenario | Outcome |
|----------|---------|
| Same key + same payload | Replays the stored result or existing refund. No second provider call. |
| Same key + different payload | `409 IDEMPOTENCY_KEY_REUSED` |
| Concurrent identical requests | One reserves the key; others get `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` |
| Key expired (after TTL) | Key can be reused as new |
| Merchant A key = Merchant B key | Independent; no collision |

The `idempotency_keys` table is extended with a nullable `refund_id` column linking the record to the refund created.

---

## 6. Provider Timeout Handling

If the provider call times out:
- The refund stays in **PENDING** state with its reservation held
- `504 REFUND_PROVIDER_TIMEOUT` is returned to the client
- The idempotency key remains **PROCESSING** and is linked to the created refund
- Client may replay the same `Idempotency-Key` to retrieve that refund; no second provider call is made
- Recovery also possible via provider webhook when provider eventually confirms

This avoids:
- Releasing the reservation prematurely (could allow over-refund)
- Creating a duplicate provider refund on retry

---

## 7. Provider Webhook

Inbound provider refund events reuse the existing webhook endpoint:

```
POST /api/v1/webhooks/providers/:provider
```

The MOCK provider supports two refund event types:

| Event type | Effect |
|------------|--------|
| `REFUND_SUCCEEDED` | Marks refund SUCCEEDED; moves amount from reserved to refunded; enqueues `refund.succeeded` outbox event |
| `REFUND_FAILED` | Marks refund FAILED; releases reservation; enqueues `refund.failed` outbox event |

**Duplicate webhook safety:**
- `webhook_events` table has `UNIQUE(provider, event_id)` — duplicate events are inserted atomically
- SUCCEEDED terminal state prevents double-application of counters

**Amount/currency mismatch:** Webhook is marked IGNORED; no financial change.

---

## 8. Merchant Outbound Webhook Events

Phase 7B adds refund lifecycle events to the Phase 6 transactional outbox:

| Event | When |
|-------|------|
| `refund.created` | Refund created (PENDING) |
| `refund.processing` | Provider accepted; async |
| `refund.succeeded` | Provider confirmed success |
| `refund.failed` | Terminal failure |

All events are delivered **AT-LEAST-ONCE** via the existing Phase 6 webhook worker.

**Payload example:**
```json
{
  "id": "evt_abc123",
  "type": "refund.succeeded",
  "version": "1",
  "created_at": "2026-09-15T01:00:01Z",
  "data": {
    "refund_id": "uuid",
    "transaction_id": "uuid",
    "amount": 30000,
    "currency": "IDR",
    "status": "SUCCEEDED",
    "provider": "MOCK",
    "provider_refund_id": "MOCK-REF-abc123",
    "transaction_amount": 100000,
    "refunded_amount": 30000,
    "reserved_refund_amount": 0,
    "refundable_amount": 70000,
    "requested_at": "...",
    "succeeded_at": "...",
    "created_at": "...",
    "updated_at": "..."
  }
}
```

---

## 9. Concurrency Protection

Multiple concurrent refund requests for the same transaction are serialized by `SELECT … FOR UPDATE` on the transaction row in PostgreSQL. This ensures:

- Exactly one refund can reserve the balance at a time
- The `reserved_refund_amount + refunded_amount <= amount` invariant is maintained under concurrent load
- No process-level locks are used (safe for multi-instance deployments)

---

## 10. FAILED Refund Policy

| Scenario | Behavior |
|----------|----------|
| Failed before provider call | Reservation released; refund FAILED |
| Provider definitively rejected (4xx) | Reservation released; refund FAILED |
| Provider timeout | Refund stays PENDING; reservation held; same idempotency key replays the existing refund |
| Retry after FAILED | Create a new refund row with a new idempotency key (safer than same-row retry) |

**Why not retry on the same row?** The provider may have accepted the refund before the timeout or failure. Creating a duplicate provider refund request risks double-refunding the customer. Use provider webhooks or polling to determine the actual outcome.

---

## 11. Partial Refund Examples

### Multiple partials summing to full amount

```
Transaction: 100,000 IDR

POST refunds amount=20,000  → SUCCEEDED  refunded=20,000 reserved=0
POST refunds amount=30,000  → SUCCEEDED  refunded=50,000 reserved=0
POST refunds amount=50,000  → SUCCEEDED  refunded=100,000 reserved=0

POST refunds amount=1       → 400 REFUND_AMOUNT_EXCEEDED
```

### Concurrent partial refunds

```
Transaction: 100,000 IDR

Request A: amount=60,000  (arrives first, locks transaction)
Request B: amount=50,000  (arrives simultaneously)

Request A succeeds: refunded=60,000  refundable=40,000
Request B rejected: REFUND_AMOUNT_EXCEEDED (40,000 < 50,000)
```

---

## 12. Constraints and Invariants

### Database constraints (enforced by PostgreSQL)

```sql
-- On transactions:
CHECK (refunded_amount >= 0)
CHECK (reserved_refund_amount >= 0)
CHECK (refunded_amount + reserved_refund_amount <= amount)

-- On refunds:
CHECK (amount > 0)
CHECK (status IN ('PENDING', 'PROCESSING', 'SUCCEEDED', 'FAILED'))

-- Unique:
UNIQUE (provider, provider_refund_id) WHERE provider_refund_id IS NOT NULL
```

### Application invariants

- Refund only allowed for `transaction.status = PAID`
- `refund.currency` must equal `transaction.currency`
- `SUCCEEDED` refund amount is immutable
- `transaction.amount` is immutable after PAID

---

## 13. Known Limitations (Phase 7B)

- **No real Midtrans adapter**: `RefundProvider` is implemented as `MockRefundProvider` only. A real Midtrans adapter will be added in a future phase once the Midtrans refund sandbox is validated.
- **No `CANCELLED` status**: Phase 7A proposed a `CANCELLED` state for cancelling PENDING refunds before provider call. Not implemented in Phase 7B (create new refund with released balance instead).
- **No retry endpoint**: FAILED refunds require a new refund request with a new idempotency key.
- **No admin tools**: Stuck PROCESSING refunds require manual investigation (Phase 7C+).

---

## 14. Future Phase 7C

Phase 7C will add:
- Settlement batch import
- Reconciliation engine (gateway vs provider settlement comparison)
- `settlements`, `settlement_items`, `reconciliation_runs`, `reconciliation_items` tables
- Admin APIs for settlement and reconciliation review
