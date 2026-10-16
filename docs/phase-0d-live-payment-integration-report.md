# GO-SAAS Phase 0D — Live Payment Integration Verification Report

**Repository:** `pay-gate-backend` (backend) + `pay-gate-dashboard` (frontend, separate repo)
**Branch:** `main` (both repos)
**Backend Go:** `go1.25.0` | **Frontend Node:** `v22.22.1`, npm `9.2.0`
**Overall status:** **PARTIAL** — the full API-level payment flow was verified live against the running applications; interactive browser E2E could not be performed because no browser automation tooling is available in this environment (see Remaining Blockers).

---

## 1. Environment and Runtime Prerequisites

All prerequisites were present and running. No servers were started; existing running instances were reused.

| Prerequisite | Status | Detail |
|--------------|--------|--------|
| PostgreSQL 18 | Running | `0.0.0.0:5432`, database `payment_gateway`, user `payment` (schema migrated through `000025`) |
| Backend | Running | Docker container `pay-gate-app`, healthy, host port `8081` → container `8080` |
| Frontend | Running | Vite dev server (node), `127.0.0.1:5173` |
| Backend health | `200 OK` | `GET http://127.0.0.1:8081/health` and `/health/ready` both `{"status":"ok"}` |
| Frontend root | `200 OK` | `GET http://127.0.0.1:5173/` returns SPA HTML |

### Configuration (secret values masked, not reported)

Backend `.env` (relevant non-secret keys):
- `APP_ENV=development`, `APP_PORT=18080` (on-disk), `APP_BIND_HOST=127.0.0.1`
- `PAYMENT_PROVIDER=mock`, `PAYMENT_SIMULATOR_ENABLED=true`, `SIMULATOR_MERCHANT_ID=90d3fe91-…`
- `CORS_ALLOWED_ORIGINS=http://localhost:5173`
- `FRONTEND_PUBLIC_URL` **not set** in `.env` → docker-compose default `http://localhost:5173` applies
- `DB_HOST=host.docker.internal` (container-internal), `DB_NAME=payment_gateway`
- `ADMIN_API_KEY` present (used internally only, not disclosed)

Frontend `pay-gate-dashboard/.env`:
- `VITE_API_BASE_URL=http://localhost:8081` (frontend → backend origin)
- `VITE_ENABLE_SIMULATOR=false`

**Observed discrepancy (documentation, not a defect):** the on-disk `.env` has `APP_PORT=18080`, but the running backend container is reached on host port `8081` (docker-compose mapping `8081:8080`). The container was apparently started with the compose default; the on-disk `APP_PORT=18080` was not applied to the running instance. This does not affect `payment_url`, which is derived from `FRONTEND_PUBLIC_URL`, not `APP_PORT`.

### Topology (separate origins — preserved, not same-origin)

| Surface | Origin |
|---------|--------|
| Backend API | `http://localhost:8081` |
| Frontend SPA / payment page | `http://localhost:5173` |
| `payment_url` origin | `http://localhost:5173` (via `FRONTEND_PUBLIC_URL` default) |

The backend and frontend run on separate origins. CORS allows `http://localhost:5173`.

---

## 2. Payment Creation Result

A test merchant was provisioned via the documented admin onboarding API (`POST /api/v1/admin/onboarding/merchants`, `X-Admin-Key` from `.env`, used internally only, not disclosed). This returned an active `key_id:secret` API credential. Runtime data only — no application code modified.

**Request:** `POST http://127.0.0.1:8081/api/v1/payments` with `X-API-Key` and `Idempotency-Key`, body `{merchant_order_id: "P0D-ORDER-001", amount: 150000, currency: "IDR", payment_method: "QRIS"}`.

**Response (201):**
- `transaction_id`: `398990c2-…`
- `provider_transaction_id`: `MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e`
- `status`: `PENDING`
- `provider`: `MOCK`
- `payment_url`: `http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e`
- `expired_at`: `2026-10-16T02:45:13.814520207Z`
- `created_at`: `2026-10-16T02:15:13.792252454Z`

Persistence confirmed via `GET /api/v1/payments/:id` (merchant API): row present, `status: PENDING`.

## 3. Actual payment_url Format

```
http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e
```

Format: `{FRONTEND_PUBLIC_URL}/pay/{providerTransactionId}` = `http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e`.

Verified:
- Points to the frontend SPA route `/pay/:identifier` (origin 5173), **not** the backend API endpoint (8081).
- No hardcoded ports in application logic; origin comes from `FRONTEND_PUBLIC_URL` (config `internal/config/config.go:699`).
- Trailing slashes normalised (`resolveFrontendPublicURL` trims; provider trims).
- Matches the documented contract in `docs/integration/payments.md`, `docs/mock-payment-provider.md`, `docs/architecture.md`.

## 4. Public Payment API Response Contract

**Endpoint:** `GET http://127.0.0.1:8081/api/v1/mock-payments/{identifier}` (unauthenticated, no dashboard JWT / merchant API key required).

**Response (200) for the created QRIS payment:**
```json
{
  "success": true,
  "data": {
    "reference": "P0D-ORDER-001",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "status": "PENDING",
    "expired_at": "2026-10-16T02:45:13.81452Z",
    "provider_transaction_id": "MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e",
    "qris_payload": "PAYGATE-MOCK-QRIS:MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e"
  }
}
```

| Field | Value | Matches frontend `MockPaymentResponse` DTO |
|-------|-------|--------------------------------------------|
| `reference` | `P0D-ORDER-001` | ✓ (`merchant_order_id`) |
| `amount` | `150000` | ✓ |
| `currency` | `IDR` | ✓ |
| `payment_method` | `QRIS` | ✓ |
| `status` | `PENDING` | ✓ (`MockPaymentStatus`) |
| `expired_at` | RFC3339 string | ✓ |
| `provider_transaction_id` | `MOCK-TXN-…` | ✓ |
| `qris_payload` | `PAYGATE-MOCK-QRIS:<id>` | ✓ (present for QRIS) |

The public projection intentionally omits `provider`, `payment_url`, and `timeline` — consistent with the frontend DTO, which does not declare them. `provider`/`payment_url` are present in merchant-facing responses (`POST/GET /api/v1/payments`).

## 5. CREATED / CANCELLED Observation

- **CREATED:** Observed as the initial stored transaction state (canonical `transactions` row begins `CREATED`, then moves to `PENDING`). The public mock-payment DTO does not expose `CREATED` — the public `MockPaymentStatus` type is `'PENDING' | 'PAID' | 'FAILED' | 'EXPIRED'`, which is correct because the public mock payment is created directly as `PENDING` and the customer-facing simulation only transitions to `PAID`/`FAILED`.
- **CANCELLED:** **Not observed via the public payment endpoint.** The public mock-payment endpoints (`POST …/success`, `POST …/fail`) only produce `PAID` and `FAILED`. CANCELLED is reachable only via the merchant cancellation flow `POST /api/v1/payments/:id/cancel` (authenticated, works on `CREATED`/`PENDING`). Verified separately below (Section 7). This is by design: the public hosted payment page is a customer-facing simulation; cancellation is a merchant action. The frontend `MockPaymentStatus` type correctly excludes `CANCELLED`.

## 6. Browser Route and Refresh Results

**Browser automation tooling is not available in this environment**, so interactive browser E2E (clicking simulation buttons, observing QRIS render after JS hydration, observing status change after navigation) could not be performed.

HTTP-level verification of the route:
- `GET http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e` → returns SPA HTML ("PayGate Merchant Dashboard"), **not** raw JSON, **not** 404, **not** an invalid redirect.
- Refetching the same URL (simulating a page refresh at the HTTP level) returns the same SPA HTML — the Vite dev server's history fallback serves `index.html` for the `/pay/:identifier` client route.

This confirms the `payment_url` opens the SPA page at the configured frontend origin and survives refresh at the route level. Remaining (manual): open in a real browser, click "Simulate successful/failed payment", confirm the status panel updates and QRIS renders for QRIS.

## 7. Payment Status Transition Results

Separate transactions were used for each scenario (per instructions). All via live API.

| Scenario | Action | Result | Verified via |
|----------|--------|--------|--------------|
| Successful (PAID) | `POST …/mock-payments/MOCK-TXN-b23f…/success` on payment 1 | `status: PAID` | public lookup + merchant API `GET /api/v1/payments/:id` |
| Failed (FAILED) | `POST …/mock-payments/MOCK-TXN-e4b0…/fail` on payment 2 | `status: FAILED` | public lookup |
| Cancelled (CANCELLED) | `POST …/api/v1/payments/:cca4…/cancel` (merchant API) on payment 3 | `status: CANCELLED`, `payment_url` preserved | merchant API |
| Expired (EXPIRED) | Not executed | — | Requires waiting ~30 min for `expired_at` or manipulating `expired_at` (not permitted — see blockers) |

Terminal-state idempotency confirmed: repeating the success transition on an already-PAID payment returns the same state without re-processing (handler guards `previousStatus == to`).

`qris_payload` is preserved across PAID/FAILED transitions (`PAYGATE-MOCK-QRIS:<publicId>`).

**CREATED** is not a terminal public state; the public mock payment is observable as `PENDING` immediately after creation (the mock provider persists `PENDING`).

## 8. Open Payment Result

Dashboard login succeeded (`POST /api/v1/auth/login` with the onboarding owner credentials, used internally only). Bearer token obtained.

**Request:** `GET http://127.0.0.1:8081/api/v1/dashboard/payments/398990c2-…` with `Authorization: Bearer <token>`.

**Result (200):**
- `status`: `PAID`
- `payment_url`: `http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e` — **exactly** matches the `payment_url` returned by the creation API.
- `provider`: `MOCK`
- `timeline`: 2 events derived from persisted timestamps only — `CREATED` (`created_at`) and `PAID` (`paid_at`). No events fabricated (matches `buildPaymentTimeline`, `docs` contract).

Same-origin validation: the `payment_url` origin (`http://localhost:5173`) matches `CORS_ALLOWED_ORIGINS=http://localhost:5173` and `FRONTEND_PUBLIC_URL`, so a valid payment URL would not be rejected by the frontend's same-origin guard.

## 9. Dashboard Consistency Result

**Request:** `GET http://127.0.0.1:8081/api/v1/dashboard/overview` (Bearer JWT), default period = current UTC calendar month `[2026-10-01, 2026-11-01)`.

**Result (200):**
- `payments.total`: 3 (the 3 E2E test payments)
- `payments.paid`: 1
- `payments.failed`: 1
- `payments.cancelled`: 1
- `payments.pending`: 0
- `payments.unsuccessful_transactions`: 2 (= FAILED 1 + CANCELLED 1 + EXPIRED 0)
- `revenue.amount`: 150000 (the single PAID payment, IDR minor units)
- `revenue.currency`: `IDR`
- `refunds.count`: 0

All consistent: `unsuccessful_transactions = FAILED + EXPIRED + CANCELLED`; revenue = `SUM(amount)` where `status = PAID` and `paid_at ∈ [from, to)`; single currency IDR (no cross-currency aggregation).

## 10. QRIS Verification Result

- `payment_method` = `QRIS` on creation.
- Public response includes `qris_payload`: `PAYGATE-MOCK-QRIS:MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e`.
- `qris_payload` present and unchanged after PAID and FAILED transitions.
- Value is explicitly mock data (`PAYGATE-MOCK-QRIS:<publicId>`, documented in `docs/mock-payment-provider.md`), not production-valid. QRIS is the only supported payment method (`SupportedPaymentMethods = {"QRIS"}`).
- Frontend renders QRIS section only when `payment_method` is QRIS and uses the backend `qris_payload` (confirmed by source inspection of `PublicPaymentPage.tsx` and `MockPaymentQrCode`).

## 11. Automated Test Results

**Backend:**
| Command | Result |
|---------|--------|
| `go test -count=1 ./...` | 11/11 packages `ok` |
| `go vet ./...` | Clean (exit 0) |

**Frontend:**
| Command | Result |
|---------|--------|
| `npm run typecheck` | Pass (exit 0) |
| `npm run lint` | Pass (exit 0) |
| `npm test` | 255/255 pass |
| `npm run build` | Pass (exit 0; pre-existing chunk-size warning only) |

No application code was modified in Phase 0D, so no new regression tests were added. BE-009 (path-only URL in `GetPayment`) was fixed in Phase 0B with regression tests, which pass here.

## 12. Confirmed Defects and Fixes

No new defects were found in Phase 0D. The live system behaves consistently with the Phase 0B source-level audit:

| ID | Finding | Severity | Disposition |
|----|---------|----------|-------------|
| BE-009 (Phase 0B) | `GetPayment` could emit path-only URL when frontend origin empty | Low | Fixed in Phase 0B; confirmed not an issue in live flow — `config.Load` requires a non-empty `FRONTEND_PUBLIC_URL` for mock provider, and live `payment_url` is absolute |

No defects introduced by Phase 0D activity. Runtime data created (test merchant + 3 payments) is verifiable in the database but is not a code change.

## 13. Tests Not Performed and Why

| Test | Reason |
|------|--------|
| Interactive browser E2E (open `payment_url`, click simulate, observe QRIS render, refresh with JS router) | No browser automation tooling available in this environment; HTTP fetch confirmed the route serves SPA HTML but cannot exercise JavaScript/hydration |
| Expired (EXPIRED) transition | Would require waiting ~30 minutes for `expired_at` or manipulating `expired_at`; neither permitted |
| PostgreSQL `*_pg_test.go` integration suites | No `TEST_DATABASE_URL` / disposable test database available; suites skip automatically (`TEST_DATABASE_URL not set`) |
| Dashboard UI interaction (click "Open Payment" button) | No browser; verified the equivalent API contract (`GET /api/v1/dashboard/payments/:id` returns same `payment_url`) |

## 14. Remaining Blockers

1. **Browser E2E** — Open `http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e` in a real browser; confirm SPA loads (not JSON/404), refresh survives, click "Simulate successful payment" / "Simulate failed payment", confirm status panel and QRIS update. Required to complete the "browser-renderable page" acceptance criterion.
2. **Expired (EXPIRED) flow** — Verify the public page handles `EXPIRED` (either wait for `expired_at` or use a fixture with a past `expired_at` in a non-user database).
3. **Same-origin reverse-proxy deployment** — If SPA and API are fronted on one origin in production, backend route `GET /pay/:identifier` may shadow the SPA. Documented in Phase 0B (BE-010); mitigation is to keep origins separate (current design).

## 15. Files Changed

**Backend:** none in Phase 0D (no application code modified). Pre-existing Phase 0B changes remain: `internal/service/mock_payment_provider.go`, `internal/service/mock_payment_provider_test.go` (BE-009 fix + regression tests), plus pre-existing uncommitted work (`.env.example`, `cmd/server/main.go`, Phase 8E/10L, etc.) — all preserved untouched.

**Frontend:** none in Phase 0D. Pre-existing uncommitted work preserved untouched.

**Report (this file):**
- `docs/phase-0d-live-payment-integration-report.md` — new (did not previously exist).

## 16. Exact Next Action

Complete interactive browser E2E:
1. Open `http://localhost:5173/pay/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e` in a real browser.
2. Confirm the PayGate payment page loads (SPA, not raw JSON/404).
3. Refresh; confirm the SPA route still loads.
4. Confirm the public API request `GET http://localhost:8081/api/v1/mock-payments/MOCK-TXN-b23f3c6cbb21f303ab2f6d35e5bb8c3e` succeeds and displays amount/currency/method/status.
5. Confirm QRIS renders only for `payment_method === QRIS` and uses `qris_payload`.
6. Click "Simulate successful payment"; confirm status becomes PAID in the UI.
7. (Optional, separate payment) Click "Simulate failed payment"; confirm FAILED.
8. Confirm unknown identifiers show a safe error state.

Once browser E2E passes, overall status moves to PASS.
