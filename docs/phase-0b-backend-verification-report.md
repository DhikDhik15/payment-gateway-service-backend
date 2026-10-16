# GO-SAAS Phase 0B — Backend Payment Integration Audit & Verification Report

**Repository:** `pay-gate-backend` (GitHub: `github.com/dhikaarta/pay-gate-backend`)
**Branch:** `main` (working tree contains pre-existing uncommitted changes — all preserved)
**Go version:** `go1.25.0 linux/amd64` (matches `go.mod`)
**Task scope:** Backend-only verification of the payment flow consumed by the separately maintained frontend. No source redesign, no new features, no unrelated changes.

---

## 1. Executive Summary

The backend already implements the intended payment integration topology documented in `docs/`:

- **Separate origins.** The merchant-facing API runs on `APP_PORT` (dev `http://localhost:8080`, Docker host `localhost:8081`), while the customer payment page runs on `FRONTEND_PUBLIC_URL` (dev `http://localhost:5173`, the Vite frontend origin). These are independent; the backend never assumes same-origin deployment.
- **`payment_url`** is constructed as `{FRONTEND_PUBLIC_URL}/pay/{providerTransactionId}` (e.g. `http://localhost:5173/pay/MOCK-TXN-abc123`), which matches the frontend route `/pay/:identifier`. No ports are hardcoded in application logic, trailing slashes are normalised, and a missing `FRONTEND_PUBLIC_URL` fails config load rather than deriving a wrong URL from `APP_PORT`.
- **Public payment lookup** used by the hosted payment page is `GET /api/v1/mock-payments/:identifier` (and the same-shape `GET /pay/:identifier` JSON projection on the API origin). Both require no dashboard JWT or merchant API key.
- **Response fields verified:** `provider` (`"MOCK"`, merchant-facing responses only), `timeline` (dashboard detail only, derived from persisted `created_at`/`paid_at`, never fabricated), `unsuccessful_transactions` (`FAILED + EXPIRED + CANCELLED`), `revenue` (`SUM(amount)` where `status = PAID` and `paid_at ∈ [from, to)`, single currency IDR), `qris_payload` (QRIS only, mock value `PAYGATE-MOCK-QRIS:<publicId>`).
- **Status semantics** match the documented state machine `CREATED → PENDING → {PAID, FAILED, EXPIRED, CANCELLED}` with terminal states enforced.

**One confirmed defect (BE-009):** `MockPaymentProvider.GetPayment` could emit a path-only URL (`/pay/<id>`) when constructed with an empty frontend origin, unlike `CreatePayment` in the same file which applies a fallback. Corrected with a minimal symmetric fallback plus regression tests.

**No other source changes were required.** All existing uncommitted changes (`.env.example`, `cmd/server/main.go`, Phase 8E/10L work, etc.) were preserved untouched.

---

## 2. Repository and Environment Findings

| Item | Finding |
|------|---------|
| Module | `github.com/dhikaarta/pay-gate-backend`, Go 1.25 |
| Framework | Gin v1.10.0, pgx v5, PostgreSQL 16 |
| Entry point | `cmd/server/main.go` |
| Config loader | `internal/config/config.go` (`config.Load`, godotenv) |
| Test command | `make test` → `go test -v -race -count=1 ./...` (unit/in-memory only) |
| Integration tests | `make test-integration` (requires `TEST_DATABASE_URL`; `*_pg_test.go` suites skip when unset) |
| Vet | `make vet` → `go vet ./...` (clean) |
| Linter | `golangci-lint` (optional, not installed — not introduced) |
| Compose | `docker-compose.yml`: app container port 8080 published as host 8081; PostgreSQL external; `FRONTEND_PUBLIC_URL` defaults to `http://localhost:5173` |
| `.env` | Gitignored, untracked, user-managed — not inspected or modified |
| Pre-existing uncommitted changes | 13 modified files + 28 untracked files (Phases 8E, 10L, etc.) — preserved, not touched by this task |
| `docs/phase-10-frontend-contract.md` | Does **not** exist in this repo (frontend contract lives in the separate frontend repository) |

Existing config-level coverage already present: `internal/config/mock_payment_base_url_test.go` verifies `FRONTEND_PUBLIC_URL` canonical-wins-over-alias, independence from `APP_PORT`, required-for-mock error, invalid-value rejection, and legacy alias compatibility.

---

## 3. Payment URL Construction Analysis

### 3.1 Where it is constructed

`internal/service/mock_payment_provider.go`, `MockPaymentProvider.CreatePayment`:

```go
frontendURL := m.frontendURL
if frontendURL == "" {
    frontendURL = "https://mock-provider.test"
}
return &ProviderPaymentResponse{
    ...
    PaymentURL: fmt.Sprintf("%s/pay/%s", frontendURL, mockTxID),
    ...
}, nil
```

`m.frontendURL` comes from `cfg.Provider.FrontendPublicURL`, set in `cmd/server/main.go` via `NewPersistedMockPaymentProvider(mockPaymentRepo, cfg.Provider.FrontendPublicURL)` (main.go:1088–1097). `NewPersistedMockPaymentProvider` applies `strings.TrimRight(frontendURL, "/")` (mock_payment_provider.go:54). `config.Load` also normalises via `resolveFrontendPublicURL` which trims the trailing slash and validates the value is an absolute URL without userinfo/query/fragment (config.go:699–717).

### 3.2 Resulting URL shape

```
{FRONTEND_PUBLIC_URL}/pay/{providerTransactionId}
```

Example from `docs/integration/payments.md:44`, `docs/integration/end-to-end.md:39`, `docs/integration/webhooks.md:48`:

```
http://localhost:5173/pay/MOCK-TXN-abc123
```

### 3.3 Verification checks

| Check | Result | Evidence |
|-------|--------|----------|
| Route matches frontend `/pay/:identifier` | PASS | Path segment `/pay/` + provider transaction ID; frontend route per docs/development.md:37 and mock-payment-provider.md:12 |
| Trailing slash handling | PASS | `resolveFrontendPublicURL` trims trailing slash; `NewPersistedMockPaymentProvider` trims; `Sprintf("%s/pay/%s", ...)` yields a single slash |
| Preserves identifier | PASS | `mockTxID` appended as the last path segment; verified by `TestPersistedMockPaymentProviderUsesConfiguredFrontendURL` |
| No hardcoded ports in application logic | PASS | Only origin comes from config; the `https://mock-provider.test` fallback in `CreatePayment`/`GetPayment` is host-only, no port, and is not reachable via server wiring (config.Load requires a configured origin for mock) |
| Missing/invalid config | PASS | `config.Load` errors: `FRONTEND_PUBLIC_URL is required when PAYMENT_PROVIDER=mock` (config.go:706); relative/rejected values fail validation (config.go:713) |
| URL points to frontend SPA, not API endpoint | PASS | Origin is `FRONTEND_PUBLIC_URL` (frontend origin), independent of `APP_PORT`/API origin; documented in mock-payment-provider.md:23–25 |
| Development vs production | PASS | Same code path; only the configured origin differs (`http://localhost:5173` dev, `https://app.example.com` prod per development.md:37) |

### 3.4 The `GET /pay/:identifier` backend route

`cmd/server/main.go:497` registers `r.GET("/pay/:identifier", mockPaymentHandler.Get)` on the API origin, returning the JSON `publicMockPayment` projection. Per `docs/mock-payment-provider.md:20` this is the "backend JSON provider projection"; the frontend consumes `GET /api/v1/mock-payments/:identifier`. The browser payment page itself is served by the frontend origin at `FRONTEND_PUBLIC_URL/pay/:identifier`. This is **intentional, documented architecture for separate origins** — not a code defect (see BE-010).

---

## 4. MOCK_PAYMENT_BASE_URL Behavior

- **Defined:** `ProviderConfig.MockPaymentBaseURL` (config.go:206) and `ProviderConfig.FrontendPublicURL` (config.go:202).
- **Loaded:** `resolveFrontendPublicURL(providerName)` (config.go:699): reads `FRONTEND_PUBLIC_URL` first, falls back to `MOCK_PAYMENT_BASE_URL`, trims trailing slash, validates absolute URL, errors for mock provider when both are empty.
- **Default:** None. Empty for mock → `Load()` fails. For non-mock providers → empty (allowed).
- **Precedence:** `FRONTEND_PUBLIC_URL` wins when both are set (mock_payment_base_url_test.go:5–19).
- **Alias semantics:** `MockPaymentBaseURL` is populated with the resolved value as a deprecated compatibility alias (config.go:657); new deployments use `FRONTEND_PUBLIC_URL`.
- **Application logic:** No handler/service reads `MockPaymentBaseURL` directly; wiring uses `FrontendPublicURL` (main.go:1088).

Verdict: no defect. Deprecation path is intact and tested.

---

## 5. Actual Payment Lookup Endpoint

The hosted payment page retrieves payment information from:

| Endpoint | Method | Auth | Source |
|----------|--------|------|--------|
| `GET /api/v1/mock-payments/:identifier` | GET | None (intentionally, per main.go:517–519 comment) | cmd/server/main.go:519 |
| `GET /pay/:identifier` | GET | None (JSON provider projection on API origin) | cmd/server/main.go:497 |

Handler: `internal/handler/mock_payment_handler.go` `MockPaymentHandler.Get` (mock_payment_handler.go:39).

Behavior by scenario:

| Scenario | Status | Response |
|----------|--------|----------|
| Valid reference, pending | 200 | `publicMockPayment` JSON (see §6) |
| Invalid / unknown reference | 404 | `TRANSACTION_NOT_FOUND`, "Payment not found" |
| Missing reference (empty) | 404 | `TRANSACTION_NOT_FOUND` |
| Expired payment (transition attempted) | 400 (success/fail endpoints) | `INVALID_TRANSACTION_STATE`, "Payment has expired" |
| Completed (PAID) | 200 on read; success/fail transitions return `INVALID_TRANSACTION_STATE` ("already in a terminal state") | Terminal states enforced by repository `Transition` |
| Cancelled (CANCELLED) | Same as completed — terminal | Terminal |

The public response does **not** require the dashboard's authentication state — consistent with the product design (public, unauthenticated hosted payment page). Authorization is **not** weakened on any merchant/dashboard endpoint.

Note: public payment lookup is keyed by the mock `public_id` (the `MOCK-TXN-...` capability identifier), not the merchant transaction UUID. Merchant-authenticated retrieval of the canonical transaction is `GET /api/v1/payments/:id` (X-API-Key, merchant-scoped).

---

## 6. Backend Response DTO Inventory

### 6.1 Merchant-facing: `POST /api/v1/payments` → `model.CreatePaymentResponse` (transaction.go:164)

| JSON field | Go type | Presence | Nullable | Endpoint | Notes |
|------------|---------|----------|----------|----------|-------|
| `transaction_id` | `uuid.UUID` | always | no | create/get | |
| `merchant_order_id` | `string` | always | no | create/get | |
| `amount` | `int64` | always | no | create/get | minor units |
| `currency` | `string` | always | no | create/get | `IDR` only |
| `payment_method` | `string` | always | no | create/get | `QRIS` only |
| `provider` | `string` | always | no | create/get | `"MOCK"` for mock provider (`service.Provider.Name()`) |
| `provider_transaction_id` | `string` | always | no | create/get | `MOCK-TXN-...` |
| `status` | `TransactionStatus` | always | no | create/get | `PENDING` on success |
| `payment_url` | `string`, `omitempty` | always on success | no | create/get | `{FRONTEND_PUBLIC_URL}/pay/{id}`; null on provider failure (flow.md:196) |
| `expired_at` | `*time.Time`, `omitempty` | on PENDING | yes | create/get | RFC3339 |
| `created_at` | `time.Time` | always | no | create/get | |

### 6.2 Merchant-facing: `GET /api/v1/payments/:id` → `model.PaymentResponse` (transaction.go:180)

Same fields, but `provider`, `provider_transaction_id`, `payment_url` are `*string` with `omitempty` → omitted (`null`) when not persisted (e.g. failed at creation). `status` reflects stored state.

### 6.3 Public (hosted payment page): `publicMockPayment` (mock_payment_handler.go:28)

| JSON field | Go type | Presence | Endpoint | Conditions |
|------------|---------|----------|----------|------------|
| `reference` | `string` | always | `/api/v1/mock-payments/:id` | = `MerchantOrderID` |
| `amount` | `int64` | always | same | |
| `currency` | `string` | always | same | |
| `payment_method` | `string` | always | same | |
| `status` | `TransactionStatus` | always | same | `PENDING`/`PAID`/… |
| `expired_at` | `interface{}` | always | same | holds `time.Time` → RFC3339 on serialise |
| `provider_transaction_id` | `string` | always | same | |
| `qris_payload` | `string`, `omitempty` | only QRIS | same | `PAYGATE-MOCK-QRIS:<PublicID>` when `PaymentMethod == "QRIS"` |

Not present in the public projection: `provider`, `payment_url`, `timeline`. These are investigated in §7–§9; absence is by design (public projection), not a confirmed defect.

### 6.4 Dashboard (authenticated): `GET /api/v1/dashboard/overview` → `model.DashboardOverviewResponse` (dashboard_overview.go:11)

`payments.unsuccessful_transactions`, `revenue.amount`, `revenue.currency` — verified in §9–§10.

### 6.5 Dashboard (authenticated): `GET /api/v1/dashboard/payments/{payment_id}` → `model.DashboardPaymentDetailResponse` (dashboard_overview.go:58)

Embeds `PaymentResponse` plus `timeline []DashboardTimelineEvent` (§8).

---

## 7. Verification of `provider`

| Aspect | Actual |
|--------|--------|
| JSON field name | `provider` |
| Go type | `string` in `CreatePaymentResponse` (transaction.go:171); `*string` `omitempty` in `PaymentResponse` (transaction.go:187) |
| Value | `"MOCK"` for the mock provider (`service.mockProviderName`, mock_payment_provider.go:17); Midtrans provider name for Midtrans |
| Required/optional | Required (non-pointer) in create response; optional in get response |
| Nullable | No |
| Endpoint | `POST/GET /api/v1/payments`, dashboard equivalents |
| Public payment page | **Absent** from `publicMockPayment` — the page identifies the instrument via `payment_method` (`QRIS`) instead |

Compatibility: the frontend audit listed `provider` for verification. The backend emits `"MOCK"` in all merchant-facing payment responses. The public hosted-page projection deliberately does not include it; whether the frontend requires it there cannot be confirmed without the frontend repository's TypeScript types. Classified as an investigation target, not a defect.

---

## 8. Verification of `timeline`

| Aspect | Actual |
|--------|--------|
| Exposed? | Yes, but **only** on the authenticated dashboard detail endpoint `GET /api/v1/dashboard/payments/{payment_id}` |
| Go type | `[]model.DashboardTimelineEvent` (dashboard_overview.go:78: `Type string`, `At time.Time`) |
| JSON field | `timeline` |
| Population | `buildPaymentTimeline` (dashboard_payment_handler.go:354): `CREATED` from `created_at`; `PAID` only when `paid_at` is set. Terminal states other than PAID produce no event |
| Fabrication | Explicitly not fabricated — comment at dashboard_payment_handler.go:353: "Other terminal statuses are not fabricated — no event history table exists yet." |
| Public payment page | **Absent** from `publicMockPayment` |

Verdict: `timeline` exists where documented (dashboard detail), derived solely from persisted timestamps. Not present on the public hosted-payment-page response. Investigation target; no defect.

---

## 9. Verification of `unsuccessful_transactions`

| Aspect | Actual |
|--------|--------|
| JSON field | `payments.unsuccessful_transactions` |
| Go type | `int64` (dashboard_overview.go:38) |
| Endpoint | `GET /api/v1/dashboard/overview` (Bearer JWT) |
| Logic | `dashboard_overview_repository.go:85`: `stats.UnsuccessfulTransactions = stats.Failed + stats.Expired + stats.Cancelled` |
| Counted statuses | FAILED, EXPIRED, CANCELLED (terminal, unsuccessful) |
| Not counted | CREATED, PENDING (in flight), PAID (success) |
| Period | `created_at ∈ [from, to)` (half-open); default current UTC calendar month |
| Frontend semantics (FAILED/EXPIRED/CANCELLED) | Matches exactly |

Verdict: no defect. Terminal unsuccessful statuses match the domain model; no other statuses are counted.

---

## 10. Verification of `revenue`

| Aspect | Actual |
|--------|--------|
| JSON field | `revenue.amount`, `revenue.currency` |
| Go type | `int64`, `string` (dashboard_overview.go:45–46) |
| Endpoint | `GET /api/v1/dashboard/overview` |
| Aggregation SQL | `dashboard_overview_repository.go:89`: `COALESCE(SUM(amount), 0)` WHERE `status = 'PAID'` AND `paid_at IS NOT NULL` AND `paid_at ∈ [from, to)` |
| Amount representation | Integer minor units, same as `transactions.amount` |
| Currency | Dominant currency among matched rows (first by `paid_at DESC`), default `IDR` when empty |
| Date range | Half-open `[from, to)` on `paid_at` |
| Multi-currency | Not applicable — `model.SupportedCurrencies = {"IDR": true}` only (transaction.go:93); no incompatible currencies are aggregated |

Verdict: no defect. Revenue counts only PAID transactions within the paid_at window; currency semantics are explicit and single-currency.

---

## 11. Verification of `qris_payload`

| Aspect | Actual |
|--------|--------|
| JSON field | `qris_payload` |
| Go type | `string`, `omitempty` (mock_payment_handler.go:36) |
| Endpoint | `GET /api/v1/mock-payments/:identifier` (and `/pay/:identifier`) |
| Condition | Included only when `PaymentMethod == "QRIS"` (mock_payment_handler.go:71–73) |
| Value | `PAYGATE-MOCK-QRIS:` + `PublicID` (transaction-specific, mock) |
| Supported methods | `model.SupportedPaymentMethods = {"QRIS": true}` (transaction.go:99) — QRIS is the only method |
| Expiration | `expired_at` returned separately (30 minutes default from creation) |
| Post-completion behavior | `success`/`fail` endpoints transition PENDING → PAID/FAILED; terminal transitions rejected; `qris_payload` reflects stored `PaymentMethod` |
| Repeated requests | Idempotent read; transitions guarded by repository (`ErrMockPaymentInvalidState` for terminal, `ErrMockPaymentExpired` when `expired_at <= now`) |

Verdict: no defect. QRIS payload is returned for the only supported mock payment method, is explicitly mock data (`PAYGATE-MOCK-QRIS:<id>`, documented as such in mock-payment-provider.md:34), and is never presented as production-valid. Expiration and terminal-state behavior are enforced.

---

## 12. Payment Status Semantics

State machine (`internal/model/transaction.go:13–41`):

```
CREATED → PENDING → PAID (terminal)
            → FAILED (terminal)
            → EXPIRED (terminal)
            → CANCELLED (terminal)
CREATED → CANCELLED (terminal)
```

| Aspect | Actual |
|--------|--------|
| Valid transitions | Enforced by `TransactionStatus.CanTransitionTo()`; service layer enforces before DB writes (conditional `UPDATE … WHERE status = $from`) |
| Terminal states | `PAID`, `FAILED`, `EXPIRED`, `CANCELLED` — no outbound transitions; `IsTerminal()` true |
| Cancellable | `CREATED`, `PENDING` only (`IsCancellable()`) |
| Terminal misrepresented as pending | Not possible — stored status is returned as-is; no mapping downgrades terminal to pending |
| Invalid transitions | `ErrInvalidTransactionState` → HTTP 409 `INVALID_TRANSACTION_STATE` |
| Expiry | Background expiry worker (`service.NewExpiryService`) transitions PENDING → EXPIRED when `expired_at` passes; mock repository `Transition` enforces `expired_at > NOW()` |
| New status values | None introduced; none required |

Verdict: no defect. Terminal statuses are never represented as pending; invalid transitions are rejected per existing domain rules.

---

## 13. QRIS Behavior

| Aspect | Actual |
|--------|--------|
| Payload generation | Mock-constructed in handler: `PAYGATE-MOCK-QRIS:` + `PublicID` (mock_payment_handler.go:72) |
| Storage | Not persisted separately; derived from `PaymentMethod`/`PublicID` at read time |
| Response exposure | `GET /api/v1/mock-payments/:identifier` → `qris_payload` field (QRIS only) |
| Optional semantics | `omitempty` — absent for non-QRIS (none exist; QRIS is the sole supported method) |
| Expiration | `expired_at` (30 min default, `MockPaymentProvider.expiredAt`) |
| After completion/expiry | Terminal transitions enforced; success/fail webhooks processed via `WebhookService.ProcessWebhook`; repeated terminal transitions return the same state without re-processing webhooks (mock_payment_handler.go:98–101) |
| Production validity | Not claimed — explicitly mock data, development only |

Verdict: no defect. No QRIS payload fabrication; mock-provider architecture preserved.

---

## 14. Tests Added or Updated

### Files changed by this task

| File | Change |
|------|--------|
| `internal/service/mock_payment_provider.go` | Defect fix BE-009 (see §16) |
| `internal/service/mock_payment_provider_test.go` | Two new regression tests |

### New tests

1. `TestPersistedMockPaymentProviderGetPaymentPreservesConfiguredFrontendURL` — `GetPayment` returns `PaymentURL == http://localhost:5173/pay/<publicId>` for a configured origin, preserving provider transaction ID and pending status.
2. `TestPersistedMockPaymentProviderGetPaymentNeverReturnsPathOnlyURL` — `GetPayment` constructed with an empty frontend origin never returns a path-only URL; falls back to `https://mock-provider.test/pay/<publicId>` and preserves the identifier.

### Existing relevant tests (verified, not modified)

- `TestPersistedMockPaymentProviderUsesConfiguredFrontendURL` (mock_payment_provider_test.go:11) — `CreatePayment` URL prefix + identifier preservation.
- `TestMockPaymentPublicReadAndTransition` (mock_payment_handler_test.go:57) — public read returns `qris_payload` for QRIS and excludes internal fields; success/fail transitions and idempotent repeat.
- `TestLoad_FrontendPublicURLIsRespected`, `TestLoad_AppPortDoesNotDetermineFrontendPublicURL`, `TestLoad_MockProviderRequiresFrontendPublicURL`, `TestLoad_FrontendPublicURLRejectsInvalidValue`, `TestLoad_LegacyMockPaymentBaseURLRemainsCompatible` (mock_payment_base_url_test.go) — config resolution.
- `payment_handler_test.go`, `payment_service_test.go`, `payment_idempotency_test.go`, `dashboard_overview_*_test.go` — existing merchant-facing payment/overview coverage (run, untouched).

No new dependencies were added. No existing assertions or tests were removed.

---

## 15. Validation Results

| Command | Result |
|---------|--------|
| `go test -count=1 ./...` | 11/11 packages `ok` (cmd/server, internal/audit, config, handler, middleware, model, ratelimit, repository, service, ssrf, pkg/database); `pkg/response` and `swagger` have no test files |
| `go vet ./...` | Clean (exit 0) |
| `gofmt -l` on the two files touched by this task | Clean |
| `gofmt -l .` (repo-wide) | 4 pre-existing untracked files need formatting (`internal/model/dashboard_analytics.go`, `internal/repository/dashboard_analytics_pg_test.go`, `internal/repository/dashboard_overview_pg_test.go`, `internal/repository/dashboard_overview_recent_pg_test.go`) — all are pre-existing Phase 10L work unrelated to this task; left unmodified per repository-safety rules |
| `make test-integration` / PostgreSQL | **Not run** — no `TEST_DATABASE_URL` available and no PostgreSQL service running in this environment; `*_pg_test.go` suites skip automatically (`TEST_DATABASE_URL not set`). Documented as a limitation in §17. |
| Browser / frontend integration | **Not performed** — frontend lives in a separate repository; not part of this workspace |

Note: `make fmt-check` would fail on the repo due to the 4 pre-existing unformatted files above, unrelated to this task's changes.

---

## 16. Verified Defects and Corrections

| ID | Finding | Evidence | Severity | Action |
|----|---------|----------|----------|--------|
| BE-001 | `payment_url` = `{FRONTEND_PUBLIC_URL}/pay/{providerTxId}` matches frontend route `/pay/:identifier`; no hardcoded ports; trailing slashes normalised; missing config fails fast | mock_payment_provider.go:80–91; config.go:699–717; main.go:1088–1097; mock_payment_base_url_test.go | — (verified, no defect) | None |
| BE-002 | `MOCK_PAYMENT_BASE_URL` is deprecated alias; `FRONTEND_PUBLIC_URL` canonical; required for mock provider | config.go:699–717, mock_payment_base_url_test.go | — (verified) | None |
| BE-003 | Public lookup endpoints `GET /api/v1/mock-payments/:identifier` and `GET /pay/:identifier`; unauthenticated; 404/terminal-state behavior correct | main.go:497,519; mock_payment_handler.go:39–75, 76–107 | — (verified) | None |
| BE-004 | DTO inventory documented (§6); `provider`/`timeline` absent from public projection by design | mock_payment_handler.go:28–37 | — (verified) | None |
| BE-005 | `unsuccessful_transactions = FAILED + EXPIRED + CANCELLED` matches frontend semantics | dashboard_overview_repository.go:83–85 | — (verified) | None |
| BE-006 | `revenue = SUM(amount) WHERE status=PAID AND paid_at ∈ [from,to)`; single currency IDR | dashboard_overview_repository.go:89–118 | — (verified) | None |
| BE-007 | `qris_payload` returned for QRIS only; mock value `PAYGATE-MOCK-QRIS:<id>`; expiration/terminal behavior enforced | mock_payment_handler.go:71–73; mock_payment_repository.go:64–83 | — (verified) | None |
| BE-008 | Status machine `CREATED → PENDING → {PAID,FAILED,EXPIRED,CANCELLED}`; terminal states enforced; no invalid downgrade to pending | model/transaction.go:24–41 | — (verified) | None |
| BE-009 | `MockPaymentProvider.GetPayment` emitted path-only URL `/pay/<id>` when `frontendURL` empty — asymmetric with `CreatePayment` in the same file, violating the absolute-URL contract for `ProviderPaymentResponse.PaymentURL` | mock_payment_provider.go:118 (before fix); CreatePayment fallback at :80–83 | Low (latent; unreachable via server wiring since config.Load requires a non-empty origin) | Fixed: added the same empty-origin fallback to `GetPayment`; two regression tests added |
| BE-010 | Backend `GET /pay/:identifier` returns JSON on the API origin; frontend SPA route lives on separate origin `FRONTEND_PUBLIC_URL`. Same-origin (reverse-proxy) deployment would shadow the SPA route → raw JSON on refresh | main.go:495–497; mock-payment-provider.md:20–25; development.md:34–39 | — (deployment topology note, not a code defect) | Documented; no code change (architecture preserved) |
| BE-011 | `provider` absent from public mock-payment response; present as `"MOCK"` in merchant-facing responses | mock_payment_handler.go:28–37; transaction.go:171 | — (investigation target; frontend requirement unconfirmed) | None — document as compatibility consideration |
| BE-012 | `timeline` absent from public response; present (created_at/paid_at only, never fabricated) on authenticated dashboard detail | dashboard_payment_handler.go:351–362 | — (investigation target) | None |

All verified defects: BE-009 corrected; all others explicitly documented as no-defect or out-of-scope dependencies.

---

## 17. Remaining Frontend / Deployment Dependencies

The following cannot be verified from this backend repository and require the separate frontend repository or live services:

1. **Frontend SPA route registration** — confirm the frontend actually renders at `/pay/:identifier` on `FRONTEND_PUBLIC_URL` and handles direct navigation/refresh (SPA fallback routing). Backend cannot verify this.
2. **`VITE_API_BASE_URL` → backend origin** — the payment page calls `GET {API_ORIGIN}/api/v1/mock-payments/:identifier`; confirm CORS (`CORS_ALLOWED_ORIGINS`) includes the frontend origin and the frontend points `VITE_API_BASE_URL` at the backend API origin, not at `FRONTEND_PUBLIC_URL`.
3. **Same-origin reverse proxy** — if the operator fronts both SPA and API on one origin, backend route `GET /pay/:identifier` will shadow the SPA. Mitigation: keep origins separate (documented), or re-route the API under a dedicated prefix. No backend change made; documented as BE-010.
4. **PostgreSQL** — integration/`*_pg_test.go` suites require `TEST_DATABASE_URL` against a migrated database; not available in this environment. Skipped automatically by the test suite.
5. **Browser-level flow** — end-to-end: create payment → open `payment_url` → hosted page fetches `/api/v1/mock-payments/:id` → displays status/qris_payload → simulate success/fail → status update. Requires frontend + running backend + DB.

---

## 18. Exact Steps for End-to-End Verification

1. Provision PostgreSQL and apply migrations (`make migrate-up DATABASE_URL=...`).
2. Create `.env` (from `.env.example`, never commit) with at minimum:
   - `APP_ENV=development`, `APP_PORT=8080`
   - `FRONTEND_PUBLIC_URL=http://localhost:5173` (or production origin)
   - `PAYMENT_PROVIDER=mock`, `DB_*`, `MOCK_WEBHOOK_SECRET` (≥32 bytes)
3. Start backend: `go run ./cmd/server` (or `docker compose up -d --build`; app publishes on host 8081).
4. Start frontend (separate repo) on `http://localhost:5173` with `VITE_API_BASE_URL=http://localhost:8080` (or `http://localhost:8081` via Docker).
5. `POST /api/v1/payments` with `X-API-Key` + `Idempotency-Key`; confirm `payment_url` = `http://localhost:5173/pay/MOCK-TXN-…`.
6. Open `payment_url` in a browser; refresh must render the SPA (not raw JSON/404).
7. Confirm the payment page fetches `GET {VITE_API_BASE_URL}/api/v1/mock-payments/:identifier` and displays `status`, `amount`, `qris_payload` (QRIS).
8. Call `POST /api/v1/mock-payments/:identifier/success` and `/fail`; confirm status transitions to `PAID`/`FAILED` and repeat calls are idempotent.
9. Run `go test -count=1 ./...` and `go vet ./...` (this repo).

---

## Acceptance Criteria

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Backend architecture inspected | PASS | Full source trace completed (§2–§13) |
| 2 | `MOCK_PAYMENT_BASE_URL` traced to implementation | PASS | config.go:699–717 + mock_payment_base_url_test.go |
| 3 | Payment URL construction verified | PASS | mock_payment_provider.go:80–91; BE-001 |
| 4 | Expected frontend route identified | PASS | `/pay/:identifier` (docs/mock-payment-provider.md:12) |
| 5 | Dev/prod topology documented | PASS | development.md:34–39; docker-compose.yml:34; §3.4 |
| 6 | Payment lookup endpoint + response verified | PASS | main.go:519; mock_payment_handler.go:39–75; BE-003 |
| 7 | `provider` verified | PASS | transaction.go:171; §7; BE-011 |
| 8 | `timeline` verified | PASS | dashboard_payment_handler.go:354; §8; BE-012 |
| 9 | `unsuccessful_transactions` verified | PASS | dashboard_overview_repository.go:85; §9 |
| 10 | `revenue` verified | PASS | dashboard_overview_repository.go:89; §10 |
| 11 | `qris_payload` verified | PASS | mock_payment_handler.go:72; §11 |
| 12 | Payment status semantics reviewed | PASS | model/transaction.go; §12 |
| 13 | QRIS behavior reviewed | PASS | §13 |
| 14 | Verified defects corrected or documented blocked | PASS | BE-009 fixed; others documented (§16) |
| 15 | Regression tests added/updated | PASS | Two new tests in mock_payment_provider_test.go (§14) |
| 16 | Backend tests executed | PASS | `go test -count=1 ./...` — 11/11 ok (§15) |
| 17 | `go vet` executed | PASS | Clean (§15) |
| 18 | External-service limitations documented | PASS | PostgreSQL/browser/frontend (§17) |
| 19 | Documentation created without overwriting | PASS | New file `docs/phase-0b-backend-verification-report.md` |
| 20 | Existing user modifications preserved | PASS | git status shows all pre-existing changes untouched |
| 21 | Final git status inspected | PASS | See below |

### Final Git Status

Working tree retains all pre-existing modifications (`.env.example`, `cmd/server/main.go`, Phase 8E/10L work, etc.) untouched. Only two files were changed by this task:

- `M internal/service/mock_payment_provider.go` (BE-009 fix)
- `M internal/service/mock_payment_provider_test.go` (BE-009 regression tests)
- `?? docs/phase-0b-backend-verification-report.md` (this report)

No commits or pushes were made. No user modifications were reset, stashed, or discarded. No database migrations or destructive operations were run.
