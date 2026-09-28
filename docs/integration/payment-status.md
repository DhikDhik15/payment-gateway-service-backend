# Payment Status

## Status Values

| Status | Meaning | Terminal |
|--------|---------|----------|
| `CREATED` | Transaction persisted, provider not yet called | No |
| `PENDING` | Provider accepted, awaiting customer payment | No |
| `PAID` | Customer payment confirmed | Yes |
| `FAILED` | Payment failed | Yes |
| `EXPIRED` | Payment expired | Yes |
| `CANCELLED` | Payment cancelled | Yes |

## State Machine

```
CREATED → PENDING, CANCELLED
PENDING → PAID, FAILED, EXPIRED, CANCELLED
PAID → (terminal)
FAILED → (terminal)
EXPIRED → (terminal)
CANCELLED → (terminal)
```

## Important

- `CREATED` and `PENDING` are NOT successful payment states
- Only `PAID` means the customer has paid
- Terminal states cannot transition further
- Use `GET /api/v1/payments/{id}` to retrieve current status

## Polling

For non-terminal statuses (`CREATED`, `PENDING`), you may poll:

```
GET /api/v1/payments/{transaction_id}
X-API-Key: pk_xxx:sk_xxx
```

Stop polling when status reaches a terminal state (`PAID`, `FAILED`, `EXPIRED`, `CANCELLED`).

## Webhook Alternative

Instead of polling, configure a webhook endpoint to receive real-time status change notifications. See [Webhooks](webhooks.md).
