# Webhooks

## Overview

Webhooks are asynchronous HTTP POST notifications sent to your configured endpoint when payment events occur.

## Event Types

| Event Type | Description |
|------------|-------------|
| `payment.created` | Transaction created |
| `payment.pending` | Provider accepted, awaiting payment |
| `payment.paid` | Customer payment confirmed |
| `payment.failed` | Payment failed |
| `payment.expired` | Payment expired |
| `payment.cancelled` | Payment cancelled |
| `refund.created` | Refund created |
| `refund.processing` | Refund processing |
| `refund.succeeded` | Refund completed |
| `refund.failed` | Refund failed |

## HTTP Headers

| Header | Description |
|--------|-------------|
| `Content-Type` | `application/json` |
| `User-Agent` | `PayGate-Webhook/1.0` |
| `X-PayGate-Event-ID` | Event ID (evt_...) |
| `X-PayGate-Event-Type` | Event type (e.g., payment.paid) |
| `X-PayGate-Timestamp` | Unix timestamp |
| `X-PayGate-Signature` | `sha256=<hex>` |

## Payload Structure

```json
{
  "id": "evt_abc123",
  "type": "payment.paid",
  "version": "1",
  "created_at": "2026-01-01T00:00:00Z",
  "data": {
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "status": "PAID",
    "payment_url": "http://localhost:5173/pay/MOCK-TXN-abc123",
    "provider_transaction_id": "MOCK-TXN-abc123",
    "expired_at": "2026-01-01T00:30:00Z",
    "paid_at": "2026-01-01T00:05:00Z",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z"
  }
}
```

## Response

Return HTTP 2xx to acknowledge receipt. Non-2xx responses trigger retry.

## Retry Policy

- Max attempts: 8
- Backoff: 0, 10s, 30s, 2m, 10m, 30m, 2h, 6h (+/-10% jitter)
- Retryable: 408, 429, 5xx
- Non-retryable: 4xx (except 408, 429)

## Delivery Semantics

- Asynchronous (background worker)
- Transactional outbox pattern (atomic with status change)
- No ordering guarantee
- At-least-once delivery

## Configuration

```bash
POST /api/v1/merchants/{merchantId}/webhook
X-API-Key: pk_xxx:sk_xxx

{"url": "https://merchant.example.com/webhook"}
```

The response includes the `secret` — store it securely for signature verification.
