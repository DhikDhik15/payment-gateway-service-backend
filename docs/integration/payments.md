# Payments

## Create Payment

```http
POST /api/v1/payments
X-API-Key: pk_xxx:sk_xxx
Idempotency-Key: <unique-key>
Content-Type: application/json
```

### Request Body

```json
{
  "merchant_order_id": "ORDER-2026-0001",
  "amount": 150000,
  "currency": "IDR",
  "payment_method": "QRIS"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `merchant_order_id` | string | Yes | 1-100 chars, unique per merchant |
| `amount` | int64 | Yes | Min 1, in rupiah |
| `currency` | string | Yes | Only `IDR` |
| `payment_method` | string | Yes | Only `QRIS` |

### Success Response (201)

```json
{
  "success": true,
  "data": {
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "provider_transaction_id": "MOCK-TXN-abc123",
    "status": "PENDING",
    "payment_url": "http://localhost:5173/pay/MOCK-TXN-abc123",
    "expired_at": "2026-01-01T00:30:00Z",
    "created_at": "2026-01-01T00:00:00Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

## Get Payment

```http
GET /api/v1/payments/{transaction_id}
X-API-Key: pk_xxx:sk_xxx
```

### Success Response (200)

```json
{
  "success": true,
  "data": {
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "provider_transaction_id": "MOCK-TXN-abc123",
    "payment_url": "http://localhost:5173/pay/MOCK-TXN-abc123",
    "status": "PAID",
    "expired_at": "2026-01-01T00:30:00Z",
    "paid_at": "2026-01-01T00:05:00Z",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z"
  }
}
```

## List Payments

```http
GET /api/v1/payments?page=1&limit=20&status=PENDING
X-API-Key: pk_xxx:sk_xxx
```

### Query Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `page` | int | 1 | Page number (min 1) |
| `limit` | int | 20 | Items per page (1-100) |
| `status` | string | — | Filter by status |
| `merchant_order_id` | string | — | Exact match |
| `payment_method` | string | — | Filter by method |
| `created_from` | RFC3339 | — | Inclusive lower bound |
| `created_to` | RFC3339 | — | Exclusive upper bound |

## Customer Payment Flow

1. Merchant creates payment → receives `payment_url`
2. Merchant redirects customer to `payment_url`
3. Customer completes payment on the payment page
4. Gateway updates transaction status
5. Gateway sends webhook (if configured)
6. Merchant retrieves payment status via `GET /api/v1/payments/{id}`

**Important:** HTTP 201 means payment was created, NOT that customer paid. Payment creation and payment completion are two separate stages.
