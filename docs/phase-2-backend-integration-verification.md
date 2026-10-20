# GO-SAAS Phase 2 — Backend Integration Verification Report

**Backend repository:** `pay-gate-backend` — branch `main`, commit `fd49157` plus the verification changes listed in §15 (uncommitted, preserved)
**Frontend repository:** `pay-gate-dashboard` — branch `main`, commit `d25a232` plus pre-existing uncommitted Phase 1 work (not modified)
**Verification date:** 2026-10-20
**Backend Go toolchain:** `go1.25.0` (`go.mod` declares `go 1.25`; `GOTOOLCHAIN=auto`)
**Frontend runtime:** Node `v22.22.1`
**Overall status:** **PARTIAL**

> Source-level contract verification, automated backend tests, and live API-level end-to-end verification **PASS**. Interactive browser E2E is **BLOCKED** because no desktop browser is connected to this session. The overall result is therefore **PARTIAL**, not a full PASS. See §6, §13, and §14.

---

## 1. Environment and Runtime Prerequisites

All prerequisites were already running; existing services were reused. No server was started for this verification, no deployment was performed, and no production configuration was changed.

| Prerequisite | Status | Evidence |
|---|---|---|
| PostgreSQL | Available | Backend `/health/ready` returned `200` with `{"status":"ok"}` |
| Backend container | Healthy | Docker container `pay-gate-app`, host `127.0.0.1:8081` → container `8080` |
| Backend liveness | `200` | `GET http://127.0.0.1:8081/health` → `{"success":true,"data":{"status":"ok"}}` |
| Backend readiness | `200` | `GET http://127.0.0.1:8081/health/ready` → `{"success":true,"data":{"status":"ok"}}` |
| Frontend dev server | Running | `GET http://127.0.0.1:5173/` → `200`, Vite SPA HTML |
| Test merchant | ACTIVE | Disposable merchant `a14c8494-91ca-4a77-b0ef-e91e393d168a` |
| Dashboard login | `200` | OWNER login succeeded; access token TTL reported as 900 seconds |

### Relevant runtime configuration (non-secret values only)

| Variable | Running value |
|---|---|
| `APP_ENV` | `development` |
| `APP_PORT` | `8080` (container; published `8081`) |
| `PAYMENT_PROVIDER` | `mock` |
| `FRONTEND_PUBLIC_URL` | `http://localhost:5173` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` |
| `PAYMENT_EXPIRY_ENABLED` | `true` |
| `PAYMENT_SIMULATOR_ENABLED` | `true` (development only) |

The configuration contract is documented in `.env.example:7-13` (frontend origin independent from `APP_PORT`), `.env.example:94-99` (mock provider and expiry worker), and `.env.example:222-228` (explicit CORS allowlist, no wildcard).

### Security and scope constraints honored

- No production env file was modified.
- No secret, access token, API key, refresh token, or webhook secret is reproduced in this report.
- No real external payment provider or webhook was contacted; only the local mock provider was used.
- Authenticated dashboard routes remain JWT-protected; the public mock-payment routes remain intentionally unauthenticated for the hosted payment page.
- CORS remains an exact-origin allowlist with credentials enabled only for allowed origins.
- The frontend working tree, including uncommitted Phase 1 changes, was not modified.

### CORS preflight evidence

| Case | Result | Headers |
|---|---|---|
| Allowed origin `http://localhost:5173` → `OPTIONS /api/v1/auth/refresh` | `204 No Content` | `Access-Control-Allow-Origin: http://localhost:5173`, `Access-Control-Allow-Credentials: true`, `Vary: Origin` |
| Disallowed origin `http://malicious.example` → same preflight | `404 Not Found` | No `Access-Control-Allow-Origin`; no credentials grant |

Implementation: `internal/middleware/cors.go:23-46`; configuration validation: `internal/config/config.go:855-876`; global middleware registration: `cmd/server/main.go:139`.

---

## 2. Payment Creation Result

A disposable payment was created through the authenticated dashboard API, which shares the same payment service and provider path as the merchant API.

**Request:** `POST http://127.0.0.1:8081/api/v1/dashboard/payments`

- `Authorization: Bearer <dashboard access token>`
- `Idempotency-Key: <unique key>`
- Body:

```json
{
  "merchant_order_id": "P2-ORDER-1792464272",
  "amount": 125000,
  "currency": "IDR",
  "payment_method": "QRIS"
}
```

**Response:** `201 Created`

| Field | Value |
|---|---|
| `transaction_id` | `67604d0a-ddf6-4c92-b540-3b2a01b9bf3f` |
| `merchant_order_id` | `P2-ORDER-1792464272` |
| `amount` | `125000` (integer IDR minor units) |
| `currency` | `IDR` |
| `payment_method` | `QRIS` |
| `provider` | `MOCK` |
| `provider_transaction_id` | `MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` |
| `status` | `PENDING` |
| `payment_url` | `http://localhost:5173/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` |
| `expired_at` | `2026-10-20T03:14:32.927302423Z` |
| `created_at` | `2026-10-20T02:44:32.924495Z` |

### Idempotency verification

| Scenario | Result |
|---|---|
| Same key + identical payload | `201`, same `transaction_id`, no duplicate transaction |
| Same key + changed payload (`amount` changed) | `409`, `IDEMPOTENCY_KEY_REUSED` |
| Dashboard list after creation | One transaction, `PENDING`, then `PAID` after transition |

Implementation: `internal/service/payment_service.go:144-218` (reserve/replay/conflict handling), `internal/handler/dashboard_payment_handler.go:478-511` (error mapping). Provider URL construction: `internal/service/mock_payment_provider.go:53-54,80-90`.

### Persistence and ownership

- Dashboard reads use a merchant-scoped transaction lookup; a different merchant's UUID is indistinguishable from a missing transaction (`internal/repository/transaction_repository.go:258-260`; `internal/handler/dashboard_payment_handler.go:287-293`).
- The transaction ID and provider-transaction ID are distinct values by design: dashboard paths use the transaction UUID; the public payment page uses the provider capability ID.

---

## 3. Actual `payment_url` Format

```text
http://localhost:5173/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb
```

This matches the documented contract:

```text
{FRONTEND_PUBLIC_URL}/pay/{providerTransactionId}
```

Evidence:

| Check | Result | Evidence |
|---|---|---|
| Origin is the frontend SPA | PASS — `http://localhost:5173` | `FRONTEND_PUBLIC_URL` runtime config; `.env.example:8-11` |
| Not the backend API origin | PASS — backend is `8081`, URL uses `5173` | Live creation response |
| Path matches frontend route | PASS — `/pay/:identifier` | Frontend `src/routes/index.tsx:70-73` |
| Identifier preserved | PASS — exact `provider_transaction_id` in last segment | Live response; `mock_payment_provider.go:89` |
| Trailing slash normalized | PASS | `config.go:711`; `mock_payment_provider.go:54` |
| Scheme restricted to HTTP(S) | PASS after fix | `config.go:716-721`; regression tests in §11 |
| No hard-coded application ports | PASS | Origin comes only from configuration; `cmd/server/main.go:1088-1097` |
| Frontend opens the URL as opaque backend data | PASS | `PaymentDetailPage.tsx:94-99`; `src/lib/utils/url.ts` |

**Important deployment requirement:** the frontend validates `payment_url` against `window.location.origin` before offering the "Open payment page" action (`PaymentDetailPage.tsx:46-53,94-99`). Therefore the browser origin used to open the dashboard must exactly match `FRONTEND_PUBLIC_URL` (including `localhost` vs `127.0.0.1`). A mismatch suppresses the button even when both names resolve to the same server. This is a configuration prerequisite, not a backend URL-construction defect.

---

## 4. Public Payment API Response Contract

**Endpoint:** `GET http://127.0.0.1:8081/api/v1/mock-payments/{identifier}`

This endpoint intentionally requires no dashboard JWT and no merchant API key (`cmd/server/main.go:517-521`). The frontend's public client explicitly strips authorization headers and cookies (`src/lib/api/client.ts:371-386`).

**Observed response:** `200 OK`

```json
{
  "success": true,
  "data": {
    "reference": "P2-ORDER-1792464272",
    "amount": 125000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "status": "PENDING",
    "expired_at": "2026-10-20T03:14:32.927302Z",
    "provider_transaction_id": "MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb",
    "qris_payload": "PAYGATE-MOCK-QRIS:MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb"
  },
  "meta": {
    "request_id": "<omitted>"
  }
}
```

| Field | Backend type / source | Frontend expectation | Result |
|---|---|---|---|
| `reference` | `string`, `MockPayment.MerchantOrderID` | `MockPaymentResponse.reference` | Match |
| `amount` | `int64` integer minor units | `number` | Match |
| `currency` | `string` (`IDR`) | `string` | Match |
| `payment_method` | `string` (`QRIS`) | `string` | Match |
| `status` | `model.TransactionStatus` | `PENDING \| PAID \| FAILED \| EXPIRED` | Match for currently reachable public states; see §5 |
| `expired_at` | RFC3339 timestamp | `string` | Match |
| `provider_transaction_id` | `string` | `string` | Match |
| `qris_payload` | `string`, QRIS only, `omitempty` | optional `string \| null` | Match |

The public projection intentionally omits `provider`, `payment_url`, `timeline`, and gateway-internal IDs. This is by design, not a contract failure: `internal/handler/mock_payment_handler.go:28-37,69-73`; frontend DTO `src/types/backend.ts:54-66`.

### Error behavior

| Request | Result |
|---|---|
| Unknown identifier | `404`, `TRANSACTION_NOT_FOUND`, `"Payment not found"` |
| Empty identifier | Not routable; no lookup performed |
| Public read without auth | `200` for a valid identifier |
| Public read after PAID | `200`, `status: PAID` |
| Public read after FAILED | `200`, `status: FAILED` |

Regression coverage: `internal/handler/mock_payment_handler_test.go:84-180` (real 404, public schema, no gateway ID leakage, non-QRIS payload omission, terminal status).

---

## 5. CREATED / CANCELLED Observation

### CREATED

`CREATED` is a canonical transaction state before the provider call (`internal/model/transaction.go:13-20`; `internal/service/payment_service.go:273-298`). The public mock provider persists its own record directly as `PENDING` (`internal/service/mock_payment_provider.go:73-77`), so the public page normally observes `PENDING` immediately after creation. This is consistent with the frontend `MockPaymentStatus` union, which does not include `CREATED` (`src/types/backend.ts:52`).

### CANCELLED

`CANCELLED` is not produced by the public mock-payment simulation endpoints; those only expose success (`PAID`) and failure (`FAILED`) transitions (`internal/handler/mock_payment_handler.go:46-50`). Cancellation belongs to the authenticated merchant API/canonical transaction lifecycle. The frontend public DTO correctly excludes `CANCELLED`.

### Current public status set

The mock repository creates `PENDING` and permits transitions only from `PENDING` to a requested terminal state (`internal/repository/mock_payment_repository.go:64-83`). The handlers request only `PAID` or `FAILED`. Consequently, the currently reachable public API statuses are:

```text
PENDING, PAID, FAILED
```

`EXPIRED` is a frontend-derived presentation state when `expired_at` has passed (`src/lib/mock-payment/contract.ts:41-59`), and the canonical transaction can be moved to `EXPIRED` by the backend expiry worker. This is a design split worth retaining in contract tests; see the expiry risk in §14.

---

## 6. Browser Route and Refresh Results

### HTTP-level route verification

| Request | Observed |
|---|---|
| `GET http://127.0.0.1:5173/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` | `200`, `Content-Type: text/html`, Vite SPA shell |
| Same frontend URL after a second request | `200`, same SPA shell (history fallback works at the HTTP level) |
| `GET http://127.0.0.1:8081/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` | `200`, JSON provider projection — not the browser page |

The JSON response on the backend `/pay/:identifier` route is intentional and documented (`docs/mock-payment-provider.md:18-21`; `cmd/server/main.go:495-497`). The returned `payment_url` points to the frontend origin, not this backend JSON route.

### Browser E2E

**BLOCKED.** A browser retry returned:

```text
[browser.disconnected] No desktop browser is connected to this session.
```

Therefore the following were **not** observed in a real browser:

- JavaScript hydration of `PublicPaymentPage`
- QR code rendering from `qris_payload`
- "Simulate successful payment" / "Simulate failed payment" button interaction
- Client-side countdown rendering
- Status refresh/polling after a mutation
- Dashboard "Open payment page" button click

The HTTP-level route test proves the SPA HTML is served at the required URL; it does **not** prove JavaScript rendering or interaction. This is the reason for the overall PARTIAL classification.

---

## 7. Payment Status Transition Results

Two disposable payments were used so that success and failure were verified independently.

| Scenario | Transaction | Action | Final status | Evidence |
|---|---|---|---|---|
| Success | `67604d0a-ddf6-4c92-b540-3b2a01b9bf3f` | `POST /api/v1/mock-payments/MOCK-TXN-8d…/success` | `PAID` | Public API, dashboard detail, overview |
| Failure | `acf3e10b-72c8-41e9-9995-e53867bdbd65` | `POST /api/v1/mock-payments/MOCK-TXN-6c3a…/fail` | `FAILED` | Public API, dashboard detail, overview |

### Success path

- Public response after success: `status: PAID`, same `reference`, same `provider_transaction_id`, `qris_payload` preserved.
- Dashboard detail after success:
  - `status: PAID`
  - `provider: MOCK`
  - `paid_at: 2026-10-20T02:45:58.328908Z`
  - `timeline`:
    - `CREATED` at `2026-10-20T02:44:32.924495Z`
    - `PAID` at `2026-10-20T02:45:58.328908Z`

### Failure path

- Public response after failure: `status: FAILED`, `qris_payload` preserved.
- Dashboard detail after failure:
  - `status: FAILED`
  - `paid_at: null`
  - `timeline`: only `CREATED` (no fabricated failure event)

### Terminal-state behavior

| Case | Result |
|---|---|
| Repeat success on an already-`PAID` payment | `200`, remains `PAID`, no duplicate transition |
| Repeat failure on an already-`FAILED` payment | `200`, remains `FAILED`, no duplicate transition |
| Request success on a `FAILED` payment | `400`, `INVALID_TRANSACTION_STATE` |
| Transition attempt after `expired_at` | `400`, `INVALID_TRANSACTION_STATE`, "Payment has expired" |

Implementation: `internal/handler/mock_payment_handler.go:76-107`; repository conditional transition and expired guard: `internal/repository/mock_payment_repository.go:64-83`; canonical state machine: `internal/model/transaction.go:22-56`. Regression coverage: `internal/handler/mock_payment_handler_test.go:56-83`.

### Expiry

The running backend started the expiry worker (`PAYMENT_EXPIRY_ENABLED=true`, 30-second interval). The worker transitions **canonical transactions** from `PENDING` to `EXPIRED`; it does not update the provider-side `mock_payments` row (`internal/service/expiry_service.go:44-65`). A full live wait for the 30-minute provider expiry was not performed. Automated expiry/transition tests pass, but the public API's post-expiry status is covered by the risk below rather than by a live timed observation.

**Confirmed behavioral limitation (risk R-1, not counted as a release-blocking source defect):** after `expired_at`, `GET /api/v1/mock-payments/:identifier` can continue to return the persisted `PENDING` status because the public handler returns the provider row unchanged (`internal/handler/mock_payment_handler.go:69-73`; `internal/repository/mock_payment_repository.go:49-54`). The frontend compensates by deriving an effective `EXPIRED` presentation state (`src/lib/mock-payment/contract.ts:41-59`), and transition attempts are rejected as expired. Backend expiry convergence for the canonical transaction depends on `PAYMENT_EXPIRY_ENABLED=true`.

---

## 8. Open Payment Result

Dashboard login succeeded with the disposable OWNER account and the payment was retrieved through the authenticated dashboard endpoint.

**Request:** `GET http://127.0.0.1:8081/api/v1/dashboard/payments/67604d0a-ddf6-4c92-b540-3b2a01b9bf3f`

**Result:** `200 OK`

| Field | Value |
|---|---|
| `transaction_id` | `67604d0a-ddf6-4c92-b540-3b2a01b9bf3f` |
| `merchant_order_id` | `P2-ORDER-1792464272` |
| `provider` | `MOCK` |
| `provider_transaction_id` | `MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` |
| `payment_url` | `http://localhost:5173/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` |
| `status` | `PAID` |
| `paid_at` | `2026-10-20T02:45:58.328908Z` |
| `timeline` | `CREATED`, `PAID` |

The dashboard `payment_url` is exactly the same value returned at creation. This confirms there is no URL rewriting between creation and detail retrieval, and that the backend supplies the frontend origin rather than the API origin.

The dashboard route is JWT-protected and merchant-scoped (`cmd/server/main.go:699-702,789-794`; `internal/middleware/dashboard_auth.go:33-131`). A request with an expired access token returned `401 INVALID_CREDENTIALS`, confirming the route does not fall back to the public path.

### Field consistency

| Field | Backend dashboard response | Frontend type | Result |
|---|---|---|---|
| `provider` | `"MOCK"` | optional `string \| null` | Present and valid |
| `provider_transaction_id` | `MOCK-TXN-…` | optional `string \| null` | Present and valid |
| `payment_url` | frontend `/pay/` URL | optional `string \| null` | Present and valid HTTP(S) |
| `timeline` | array of persisted timestamps | required array | Present |
| `paid_at` | timestamp for PAID, null for FAILED | optional `string \| null` | Correct |

Frontend types: `src/types/backend.ts:36-50,135-148`. Timeline construction: `internal/handler/dashboard_payment_handler.go:351-362`; DTO: `internal/model/dashboard_overview.go:57-81`.

---

## 9. Dashboard Consistency Result

### Overview after both payments

`GET /api/v1/dashboard/overview` returned `200` for the default UTC calendar-month period `[2026-10-01, 2026-11-01)`:

```json
{
  "payments": {
    "total": 2,
    "created": 0,
    "pending": 0,
    "paid": 1,
    "failed": 1,
    "expired": 0,
    "cancelled": 0,
    "unsuccessful_transactions": 1
  },
  "revenue": {
    "amount": 125000,
    "currency": "IDR"
  },
  "refunds": {
    "count": 0,
    "amount": 0,
    "currency": "IDR"
  }
}
```

- `payments.total = PAID + FAILED = 2`.
- `payments.unsuccessful_transactions = FAILED + EXPIRED + CANCELLED = 1`.
- `revenue.amount = 125000`, equal to the one `PAID` amount; the failed 55,000 payment is excluded.
- `recent_payments` contained both transactions with statuses `FAILED` and `PAID`, newest first.
- Period counts use `created_at`; revenue uses `status='PAID'` and `paid_at` in the half-open window. Source: `internal/repository/dashboard_overview_repository.go:38-118`; period resolution: `internal/service/dashboard_overview_service.go:33-102`.

### Analytics after both payments

`GET /api/v1/dashboard/analytics` returned `200`:

- `summary.total_revenue = 125000`
- `summary.successful_payments = 1`
- `summary.units_sold = 0`
- `summary.average_order_value = null`
- `currency = IDR`
- `interval = day`
- `top_products = []`

This is consistent: the payments were created directly rather than through order-backed checkout, so there are no order-item units or product snapshots to report. Revenue is still gross paid transaction revenue. Source: `internal/service/dashboard_analytics_service.go:31-87`.

### Dashboard payment list filters

Live checks:

| Query | Result |
|---|---|
| `status=PAID` | `200`, total `1` |
| `status=FAILED` | `200`, total `1` |
| `search=P2-ORDER` | `200`, total `1` |
| `merchant_order_id=P2-FAIL-1792466017` | `200`, total `1` |

Supported filters: `page`, `limit`, `status`, `merchant_order_id`, `payment_method`, `search`, `created_from`, `created_to` (`internal/handler/dashboard_payment_handler.go:364-442`; `internal/model/transaction.go:110-126`; `internal/repository/transaction_repository.go:443-505`).

The list is deterministically ordered by `(created_at DESC, id DESC)` and the order is not client-controlled (`internal/repository/transaction_repository.go:400,502`).

### Confirmed list capability gaps (not defects)

The following are **not implemented** and remain product/API gaps:

| Gap | Evidence | Impact |
|---|---|---|
| Client-selected sorting | No `sort`/`order` field in `TransactionListFilter` (`internal/model/transaction.go:110-126`) or parser (`internal/handler/dashboard_payment_handler.go:364-442`) | Server-side pagination cannot be sorted by amount/date/status; fixed newest-first only |
| Amount-range filter | No `amount_min`/`amount_max` in filter or SQL | UI cannot filter by amount |
| Provider filter | No `provider` filter despite `provider` being present in responses | UI cannot select provider; current provider is effectively `MOCK` in this environment |

These gaps are **not classified as defects** because the frontend Phase 1 implementation does not request them (`src/hooks/paymentsQuery.ts:7-16`) and the current API contract does not promise them. They would require a scope/product decision before implementation.

---

## 10. QRIS Verification Result

| Check | Result |
|---|---|
| `payment_method` on creation | `QRIS` |
| `qris_payload` on public read | `PAYGATE-MOCK-QRIS:MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` |
| `qris_payload` after PAID | Present and unchanged |
| `qris_payload` after FAILED | Present and unchanged |
| Non-QRIS payload omission | Covered by regression test; `qris_payload` omitted |
| QR code rendered in browser | **Not verified** — browser blocked |
| Real EMVCo/QRIS compliance | **No** — this is a development marker, explicitly documented in `docs/mock-payment-provider.md:34-35` |

The frontend renders the QR only from the backend-provided payload and does not fabricate a replacement (`src/pages/PublicPaymentPage.tsx:180-184`; frontend contract notes). The value is not a production payment payload.

---

## 11. Automated Test Results

All backend commands were run from the backend repository after the changes in §12.

| Command | Result | Exit code |
|---|---|---|
| `gofmt -l .` | No files listed (formatted) | `0` |
| `go vet ./...` | Clean, no diagnostics | `0` |
| `go test -count=1 ./...` | 11 tested packages `ok`; 2 packages with no test files; no failures | `0` |

Tested packages: `cmd/server`, `internal/audit`, `internal/config`, `internal/handler`, `internal/middleware`, `internal/model`, `internal/ratelimit`, `internal/repository`, `internal/service`, `internal/ssrf`, `pkg/database`. `pkg/response` and `swagger` have no test files.

### Regression tests added or strengthened

| File | Coverage |
|---|---|
| `internal/config/mock_payment_base_url_test.go:59-87` | Rejects `ftp://`, `file://`, `javascript://` for `FRONTEND_PUBLIC_URL`; trims trailing slash |
| `internal/handler/mock_payment_handler_test.go:84-180` | Real unknown-identifier `404`; exact public DTO fields; no gateway-ID leakage; non-QRIS payload omission; terminal status returned |
| `internal/middleware/cors_test.go:148-193` | Allowed preflight grants exact origin + credentials; disallowed preflight receives no CORS grant |

The prior handler test named `TestMockPaymentPublicUnknown` asserted a sentinel error against itself and did not exercise the HTTP handler; it was replaced with an actual request/response test. This was a test-quality gap, not a production behavior defect.

### Test suite scope

`go test ./...` is the unit/in-memory suite. PostgreSQL integration tests (`*_pg_test.go`) require `TEST_DATABASE_URL`; they were not separately enabled for this run. The live API exercise in §2–§9 provided database-backed runtime evidence for the payment flow.

### Frontend gates

Frontend gates were **not rerun** in this backend verification. The frontend Phase 1 report claims `typecheck`, `lint`, 266/266 tests, and build pass; that claim was not independently reproduced here. The frontend source was inspected read-only for contract and origin-guard behavior.

---

## 12. Confirmed Defects and Fixes

### Confirmed defect — BE-P2-01 (Low, fixed)

**Defect:** `FRONTEND_PUBLIC_URL` accepted any absolute URL scheme. `url.ParseRequestURI` plus host checks allowed values such as `ftp://`, `file://`, or `javascript://`, even though the frontend only opens HTTP(S) payment URLs. This could produce a startup-valid configuration whose `payment_url` is unusable or unsafe for the browser.

**Fix:** `internal/config/config.go:716-721` now rejects any scheme other than `http` or `https` (case-insensitive), with the existing absolute-URL, no-userinfo, no-query, no-fragment validation retained.

**Regression tests:** `internal/config/mock_payment_base_url_test.go:59-74` (three rejected schemes) and `:76-87` (trailing-slash normalization).

**Result:** fixed and covered; all quality gates pass.

### No other confirmed runtime or contract defects

| Area | Finding | Classification |
|---|---|---|
| Payment URL | Correct frontend origin, identifier, trailing-slash handling | Verified, no defect |
| Public DTO | Matches frontend fields; no internals leaked | Verified, no defect |
| `provider` | Present as `MOCK` in dashboard/merchant responses | Verified, no defect |
| `timeline` | Real timestamps only; PAID only when `paid_at` set | Verified, no defect |
| `unsuccessful_transactions` | Equals failed + expired + cancelled | Verified, no defect |
| `revenue` | Paid amount with `paid_at` in window | Verified, no defect |
| QRIS | Correct mock payload for QRIS, omitted otherwise | Verified, no defect |
| CORS | Exact origin, credentials only when allowed | Verified, no defect |
| Idempotency | Replay and conflict behavior correct | Verified, no defect |
| Auth | Dashboard routes reject expired/missing tokens; public routes remain public | Verified, no defect |

### Documentation gap — OpenAPI is partial

`docs/api/openapi.yaml` is a hand-maintained, partial specification: it declares 25 paths (`docs/api/openapi.yaml:59` onward), but does not document several routes used by this integration, including:

- `POST /api/v1/auth/login`, `/logout`, `/refresh`, `GET /auth/me`
- `GET /api/v1/dashboard/overview`
- `GET /api/v1/dashboard/analytics`
- `GET/POST /api/v1/dashboard/payments`, `GET /dashboard/payments/{payment_id}`
- `POST /api/v1/mock-payments/{identifier}/success` and `/fail`
- public `GET /api/v1/mock-payments/{identifier}` and `GET /pay/{identifier}`

This is a documentation/contract-publication gap, not a runtime defect. The generated Swagger annotations are also noted as stale in places in the frontend contract notes. Rewriting or auto-generating the full spec was outside this verification scope and was not attempted.

---

## 13. Tests Not Performed and Why

| Test | Reason |
|---|---|
| Interactive browser E2E (SPA hydration, QR render, simulate buttons, countdown, open-payment click) | No desktop browser connected; browser tool returned `[browser.disconnected]` |
| Live 30-minute expiry and public status convergence | Would require waiting for the provider expiry window or altering timestamps; not performed in this pass |
| Live merchant cancellation (`POST /api/v1/payments/:id/cancel`) | Not needed to verify the dashboard/public success/failure path; cancellation remains covered by source-level state-machine tests |
| Full PostgreSQL integration suite (`*_pg_test.go`) | Requires `TEST_DATABASE_URL` and a disposable test database; not enabled for this run |
| Frontend typecheck/lint/test/build | Backend verification scope; frontend Phase 1 report claims pass, but was not rerun here |
| Production/staging deployment E2E | Prohibited by scope; no deployment or production request was made |

The omitted tests do not invalidate the API-level evidence in this report, but they are why the overall verdict is PARTIAL rather than PASS.

---

## 14. Remaining Blockers and Risks

### Blockers

| ID | Blocker | Required resolution |
|---|---|---|
| B-1 | Browser E2E not performed | Open the dashboard and `/pay/:identifier` in a real browser; execute the manual steps in §16 |
| B-2 | Public expiry not observed live | Use a disposable fixture with a past `expired_at` or wait out the provider expiry; confirm frontend presentation and backend state |
| B-3 | OpenAPI incomplete | Generate or maintain a complete spec for auth, dashboard payments/overview/analytics, and public mock-payment routes |

### Risks and deployment prerequisites

| ID | Risk | Severity | Evidence / action |
|---|---|---|---|
| R-1 | Public mock-payment API may continue returning `PENDING` after `expired_at`; only the canonical transaction is expired by the worker, and the frontend derives the visible `EXPIRED` state | Medium | `internal/service/expiry_service.go:44-65`; `internal/handler/mock_payment_handler.go:69-73`; frontend `contract.ts:41-59`. Keep expiry worker enabled; consider making the provider row converge later. |
| R-2 | `.env.stage` sets `FRONTEND_PUBLIC_URL=http://127.0.0.1:18080` (the staging backend origin) and disables `PAYMENT_EXPIRY_ENABLED` | High for staging dashboard use | `.env.stage:37,44`. That staging profile targets the separate Flutter backend scenario, not the dashboard SPA. For dashboard staging, set `FRONTEND_PUBLIC_URL` to the actual SPA origin and enable expiry convergence. Do not reuse backend port 18080. |
| R-3 | Exact-origin mismatch (`localhost` vs `127.0.0.1`) suppresses the frontend's "Open payment page" button | Medium | `PaymentDetailPage.tsx:46-53,94-99`. Set `FRONTEND_PUBLIC_URL`, `CORS_ALLOWED_ORIGINS`, and the browser origin to the same hostname and port. |
| R-4 | Public mock-payment API status set is narrower than canonical transaction statuses; a future public state expansion could fail frontend DTO validation | Low | `src/types/backend.ts:52`; `src/lib/mock-payment/contract.ts:3-8,28-38`. Current reachable set is `PENDING/PAID/FAILED`; add contract tests if `EXPIRED`/`CANCELLED` become reachable. |
| R-5 | OpenAPI drift makes external contract validation unreliable | Medium | `docs/api/openapi.yaml` has 25 paths; missing routes listed in §12. Regenerate after the API surface stabilizes. |
| R-6 | List sorting, amount range, and provider filter are absent | Low product gap | Confirmed in §9. Not required by the current frontend; implement only with a product decision and contract update. |
| R-7 | Same-origin reverse proxy would collide with backend `GET /pay/:identifier` | Medium deployment risk | The backend owns `/pay/:identifier` JSON on its origin; the frontend owns the browser route on its origin. Keep origins separate or route `/pay/*` to the SPA at the edge before it reaches the backend. |
| R-8 | Production/staging secrets and TLS were not exercised | Expected for this pass | No deployment was performed. Production must provide strong JWT/webhook secrets, HTTPS origins, and explicit CORS configuration. |

---

## 15. Files Changed

### Backend verification changes (uncommitted, preserved)

| File | Change |
|---|---|
| `internal/config/config.go` | Reject non-HTTP(S) `FRONTEND_PUBLIC_URL` schemes at startup |
| `internal/config/mock_payment_base_url_test.go` | Added scheme-rejection and trailing-slash regression tests |
| `internal/handler/mock_payment_handler_test.go` | Replaced a self-referential sentinel test with real 404/public-contract tests; added non-QRIS omission and terminal-status coverage |
| `internal/middleware/cors_test.go` | Added allowed/disallowed preflight credential tests |
| `docs/phase-2-backend-integration-verification.md` | This report (new) |

`git status` before this report showed four modified backend files (the four above excluding the new report). No backend commit was created. The frontend repository was not modified; its pre-existing uncommitted Phase 1 changes remain intact.

### Runtime test data

Disposable runtime records were created in the local development database:

- Merchant `a14c8494-91ca-4a77-b0ef-e91e393d168a`
- Paid transaction `67604d0a-ddf6-4c92-b540-3b2a01b9bf3f` / `P2-ORDER-1792464272`
- Failed transaction `acf3e10b-72c8-41e9-9995-e53867bdbd65` / `P2-FAIL-1792466017`

E2E artifacts are under `/tmp/opencode/phase2-e2e-1792464265/`; sensitive files are mode `600`. No secret values are reproduced in this report.

---

## 16. Exact Next Action

Complete the browser acceptance pass against the already-running local stack, with the correct origin consistency:

1. Open the dashboard at exactly `http://localhost:5173` (not `127.0.0.1`) so it matches `FRONTEND_PUBLIC_URL=http://localhost:5173`.
2. Log in with the disposable OWNER account and open the paid payment detail:
   - transaction `67604d0a-ddf6-4c92-b540-3b2a01b9bf3f`
   - confirm `provider=MOCK`, `payment_url` is present, and the "View payment page" button is enabled.
3. Click "View payment page"; confirm the SPA loads at `http://localhost:5173/pay/MOCK-TXN-8d198858e3f808804a2cc2de3cfdeafb` (not backend JSON and not a 404).
4. Refresh the page; confirm the SPA route survives refresh.
5. Confirm the public page displays amount `Rp125.000`, `IDR`, `QRIS`, `PENDING/PAID` behavior, and the mock QRIS payload.
6. Use the public page's success/failure actions on a fresh disposable payment; confirm the status panel updates and the dashboard detail/overview reflect the new status.
7. Repeat for an unknown identifier and confirm the safe "Payment not found" state.
8. Confirm the dashboard "Open payment page" guard passes; if it does not, verify the browser hostname matches `FRONTEND_PUBLIC_URL` exactly.

After those steps pass, the browser layer moves from **BLOCKED** to **PASS**. Until then, the correct overall classification remains **PARTIAL**: backend API behavior is verified, browser rendering/interaction is not.
