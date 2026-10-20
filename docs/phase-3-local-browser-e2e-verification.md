# GO-SAAS Phase 3 — Local Browser E2E Verification

**Backend repository:** `pay-gate-backend` — branch `main`, commit `fd49157`, plus the four uncommitted Phase 2 changes and the Phase 2 report (preserved)  
**Frontend repository:** `pay-gate-dashboard` — branch `main`, commit `d25a232`, plus pre-existing uncommitted Phase 1 work (preserved)  
**Verification date:** 2026-10-20  
**Browser engine:** Google Chrome `155.0.8059.39` (headless, driven through the Chrome DevTools Protocol)  
**Node runtime:** `v22.22.1`  
**Overall verdict:** **PASS**

> Browser E2E was actually executed against the running frontend and backend with a real Chrome engine. All six scenarios passed. No application code was changed in this phase. The limitations of the execution method are recorded in §4 and §16; they do not change any scenario result.

---

## 1. Executive summary

Phase 2 verified the backend API and reported **PARTIAL** because the desktop browser tool was disconnected. This phase closes that gap by launching the locally installed Chrome binary in headless mode and driving it through CDP, without adding a Playwright/Cypress dependency and without modifying either repository.

The following was exercised end to end against `http://localhost:5173` and `http://localhost:8081`:

- Merchant login through the real login form and dashboard rendering.
- Payment creation through the real create-payment form.
- “Open payment page” navigation from the transaction detail page into a new tab.
- Public payment page rendering as HTML with the backend-provided amount, QRIS payload, status and expiry timestamp.
- Successful and failed simulator transitions, followed by dashboard state verification.
- Public-page reload, unknown identifier handling, and API-unavailable handling.
- Browser console, network hosts, CORS, auth-header and redirect inspection.
- Expired-payment UI using an isolated browser clock offset on one public page.

All scenarios are **PASS**. Backend state agreed with the UI for every transition. The browser made no request to an unintended host, sent no dashboard credentials to the public API, and produced no runtime errors after authentication.

Quality gates passed: frontend typecheck, lint, 266 tests and production build; backend `gofmt`, `go vet` and `go test`.

No defect was found that required a code change. The only repository change produced by this phase is this report.

---

## 2. Overall verdict

**PASS.**

| Area | Verdict | Basis |
|---|---|---|
| Scenario A — Merchant authentication | **PASS** | Real form login, dashboard rendered, no credential leakage in captured output |
| Scenario B — Create payment | **PASS** | Form submission created a backend transaction and navigated to its detail page |
| Scenario C — Open payment page | **PASS** | New tab reached `http://localhost:5173/pay/<identifier>`, served HTML, loaded public data without dashboard auth |
| Scenario D — Status and expiry | **PASS** | Pending → paid and pending → failed; terminal transition rejected; dashboard reflected both; countdown used backend expiry; expired UI rendered |
| Scenario E — Refresh and errors | **PASS** | Reload consistent, 404 state shown, API failure ended in a retryable error, no wrong payment shown |
| Scenario F — Console and network | **PASS** | No runtime errors, no CORS errors, no failed authenticated requests, only expected local hosts |
| Quality gates | **PASS** | See §14 |

This verdict is for **browser E2E of the local mock flow**. It is not a claim of real payment processing, cross-browser certification, or desktop-manual sign-off.

---

## 3. Repositories and working-tree state inspected

| Repository | Branch | HEAD | Working tree at start and end |
|---|---|---|---|
| `pay-gate-backend` | `main` | `fd49157 add env mobile final verification` | 4 modified files + untracked Phase 2 report (unchanged by this phase) |
| `pay-gate-dashboard` | `main` | `d25a232 maintenance frontend` | 7 modified files + 7 untracked files from earlier phases (unchanged by this phase) |

Backend modified files (pre-existing from Phase 2):

- `internal/config/config.go`
- `internal/config/mock_payment_base_url_test.go`
- `internal/handler/mock_payment_handler_test.go`
- `internal/middleware/cors_test.go`

Frontend pre-existing changes were inspected but not modified. No reset, clean, stash, commit, push, checkout or deployment was performed.

The production build command regenerated `dist/`, which is ignored by Git; no tracked frontend file changed.

---

## 4. Browser and automation environment

The OpenCode desktop browser tool remained disconnected:

```text
[browser.disconnected] No desktop browser is connected to this session.
```

Chrome was nevertheless available locally at `/usr/bin/google-chrome` (`155.0.8059.39`). Browser E2E was therefore executed with **headless Chrome over CDP**:

| Item | Value |
|---|---|
| Browser | Google Chrome 155.0.8059.39, `--headless=new` |
| Transport | CDP over `http://127.0.0.1:9223` |
| Driver | Small Node 22 harness using the built-in `WebSocket` and `fetch` |
| New dependency added | **None** |
| Test runner | Existing Node test tooling for unit tests; CDP scripts for browser scenarios |
| Viewport | 1440×1000 for dashboard, 1200×1000 for the public page |
| Artifacts | `/tmp/opencode/phase3-browser/artifacts/` (outside both repositories) |

Screenshots captured for each visual state:

```text
01-login.png                 07-paid.png
02-dashboard.png             08-dashboard-paid.png
03-create-form.png           09-failed.png
04-payment-detail.png        10-not-found.png
05-public-payment.png        11-load-error.png
06-pending-countdown.png     12-paid-reload.png
                             13-payments-list.png
                             14-expired.png
```

The harness recorded console messages, uncaught exceptions, failed requests, request headers, response statuses and target URLs. Sensitive headers were replaced with `<redacted>` before being written to artifacts.

This is a real Chromium browser rendering the real React application. It is not a desktop-browser session and not a multi-browser compatibility test.

---

## 5. Frontend/backend configuration verified

Configuration was read from the running container and from the observed network traffic. No secret, token, key or credential is reproduced below.

| Setting | Effective value | Verification |
|---|---|---|
| Frontend origin | `http://localhost:5173` | Vite dev server returned the SPA shell |
| Backend API origin | `http://localhost:8081` | `/health` and `/health/ready` returned `200` |
| Frontend API base | `http://localhost:8081` | `VITE_API_BASE_URL` in the local frontend env; all dashboard calls used it |
| `APP_ENV` | `development` | Container environment |
| `PAYMENT_PROVIDER` | `mock` | Container environment |
| `FRONTEND_PUBLIC_URL` | `http://localhost:5173` | Container environment |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | Container environment and successful browser traffic |
| `PAYMENT_EXPIRY_ENABLED` | `true` | Container environment |
| `PAYMENT_EXPIRY_INTERVAL` | `30s` | Container environment |
| `PAYMENT_SIMULATOR_ENABLED` | `true` | Container environment (development only) |
| Backend health | `healthy running` | Container health check |

**Generated payment URL.** The dashboard detail API returned, for example:

```text
http://localhost:5173/pay/MOCK-TXN-625adb86b1d1fe2292778fae0b635330
```

This is the frontend origin plus the frontend route `path: '/pay/:identifier'` (`src/routes/index.tsx:73`), constructed by the mock provider at `internal/service/mock_payment_provider.go:89` and `:125`. It does **not** point at port `8081`.

**Frontend route.** The public page is intentionally outside `AuthLayout` and `ProtectedRoute`. The detail page only enables the action when the backend URL is HTTP(S), same-origin with the dashboard, and safe to open (`src/pages/PaymentDetailPage.tsx:94-99`, `:152-153`); `openPaymentPage` passes the opaque URL unchanged into a new tab with `noopener,noreferrer` (`src/lib/utils/url.ts:16-19`).

**Public API independence.** The public client sets `skipAuth: true`, `skipRefresh: true` and `credentials: 'omit'` (`src/lib/api/client.ts:382-390`; request construction at `:244`). The public read is served by the unauthenticated group at `cmd/server/main.go:519-521`. In the browser, the request to `/api/v1/mock-payments/<identifier>` carried **no `Authorization` header and no `Cookie` header**, even though the same browser profile was logged into the dashboard.

**CORS.** Requests from `http://localhost:5173` succeeded without CORS errors. The allowlist remains exact-origin with credentials, implemented in `internal/middleware/cors.go:23-46` and validated at `internal/config/config.go:865-872`. No wildcard was used or introduced.

**Origin discipline.** `localhost` and `127.0.0.1` are different origins. All tests used `http://localhost:5173`, matching both `FRONTEND_PUBLIC_URL` and the CORS allowlist. The 127.0.0.1 mismatch risk from Phase 2 remains a documentation/operator concern only.

**Expiry.** Expiry is enabled. The backend supplied `expired_at` timestamps approximately 30 minutes after creation, and the UI countdown was consistent with the API timestamp. The backend worker interval is 30 seconds; this matters for the risk in §16.

---

## 6. Test data and fixture strategy

No credential was invented. The existing disposable Phase 2 merchant fixture was reused through the documented local credentials file; the password was read by the test process and never printed, written to the report, or included in console/network evidence.

Three isolated payments were created during this phase:

| Purpose | Merchant order ID | Transaction ID | Amount | Final backend status |
|---|---|---|---|---|
| Success path / Scenario C | `P3-ORDER-1792468460666` | `50db2357-8d05-4663-b02b-ec722e91a215` | 125000 IDR | `PAID` |
| Failure path | `P3-FAIL-1792468562076` | `f6e1ebaa-ac23-49dd-88e7-f2e4807c5f70` | 50000 IDR | `FAILED` |
| Expiry UI fixture | `P3-EXP-1792469118939` | `da36afc0-a355-4922-a534-ff582d668235` | 75000 IDR | `PENDING` |

All three were created through the dashboard UI form, not seeded directly into the database. The mock provider assigned opaque identifiers such as `MOCK-TXN-625adb86b1d1fe2292778fae0b635330`. No pre-existing mutable transaction was used to prove a transition.

After the browser run, the backend dashboard API confirmed the three records, their amounts, their order IDs and one `payment_url` per record. The merchant overview showed the combined development dataset (including the two Phase 2 records): total 5, paid 2, failed 2, pending 1, unsuccessful 2, revenue 250000 IDR.

Test data is development-only and was left in place; no destructive cleanup was performed.

---

## 7. Scenario A — Merchant authentication

**Result: PASS**

- Opened `http://localhost:5173/login`; the React login form rendered (`#email`, `#password`, “Sign in”).
- Submitted the existing safe test credentials through the form.
- Redirected to `http://localhost:5173/dashboard`.
- The dashboard shell, merchant name, role and overview content rendered.
- No credentials appeared in console output, artifacts, or the report. Network capture stored request headers only, with authorization/cookie values redacted.
- Uncaught page exceptions: **0**.

Evidence: `01-login.png`, `02-dashboard.png`, `scenario-abc.json`.

The only pre-login console entry was the expected `401` from the session-restore refresh attempt when no session existed. It disappeared after login and is not a failed login.

---

## 8. Scenario B — Create payment

**Result: PASS**

- Opened `/dashboard/payments/new` through the UI.
- Entered order ID `P3-ORDER-1792468460666` and amount `125000`.
- Submitted the form.
- The app navigated to `/dashboard/payments/50db2357-8d05-4663-b02b-ec722e91a215`.
- The detail page showed `Rp 125.000`, `QRIS`, `PENDING`, the correct order ID and the pending-status notice.
- The backend dashboard API confirmed the same transaction, amount, order ID and `PENDING` state at creation time; it later became `PAID` through the public simulation.

Evidence: `03-create-form.png`, `04-payment-detail.png`, `scenario-abc.json`, `backend-state.json`.

The success toast is rendered on the create screen, but navigation to the detail page replaces that screen quickly. Visible success is therefore proven by the navigation plus the matching detail record, not by the toast text alone.

---

## 9. Scenario C — Open payment page

**Result: PASS**

- On the transaction detail page, activated **Open payment page**.
- A new browser target opened at:

```text
http://localhost:5173/pay/MOCK-TXN-625adb86b1d1fe2292778fae0b635330
```

- The URL origin is the frontend origin, **not** `http://localhost:8081`.
- `document.contentType` was `text/html`; the page rendered the React payment UI, not raw JSON.
- The page rendered: “Payment pending”, badge `PENDING`, `Rp 125.000`, `IDR · QRIS`, the mock QRIS QR code, the backend reference, and “Expires in 28m 49s”.
- The public request went to `GET http://localhost:8081/api/v1/mock-payments/MOCK-TXN-625adb86b1d1fe2292778fae0b635330`.
- Captured request headers contained no `Authorization` and no `Cookie`, despite the dashboard session in the same browser profile.
- No console errors or page errors on the public page.

Evidence: `05-public-payment.png`, `scenario-abc.json`, `evidence-abc.json`.

The page used the backend-provided QRIS payload. No QR code or payload was fabricated.

---

## 10. Scenario D — Payment status and expiry

**Result: PASS**, with the expiry sub-check executed by an isolated browser clock offset (method disclosed below).

### D1 — Initial state and countdown

- Initial status: `PENDING` (“Payment pending”).
- Backend DTO `expired_at`: `2026-10-20T04:24:51.633762Z`.
- The UI showed “Expires in 28m 49s” and, 2.5 seconds later, “Expires in 28m 46s”.
- This proves the countdown is driven by the backend timestamp and ticks locally; it is not a static string.
- Simulator actions were present while pending.

### D2 — Successful transition

- Activated **Simulate successful payment** on the public page.
- The page changed to “Payment successful” with badge `PAID`.
- The simulator buttons disappeared.
- Reloading the same public URL preserved `PAID`.
- The dashboard detail page changed to `PAID` and displayed a “Paid at” timestamp.
- The backend dashboard API reported `status: PAID`, `paid_at` set, and `expired_at` unchanged.

### D3 — Terminal-state protection

After `PAID`, a direct browser `POST` to the public failure endpoint returned:

```text
HTTP 400
{"success":false,"error":{"code":"INVALID_TRANSACTION_STATE","message":"Payment is already in a terminal state"}}
```

This is a real backend rejection issued from the browser context, not a hidden-button claim. The UI also correctly hides simulation actions once terminal.

### D4 — Failed transition

A second payment (`P3-FAIL-…`) was created through the form. **Simulate failed payment** changed the public page to “Payment failed” with badge `FAILED`, hid the simulator, and the dashboard detail page showed `FAILED`. The backend API confirmed `status: FAILED` and `paid_at: null`.

### D5 — Expired UI

A third payment was created and its public page was loaded. In that one tab only, the harness applied a `Date.now()` offset of +31 minutes before reload:

```text
Date.now = real Date.now + 31 minutes
```

This simulates elapsed time without changing application code, backend configuration, or stored data. After reload:

- Header: “Payment expired”.
- Badge: `EXPIRED`.
- Expiration field: “Expired”.
- Simulator actions: hidden.
- Backend API still returned `status: PENDING` because the expiry worker had not yet processed the record.

This confirms the frontend derives the effective `EXPIRED` state from the backend-provided timestamp (`src/lib/mock-payment/contract.ts:55-59`; countdown at `src/pages/PublicPaymentPage.tsx:31-63`). It does **not** prove that the backend has already transitioned the stored status; that is recorded as a risk in §16.

Evidence: `06-pending-countdown.png`, `07-paid.png`, `08-dashboard-paid.png`, `09-failed.png`, `14-expired.png`, `scenario-d.json`, `scenario-d-expiry.json`, `backend-state.json`.

No real payment occurred. All transitions used the mock provider. No QR code was invented.

---

## 11. Scenario E — Refresh and error handling

**Result: PASS**

| Check | Result | Evidence |
|---|---|---|
| Reload a paid public page | Consistent | Same status, amount, reference and order data; only the live countdown text changes |
| Unknown identifier | “Payment not found” with “Check the payment link and try again.” in about 0.8 s | `10-not-found.png`, `scenario-e.json` |
| Unknown identifier leaks an amount | No | No `Rp` amount rendered |
| API unavailable | “Could not load payment” + “We could not reach the payment service…” after about 15.1 s | `11-load-error.png` |
| Indefinite loading | No; the loading state terminated in an error state | `stuckLoading: false` |
| Retry affordance | Present | `Retry` button rendered |
| Wrong payment shown during failure | No | No paid/failed status or amount rendered |
| Public page repeat load | Consistent after excluding the ticking countdown | `reloadConsistency: true` |

The API-unavailable case was produced by failing only `/api/v1/mock-payments/*` requests at the browser network layer. This is fault injection in the test harness, not an application change.

The 15-second delay comes from the client’s bounded retry policy (three attempts with backoff). It is a UX observation, not a functional failure: the page does not hang.

---

## 12. Scenario F — Browser console and network

**Result: PASS**

A clean login flow was repeated after clearing browser cookies and origin storage. Traffic after authentication was inspected separately from pre-login traffic.

| Check | Result |
|---|---|
| Uncaught JavaScript exceptions after login | **0** |
| Console errors after login | **0** |
| Failed requests after login | **0** |
| CORS errors | **0** |
| HTTP responses ≥ 400 after login | **0** |
| Unexpected redirects | None; login → `/dashboard`, payments → `/dashboard/payments` |
| Hosts contacted | `http://localhost:5173`, `http://localhost:8081` only |
| Public payment URL origin | `http://localhost:5173` (correct) |
| Public API request auth | No `Authorization`, no `Cookie` |
| Auth refresh | Pre-login restore probe returned `401` as expected; no failed refresh after login |

Pre-login console noise (expected, not a defect):

- `401` on `/api/v1/auth/refresh` when no session exists.
- `404` on `/favicon.ico`.

The post-login status sequence included `200` for logout, login, merchant settings, overview and analytics. The payments list loaded with no failed request. A `data:`-URL favicon request is not a network host and was excluded from the host check.

No secret, cookie or authorization value is reproduced in this report.

Evidence: `13-payments-list.png`, `scenario-f.json`.

---

## 13. Defects found and fixes applied

**No reproducible defect was found. No application code was changed.**

The following observations were recorded but deliberately not “fixed”, because they are either expected behavior or product/UX decisions outside this phase:

1. **Public API failure takes about 15 seconds to surface.** The client performs bounded retries before showing the error. The UI does not hang and offers retry. This is a latency/UX consideration, not a correctness defect.
2. **Terminal public pages still show an expiration countdown.** A paid or failed payment displays “Expires in …” based on the original expiry timestamp. This is accurate historical data, though arguably unnecessary on a terminal page.
3. **Pre-login `401` and favicon `404` appear in the console.** Both are expected in this setup; neither affects the flow.
4. **Expired UI can precede the backend worker.** The page correctly shows `EXPIRED` from the timestamp while the API still returns `PENDING` until the 30-second worker interval processes it. This is a known design consequence; see §16.
5. **`127.0.0.1` vs `localhost`.** Using a different origin than the configured frontend/CORS origin suppresses the safe-navigation button. This is intentional exact-origin behavior; operators must use the configured origin.

No sorting, amount filter, provider filter, OpenAPI expansion, expiry change or security weakening was implemented.

---

## 14. Quality gate results

### Frontend (`pay-gate-dashboard`)

| Gate | Command | Exit code | Result |
|---|---|---|---|
| Type check | `npm run typecheck` | `0` | Pass |
| Lint | `npm run lint` | `0` | Pass |
| Unit tests | `npm run test` | `0` | **266 passed, 0 failed**, 0 skipped |
| Production build | `npm run build` | `0` | Pass; Vite built 1760 modules |

The test run emitted React `useLayoutEffect` server-render warnings from the existing Webhooks tests. They are warnings only; all tests passed.

### Backend (`pay-gate-backend`)

No backend code changed in this phase. The gates were still run to confirm the preserved Phase 2 state:

| Gate | Command | Exit code | Result |
|---|---|---|---|
| Formatting | `gofmt -l .` | `0` | No files listed |
| Static analysis | `go vet ./...` | `0` | Pass |
| Tests | `go test -count=1 ./...` | `0` | All packages `ok`; `pkg/response` and `swagger` have no test files |

These gates are real executions from this session, not copied claims from Phase 2.

---

## 15. Changed files

| Repository | Change | Notes |
|---|---|---|
| `pay-gate-backend` | `docs/phase-3-local-browser-e2e-verification.md` (new) | This report |

No other file was added, modified or deleted in either repository by this phase.

- The four backend files modified in Phase 2 and the Phase 2 report remain untouched.
- The frontend Phase 1 modifications and untracked files remain untouched.
- The browser harness (`cdp.mjs`, scenario scripts), screenshots, JSON evidence and credentials live under `/tmp/opencode/phase3-browser/` and `/tmp/opencode/phase2-e2e-1792464265/`, outside both repositories.
- The backend container, frontend dev server and database were used as running; no service configuration was changed.
- `dist/` was rebuilt but is Git-ignored.

---

## 16. Remaining risks and blockers

| Risk | Severity | Detail |
|---|---|---|
| Expiry state is frontend-derived before backend processing | Medium | With `PAYMENT_EXPIRY_INTERVAL=30s`, a payment can render as `EXPIRED` while its stored status is still `PENDING`. Whether the public simulator endpoints reject a past-expiry `PENDING` payment before the worker runs was **not** verified in this phase. |
| Expired UI was verified with a clock offset | Low | Real elapsed-time expiry (waiting ~30 minutes) was not exercised in the browser. The frontend logic uses the backend timestamp, but end-to-end worker timing was not observed. |
| Headless Chrome only | Low | Real Chromium rendering and interaction were verified. Firefox/Safari, desktop-window behavior, responsive breakpoints and visual regression were not. The OpenCode desktop browser remained disconnected. |
| Test fixture is not codified | Low | The run reused the disposable Phase 2 merchant. There is no repository-level seed script that recreates this account, so another operator needs the existing fixture or must provision a merchant. |
| API-unavailable feedback is slow | Low | Approximately 15 seconds pass before the error state appears because of retries. Correct, but potentially perceived as slow. |
| Staging configuration remains unsafe | Medium | Phase 2 found `.env.stage` pointing `FRONTEND_PUBLIC_URL` at the backend port and disabling expiry. It was not changed in this phase; dashboard staging must not use that profile without correction. |
| OpenAPI remains partial | Low | Auth, dashboard and public mock-payment paths are still missing from the published specification. Documented in Phase 2; not expanded here. |
| Mock-only validation | Expected | No real provider, real money, external webhook or production deployment was used. The QRIS payload is a development mock. |
| Public API is unauthenticated by design | Expected | The hosted payment page must work without dashboard JWT. Access relies on the opaque identifier. This was verified as intended, not treated as a vulnerability. |

No blocker prevented browser E2E.

---

## 17. API E2E vs browser E2E

| Layer | Phase | What it proves | Result |
|---|---|---|---|
| Unit / component tests | Existing suites | Handlers, repositories, config, DTO validation, auth helpers, URL safety, UI state helpers | **PASS** (backend all packages; frontend 266 tests) |
| Backend API E2E | Phase 2 | Real HTTP requests against PostgreSQL-backed services: creation, idempotency, public read, transitions, dashboard list/detail, overview, analytics, CORS | **PASS** |
| Browser E2E | Phase 3 (this report) | Real Chrome rendering and user interaction: login, form submit, popup navigation, HTML payment page, simulator buttons, status refresh, dashboard reflection, 404/API-failure states, console/network behavior | **PASS** |

Phase 2's **PARTIAL** verdict was caused solely by the missing browser layer. That layer has now been executed, so the two phases together cover API correctness and browser behavior. This report does **not** retroactively change any Phase 2 evidence; it adds the missing layer.

The browser E2E also independently confirmed backend state through the dashboard API after each UI transition, so a rendered success message alone was never treated as proof.

---

## 18. Recommended next phase

1. **Server-side expiry enforcement test.** Create a short-expiry fixture (local test configuration, not a staging change) and verify that success/failure simulation is rejected once `expired_at` has passed, both before and after the expiry worker runs.
2. **Codify the browser harness.** If adding a browser-test dependency is acceptable, port the verified CDP scenarios to Playwright with a checked-in fixture bootstrap. Otherwise, keep the dependency-free CDP harness under a documented scripts directory.
3. **Fix the staging profile.** Correct `.env.stage` so `FRONTEND_PUBLIC_URL` is the frontend origin and expiry remains enabled, then validate it without exposing secrets.
4. **Expand contract publication.** Add the verified auth, dashboard payment/overview/analytics and public mock-payment paths to OpenAPI.
5. **Optional UX polish.** Consider hiding the expiry countdown on terminal payments and shortening the public-error retry window; both are cosmetic/UX decisions, not blockers.
6. **Cross-browser and responsive pass.** Repeat the critical path in Firefox and at mobile widths if those are release requirements.

No implementation beyond this report was performed in Phase 3.
