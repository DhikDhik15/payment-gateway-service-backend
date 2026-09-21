# Payment Flow Documentation

## Overview

This document describes the end-to-end flow for each payment operation.

---

## 1. Create Payment Flow

### Sequence

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway (Handler)
    participant S  as Payment Service
    participant P  as Mock Provider
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments<br/>X-API-Key + Idempotency-Key
    API->>API: Auth Middleware — validate API key, load merchant
    API->>S: CreatePaymentWithIdempotency(merchantID, req, key)

    S->>S: Validate currency (IDR)
    S->>S: Validate payment method (QRIS)
    S->>DB: INSERT idempotency_keys (PROCESSING)<br/>unique merchant_id + key
    DB-->>S: reservation owner
    S->>DB: SELECT — check duplicate merchant_order_id
    DB-->>S: not found → proceed

    S->>DB: INSERT transaction (status=CREATED)
    DB-->>S: OK

    S->>P: CreatePayment(transactionID, amount, …)
    note over S,P: External call — outside any DB transaction<br/>Provider call is always made outside a DB connection

    alt Provider success
        P-->>S: {provider_tx_id=MOCK-TXN-xxx, status=PENDING, payment_url, expired_at}
        S->>DB: INSERT payment_attempt (status=SUCCESS)
        S->>DB: UPDATE transaction SET status=PENDING,<br/>provider=MOCK, provider_transaction_id, payment_url<br/>(WHERE status=CREATED)
        DB-->>S: 1 row updated
        S->>DB: UPDATE idempotency_keys SET COMPLETED, response_body, transaction_id
        S-->>API: CreatePaymentResponse (replayable)
        API-->>M: 201 Created
    else Provider failure
        P-->>S: ErrProviderFailure / ErrProviderTimeout
        S->>DB: INSERT payment_attempt (status=FAILED)
        S->>DB: UPDATE transaction SET status=FAILED<br/>(WHERE status=CREATED)
        DB-->>S: 1 row updated
        S->>DB: UPDATE idempotency_keys SET FAILED, stable error code
        S-->>API: ErrProviderFailure / ErrProviderTimeout (replayable)
        API-->>M: 502 / 504
    end

    note over M,DB: Same key + same payload replays. Different payload returns 409 IDEMPOTENCY_KEY_REUSED. Active PROCESSING returns 409 IDEMPOTENCY_REQUEST_IN_PROGRESS. Expired keys can be reserved again.
```

### Atomicity Design

The external provider call **cannot** participate in a PostgreSQL transaction.
The chosen design avoids holding a DB connection open across a slow network call:

| Step | DB transaction? | Notes |
|------|----------------|-------|
| 1. Validate | No | Pure in-memory checks |
| 2. Duplicate check | No | SELECT — read only |
| 3. INSERT transaction (CREATED) | No | Isolated write |
| 4. Call provider | No | External I/O — may be slow |
| 5. INSERT payment_attempt | No | Audit trail — always written |
| 6. UPDATE transaction status | No | Conditional: `WHERE status=CREATED` |

If step 4 fails, steps 5 and 6 still run to record the failure.
If step 6 fails after a successful provider call, the attempt is already saved
and the discrepancy is logged as a critical error for manual resolution.

---

## 2. Get Payment Flow

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant DB as PostgreSQL

    M->>API: GET /api/v1/payments/{id}<br/>X-API-Key: pk_xxx
    API->>API: Auth Middleware — validate API key
    API->>S: GetPayment(merchantID, transactionID)
    S->>DB: SELECT … WHERE id=$1 AND merchant_id=$2
    alt Found
        DB-->>S: Transaction row (with payment_url, provider_transaction_id)
        S-->>API: PaymentResponse
        API-->>M: 200 OK
    else Not found / wrong merchant
        DB-->>S: no rows
        S-->>API: ErrTransactionNotFound
        API-->>M: 404 TRANSACTION_NOT_FOUND
    end
```

### Merchant Isolation

The SQL query always includes `AND merchant_id = $merchantID`.
There is **no** secondary check after fetching — the DB is the enforcement point.
A transaction belonging to Merchant B returns `404` to Merchant A,
identical to a non-existent transaction. This prevents information leakage.

---

## 3. Cancel Payment Flow

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant P  as Mock Provider
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments/{id}/cancel<br/>X-API-Key: pk_xxx
    API->>API: Auth Middleware
    API->>S: CancelPayment(merchantID, transactionID)

    S->>DB: SELECT … WHERE id=$1 AND merchant_id=$2
    DB-->>S: Transaction row

    S->>S: IsCancellable()? (CREATED or PENDING only)

    alt Not cancellable (PAID / FAILED / EXPIRED / CANCELLED)
        S-->>API: ErrInvalidTransactionState
        API-->>M: 409 INVALID_TRANSACTION_STATE
    end

    alt Status is PENDING
        S->>DB: SELECT payment_attempts WHERE transaction_id=$1
        DB-->>S: attempts (to resolve provider_transaction_id)
        S->>P: CancelPayment(providerTransactionID)
        alt Provider refuses
            P-->>S: ErrProviderFailure / ErrProviderTimeout
            S-->>API: ErrProviderFailure / ErrProviderTimeout
            API-->>M: 502 / 504 (transaction NOT changed)
        end
        P-->>S: nil (success)
    end

    note over S: Status CREATED → no provider call needed

    S->>DB: UPDATE transactions SET status=CANCELLED<br/>WHERE id=$1 AND status=$2
    note over S,DB: Conditional update prevents concurrent state corruption
    DB-->>S: 1 row updated
    S-->>API: PaymentResponse (status=CANCELLED)
    API-->>M: 200 OK
```

### Concurrency Safety

The `UPDATE … WHERE status = $currentStatus` pattern acts as an optimistic lock.
If a concurrent request has already changed the status, `RowsAffected() == 0`
and `ErrTransactionNotFound` is returned, causing a `409` response.
This prevents two concurrent cancel requests from both succeeding.

---

## 4. Provider Failure Flow

With `PAYMENT_PROVIDER=midtrans`, the same flow invokes the Midtrans Snap adapter.
Calls are context-aware and are never automatically retried after a timeout.

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant P  as Mock Provider
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments
    API->>S: CreatePayment(...)
    S->>DB: INSERT transaction (CREATED)
    S->>P: CreatePayment(...)
    P-->>S: ErrProviderFailure
    S->>DB: INSERT payment_attempt (status=FAILED)
    S->>DB: UPDATE transaction SET status=FAILED
    S-->>API: ErrProviderFailure
    API-->>M: 502 Bad Gateway
```

**Transaction state after provider failure:**

| Field | Value |
|-------|-------|
| status | FAILED |
| provider | (not set — provider was never reached successfully) |
| payment_url | null |
| provider_transaction_id | null |

The `payment_attempts` table records the failed attempt for full audit trail.

---

## 5. Duplicate Order Flow

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments (ORDER-001, second time)
    API->>S: CreatePayment(merchantID, {merchant_order_id: "ORDER-001"})
    S->>DB: SELECT — check duplicate merchant_order_id
    DB-->>S: row found (ORDER-001 already exists for this merchant)
    S-->>API: ErrDuplicateOrder
    API-->>M: 409 DUPLICATE_ORDER
```

**Note:** The uniqueness constraint is per-merchant.
`Merchant A + ORDER-001` and `Merchant B + ORDER-001` are two different orders.

---

## 6. Transaction State Machine

```
          ┌─────────────────────────────────────────────┐
          │                                             │
       CREATED ──────────────────────────────────► CANCELLED
          │                                             ▲
          ▼                                             │
       PENDING ──────────────────────────────────────── ┘
          │
          ├──────────────────► PAID       (terminal — Phase 3+)
          │
          ├──────────────────► FAILED     (terminal)
          │
          └──────────────────► EXPIRED    (terminal — Phase 3+)
```

| From | To | Trigger |
|------|----|---------|
| CREATED | PENDING | Provider accepts payment |
| CREATED | FAILED | Provider rejects at creation |
| CREATED | CANCELLED | Merchant cancels before provider call |
| PENDING | PAID | Payment received (webhook/polling — Phase 3+) |
| PENDING | FAILED | Provider reports failure |
| PENDING | EXPIRED | Expiry job runs (Phase 3+) |
| PENDING | CANCELLED | Merchant cancels + provider confirms |

All transitions are validated by `TransactionStatus.CanTransitionTo()` in
`internal/model/transaction.go`. The service layer enforces this before any DB write.

---

## 7. Provider Error Scenarios Summary

| Scenario | Transaction Status | Payment Attempt | HTTP |
|----------|--------------------|-----------------|------|
| Provider accepts | PENDING | SUCCESS | 201 |
| Provider rejects create (ErrProviderFailure) | FAILED | FAILED | 502 |
| Provider timeout on create (ErrProviderTimeout) | FAILED | FAILED | 504 |
| Provider cancel refuses | Unchanged (CREATED/PENDING) | — | 502 |
| Provider cancel timeout | Unchanged (CREATED/PENDING) | — | 504 |

The `payment_attempts` table is the full audit trail for every provider interaction.

---

## 8. Idempotency State Machine

Every `POST /api/v1/payments` request creates or reuses an `idempotency_keys` row.
The row is scoped to `(merchant_id, key)` and moves through three states:

```
                    Reserve (INSERT)
                          │
                          ▼
                     PROCESSING          ← key is held; provider call in flight
                          │
              ┌───────────┴───────────┐
              │                       │
              ▼                       ▼
          COMPLETED               FAILED
    (success result stored)  (error code stored)
              │                       │
         replayed as                replayed as
           HTTP 201               HTTP 502/504
```

| Status | Description | Replay behaviour |
|--------|-------------|-----------------|
| `PROCESSING` | Reservation held; provider call in flight | `409 IDEMPOTENCY_REQUEST_IN_PROGRESS` |
| `COMPLETED` | Successful payment stored as JSONB | Return stored response (HTTP 201) |
| `FAILED` | Provider error stored as stable error code | Return stored error (HTTP 502/504) |

Records expire after `IDEMPOTENCY_TTL` (default `24h`). An expired record is
replaced atomically on the next `Reserve` call — no cleanup worker is needed.

---

## 9. Idempotency Replay Flow

### 9a. COMPLETED replay

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments<br/>Idempotency-Key: PAYMENT-001 (repeat)
    API->>S: CreatePaymentWithIdempotency(merchantID, req, "PAYMENT-001")
    S->>DB: INSERT idempotency_keys ON CONFLICT — key active → no-op
    DB-->>S: 0 rows (not owner)
    S->>DB: SELECT idempotency_keys WHERE merchant_id=$1 AND key=$2
    DB-->>S: {status=COMPLETED, request_hash, response_body}
    S->>S: compare request hash — matches
    S->>S: unmarshal stored CreatePaymentResponse
    S-->>API: stored response (no provider call)
    API-->>M: 201 Created (same transaction_id)
```

### 9b. Payload mismatch

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway
    participant S  as Payment Service
    participant DB as PostgreSQL

    M->>API: POST /api/v1/payments<br/>Idempotency-Key: PAYMENT-001<br/>amount: 100000 (changed)
    API->>S: CreatePaymentWithIdempotency(merchantID, req, "PAYMENT-001")
    S->>DB: INSERT — conflict, key active
    DB-->>S: 0 rows (not owner)
    S->>DB: SELECT existing record
    DB-->>S: {request_hash: <original hash>}
    S->>S: compare hashes — MISMATCH
    S-->>API: ErrIdempotencyKeyReused
    API-->>M: 409 IDEMPOTENCY_KEY_REUSED
    note over M,DB: Original transaction and idempotency record are NOT modified
```

---

## 10. Concurrent Request Flow

Two goroutines submit the same key at the same time. PostgreSQL's unique
constraint on `(merchant_id, key)` is the sole arbitration mechanism —
no application-level mutex or map is used.

```mermaid
sequenceDiagram
    participant A  as Request A
    participant B  as Request B
    participant DB as PostgreSQL
    participant P  as Provider

    par
        A->>DB: INSERT idempotency_keys PROCESSING
        B->>DB: INSERT idempotency_keys PROCESSING
    end
    note over DB: Only one INSERT wins the unique constraint
    DB-->>A: RETURNING id (owner)
    DB-->>B: 0 rows (conflict — not owner)

    A->>P: CreatePayment(...)
    B->>DB: SELECT existing record
    DB-->>B: {status=PROCESSING}
    B-->>B: return ErrIdempotencyInProgress
    note over B: 409 IDEMPOTENCY_REQUEST_IN_PROGRESS

    P-->>A: success
    A->>DB: UPDATE idempotency_keys SET COMPLETED
    note over A: 201 Created
```

**Invariants guaranteed:**

| Invariant | Mechanism |
|-----------|-----------|
| At most one provider call per (merchant, key) | PostgreSQL UNIQUE(merchant_id, key) |
| At most one transaction per reservation | Reserve-then-create sequence; duplicate order check inside |
| No second provider call on FAILED replay | FAILED status checked before any processing |
| No second provider call on COMPLETED replay | COMPLETED status checked before any processing |
| No second provider call on timeout | Timeout stored as FAILED; replayed, never retried |

---

## 11. Provider Timeout — Ambiguous External Result

When the provider call times out, the gateway has already sent the request.
The external payment **may or may not** have been created. The gateway:

1. Records the attempt as `FAILED` in `payment_attempts`.
2. Marks the transaction `FAILED` in `transactions`.
3. Stores `FAILED` + `PAYMENT_PROVIDER_TIMEOUT` error code in `idempotency_keys`.
4. Returns `504 PAYMENT_PROVIDER_TIMEOUT` to the merchant.

On replay (same key + same payload), the stored `504` is returned without
calling the provider again. This prevents a second external charge at the cost
of not confirming whether the first charge succeeded.

**If the provider supports idempotency** (e.g. Midtrans uses `order_id` as a
natural key), the merchant can query the provider directly to reconcile the
ambiguous charge. The gateway does not perform this reconciliation automatically.

## Phase 7C Settlement Flow

Provider settlement evidence is imported through the dedicated admin API, persisted atomically with its items, then reconciled explicitly:

```text
provider report
    |
    v
admin import --(provider + settlement_ref + payload hash)--> IMPORTED
    |
    v
reconcile --(provider IDs, lifecycle, currency, amount)--> RECONCILED / PARTIAL / MISMATCH
    |
    v
ops reviews audit result and may rerun
```

This flow never changes `transactions.status`, transaction amount, refund status, or refund counters. Settlement is external evidence only.

Exact-once provider execution cannot be guaranteed by the gateway alone when
the provider does not support idempotency on its end.

---

## 12. Payment Listing Flow (Phase 5B)

### Sequence

```mermaid
sequenceDiagram
    participant M  as Merchant
    participant API as Payment Gateway (Handler)
    participant S  as Payment Service
    participant DB as PostgreSQL

    M->>API: GET /api/v1/payments?page=1&limit=20&status=PAID
    API->>API: Auth Middleware — validate X-API-Key, load merchant
    API->>API: parseListFilter — validate page, limit, status, dates
    note over API: merchant_id comes from auth context ONLY<br/>Never from query parameters

    API->>S: ListPayments(merchantID, filter)
    S->>S: Apply defaults (page=1, limit=20 if zero)
    S->>S: Validate page ≥ 1, 1 ≤ limit ≤ 100
    S->>S: Validate status (if supplied)
    S->>S: Validate payment_method (if supplied)
    S->>S: Validate created_from < created_to (if both supplied)

    S->>DB: SELECT COUNT(*) FROM transactions WHERE merchant_id=$1 [AND filters…]
    DB-->>S: total = 42

    S->>DB: SELECT … FROM transactions WHERE merchant_id=$1 [AND filters…]<br/>ORDER BY created_at DESC, id DESC LIMIT $N OFFSET $M
    DB-->>S: rows (page slice)

    S->>S: Convert rows → []PaymentResponse DTO
    S->>S: Compute total_pages = ceil(42 / 20) = 3
    S-->>API: ListPaymentsResult

    API->>M: 200 OK { data: [...], meta: { page, limit, total, total_pages } }
```

### Merchant Isolation Guarantee

The `merchant_id` parameter in every query is sourced exclusively from the
authenticated merchant stored in the Gin context by the `Auth` middleware.
The repository always prepends `WHERE merchant_id = $1` before any optional
filter. A merchant cannot enumerate another merchant's transactions — even by
guessing a `merchant_order_id` that belongs to another merchant, because the
scoped count and list will return zero results.

### Date Range Semantics

```
created_from = 2026-01-01T00:00:00Z  →  created_at >= 2026-01-01 00:00:00 UTC  (inclusive)
created_to   = 2026-02-01T00:00:00Z  →  created_at <  2026-02-01 00:00:00 UTC  (exclusive)
```

This half-open interval `[from, to)` is standard and consistent with ISO 8601
interval notation. Specifying only one boundary is allowed.

### Deterministic Ordering

`ORDER BY created_at DESC, id DESC` is fixed and not client-controllable.
The `id DESC` tie-breaker ensures stable results when multiple rows share the
same `created_at` timestamp, which is common when transactions are created in
rapid succession or in bulk tests.

### Page Beyond Last Page

Requesting a page that exceeds the available data returns an empty `data` array
with the accurate `total` and `total_pages` values unchanged. This is not an
error — it matches the behaviour of standard REST APIs and allows clients to
detect end-of-results via `len(data) == 0`.

### Index Strategy

Migration `000009` adds two composite indexes specifically for this endpoint:

| Index | Columns | Use case |
|-------|---------|----------|
| `idx_transactions_merchant_created_at` | `(merchant_id, created_at DESC, id DESC)` | Unfiltered listing |
| `idx_transactions_merchant_status_created_at` | `(merchant_id, status, created_at DESC, id DESC)` | Status-filtered listing |

Both indexes support the `ORDER BY created_at DESC, id DESC` sort without a
separate sort step, making large paginated queries efficient.

---

## 12. Merchant Outbound Webhook Flow (Phase 6)

Distinct from §7 (inbound provider webhooks). This is **gateway → merchant**.

### Outbox enqueue (same DB transaction as status change)

```mermaid
sequenceDiagram
    participant S as Payment / Webhook / Expiry Service
    participant DB as PostgreSQL
    participant W as Merchant Webhook Worker
    participant M as Merchant Endpoint

    S->>DB: BEGIN
    S->>DB: UPDATE transactions SET status = …
    S->>DB: INSERT merchant_webhook_deliveries (PENDING)
    S->>DB: COMMIT

    loop every WEBHOOK_DELIVERY_INTERVAL
        W->>DB: Claim PENDING rows (FOR UPDATE SKIP LOCKED)
        W->>DB: status → PROCESSING
        W->>M: POST payload + HMAC headers
        alt 2xx
            W->>DB: status → DELIVERED
        else retryable (5xx, 429, timeout)
            W->>DB: status → PENDING, next_attempt_at += backoff
        else non-retryable 4xx
            W->>DB: status → FAILED
        else max attempts
            W->>DB: status → DEAD
        end
    end
```

### At-least-once semantics

Every delivery carries a stable `evt_…` ID in `X-PayGate-Event-ID` and in the
JSON payload. Retries reuse the same event ID — merchants must deduplicate.

See [merchant-webhooks.md](./merchant-webhooks.md) for signature format, retry
schedule, and configuration API.

---

## Phase 7B — Refund flow (IMPLEMENTED)

> Full documentation: [refunds.md](./refunds.md). Design background: [phase-7a-financial-design.md](./phase-7a-financial-design.md).

```mermaid
sequenceDiagram
    participant M as Merchant
    participant API as Refund API
    participant S as RefundService
    participant DB as PostgreSQL
    participant P as RefundProvider
    participant O as Outbox Worker

    M->>API: POST /payments/:id/refunds + Idempotency-Key
    API->>S: CreateRefundWithIdempotency
    S->>DB: Reserve idempotency key
    S->>DB: FOR UPDATE transaction + insert refund PENDING + reserve amount + outbox refund.created
    S->>P: CreateRefund (outside DB tx)
    alt Provider success (sync)
        P-->>S: SUCCEEDED + provider_refund_id
        S->>DB: SUCCEEDED + move reserved→refunded + outbox refund.succeeded
        S->>DB: Complete idempotency
        API-->>M: 201 Created
    else Provider async
        P-->>S: PROCESSING + provider_refund_id
        S->>DB: PROCESSING + outbox refund.processing
        Note over DB: Later REFUND_SUCCEEDED webhook finalizes
    else Provider timeout
        P-->>S: ErrProviderTimeout
        Note over S,DB: Refund stays PENDING; reservation held
        API-->>M: 504 REFUND_PROVIDER_TIMEOUT
    else Provider hard failure
        P-->>S: ErrProviderFailure
        S->>DB: FAILED + release reservation + outbox refund.failed
        API-->>M: 502 REFUND_PROVIDER_ERROR
    end
    O->>M: refund.* webhook (at-least-once)
```

**Concurrency:** Concurrent creates serialize on `SELECT … FOR UPDATE` of the parent transaction. PENDING and PROCESSING both reserve balance.

**Idempotency:** Same key + same payload replays. Same key + different payload → `409 IDEMPOTENCY_KEY_REUSED`.
