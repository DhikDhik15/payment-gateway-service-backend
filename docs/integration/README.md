# Merchant Integration Documentation

This documentation helps external merchant applications integrate with the Payment Gateway API.

## Overview

The Payment Gateway provides a REST API for merchants to create payments, retrieve payment status, manage refunds, and receive webhook notifications when payment events occur.

## Quick Start

### 1. Obtain API Credentials

Contact the Payment Gateway administrator to receive your API key. The credential format is:

```
pk_<hex>:sk_<hex>
```

### 2. Create a Payment

```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "X-API-Key: pk_xxx:sk_xxx" \
  -H "Idempotency-Key: order-123-payment" \
  -H "Content-Type: application/json" \
  -d '{
    "merchant_order_id": "ORDER-2026-0001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS"
  }'
```

### 3. Redirect Customer

The response contains a `payment_url`. Redirect the customer to this URL to complete payment.

### 4. Receive Webhook

When the payment status changes, the gateway sends a webhook to your configured endpoint.

### 5. Retrieve Payment Status

```bash
curl http://localhost:8080/api/v1/payments/{transaction_id} \
  -H "X-API-Key: pk_xxx:sk_xxx"
```

## Documentation Index

- [Overview](overview.md) — Architecture and integration flow
- [Authentication](authentication.md) — API key authentication
- [Payments](payments.md) — Create and retrieve payments
- [Idempotency](idempotency.md) — Idempotency key behavior
- [Payment Status](payment-status.md) — Payment lifecycle states
- [Webhooks](webhooks.md) — Webhook events and payloads
- [Webhook Signature](webhook-signature.md) — HMAC signature verification
- [Cancellation](cancellation.md) — Cancel payments
- [Refunds](refunds.md) — Create and manage refunds
- [Errors](errors.md) — Error codes and handling
- [Rate Limits](rate-limits.md) — Rate limiting behavior
- [Security](security.md) — Security best practices
- [Development](development.md) — Development environment setup
- [End-to-End Example](end-to-end.md) — Complete integration walkthrough

## API Reference

The complete machine-readable API specification is available at [`docs/api/openapi.yaml`](../api/openapi.yaml).
