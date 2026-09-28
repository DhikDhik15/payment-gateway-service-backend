# Idempotency

## Overview

Payment creation and refund creation require an `Idempotency-Key` header. This ensures safe retries without creating duplicate transactions.

## Header

```
Idempotency-Key: <unique-key>
```

- Required for `POST /api/v1/payments` and `POST /api/v1/payments/{id}/refunds`
- Length: 1-255 characters
- Scope: Per-merchant (unique constraint: `merchant_id + key`)
- TTL: 24 hours (default)

## Behavior

### Same Key + Same Payload

Returns the stored result. No second transaction is created.

```
Request A: Idempotency-Key: order-123-payment
→ 201 Created, transaction_id: tx-abc

Request B: Idempotency-Key: order-123-payment (same payload)
→ 201 Created, transaction_id: tx-abc (same)
```

### Same Key + Different Payload

Returns `409 IDEMPOTENCY_KEY_REUSED`. The original record is not modified.

```
Request A: Idempotency-Key: order-123-payment, amount: 100000
→ 201 Created

Request B: Idempotency-Key: order-123-payment, amount: 200000
→ 409 IDEMPOTENCY_KEY_REUSED
```

### Concurrent Requests

One request reserves the key (`PROCESSING`). The other receives `409 IDEMPOTENCY_REQUEST_IN_PROGRESS`.

### Provider Timeout

If the provider times out, the result is stored as FAILED. Replaying the same key returns the same `504 PAYMENT_PROVIDER_TIMEOUT`.

### Expired Key

After TTL (24h), expired keys can be reserved again.

## Best Practices

- Use a unique key per logical payment attempt (e.g., `order-123-payment`)
- Reuse the same key when retrying after timeout
- Do not use a global key for all payments
- Generate keys with `crypto.randomUUID()` or equivalent
