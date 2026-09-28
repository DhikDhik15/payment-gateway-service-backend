# Cancellation

## Cancel Payment

```http
POST /api/v1/payments/{transaction_id}/cancel
X-API-Key: pk_xxx:sk_xxx
```

No request body required.

## Allowed States

Cancellation is only allowed for:

- `CREATED`
- `PENDING`

Terminal states (`PAID`, `FAILED`, `EXPIRED`, `CANCELLED`) cannot be cancelled.

## Success Response (200)

```json
{
  "success": true,
  "data": {
    "transaction_id": "550e8400-e29b-41d4-a716-446655440000",
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "status": "CANCELLED",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:10:00Z"
  }
}
```

## Error Responses

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 400 | `INVALID_REQUEST` | Invalid UUID format |
| 401 | `INVALID_API_KEY` | Authentication failed |
| 404 | `TRANSACTION_NOT_FOUND` | Not found or belongs to another merchant |
| 409 | `INVALID_TRANSACTION_STATE` | Transaction cannot be cancelled in current state |
| 500 | `INTERNAL_ERROR` | Internal server error |

## Behavior

- For `PENDING` transactions, the provider is asked to cancel first
- If provider refuses, the cancellation fails
- Cancellation is idempotent for already-cancelled transactions (returns current state)
