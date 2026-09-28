# Mock payment provider (development only)

`PAYMENT_PROVIDER=mock` uses the same backend provider projection while the
React frontend owns the customer-facing payment page.

`FRONTEND_PUBLIC_URL` is the browser-facing origin that serves the frontend
route `/pay/:identifier`. It is required when the mock provider is enabled and
is intentionally independent from `APP_PORT` and the backend API origin. The
backend constructs the returned URL as:

```text
{FRONTEND_PUBLIC_URL}/pay/{provider-transaction-id}
```

`MOCK_PAYMENT_BASE_URL` is retained only as a deprecated compatibility alias
for older deployments. New configurations must use `FRONTEND_PUBLIC_URL`.

The public read endpoints require no dashboard JWT or merchant API key:

- `GET /pay/:identifier` (backend JSON provider projection)
- `GET /api/v1/mock-payments/:identifier` (JSON API consumed by the React page)

The React frontend must receive `FRONTEND_PUBLIC_URL` as the origin of its
returned PaymentURL. It then calls the backend API origin, normally configured
separately as `VITE_API_BASE_URL`, at `/api/v1/mock-payments/:identifier`.

The public simulation actions are `POST /api/v1/mock-payments/:identifier/success`
and `/fail`. The random `MOCK-TXN-<32-bit-random-hex>` identifier is the
development payment capability; it does not authorize merchant/dashboard APIs.
They update `mock_payments` then submit a signed MOCK event to the ordinary
webhook service, which is responsible for the canonical transaction transition
and webhook-event idempotency. Do not enable the mock provider in production.

For QRIS, `qris_payload` is transaction-specific (`PAYGATE-MOCK-QRIS:<id>`),
not a shared QR image.
