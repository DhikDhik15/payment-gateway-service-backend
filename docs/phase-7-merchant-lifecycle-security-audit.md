# Phase 7 — Merchant Lifecycle & Merchant Read Security Audit

**Date:** 2026-09-22  
**Phase:** 7 (pre-implementation audit)  
**Scope:** Read-only repository analysis — no code was modified  
**Status:** AUDIT COMPLETE

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Current Merchant Lifecycle](#2-current-merchant-lifecycle)
3. [Current Status Behavior](#3-current-status-behavior)
4. [Lifecycle State Matrix](#4-lifecycle-state-matrix)
5. [Status Transition Analysis](#5-status-transition-analysis)
6. [API Authentication Impact](#6-api-authentication-impact)
7. [Dashboard Session Impact](#7-dashboard-session-impact)
8. [API Key Impact](#8-api-key-impact)
9. [Payment Impact](#9-payment-impact)
10. [Provider Webhook Impact](#10-provider-webhook-impact)
11. [Outbound Webhook Impact](#11-outbound-webhook-impact)
12. [Merchant Read API Security](#12-merchant-read-api-security)
13. [Tenant Isolation Findings](#13-tenant-isolation-findings)
14. [Legacy Authentication Findings](#14-legacy-authentication-findings)
15. [Audit Logging Findings](#15-audit-logging-findings)
16. [Database Findings](#16-database-findings)
17. [Frontend/Consumer Findings](#17-frontendconsumer-findings)
18. [Proposed Phase 7 API Surface](#18-proposed-phase-7-api-surface)
19. [Proposed Read API Security](#19-proposed-read-api-security)
20. [Required Test Matrix](#20-required-test-matrix)
21. [MUST HAVE](#21-must-have)
22. [SHOULD HAVE](#22-should-have)
23. [OUT OF SCOPE](#23-out-of-scope)
24. [Risks](#24-risks)
25. [Open Business Decisions](#25-open-business-decisions)
26. [Recommended Implementation Order](#26-recommended-implementation-order)

---

## 1. Executive Summary

The repository defines three merchant lifecycle statuses (`ACTIVE`, `INACTIVE`, `SUSPENDED`) in the model and enforces them at the database level via a `CHECK` constraint. However, **there is no API to mutate merchant status**, and the status distinctions between `INACTIVE` and `SUSPENDED` are completely unused — both produce identical behavior everywhere in the codebase.

Three critical security gaps exist that must be addressed in Phase 7:

1. **No merchant lifecycle API.** Merchant status can only be changed directly in the database. There is no `PATCH /admin/merchants/:id/status` endpoint or equivalent.
2. **Dashboard is not merchant-status-aware.** `RequireDashboardAuth` (`internal/middleware/dashboard_auth.go`) checks the *user's* status but never checks the *merchant's* status. A SUSPENDED merchant's dashboard users continue to have full dashboard access with valid JWTs.
3. **`GET /api/v1/merchants/:id` is public and unauthenticated.** Any caller can query any merchant's name, code, and current lifecycle status by UUID — no API key, no JWT, no authentication required.

Secondary findings:

- No audit logging exists for any merchant lifecycle event.
- No session invalidation mechanism exists at the merchant level (only at the individual user level via `DeleteByUserID`).
- Provider webhooks correctly bypass merchant status checks (intentional: financial state must be settled regardless of merchant status).
- Outbound webhook workers also bypass merchant status checks (partially intentional: in-flight deliveries should complete, but new enqueues for SUSPENDED merchants warrant a business decision).

---

## 2. Current Merchant Lifecycle

### 2.1 Model

**File:** `internal/model/merchant.go`

```go
type MerchantStatus string

const (
    MerchantStatusActive    MerchantStatus = "ACTIVE"
    MerchantStatusInactive  MerchantStatus = "INACTIVE"
    MerchantStatusSuspended MerchantStatus = "SUSPENDED"
)

func (m *Merchant) IsActive() bool {
    return m.Status == MerchantStatusActive
}
```

`IsActive()` returns `true` only for `ACTIVE`. Both `INACTIVE` and `SUSPENDED` return `false` and are treated identically everywhere in the codebase.

### 2.2 Database Schema

**Migration:** `migrations/000001_create_merchants.up.sql`

```sql
CREATE TABLE IF NOT EXISTS merchants (
    id          UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(150)    NOT NULL,
    code        VARCHAR(50)     NOT NULL,
    api_key     VARCHAR(255)    NOT NULL,
    api_secret  VARCHAR(255)    NOT NULL,
    status      VARCHAR(20)     NOT NULL DEFAULT 'ACTIVE',
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

ALTER TABLE merchants
    ADD CONSTRAINT chk_merchants_status
    CHECK (status IN ('ACTIVE', 'INACTIVE', 'SUSPENDED'));
```

- Default status on creation: `ACTIVE`
- Database enforces valid enum values via `CHECK` constraint
- No `deleted_at` column — no soft-delete mechanism
- No `status_changed_at` column — no lifecycle timestamp

### 2.3 Creation Path

All merchants are created with `Status: model.MerchantStatusActive` hardcoded:

- **`internal/service/merchant_service.go`** → `CreateMerchant()`: sets `Status: model.MerchantStatusActive`
- **`internal/service/onboarding_service.go`** → `OnboardMerchant()`: sets `Status: model.MerchantStatusActive`

There is no code path that creates a merchant in any status other than `ACTIVE`.

### 2.4 Repository Methods

**File:** `internal/repository/merchant_repository.go`

Interface:
```go
type MerchantRepository interface {
    Create(ctx context.Context, m *model.Merchant) error
    CreateInTx(ctx context.Context, tx pgx.Tx, m *model.Merchant) error
    GetByID(ctx context.Context, id uuid.UUID) (*model.Merchant, error)
    GetByAPIKey(ctx context.Context, apiKey string) (*model.Merchant, error)
    ExistsByCode(ctx context.Context, code string) (bool, error)
}
```

**There is no `UpdateStatus`, `Suspend`, `Activate`, or `Deactivate` method on the merchant repository.** Merchant status cannot be changed through any application code path.

---

## 3. Current Status Behavior

### 3.1 Where Status Is Checked

| Location | File | Function | What Is Checked | Effect |
|---|---|---|---|---|
| Auth middleware (new key) | `internal/middleware/auth.go` | `Auth()` | `merchant.IsActive()` after Phase 5C auth | 401 `MERCHANT_INACTIVE` if not ACTIVE |
| Auth middleware (legacy key) | `internal/middleware/auth.go` | `Auth()` | `merchant.IsActive()` after legacy lookup | 401 `MERCHANT_INACTIVE` if not ACTIVE |
| Simulator handler | `internal/handler/simulator_handler.go` | `resolveConfiguredMerchant()` | `merchant.IsActive()` | 400 `MERCHANT_INACTIVE` if not ACTIVE |
| Server startup validation | `cmd/server/main.go` | `validateSimulatorMerchant()` | `merchant.IsActive()` | Server exits if simulator merchant is not ACTIVE |
| Dashboard middleware | `internal/middleware/dashboard_auth.go` | `RequireDashboardAuth()` | `user.IsActive()` (user status, NOT merchant status) | Does NOT check merchant status |

### 3.2 SUSPENDED vs INACTIVE Distinction

**Finding: SUSPENDED and INACTIVE are functionally identical today.**

Both statuses fail `IsActive()` which is `return m.Status == MerchantStatusActive`. There is no code anywhere in the repository that branches differently between `SUSPENDED` and `INACTIVE`. No handler, service, middleware, or repository treats them differently. The distinction exists only as a defined constant in the model and a constraint in the database migration.

### 3.3 Error Response for Non-ACTIVE Merchants

**File:** `internal/middleware/auth.go`, lines covering both paths:

```go
if !merchant.IsActive() {
    response.Unauthorized(c, response.CodeMerchantInactive, "Merchant account is not active")
    c.Abort()
    return
}
```

**Error code:** `MERCHANT_INACTIVE`  
**HTTP status:** `401 Unauthorized`  
**Applies to:** Both Phase 5C compound key (`pk_:sk_`) and legacy bare key (`pk_`) authentication paths.

---

## 4. Lifecycle State Matrix

Based strictly on code evidence:

| Status | API Key Auth | Dashboard Access | Create Payment | Read Payment | Cancel Payment | Inbound Webhook | Outbound Webhook |
|---|---|---|---|---|---|---|---|
| **ACTIVE** | ✅ Allowed | ✅ Allowed (user status permitting) | ✅ Allowed | ✅ Allowed | ✅ Allowed | ✅ Processed | ✅ Delivered (if config ACTIVE) |
| **INACTIVE** | ❌ 401 `MERCHANT_INACTIVE` | ✅ **Allowed** (merchant status NOT checked) | ❌ Blocked by auth | ❌ Blocked by auth | ❌ Blocked by auth | ✅ **Processed** (no merchant check) | ✅ **Delivered** (no merchant check) |
| **SUSPENDED** | ❌ 401 `MERCHANT_INACTIVE` | ✅ **Allowed** (merchant status NOT checked) | ❌ Blocked by auth | ❌ Blocked by auth | ❌ Blocked by auth | ✅ **Processed** (no merchant check) | ✅ **Delivered** (no merchant check) |

**Bold entries indicate behavior that is likely unintended.**

---

## 5. Status Transition Analysis

### 5.1 Currently Possible Transitions

**Via application code:** None. There is no `UpdateStatus` method in `MerchantRepository` and no service or handler that mutates `merchants.status`.

**Via direct database manipulation only:** Any transition between `ACTIVE`, `INACTIVE`, and `SUSPENDED` is technically possible at the DB level, constrained only by the `CHECK` constraint.

### 5.2 Implied Lifecycle

The three statuses suggest an intended lifecycle, but no code enforces any ordering. Based on naming conventions in SaaS products and the available statuses, the implied intent is:

```
[onboarding] → ACTIVE
ACTIVE → SUSPENDED   (temporary: violation, investigation, payment risk)
SUSPENDED → ACTIVE   (reinstatement)
ACTIVE → INACTIVE    (permanent deactivation / voluntary closure)
SUSPENDED → INACTIVE (escalated deactivation after suspension)
```

However, this is inferred from naming — it is not enforced by the codebase.

### 5.3 What Does Not Exist

- No status transition history table
- No `status_changed_at` timestamp on the `merchants` table
- No `changed_by` attribution on any status change
- No audit log for merchant lifecycle events
- No concept of "temporary" vs "permanent" deactivation beyond the two statuses
- No "archived" or "deleted" state — soft-delete is not implemented

### 5.4 SaaS Lifecycle Requirements

A functioning SaaS platform minimally requires:

| Transition | Trigger | Notes |
|---|---|---|
| `ACTIVE → SUSPENDED` | Admin action (fraud, non-payment, ToS) | Should block new business, preserve financial data |
| `SUSPENDED → ACTIVE` | Admin action (issue resolved) | Should restore all capabilities |
| `ACTIVE → INACTIVE` | Voluntary closure or long-term deactivation | Should block everything permanently |
| `SUSPENDED → INACTIVE` | Escalation after suspension | Admin-only |

Transitions `INACTIVE → ACTIVE` and `INACTIVE → SUSPENDED` may or may not be desirable — **NEEDS BUSINESS DECISION**.

---

## 6. API Authentication Impact

### 6.1 Phase 5C New Key Authentication

**File:** `internal/middleware/auth.go` — `Auth()` function, Stage 1 block

Flow:
1. Credential parsed: `pk_<hex>:sk_<hex>` compound format detected by `isNewStyleKey()`
2. `apiKeySvc.AuthenticateByAPIKey()` called
3. Inside `MerchantAPIKeyService.AuthenticateByAPIKey()` (`internal/service/merchant_api_key_service.go`):
   - Key looked up by `key_id`
   - Argon2id secret verified
   - `keyRecord.IsActive()` checked (key status + expiry)
   - `merchantRepo.GetByID()` called → loads merchant
4. Back in middleware: `merchant.IsActive()` checked
5. If not active → `401 MERCHANT_INACTIVE`

**Current behavior:** Suspended or inactive merchant → API key authentication fails with 401. This is the correct behavior for blocking new business operations.

**Gap:** The key's own `IsActive()` check is separate from the merchant's `IsActive()` check. A revoked key on an ACTIVE merchant returns 401 `INVALID_API_KEY`. A valid key on a SUSPENDED merchant returns 401 `MERCHANT_INACTIVE`. These are distinguishable by error code today, which is fine.

### 6.2 Legacy Key Authentication

**File:** `internal/middleware/auth.go` — Stage 2 block

Flow:
1. Bare `pk_<hex>` credential (no `:sk_` segment) detected
2. `merchantSvc.GetMerchantByAPIKey()` called → `merchantRepo.GetByAPIKey()`
3. Merchant returned; `merchant.IsActive()` checked
4. If not active → `401 MERCHANT_INACTIVE`

**Current behavior:** Identical to Phase 5C in terms of merchant status enforcement. SUSPENDED and INACTIVE both blocked.

### 6.3 Test Evidence

`internal/middleware/auth_middleware_test.go`:

```go
func TestAuth_InactiveMerchant_Legacy(t *testing.T) {
    for _, status := range []model.MerchantStatus{
        model.MerchantStatusInactive,
        model.MerchantStatusSuspended,
    } {
        // Both statuses → 401 CodeMerchantInactive
    }
}

func TestAuth_NewKey_InactiveMerchant(t *testing.T) {
    merchant := &model.Merchant{Status: model.MerchantStatusSuspended}
    // → 401 CodeMerchantInactive
}
```

Both statuses produce `401 MERCHANT_INACTIVE` via both authentication paths. The test suite confirms this is the intended behavior.

---

## 7. Dashboard Session Impact

### 7.1 Authentication Flow

**File:** `internal/middleware/dashboard_auth.go` — `RequireDashboardAuth()`

```go
// Loads the authenticated user from the database
user, err := userRepo.GetByID(c.Request.Context(), userID)
// ...
if !user.IsActive() {
    response.Unauthorized(c, response.CodeUserDisabled, "Account is disabled")
    c.Abort()
    return
}
```

**The middleware checks `user.IsActive()` (user-level status) but does NOT load or check the associated merchant's status.**

### 7.2 Critical Gap: Dashboard Ignores Merchant Status

If a merchant is set to `SUSPENDED`:
- Existing dashboard JWT access tokens remain valid until TTL expiry
- Refresh token rotation continues to work
- All dashboard endpoints remain accessible
- `GET /api/v1/dashboard/settings` will return the merchant's `SUSPENDED` status as data, but no enforcement occurs

This is a significant security gap for a SaaS platform. An operator suspending a merchant for fraud or ToS violations would expect all access to immediately stop, but dashboard users can continue operating until their JWTs expire (typically minutes to hours).

### 7.3 Session Revocation Capabilities

**File:** `internal/repository/dashboard_session_repository.go`

Available methods:
```go
Delete(ctx context.Context, id uuid.UUID) error          // revoke one session
DeleteByUserID(ctx context.Context, userID uuid.UUID) error // revoke all sessions for one user
DeleteExpired(ctx context.Context) error                    // cleanup expired sessions
```

`DeleteByUserID` is called in `dashboard_user_service.go` → `UpdateUserStatus()` when a user is DISABLED:

```go
if status == model.DashboardUserStatusDisabled && s.sessionRepo != nil {
    s.sessionRepo.DeleteByUserID(ctx, targetUserID)
}
```

**There is no `DeleteByMerchantID` method on the session repository.** Merchant-wide session revocation does not exist. To revoke all sessions for a suspended merchant, the implementation would need to: (a) add a `DeleteByMerchantID` method that JOINs through `merchant_users`, or (b) iterate and call `DeleteByUserID` per user.

### 7.4 JWT Short-Circuit Limitation

Even after deleting all refresh sessions, outstanding short-lived JWT access tokens remain valid until they expire. `RequireDashboardAuth` validates the JWT signature and expiry but does not check a revocation list. For immediate effect, the JWT TTL (typically 15 minutes) represents the maximum residual access window after session deletion.

### 7.5 Login Behavior Under Suspension

The `AuthService.Login()` method (`internal/service/auth_service.go`) checks only `user.IsActive()`, not `merchant.IsActive()`. A dashboard user can successfully log in to a SUSPENDED merchant's dashboard.

---

## 8. API Key Impact

### 8.1 Phase 5C Merchant API Keys

**File:** `internal/repository/merchant_api_key_repository.go`

The key table has its own `status` column (`ACTIVE` / `REVOKED`) independent of the merchant status.

**Key `IsActive()` check** (`internal/model/merchant_api_key.go`):
```go
func (k *MerchantAPIKey) IsActive() bool {
    if k.Status != MerchantAPIKeyStatusActive {
        return false
    }
    if k.ExpiresAt != nil && time.Now().UTC().After(*k.ExpiresAt) {
        return false
    }
    return true
}
```

**Merchant status check in Auth middleware** is a separate subsequent check.

**Current state: Merchant suspension does NOT automatically revoke API keys.** The keys remain in `ACTIVE` status in `merchant_api_keys`. They cannot authenticate because the merchant-level `IsActive()` check in the Auth middleware blocks the request, but the key records themselves are untouched.

### 8.2 Key Operations Under Suspension (Current Behavior)

Since all Phase 5C key management endpoints (`POST/GET/DELETE /api/v1/merchants/:id/api-keys`) require the `Auth` middleware, and that middleware blocks suspended merchants:

| Operation | Current Behavior |
|---|---|
| List keys | ❌ Blocked by Auth middleware (401) |
| Create key | ❌ Blocked by Auth middleware (401) |
| Revoke key | ❌ Blocked by Auth middleware (401) |
| Rotate key | ❌ Blocked by Auth middleware (401) |
| Authenticate | ❌ Blocked by Auth middleware (401 `MERCHANT_INACTIVE`) |

Exception: Dashboard API key endpoints (`/api/v1/dashboard/api-keys`) use `RequireDashboardAuth` (which does not check merchant status), so key operations via the dashboard remain accessible even when the merchant is SUSPENDED.

### 8.3 Legacy Credential Behavior Under Suspension

`merchants.api_key` and `merchants.api_secret` columns exist on the merchant row. Both the legacy key lookup and the Phase 5C lookup check `merchant.IsActive()` after resolving the merchant. Legacy keys are blocked at the same point as Phase 5C keys.

**Suspension blocks legacy key auth as completely as it blocks Phase 5C key auth.**

### 8.4 Revoked Key vs Suspended Merchant

| Scenario | Error Code | HTTP Status |
|---|---|---|
| Key is REVOKED | `INVALID_API_KEY` | 401 |
| Key is expired | `INVALID_API_KEY` | 401 |
| Merchant is SUSPENDED | `MERCHANT_INACTIVE` | 401 |
| Merchant is INACTIVE | `MERCHANT_INACTIVE` | 401 |

These are currently distinguishable by error code. The distinction between a revoked key and a suspended merchant is preserved.

---

## 9. Payment Impact

### 9.1 Payment Creation

**File:** `internal/service/payment_service.go` — `createPayment()`

The payment service does not check merchant status. It relies entirely on the Auth middleware having already verified the merchant is ACTIVE before the handler is reached. `merchantID` is extracted from `c.Get(model.ContextKeyMerchant)` in the handler, which is set only after successful auth.

**Under suspension:** New payment creation is blocked because the Auth middleware blocks the request before it reaches the payment service. The payment service itself has no awareness of merchant status.

### 9.2 Payment Read

Same as above — `GET /api/v1/payments/:id` and `GET /api/v1/payments` require the Auth middleware. Suspended merchants cannot read their own transaction history via the merchant API.

**Exception:** Dashboard payment endpoints (`/api/v1/dashboard/payments`) use `RequireDashboardAuth` and are not merchant-status-gated. Dashboard users of a SUSPENDED merchant can still read payment history via the dashboard.

### 9.3 Payment Cancellation

`POST /api/v1/payments/:id/cancel` also requires the Auth middleware. Suspended merchants cannot cancel pending transactions via the merchant API.

**This is a potential operational problem.** A merchant that is suspended while having `PENDING` transactions in the payment provider cannot cancel those transactions via the API. They remain pending until the provider expires them or the webhook arrives.

### 9.4 Effect on Existing Transactions

Changing merchant status has zero effect on existing transaction rows. There is no code that:
- Updates `transactions.status` when `merchants.status` changes
- Cancels PENDING transactions on suspension
- Freezes or marks PAID transactions

All existing transaction data is preserved regardless of merchant status changes. This is the correct financial behavior — historical records must never be modified based on merchant status.

### 9.5 Expiry Worker

**File:** `internal/service/expiry_worker.go`

The expiry worker periodically transitions PENDING transactions to EXPIRED based on `expired_at`. It does not check merchant status. PENDING transactions for SUSPENDED merchants will correctly expire on schedule.

---

## 10. Provider Webhook Impact

### 10.1 Inbound Webhook Route

**Route:** `POST /api/v1/webhooks/providers/:provider`  
**File:** `internal/handler/webhook_handler.go`  
**Middleware:** None (no merchant auth, no admin auth)

```go
// Webhook endpoints (NO merchant auth — requests originate from providers)
webhooks := v1.Group("/webhooks")
{
    webhooks.POST("/providers/:provider", webhookHandler.Receive)
}
```

**There is no merchant authentication on inbound provider webhooks by design.** Webhooks originate from payment providers (Midtrans, mock), not from merchants. Authentication is via provider-specific signature verification (`X-Webhook-Signature` / Midtrans notification_key).

### 10.2 Merchant Status in Webhook Processing

**File:** `internal/service/webhook_service.go` — `ProcessWebhook()`

The webhook service identifies transactions by `provider_transaction_id`, not by `merchant_id`. The processing flow:

1. Verify provider signature
2. Parse event
3. Find transaction by `provider` + `provider_transaction_id`
4. Apply state transition

**Merchant status is never checked.** This is intentional and correct: a `PENDING` transaction for a SUSPENDED merchant that gets paid by the customer must have its status updated to `PAID` to preserve financial accuracy.

### 10.3 Financial Correctness

Blocking webhook processing for suspended merchants would create financial inconsistency — the provider has the payment, but the gateway database would not reflect it. This would break reconciliation, settlement, and any future refund eligibility calculation.

**The current behavior (webhooks bypass merchant status) is financially correct and should be preserved.**

---

## 11. Outbound Webhook Impact

### 11.1 Delivery Enqueue

**File:** `internal/service/merchant_webhook_service.go` — `BuildDelivery()`

```go
func (p *merchantWebhookPublisher) BuildDelivery(ctx context.Context, tx *model.Transaction, ...) (*model.MerchantWebhookDelivery, error) {
    cfg, err := p.configRepo.FindActiveByMerchantID(ctx, tx.MerchantID)
    // ...
}
```

`FindActiveByMerchantID` queries:
```sql
SELECT ... FROM merchant_webhook_configs WHERE merchant_id = $1 AND status = 'ACTIVE'
```

This checks the **webhook config status** (`ACTIVE`/`DISABLED`), not the **merchant status**. If a merchant is SUSPENDED but has an ACTIVE webhook config, new deliveries will still be enqueued.

### 11.2 Delivery Dispatch

**File:** `internal/service/merchant_webhook_dispatcher.go` — `deliverOne()`

The dispatcher claims PENDING deliveries and delivers them. It does not check merchant status at any point. In-flight and queued deliveries continue to be dispatched regardless of merchant suspension.

### 11.3 Retry Worker

**File:** `internal/service/merchant_webhook_dispatcher.go` — `MerchantWebhookWorker`

The worker polls on a configurable interval. It does not check merchant status. Retries for suspended merchant deliveries continue uninterrupted.

### 11.4 Behavioral Summary

| Scenario | Current Behavior |
|---|---|
| Merchant SUSPENDED, new payment.paid event | Outbound webhook still enqueued if config is ACTIVE |
| Merchant SUSPENDED, PENDING delivery in queue | Continues to be dispatched and retried |
| Merchant SUSPENDED, webhook config DISABLED | No delivery (config status blocks it, not merchant status) |

Whether new outbound webhook delivery should stop on merchant suspension is a **BUSINESS DECISION** — see Section 25.

---

## 12. Merchant Read API Security

### 12.1 Current Endpoint

**Route:** `GET /api/v1/merchants/:id`  
**File:** `internal/handler/merchant_handler.go` — `GetByID()`  
**Authentication:** None

```go
// GET by id remains public for now (returns non-sensitive fields only); see follow-up.
merchants.GET("/:id", merchantHandler.GetByID)
```

The comment in `cmd/server/main.go` explicitly flags this as a known gap: "see follow-up".

### 12.2 Fields Returned

**Model:** `model.GetMerchantResponse`

```go
type GetMerchantResponse struct {
    ID        uuid.UUID      `json:"id"`
    Name      string         `json:"name"`
    Code      string         `json:"code"`
    Status    MerchantStatus `json:"status"`
    CreatedAt time.Time      `json:"created_at"`
    UpdatedAt time.Time      `json:"updated_at"`
}
```

No API secrets are returned. The `APISecret` field is explicitly excluded via `json:"-"` on the `Merchant` struct. The `APIKey` field is excluded from `GetMerchantResponse` by construction (it uses a dedicated DTO, not the raw `Merchant`).

### 12.3 Security Risks

| Risk | Severity | Evidence |
|---|---|---|
| UUID enumeration: attacker can iterate UUIDs and retrieve merchant profile | Medium | Route has no auth, UUIDs are v4 but volume scanning is feasible |
| Lifecycle status exposed publicly: attacker knows if a merchant is SUSPENDED | Medium | `status` field included in response |
| Competitive intelligence: merchant name and code exposed without authentication | Low-Medium | Depends on business context |
| No rate limiting visible in current middleware stack | Low | No rate-limit middleware registered in `cmd/server/main.go` |

### 12.4 Known Internal Consumers

The `GetByID` endpoint is called from two internal service paths:

1. `internal/service/merchant_api_key_service.go` → `CreateKey()`: calls `merchantRepo.GetByID()` directly (repository, not HTTP)
2. `internal/service/merchant_webhook_service.go` → `Upsert()`: calls `merchantRepo.GetByID()` directly (repository, not HTTP)
3. `internal/handler/dashboard_settings_handler.go` → calls `merchantSvc.GetMerchant()` which calls the same repository method

**These are all repository-level calls, not HTTP calls to the endpoint.** The HTTP endpoint `GET /api/v1/merchants/:id` has no identified internal HTTP consumer — it appears to be an external-facing endpoint only.

### 12.5 Authenticated Equivalent

A dashboard-authenticated equivalent exists: `GET /api/v1/dashboard/settings` (Phase 9) returns `DashboardMerchantSettingsResponse` containing the same fields, gated by `RequireDashboardAuth`. This provides an authenticated path for any legitimate consumer that needs merchant profile data.

### 12.6 Legacy POST /api/v1/merchants

**Route:** `POST /api/v1/merchants`  
**File:** `internal/handler/merchant_handler.go` — `Create()`  
**Authentication:** `middleware.AdminAuth(cfg.Admin.APIKey)` (X-Admin-Key)

This endpoint is protected. The Swagger comment explicitly designates it as a low-level row-creation endpoint and defers to `POST /api/v1/admin/onboarding/merchants` for full provisioning. It exists for backward compatibility with pre-Phase-6 tooling.

---

## 13. Tenant Isolation Findings

### 13.1 Merchant API Key Paths

**File:** `internal/handler/merchant_api_key_handler.go` — `authorizeForMerchant()`

```go
func authorizeForMerchant(c *gin.Context, targetMerchantID uuid.UUID) bool {
    m := middleware.MerchantFromContext(c)
    if m.ID != targetMerchantID {
        response.Forbidden(c, response.CodeForbidden, "...")
        return false
    }
    return true
}
```

The path parameter `:id` (merchant UUID) is compared against the authenticated merchant from context. Cross-merchant key access returns 403.

**Isolation is correctly enforced here.**

### 13.2 Dashboard Paths

**File:** `internal/handler/dashboard_user_handler.go`, `internal/handler/dashboard_api_key_handler.go`, etc.

Dashboard handlers extract `caller.MerchantID` from the authenticated user context (set by `RequireDashboardAuth`). All repository queries use this `caller.MerchantID` directly. No dashboard handler accepts a `merchant_id` from the request body or URL parameter — the merchant scope is always derived from the authenticated identity.

**Isolation is correctly enforced here.** The pattern is consistent across all Phase 9 dashboard handlers.

### 13.3 Payment Paths

**File:** `internal/handler/payment_handler.go`

`merchant.ID` is extracted from `middleware.MerchantFromContext(c)`. Repository queries use `FindByMerchantAndID(ctx, merchantID, transactionID)` which includes the merchant scope in the `WHERE` clause.

**Isolation is correctly enforced here.**

### 13.4 Webhook Config Paths

**File:** `internal/handler/merchant_webhook_handler.go`

Similarly uses merchant from context. Repository methods scope by `merchant_id`.

**Isolation is correctly enforced here.**

### 13.5 Admin Paths

**File:** `internal/handler/admin_user_handler.go`

```go
merchantID, err := uuid.Parse(c.Param("merchant_id"))
```

The `merchant_id` comes from the URL parameter. The `AdminAuth` middleware does not provide a scoped merchant identity — it only authenticates the admin key. An authenticated admin can therefore target any `merchant_id` by supplying it in the path.

**This is intentional by design:** admin operations are cross-merchant by nature. The risk is that a compromised `ADMIN_API_KEY` grants access to all merchant data across all tenants. This is an acceptable risk for a shared ops key model, but it means the admin key is extremely sensitive.

### 13.6 Onboarding Path

**File:** `internal/service/onboarding_service.go` — `OnboardMerchant()`

```go
owner := &model.MerchantUser{
    MerchantID: merchantID, // always from the merchant created above — never client-supplied
}
apiKey := &model.MerchantAPIKey{
    MerchantID: merchantID, // same tenant — never client-supplied
}
```

The onboarding service generates the `merchantID` internally and propagates it to all provisioned rows. The client cannot supply a `merchant_id` in the request body to associate new credentials with an existing merchant.

**Onboarding isolation is correct.**

### 13.7 Risk Summary

| Path | Isolation Mechanism | Verdict |
|---|---|---|
| Merchant API key endpoints | Path param compared to auth context | ✅ Correct |
| Dashboard endpoints | Auth context only | ✅ Correct |
| Payment endpoints | Auth context → merchant-scoped repo queries | ✅ Correct |
| Admin endpoints | URL param (no merchant scope in token) | ⚠️ Intentional, key is highly sensitive |
| Onboarding | merchant_id generated server-side | ✅ Correct |

---

## 14. Legacy Authentication Findings

### 14.1 Legacy Credential Storage

**Migration:** `migrations/000001_create_merchants.up.sql`

```sql
api_key     VARCHAR(255)    NOT NULL,  -- bare pk_<hex>
api_secret  VARCHAR(255)    NOT NULL,  -- SHA-256 hex of plaintext secret
```

Note: `api_secret` in `merchants` is a **SHA-256 hash** (not Argon2id). Phase 5C `merchant_api_keys.secret_hash` uses Argon2id. The legacy mechanism uses a weaker hash.

**File:** `internal/service/merchant_service.go` — `generateAPISecret()`

```go
sum := sha256.Sum256([]byte(plaintext))
hashed = hex.EncodeToString(sum[:])
```

The legacy secret hash is SHA-256. This is weaker than the Argon2id used for Phase 5C keys but the plaintext secret is never returned after creation, so brute-force would require obtaining the hash (DB breach scenario).

### 14.2 Where Legacy Credentials Are Used

The legacy `api_key` column is used in:
- `internal/middleware/auth.go` Stage 2 (legacy fallback): `merchantSvc.GetMerchantByAPIKey()`
- `internal/repository/merchant_repository.go`: `GetByAPIKey()` queries `WHERE api_key = $1`

The legacy `api_secret` column is **never verified** anywhere in the codebase. The auth middleware only looks up the merchant by `api_key`; the secret is not checked. This means the legacy authentication is effectively a **key-only** (no secret) scheme — any holder of the `api_key` string can authenticate as that merchant.

This was likely intentional for Phase 1 (simple API key), but it means the security model of legacy auth is weaker than Phase 5C (which requires both `pk_` + `sk_` with Argon2id verification).

### 14.3 Phase 5C vs Legacy Coexistence

**File:** `internal/service/onboarding_service.go`

Both legacy and Phase 5C credentials are created during onboarding:

```go
// Legacy merchant credentials (row-level) — plaintext secret discarded after hash.
legacyKey, _ := generateAPIKey()
legacySecret, legacyHash, _ := generateAPISecret()
_ = legacySecret // intentionally discarded

// Phase 5C
keyID, plaintextSecret, secretHash, _ := generateAPIKeyPair()
```

A merchant can authenticate using either mechanism. Both refer to the same merchant and grant the same access scope. There is no API surface to list or rotate the legacy `api_key` / `api_secret` — they can only be replaced by direct DB manipulation.

### 14.4 Lifecycle Impact on Both Mechanisms

Both legacy and Phase 5C authentication reach the same `merchant.IsActive()` check in the Auth middleware. Suspending a merchant blocks both mechanisms equally.

---

## 15. Audit Logging Findings

### 15.1 No Audit Logging Exists

A comprehensive search of the repository for `audit_log`, `audit log`, `AUDIT`, `status_history`, `lifecycle_event`, and related terms returned **no matches**. There is no audit logging mechanism of any kind in the codebase.

### 15.2 Structured Logging

The codebase uses `log/slog` for structured operational logging. Key events are logged:

- Merchant created: `slog.Info("merchant created", ...)` in `merchant_service.go`
- Merchant onboarded: `slog.Info("merchant onboarding succeeded", ...)` in `onboarding_service.go`
- Auth failures: `slog.Warn("auth: merchant not active ...")` in `auth.go`
- Dashboard login: `slog.Info("dashboard auth: login successful", ...)` in `auth_service.go`
- API key lifecycle: `slog.Info("merchant api key created/revoked/rotated", ...)` in `merchant_api_key_service.go`

These are log lines to stdout/stderr, not persisted audit records. They cannot be queried, attributed to admin actions, or correlated with who changed what.

### 15.3 Impact

Without audit logging, there is no way to:
- Determine who suspended a merchant
- Determine when a merchant's status changed
- Detect unauthorized status manipulation
- Meet compliance requirements (SOC2, PCI-DSS) that require audit trails for privileged operations

---

## 16. Database Findings

### 16.1 Merchants Table

**Migration:** `migrations/000001_create_merchants.up.sql`

| Column | Type | Nullable | Default | Notes |
|---|---|---|---|---|
| `id` | UUID | NOT NULL | `gen_random_uuid()` | PK |
| `name` | VARCHAR(150) | NOT NULL | — | — |
| `code` | VARCHAR(50) | NOT NULL | — | Unique index |
| `api_key` | VARCHAR(255) | NOT NULL | — | Unique index + lookup index |
| `api_secret` | VARCHAR(255) | NOT NULL | — | SHA-256 hash |
| `status` | VARCHAR(20) | NOT NULL | `'ACTIVE'` | `CHECK (status IN ('ACTIVE','INACTIVE','SUSPENDED'))` |
| `created_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | — |
| `updated_at` | TIMESTAMPTZ | NOT NULL | `NOW()` | — |

### 16.2 Constraints

- `uq_merchants_code`: unique index on `(code)`
- `uq_merchants_api_key`: unique index on `(api_key)`
- `idx_merchants_api_key`: lookup index on `(api_key)`
- `chk_merchants_status`: CHECK constraint `status IN ('ACTIVE', 'INACTIVE', 'SUSPENDED')`

**The database prevents invalid status values** at the constraint level. Any `UPDATE merchants SET status = 'INVALID'` would be rejected by PostgreSQL.

### 16.3 Missing Database Elements for Phase 7

| Element | Purpose | Currently Exists |
|---|---|---|
| `status_changed_at TIMESTAMPTZ` | Track when status last changed | ❌ No |
| `suspended_at TIMESTAMPTZ` | Specific suspension timestamp | ❌ No |
| `suspension_reason TEXT` | Reason for suspension | ❌ No |
| `merchant_status_history` table | Full status change audit trail | ❌ No |
| `deleted_at TIMESTAMPTZ` | Soft delete | ❌ No |

`updated_at` exists and would be updated on any status change, but it is a general-purpose timestamp that does not distinguish what changed.

### 16.4 Related Tables

**`merchant_api_keys`** (`migrations/000008_create_merchant_api_keys.up.sql`):
- Has its own `status VARCHAR(20) CHECK (status IN ('ACTIVE', 'REVOKED'))`
- Has `revoked_at TIMESTAMPTZ` — tracks when individual keys were revoked
- No foreign key cascade on `merchant.status` — key status does not change when merchant status changes

**`merchant_users`** (`migrations/000014_create_merchant_users.up.sql`):
- Has its own `status VARCHAR(20) CHECK (status IN ('ACTIVE', 'DISABLED'))`
- `ON DELETE CASCADE` on `merchant_id` FK — deleting a merchant cascades to its users
- No trigger on merchant status change — user statuses do not change when merchant is suspended

**`dashboard_sessions`** (`migrations/000014_create_merchant_users.up.sql`):
- `ON DELETE CASCADE` on `merchant_user_id` FK — deleting a user cascades to its sessions
- No mechanism to cascade from merchant status change

---

## 17. Frontend/Consumer Findings

### 17.1 No Frontend Code in Repository

The repository contains only the backend. No frontend dashboard code is present. The swagger docs at `swagger/docs.go` represent the API contract for any consuming clients.

### 17.2 Swagger/Docs Evidence

The Swagger documentation (`swagger/docs.go`) defines `CodeMerchantInactive = "MERCHANT_INACTIVE"` and references it in 401 response descriptions for payment endpoints. This suggests any API consumer has been advised to handle `MERCHANT_INACTIVE`.

### 17.3 Phase 9 Dashboard API Dependency

The Phase 9 dashboard API (`GET /api/v1/dashboard/settings`) returns `DashboardMerchantSettingsResponse` which includes `Status MerchantStatus`. This means the dashboard frontend is expected to be status-aware — it receives the current status on every settings fetch.

However, since the dashboard middleware does not enforce merchant status, the frontend would receive status information but no enforcement would occur server-side.

### 17.4 Potential Breaking Changes

Consumers currently depending on `GET /api/v1/merchants/:id` without authentication:
- Any such consumer would be broken if the endpoint is removed or auth-gated
- No authenticated API client (Phase 5C key or JWT) should need this endpoint since the dashboard `/settings` path provides the same data
- The endpoint was flagged with "see follow-up" in the source code, suggesting the team already intends to address it

---

## 18. Proposed Phase 7 API Surface

Based strictly on audit findings, the following API surface is recommended for merchant lifecycle management.

### 18.1 Merchant Status Mutation

**Recommended route:**

```
PATCH /api/v1/admin/merchants/:merchant_id/status
```

**Rationale for PATCH over separate endpoints:**
- A single `PATCH` endpoint is the REST-standard for partial resource updates
- Separate `POST /suspend`, `POST /activate`, `POST /deactivate` endpoints are action-based and harder to reason about idempotency
- The `PATCH` approach allows the request body to carry the target status, making allowed transitions explicit in business logic rather than route selection
- Separate action endpoints are acceptable if the business wants to enforce specific transition rules at the routing layer, but this adds route complexity

**Authentication:** `X-Admin-Key` (existing `AdminAuth` middleware)

**Request body:**
```json
{
  "status": "SUSPENDED" | "ACTIVE" | "INACTIVE",
  "reason": "string (optional, max 500 chars)"
}
```

**Allowed transitions to enforce at the service layer:**

| Current | Target | Allowed |
|---|---|---|
| ACTIVE | SUSPENDED | ✅ |
| ACTIVE | INACTIVE | ✅ |
| SUSPENDED | ACTIVE | ✅ |
| SUSPENDED | INACTIVE | ✅ |
| INACTIVE | ACTIVE | NEEDS BUSINESS DECISION |
| INACTIVE | SUSPENDED | NEEDS BUSINESS DECISION |
| Any | Same status | Idempotent — return 200 with current state |

**Response (200):**
```json
{
  "success": true,
  "data": {
    "id": "<uuid>",
    "name": "...",
    "code": "...",
    "status": "SUSPENDED",
    "updated_at": "2026-09-22T..."
  }
}
```

**Error cases:**

| Condition | HTTP | Error Code |
|---|---|---|
| Invalid or missing X-Admin-Key | 401 | `ADMIN_UNAUTHORIZED` |
| `merchant_id` not found | 404 | `MERCHANT_NOT_FOUND` |
| Invalid target status value | 400 | `VALIDATION_ERROR` |
| Transition not allowed | 422 | `INVALID_STATUS_TRANSITION` (new code needed) |
| Attempt to set current status (same) | 200 | — (idempotent) |

**Idempotency:** Setting a merchant to its current status returns 200 without error. This avoids operational double-action problems.

**Tenant isolation:** `merchant_id` comes from the URL path. The AdminAuth middleware does not provide a scoped merchant identity. Any authenticated admin can update any merchant. This is consistent with the existing admin pattern (e.g., `POST /api/v1/admin/merchants/:merchant_id/users`).

### 18.2 Side Effects to Implement

When status is changed to SUSPENDED or INACTIVE, the service layer should:

1. Update `merchants.status` and `merchants.updated_at`
2. Revoke all dashboard sessions for all users of this merchant (call `sessionRepo.DeleteByMerchantID` — new method needed)
3. (BUSINESS DECISION) Optionally: revoke all Phase 5C API keys for the merchant

Side effects for ACTIVE reinstatement:
1. Update `merchants.status` and `merchants.updated_at`
2. Sessions are not restored (users must log in again)

### 18.3 Required Repository Changes

```go
// MerchantRepository additions needed:
UpdateStatus(ctx context.Context, id uuid.UUID, status model.MerchantStatus) error

// DashboardSessionRepository addition needed:
DeleteByMerchantID(ctx context.Context, merchantID uuid.UUID) error
// Implementation: DELETE FROM dashboard_sessions WHERE merchant_user_id IN
//   (SELECT id FROM merchant_users WHERE merchant_id = $1)
```

---

## 19. Proposed Read API Security

### 19.1 Recommendation

**Option B+E: Make `GET /api/v1/merchants/:id` admin-only, plus formalize the dashboard `/settings` path as the primary read API for merchants.**

Rationale:

- **Option A (remove):** Highest security, but risks breaking unknown external consumers. The "see follow-up" comment suggests this was already planned. Viable if no backward compatibility requirement exists.
- **Option B (admin-only):** Lowest disruption. The existing `AdminAuth` middleware can be applied. Suitable for internal tooling and ops scripts that currently call this endpoint.
- **Option C (merchant-user only):** The `/api/v1/dashboard/settings` endpoint already serves this purpose. Adding a second authenticated path for the same data creates confusion.
- **Option D (API-key only):** Merchants already have access to their own data via dashboard settings. Adding an API-key-gated read on `/merchants/:id` is redundant and expands the authenticated attack surface for the endpoint.
- **Option E (replace with `/me` style):** Already implemented as `GET /api/v1/dashboard/settings`. Adding a merchant-context `GET /api/v1/merchants/me` would be a natural addition to the merchant API key path if needed.
- **Option F (keep for compatibility):** Not recommended. Public unauthenticated enumeration of merchant status is a clear risk.

### 19.2 Recommended API Contract After Phase 7

```
GET /api/v1/merchants/:id
  → Require AdminAuth (X-Admin-Key)
  → Returns: GetMerchantResponse (existing DTO, no changes)
  → 401 if no/invalid admin key
  → 404 if merchant not found
```

```
GET /api/v1/dashboard/settings  (existing, already authenticated)
  → Require RequireDashboardAuth (Bearer JWT)
  → Returns: DashboardMerchantSettingsResponse (merchant status included)
  → Primary authenticated merchant profile read path
```

If an API-key-authenticated merchant profile read is needed in future:
```
GET /api/v1/payments/merchant-profile  [DEFERRED]
  → Require Auth (X-API-Key)
  → Returns merchant profile scoped to the authenticated merchant
```

---

## 20. Required Test Matrix

The following tests must be written as part of Phase 7 implementation:

### 20.1 Merchant Status Mutation API

| Test | Scenario | Expected |
|---|---|---|
| `TestPatchMerchantStatus_Suspend_Success` | ACTIVE → SUSPENDED with valid admin key | 200, status=SUSPENDED |
| `TestPatchMerchantStatus_Activate_Success` | SUSPENDED → ACTIVE | 200, status=ACTIVE |
| `TestPatchMerchantStatus_Deactivate_Success` | ACTIVE → INACTIVE | 200, status=INACTIVE |
| `TestPatchMerchantStatus_SuspendToInactive_Success` | SUSPENDED → INACTIVE | 200, status=INACTIVE |
| `TestPatchMerchantStatus_Idempotent` | ACTIVE → ACTIVE | 200, no error |
| `TestPatchMerchantStatus_InvalidTransition` | INACTIVE → ACTIVE (if disallowed by business rule) | 422 `INVALID_STATUS_TRANSITION` |
| `TestPatchMerchantStatus_InvalidStatus` | status="UNKNOWN" | 400 `VALIDATION_ERROR` |
| `TestPatchMerchantStatus_MerchantNotFound` | Unknown merchant_id | 404 `MERCHANT_NOT_FOUND` |
| `TestPatchMerchantStatus_MissingAdminKey` | No X-Admin-Key | 401 `ADMIN_UNAUTHORIZED` |
| `TestPatchMerchantStatus_InvalidAdminKey` | Wrong X-Admin-Key | 401 `ADMIN_UNAUTHORIZED` |

### 20.2 Session Revocation on Suspension

| Test | Scenario | Expected |
|---|---|---|
| `TestSuspend_RevokesAllDashboardSessions` | Suspend merchant with active sessions | All sessions deleted from DB |
| `TestSuspend_JWTStillValidUntilExpiry` | Access JWT used after suspension | 200 until JWT TTL expires (short-term residual) |
| `TestSuspend_RefreshTokenRejectedAfterRevoke` | Refresh token used after suspension | 401 `INVALID_CREDENTIALS` |

### 20.3 Merchant Status Enforcement on Dashboard

| Test | Scenario | Expected |
|---|---|---|
| `TestDashboardAuth_SuspendedMerchant_Blocked` | If merchant status check added to RequireDashboardAuth | 401 or 403 |
| `TestDashboardLogin_SuspendedMerchant_Blocked` | Login attempt for user of suspended merchant | Appropriate error |

### 20.4 Merchant Read API Security

| Test | Scenario | Expected |
|---|---|---|
| `TestGetMerchantByID_RequiresAdminKey` | No auth → 401 | 401 `ADMIN_UNAUTHORIZED` |
| `TestGetMerchantByID_AdminKey_Success` | Valid admin key | 200 with merchant data |
| `TestGetMerchantByID_NotFound` | Unknown UUID | 404 `MERCHANT_NOT_FOUND` |

### 20.5 Regression Tests

| Test | Scenario | Expected |
|---|---|---|
| `TestLegacyKeyAuth_SuspendedMerchant` | Legacy key on suspended merchant | 401 `MERCHANT_INACTIVE` |
| `TestPhase5CKeyAuth_SuspendedMerchant` | Phase 5C key on suspended merchant | 401 `MERCHANT_INACTIVE` |
| `TestWebhook_SuspendedMerchantTransaction_StillProcessed` | Inbound webhook for suspended merchant | 200, transaction updated |
| `TestExpiryWorker_SuspendedMerchant_PendingExpired` | Expiry worker with suspended merchant pending tx | TX expires normally |
| `TestCreatePayment_SuspendedMerchant_Blocked` | POST /payments with suspended merchant key | 401 `MERCHANT_INACTIVE` |

---

## 21. MUST HAVE

Items that are required for Phase 7 to be complete:

### M1 — Merchant Status Mutation API
**`PATCH /api/v1/admin/merchants/:merchant_id/status`**
- AdminAuth protected
- Validates allowed transitions
- Updates `merchants.status` and `merchants.updated_at`
- Evidence: No mutation path exists. Status is currently only changeable via direct DB access.

### M2 — MerchantRepository.UpdateStatus
Add `UpdateStatus(ctx, id, status) error` to the repository interface and implementation.
- Evidence: `internal/repository/merchant_repository.go` has no such method.

### M3 — Merchant-Wide Session Revocation on Suspension
Add `DashboardSessionRepository.DeleteByMerchantID` and call it when status changes to SUSPENDED or INACTIVE.
- Evidence: `internal/repository/dashboard_session_repository.go` has `DeleteByUserID` but no merchant-scoped revocation. `RequireDashboardAuth` does not check merchant status, creating an access bypass window.

### M4 — Secure GET /api/v1/merchants/:id
Apply `AdminAuth` middleware to `GET /api/v1/merchants/:id`.
- Evidence: `cmd/server/main.go` route registration shows no middleware on this GET. The source comment says "see follow-up".

### M5 — Dashboard Auth Must Check Merchant Status
`RequireDashboardAuth` must check the parent merchant's status per-request (or immediately post-suspension via session revocation from M3).
- Evidence: `internal/middleware/dashboard_auth.go` checks only user status. A suspended merchant's users retain full dashboard access.

### M6 — New Error Code for Invalid Transition
Add `CodeInvalidStatusTransition ErrorCode = "INVALID_STATUS_TRANSITION"` to `pkg/response/response.go`.
- Evidence: No such code exists in the current error code registry.

---

## 22. SHOULD HAVE

Items that are strongly recommended but can follow Phase 7 if time-constrained:

### S1 — Audit Logging for Status Changes
Persist who changed a merchant's status, from what, to what, and when.
- At minimum: `merchant_id`, `old_status`, `new_status`, `changed_at`, `changed_by` (admin actor token or a placeholder admin identifier)
- Evidence: No audit mechanism exists. Required for any compliance posture.

### S2 — Status History Table
`merchant_status_history(merchant_id, old_status, new_status, changed_at, reason)` with foreign key to merchants.
- Evidence: `merchants` table has no `status_changed_at` and no history table.

### S3 — Suspension Reason Field
Accept an optional `reason` in the PATCH status request and store it (either on the merchant row as `suspension_reason TEXT` or in a status history table).
- Evidence: Business operations teams typically need to document the reason for suspension.

### S4 — `status_changed_at` Column on Merchants
Add `status_changed_at TIMESTAMPTZ` to the `merchants` table to track when status last changed without requiring a full history table.
- Evidence: `merchants` table has only `updated_at` which does not distinguish what changed.

---

## 23. OUT OF SCOPE

The following are explicitly out of scope for Phase 7:

| Item | Reason |
|---|---|
| Subscription/billing integration | No billing infrastructure exists |
| Sandbox vs production environment separation | No environment isolation model exists |
| Provider account isolation per merchant | Midtrans credentials are platform-level |
| Team invitations and member management | User management is already in Phase 8; invitations are a UX feature |
| Per-merchant Midtrans credentials | Requires provider architecture changes |
| Merchant soft-delete / archiving | `deleted_at` column does not exist; complex to retrofit safely |
| Rate limiting on lifecycle endpoints | No rate limit middleware exists; belongs in infrastructure layer |
| MFA for admin key operations | Admin key is a shared secret; MFA would require a full admin auth overhaul |
| Frontend dashboard changes | Backend API only in this phase |
| Webhook retry cancellation on suspension | Worker/queue architecture change; BUSINESS DECISION (see Section 25) |

---

## 24. Risks

### R1 — Existing API Key Clients
**Risk:** Phase 5C API key clients currently expect that `401 MERCHANT_INACTIVE` means only "my key is wrong" in an informal sense. After Phase 7, it will also mean "my merchant was suspended". Error code is already defined and used — no behavior change for clients.  
**Test required:** `TestPhase5CKeyAuth_SuspendedMerchant` — already exists in test suite.

### R2 — Dashboard Sessions Not Immediately Revoked
**Risk:** If M3 (session revocation) is not implemented, suspending a merchant does not terminate active dashboard sessions. Users retain access until JWT TTL.  
**Mitigation:** Implement M3 concurrently with M1. The window without M3 is bounded by JWT TTL.

### R3 — Admin Key Single Point of Failure
**Risk:** The `ADMIN_API_KEY` is a shared secret with no RBAC. A leaked key grants lifecycle mutation access to all merchants.  
**Mitigation:** Out of scope for Phase 7. Document the risk. Recommend rotation procedures.

### R4 — Merchant Status Check in Dashboard Middleware
**Risk:** Adding merchant status check to `RequireDashboardAuth` adds a DB query per request (load the merchant row). This is a performance regression for all dashboard requests.  
**Mitigation:** The merchant load can be cached in the request context (already happens for user load). Alternatively, embed the merchant status in the JWT claims at login time, but this risks stale claims. The safest approach is the DB check + session revocation combination: M3 eliminates stale sessions, M5 prevents newly-issued JWTs from bypassing the check.

### R5 — Legacy Merchant Endpoint Dependencies
**Risk:** `GET /api/v1/merchants/:id` being publicly accessible may be depended upon by existing integrations not visible in the backend code.  
**Mitigation:** Add AdminAuth without removing the endpoint. Do not change the response shape. Existing authenticated consumers (using X-Admin-Key) will continue to work. Unauthenticated consumers will break — this is the intended security improvement.

### R6 — INACTIVE → ACTIVE Transition
**Risk:** If INACTIVE is treated as permanent deactivation but the API allows reactivation, operational confusion can arise.  
**Mitigation:** Make the transition explicit in the API (return 422 for disallowed transitions) and document the intended business meaning. See Open Business Decisions.

### R7 — Existing Tests
**Risk:** `internal/middleware/auth_middleware_test.go` already tests both INACTIVE and SUSPENDED producing `MERCHANT_INACTIVE`. These tests will continue to pass. No regression risk from status checking in auth.  
**Risk:** Adding merchant status check to `RequireDashboardAuth` will require new tests. Existing dashboard tests do not test for merchant status enforcement because no such enforcement exists.

### R8 — Outbound Webhook Delivery to Suspended Merchants
**Risk:** If suspension stops outbound webhook delivery, merchants may miss financial lifecycle events (e.g., `payment.paid`) for transactions that were processing when they were suspended. This affects settlement and refund eligibility.  
**Mitigation:** Default to continuing delivery for already-queued items; block new enqueues. BUSINESS DECISION required — see Section 25.

### R9 — Idempotency Keys for Suspended Merchant Payments
**Risk:** An idempotency key for a payment in `PROCESSING` status exists when the merchant is suspended. If the merchant is later reactivated, the replay of that idempotency key will return the stored result correctly. No regression risk identified.

---

## 25. Open Business Decisions

The following questions cannot be answered from code alone and require explicit product/business decisions before implementation:

### BD1 — Is INACTIVE Reversible?
Can an INACTIVE merchant be reactivated (INACTIVE → ACTIVE)?  
- If NO: implement a guard rejecting this transition; document INACTIVE as permanent.  
- If YES: the same logic as SUSPENDED → ACTIVE applies.

### BD2 — Does Suspension Stop Outbound Webhook Delivery?
When a merchant is SUSPENDED, should:
- (a) New outbound webhook deliveries be enqueued? (currently: yes)
- (b) Existing PENDING deliveries continue to be dispatched? (currently: yes)
- (c) Both stop immediately?

The financially conservative answer is (b) — let queued deliveries complete, stop new enqueues. This preserves delivery of events for transactions that completed before suspension.

### BD3 — Does Suspension Auto-Revoke API Keys?
When a merchant is SUSPENDED, should all Phase 5C `merchant_api_keys` rows be set to REVOKED?  
- If YES: keys cannot be used even if the merchant is later reactivated (new keys must be created)  
- If NO: keys remain ACTIVE in the DB; authentication is blocked at the merchant-level check only; upon reactivation keys work again automatically

The softer approach (NO) is safer for operational reversibility.

### BD4 — Can a SUSPENDED Merchant Use the Dashboard?
After implementing M5 (merchant status check in dashboard auth):  
- Should SUSPENDED merchants have read-only dashboard access (view transaction history, download data)?  
- Or should all dashboard access be terminated immediately on suspension?

The answer affects whether `RequireDashboardAuth` should return 403 for SUSPENDED or whether a separate read-only mode is needed.

### BD5 — Suspension Reason: Required or Optional?
Is a reason for suspension required, optional, or not stored at all in Phase 7?

### BD6 — Admin Identity in Audit Log
The `X-Admin-Key` is a shared secret — there is no per-admin identity. If audit logging is introduced, what actor identity is recorded for status changes?  
- Option A: Log the admin key prefix (first N chars)
- Option B: Log "system_admin" as a placeholder
- Option C: This requirement defers to a future admin identity/RBAC phase

---

## 26. Recommended Implementation Order

Based on dependency analysis and risk priority:

### Step 1 — Database: Add `UpdateStatus` to Repository
- Add `UpdateStatus(ctx, id, status)` to `MerchantRepository` interface and `pgMerchantRepository`
- No migration required (column and constraint already exist)
- **Files:** `internal/repository/merchant_repository.go`

### Step 2 — Service: Merchant Status Service
- Add `UpdateMerchantStatus(ctx, merchantID, newStatus, reason)` to `MerchantService` (or create a new `MerchantLifecycleService`)
- Validate allowed transitions
- Call repository `UpdateStatus`
- **Files:** `internal/service/merchant_service.go` (or new file)

### Step 3 — Session Revocation: Add `DeleteByMerchantID`
- Add `DeleteByMerchantID(ctx, merchantID)` to `DashboardSessionRepository`
- Implementation: DELETE through JOIN with `merchant_users`
- Call this in the status service when transitioning to SUSPENDED or INACTIVE
- **Files:** `internal/repository/dashboard_session_repository.go`

### Step 4 — Handler: PATCH Admin Status Endpoint
- Add `MerchantLifecycleHandler.UpdateStatus()` handler
- Register route under existing admin group: `PATCH /api/v1/admin/merchants/:merchant_id/status`
- Uses existing `AdminAuth` middleware
- **Files:** `internal/handler/merchant_lifecycle_handler.go` (new), `cmd/server/main.go`

### Step 5 — Dashboard Auth: Add Merchant Status Check
- Load merchant status in `RequireDashboardAuth` after user load
- Return 403 `MERCHANT_SUSPENDED` (new error code) or 401 `MERCHANT_INACTIVE` for non-ACTIVE merchants
- Requires deciding BD4 first
- **Files:** `internal/middleware/dashboard_auth.go`

### Step 6 — Secure Read API
- Apply `AdminAuth` to `GET /api/v1/merchants/:id`
- No response shape changes needed
- **Files:** `cmd/server/main.go` (route registration only)

### Step 7 — Error Code
- Add `CodeInvalidStatusTransition` to `pkg/response/response.go`
- **Files:** `pkg/response/response.go`

### Step 8 — Tests
- Write unit tests per Section 20
- Run existing test suite to verify no regressions

### Step 9 — Swagger
- Update Swagger annotations for:
  - New `PATCH /api/v1/admin/merchants/:merchant_id/status` route
  - Updated `GET /api/v1/merchants/:id` (now AdminKeyAuth)
- Regenerate `swagger/docs.go`

### Dependency Graph

```
Step 1 (repo)
    └── Step 2 (service)
            ├── Step 3 (session repo)
            │       └── Step 2 calls Step 3
            └── Step 4 (handler)
                    └── Step 7 (error code, needed before Step 4)

Step 5 (dashboard auth) — depends on BD4 being decided

Step 6 (read API) — independent, can be done in parallel with Step 1
```

Steps 6 and 7 can be done immediately and independently of the rest of the work.

---

*Audit completed: 2026-09-22. No application code was modified during this audit. Only `docs/phase-7-merchant-lifecycle-security-audit.md` was created.*
