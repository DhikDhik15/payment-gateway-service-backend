# Merchant Outbound Webhooks (Phase 6)

This document describes **gateway → merchant** webhook delivery.
It is separate from Phase 3 **provider → gateway** webhooks (`webhook_events`).

## Architecture

```text
Transaction status change
        │
        ▼
PostgreSQL transactional outbox
  (merchant_webhook_deliveries)
        │
        ▼
Merchant Webhook Worker
        │
        ▼
HTTP POST + HMAC-SHA256
        │
        ▼
Merchant endpoint
```

Inbound provider webhooks remain on:

```http
POST /api/v1/webhooks/providers/:provider
```

Outbound merchant configuration lives under:

```http
/api/v1/merchants/:id/webhook
```

## Configuration (one endpoint per merchant)

| Field | Notes |
|-------|--------|
| `url` | Merchant HTTPS endpoint (HTTP allowed only when `WEBHOOK_REQUIRE_HTTPS=false`) |
| `secret` | `whsec_…` signing secret — returned **once** on create/rotate |
| `status` | `ACTIVE` \| `DISABLED` |

Signing secrets are stored **encrypted at rest** (AES-256-GCM) using
`WEBHOOK_SECRET_ENCRYPTION_KEY` (32-byte key as 64 hex chars or base64).

They are **not** hashed like API keys — the gateway must recover plaintext to sign outbound requests.

## Where does the webhook URL come from?

The webhook URL is **provided by the merchant**.

GO-SAAS does not generate the merchant's webhook URL. The merchant's backend must expose an HTTP endpoint that is reachable by the GO-SAAS gateway.

For example, if the merchant backend is:

```text
https://api.example-merchant.com
```

the merchant may expose:

```http
POST https://api.example-merchant.com/webhooks/payment
```

The merchant then registers that URL using the GO-SAAS webhook configuration endpoint:

```http
POST /api/v1/merchants/:id/webhook
```

Example configuration:

```json
{
  "url": "https://api.example-merchant.com/webhooks/payment"
}
```

After the webhook is configured, GO-SAAS will send payment events to that URL:

```text
GO-SAAS
   │
   │ HTTP POST + HMAC-SHA256
   ▼
https://api.example-merchant.com/webhooks/payment
```

### Local development

If the merchant application is running locally and GO-SAAS can reach the same machine/network, the endpoint may use a local URL when allowed by the environment configuration.

For example:

```text
http://localhost:3000/webhooks/payment
```

If GO-SAAS is running on a different machine or environment, `localhost` refers to the GO-SAAS environment itself and will not reach the developer's machine. In that case, the merchant must expose the local webhook endpoint through a reachable development URL, such as a tunnel or other publicly accessible development endpoint.

The URL must satisfy the gateway's webhook URL policy. In production, HTTPS is required when `WEBHOOK_REQUIRE_HTTPS=true`.

The merchant is responsible for implementing the endpoint, while GO-SAAS is responsible for delivering webhook events to the configured URL.

## Event types

Mapped 1:1 from transaction statuses:

| Status | Event |
|--------|--------|
| CREATED | `payment.created` |
| PENDING | `payment.pending` |
| PAID | `payment.paid` |
| FAILED | `payment.failed` |
| EXPIRED | `payment.expired` |
| CANCELLED | `payment.cancelled` |

## Payload

Versioned envelope (`version: "1"`). Money is integer minor units. Timestamps are RFC3339 UTC.

Event `id` (`evt_…`) is **stable across retries**.

## Signature

Headers:

- `X-PayGate-Event-ID`
- `X-PayGate-Event-Type`
- `X-PayGate-Timestamp` (unix seconds)
- `X-PayGate-Signature` (`sha256=<hex>`)

Signed message:

```text
timestamp + "." + raw_body_bytes
```

HMAC-SHA256 with the webhook secret. Merchants should reject timestamps older than ~5 minutes (replay protection).

## Delivery semantics

**At-least-once.** Merchants must deduplicate on `X-PayGate-Event-ID` / payload `id`.

### State machine

```text
PENDING → PROCESSING → DELIVERED
PROCESSING → PENDING   (retryable)
PROCESSING → FAILED    (non-retryable 4xx)
PROCESSING → DEAD      (max attempts)
```

Stale `PROCESSING` rows (worker crash) are reclaimed after `WEBHOOK_DELIVERY_STALE_AFTER`.

### Retry policy

Retryable: network errors, timeouts, `408`, `429`, `5xx`.

Non-retryable: `400`, `401`, `403`, `404`, `410`, `422` → `FAILED`.

Backoff (approx): 10s, 30s, 2m, 10m, 30m, 2h, 6h then `DEAD`. Max attempts: `WEBHOOK_MAX_ATTEMPTS` (default 8).

### Secret rotation

After `POST .../webhook/rotate`, new deliveries use the new secret immediately.
In-flight/pending deliveries load the **current** secret at send time.

### Disable

`DELETE .../webhook` sets `DISABLED`. No new outbox rows are created. Existing delivery history remains.

## Manual retry

```http
POST /api/v1/merchants/:id/webhook/deliveries/:delivery_id/retry
```

Re-queues `FAILED`, `DEAD`, or `PENDING` deliveries. `DELIVERED` / `PROCESSING` return 409.

## Worker env

| Variable | Default |
|----------|---------|
| `WEBHOOK_DELIVERY_ENABLED` | `true` |
| `WEBHOOK_DELIVERY_INTERVAL` | `5s` |
| `WEBHOOK_DELIVERY_BATCH_SIZE` | `20` |
| `WEBHOOK_DELIVERY_TIMEOUT` | `10s` |
| `WEBHOOK_MAX_ATTEMPTS` | `8` |
| `WEBHOOK_DELIVERY_STALE_AFTER` | `2m` |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | required in production |
| `WEBHOOK_REQUIRE_HTTPS` | `true` in production |

Claiming uses `FOR UPDATE SKIP LOCKED` so multiple app instances are safe.
