# End-to-End Integration Example

This guide walks through a complete payment integration flow.

## Prerequisites

- API key: `pk_xxx:sk_xxx`
- Webhook endpoint: `https://merchant.example.com/webhook`
- Webhook secret: `whsec_xxx`

## Step 1: Create Payment

```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "X-API-Key: pk_xxx:sk_xxx" \
  -H "Idempotency-Key: order-2026-0001-payment" \
  -H "Content-Type: application/json" \
  -d '{
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS"
  }'
```

**Response (201):**
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
  }
}
```

## Step 2: Redirect Customer

Redirect the customer to `payment_url`:
```
http://localhost:5173/pay/MOCK-TXN-abc123
```

## Step 3: Customer Completes Payment

Customer clicks "Simulate successful payment" on the payment page.

## Step 4: Receive Webhook

Your webhook endpoint receives:

```http
POST /webhook
X-PayGate-Event-ID: evt_abc123
X-PayGate-Event-Type: payment.paid
X-PayGate-Timestamp: 1704067200
X-PayGate-Signature: sha256=...

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
    "paid_at": "2026-01-01T00:05:00Z"
  }
}
```

## Step 5: Verify Signature

```go
// Verify before processing
if !verifyWebhook(secret, timestamp, body, signature) {
    return 401
}
```

## Step 6: Retrieve Payment (Optional)

```bash
curl http://localhost:8080/api/v1/payments/550e8400-e29b-41d4-a716-446655440000 \
  -H "X-API-Key: pk_xxx:sk_xxx"
```

**Response (200):**
```json
{
  "success": true,
  "data": {
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "status": "PAID",
    "paid_at": "2026-01-01T00:05:00Z"
  }
}
```

## State Transition Summary

```
CREATED → PENDING → PAID
```

## Optional: Refund

```bash
curl -X POST http://localhost:8080/api/v1/payments/550e8400-e29b-41d4-a716-446655440000/refunds \
  -H "X-API-Key: pk_xxx:sk_xxx" \
  -H "Idempotency-Key: order-2026-0001-refund" \
  -H "Content-Type: application/json" \
  -d '{"amount": 50000, "currency": "IDR", "reason": "Customer request"}'
```

## Optional: Cancel (before payment)

```bash
curl -X POST http://localhost:8080/api/v1/payments/550e8400-e29b-41d4-a716-446655440000/cancel \
  -H "X-API-Key: pk_xxx:sk_xxx"
```
