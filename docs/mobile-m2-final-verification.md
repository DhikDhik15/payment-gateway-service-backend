# GO-SAAS — M2B Final Expiry Verification Report

**Scope:** Isolated staging environment only (port 18080 staging backend; port 8081 protected backend untouched).
**Status:** M2 INTEGRATION PASS

---

## 1. Environment Identity and Isolation Checks

| Check | Value | Status |
|---|---|---|
| Staging DB container | `go-saas-m2-staging-db` (postgres:16-alpine) | healthy |
| Staging DB port / name | 127.0.0.1:55433 / `go_saas_m2_staging` | verified |
| Staging backend process | `paygate-stage` (pid 281117 after restart) | running |
| Staging backend bind address | 127.0.0.1:18080 | verified (loopback only) |
| Staging `/health` | HTTP 200 | pass |
| Staging `/health/ready` | HTTP 200 | pass |
| Protected backend (port 8081) container | `pay-gate-app` | healthy, untouched |
| Protected backend `/health` | HTTP 200 | pass |
| Migration version | 25 / 25 | clean (not dirty) |
| Migration tool | `migrate` against staging DB | exit 0 |
| Git branch | `main`, up to date with origin/main | verified |
| Uncommitted changes | only `.env.stage` (pre-existing CORS addition preserved) | no unrelated code/config changes |
| Flutter `.env` | unchanged (`API_BASE_URL` preserved; no `flutter/.env` in repo) | verified |

Safety rules honored: no code changes, no new endpoint, no expiry-duration change, no direct SQL writes to status/`expired_at`, no DB reset/cleanup, protected backend on 8081 never stopped/restarted, `PAYMENT_PROVIDER=mock` only, no secrets printed.

---

## 2. Initial and Final Expiry Configuration

| Stage | `PAYMENT_EXPIRY_ENABLED` | Source |
|---|---|---|
| Original / committed (HEAD) | `false` | `.env.stage` (git HEAD) |
| During verification window | `true` | `.env.stage` working tree; expiry worker started with 30s interval |
| Final / restored | `false` | `.env.stage` working tree (restored to original) |

Expiry interval: `PAYMENT_EXPIRY_INTERVAL=30s`, batch size `100`. No other configuration values were changed. The pre-existing unrelated `CORS_ALLOWED_ORIGINS` addition (`localhost:35597`) was preserved and not reverted.

---

## 3. Worker Startup Evidence

Staging backend startup log (`paygate-stage.log`) at bootstrap:
```
"msg":"expiry worker started","interval":30000000000,"batch_size":100
```
Interval = 30,000,000,000 ns = 30s; batch_size = 100. The ExpiryWorker was running and processing transactions where `status='PENDING' AND expired_at <= NOW()`.

After configuration restoration (Phase 6), the restarted staging backend log confirms:
```
"msg":"expiry worker disabled (PAYMENT_EXPIRY_ENABLED=false)"
```

---

## 4. Payment Identifier and Merchant Scope

A new merchant was provisioned via the supported admin onboarding API (`POST /api/v1/admin/onboarding/merchants`, `X-Admin-Key`), and a merchant API key credential (`X-API-Key`) was obtained for authentication. The merchant API key secret was treated as a secret and is not recorded here.

- Merchant ID: `b89cc882-3b4d-443f-8f55-b61709a43c34` (`M2 Expiry Staging Merchant`, ACTIVE)
- Merchant order ID: `MORDER-expiry-1792381502`
- Transaction ID (payment identifier): `26c66e80-2203-4f0e-9785-aae6aabbccc2`
- Provider: `MOCK`
- Currency: `IDR`, payment method: `QRIS`, amount: `150000`

The payment belongs to the intended staging merchant (`b89cc882…`), distinct from the pre-existing staging merchant (`b80f9794…`).

---

## 5. Creation Timestamp and `expired_at`

Official API response (`POST /api/v1/payments`, HTTP 201):

| Field | Value |
|---|---|
| `transaction_id` | `26c66e80-2203-4f0e-9785-aae6aabbccc2` |
| `status` | `PENDING` |
| `created_at` | 2026-10-19T10:45:02.632465+07:00 (= 03:45:02 UTC) |
| `expired_at` | 2026-10-19T11:15:02.635607+07:00 (= 04:15:02 UTC) |

`expired_at` is exactly ~30 minutes after `created_at`, consistent with the mock provider's default 30-minute expiry duration. The payment was not terminal at creation (status `PENDING`).

---

## 6. Timestamped Status Observations (official GET `/api/v1/payments/:id`)

Polled at 25-second intervals (conservative; never more than once per 10 seconds).

| Timestamp (UTC) | HTTP | Status | Note |
|---|---|---|---|
| 2026-10-19T03:45:02 | 201 | PENDING | Payment created (`expired_at` = 04:15:02 UTC) |
| 2026-10-19T04:14:32 | 200 | PENDING | Before `expired_at` |
| 2026-10-19T04:14:57 | 200 | PENDING | Before `expired_at` |
| 2026-10-19T04:15:22 | 200 | EXPIRED | After `expired_at` (04:15:02), within the 30s worker interval |

The `updated_at` on the expired record is 2026-10-19T11:15:08.097913+07:00 (= 04:15:08 UTC), i.e. the ExpiryWorker marked it EXPIRED ~6 seconds after `expired_at` passed.

---

## 7. Official API Evidence: PENDING → EXPIRED

Retrieving the same payment after expiration (`GET /api/v1/payments/26c66e80-2203-4f0e-9785-aae6aabbccc2`, HTTP 200):

```json
{
  "success": true,
  "data": {
    "transaction_id": "26c66e80-2203-4f0e-9785-aae6aabbccc2",
    "merchant_order_id": "MORDER-expiry-1792381502",
    "amount": 150000,
    "currency": "IDR",
    "payment_method": "QRIS",
    "provider": "MOCK",
    "status": "EXPIRED",
    "expired_at": "2026-10-19T11:15:02.635607+07:00",
    "created_at": "2026-10-19T10:45:02.632465+07:00",
    "updated_at": "2026-10-19T11:15:08.097913+07:00"
  },
  "meta": { "request_id": "req_ttn5xrogrugjxgcz" }
}
```

The real PENDING → EXPIRED transition is confirmed via the official API (no SQL manipulation, no internal endpoints, no forced status).

---

## 8. Terminal-State Regression Results

Read-only comparison of all staging transactions before and after expiration (no modifications made):

| Merchant | Order ID | Status before | Status after | Unchanged? |
|---|---|---|---|---|
| `b80f9794…` | `M2-ORDER-001` | PAID | PAID | yes |
| `b80f9794…` | `M2-ORDER-FAIL` | FAILED | FAILED | yes |
| `b80f9794…` | `65b47760…` | PAID | PAID | yes |
| `b80f9794…` | `6f3ded68…` | FAILED | FAILED | yes |
| `b80f9794…` | `a6c25617…` | CANCELLED | CANCELLED | yes |
| `b80f9794…` | `3c309f9b…` | EXPIRED | EXPIRED | yes (pre-existing expiry) |
| `b89cc882…` | `MORDER-expiry-1792381502` | PENDING | EXPIRED | expected (target payment) |

- Previously verified PAID payments remain PAID. ✓
- Previously verified FAILED payments remain FAILED. ✓
- Previously verified CANCELLED payment remains CANCELLED. ✓
- No unrelated merchant's records were changed (only the target payment transitioned to EXPIRED). ✓

Note: API-based re-checks of the pre-existing merchant (`b80f9794…`) payments were not possible because its API-key secrets are hashed/unrecoverable (Phase 8D.3 MIGRATED state, no legacy key) — this is by-design merchant isolation. Regression for those records was therefore confirmed via read-only database comparison, which rule 6 permits (reads only; no status/`expired_at` modifications).

---

## 9. Protected Backend Health Checks

| Endpoint | Result |
|---|---|
| `http://127.0.0.1:8081/health` | HTTP 200 `{"status":"ok"}` — before, during, and after verification |

The protected backend on port 8081 was never stopped, restarted, rebuilt, or reconfigured. It remained healthy throughout.

---

## 10. Configuration Restoration Evidence

- `.env.stage` `PAYMENT_EXPIRY_ENABLED` restored to exact original value: `false` (matches git HEAD).
- Only unrelated pre-existing change remains in `.env.stage`: `CORS_ALLOWED_ORIGINS` addition (`localhost:35597`) — preserved, not reverted.
- Staging backend restarted with restored config; health `/health/ready` HTTP 200.
- Restarted staging backend log confirms: `"msg":"expiry worker disabled (PAYMENT_EXPIRY_ENABLED=false)"`.
- Running process environment confirms `PAYMENT_EXPIRY_ENABLED=false`.
- Protected backend port 8081: HTTP 200, healthy.
- Staging services left running; no staging resources removed.

---

## 11. Blockers or Deviations

None. All required steps completed:

- Expiry enabled in staging configuration without code changes or a new endpoint. ✓
- Fresh payment created via supported API with mock provider; initial status PENDING, `expired_at` ≈ creation + 30 minutes. ✓
- Real expiration observed via official API: PENDING → EXPIRED (within one 30s ExpiryWorker interval after `expired_at`). ✓
- Terminal-state regression verified (PAID/FAILED/CANCELLED unchanged; no unrelated merchant changes). ✓
- Configuration restored to `false`; staging and protected backends healthy. ✓

---

## Final Overall Status

**M2 INTEGRATION PASS**

All required checks (expiry worker enabled/running, real PENDING → EXPIRED transition via official API, terminal-state regression, configuration restoration, protected backend health) are verified.

M3 is not part of this task and was not started.
