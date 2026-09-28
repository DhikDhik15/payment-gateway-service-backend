# Refunds

## Create Refund

```http
POST /api/v1/payments/{transaction_id}/refunds
X-API-Key: pk_xxx:sk_xxx
Idempotency-Key: <unique-key>
Content-Type: application/json
```

### Request Body

```json
{
  "amount": 50000,
  "currency": "IDR",
  "reason": "Customer request"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `amount` | int64 | Yes | Min 1, in rupiah |
| `currency` | string | Yes | Must match transaction currency |
| `reason` | string | No | Max 500 chars |

### Success Response (201)

```json
{
  "success": true,
  "data": {
    "refund_id": "660e8400-e29b-41d4-a716-446655440001",
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 50000,
    "currency": "IDR",
    "status": "PENDING",
    "provider": "MOCK",
    "refunded_amount": 0,
    "reserved_amount": 50000,
    "refundable_amount": 50000,
    "transaction_amount": 100000,
    "requested_at": "2026-01-01T00:00:00Z",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:00Z"
  }
}
```

## Refund Status

| Status | Meaning | Terminal |
|--------|---------|----------|
| `PENDING` | Refund created, provider not yet called | No |
| `PROCESSING` | Provider processing | No |
| `SUCCEEDED` | Refund completed | Yes |
| `FAILED` | Refund failed | Yes |

## Constraints

- Only `PAID` transactions can be refunded
- Over-refund protection: PENDING + PROCESSING refunds count against balance
- Maximum total refundable = transaction amount
- Partial refunds supported
- Same idempotency behavior as payment creation

## List Refunds for Payment

```http
GET /api/v1/payments/{transaction_id}/refunds?page=1&limit=20
X-API-Key: pk_xxx:sk_xxx
```

## Get Refund

```http
GET /api/v1/refunds/{refund_id}
X-API-Key: pk_xxx:sk_xxx
```

Returns `404 REFUND_NOT_FOUND` for non-existent refunds or refunds belonging to another merchant.
