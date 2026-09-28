# Phase 8 — Self-Service Team Management Audit

**Date:** 2026-09-22
**Phase:** 8 (pre-implementation audit)
**Scope:** Read-only repository analysis — no application code was modified
**Status:** AUDIT COMPLETE

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Current Merchant User Architecture](#2-current-merchant-user-architecture)
3. [Current User Creation Flow](#3-current-user-creation-flow)
4. [Current Role Model](#4-current-role-model)
5. [Authorization Matrix](#5-authorization-matrix)
6. [Tenant Isolation Findings](#6-tenant-isolation-findings)
7. [OWNER Protection](#7-owner-protection)
8. [ADMIN Capabilities](#8-admin-capabilities)
9. [VIEWER Capabilities](#9-viewer-capabilities)
10. [Dashboard Authentication](#10-dashboard-authentication)
11. [User Lifecycle](#11-user-lifecycle)
12. [Invitation Infrastructure](#12-invitation-infrastructure)
13. [Email Infrastructure](#13-email-infrastructure)
14. [Password Flow](#14-password-flow)
15. [Email Uniqueness](#15-email-uniqueness)
16. [Multi-Merchant User Analysis](#16-multi-merchant-user-analysis)
17. [Session Model](#17-session-model)
18. [Role Change Behavior](#18-role-change-behavior)
19. [User Disable Behavior](#19-user-disable-behavior)
20. [Admin vs Self-Service Boundary](#20-admin-vs-self-service-boundary)
21. [Proposed API Surface](#21-proposed-api-surface)
22. [Invitation Data Model Evaluation](#22-invitation-data-model-evaluation)
23. [Invitation Security](#23-invitation-security)
24. [Role Boundary Evaluation](#24-role-boundary-evaluation)
25. [Merchant Lifecycle Interaction](#25-merchant-lifecycle-interaction)
26. [User Lifecycle Matrix](#26-user-lifecycle-matrix)
27. [IDOR / Security Findings](#27-idor--security-findings)
28. [Database Findings](#28-database-findings)
29. [Frontend Findings](#29-frontend-findings)
30. [Test Coverage](#30-test-coverage)
31. [MUST HAVE](#31-must-have)
32. [SHOULD HAVE](#32-should-have)
33. [OUT OF SCOPE](#33-out-of-scope)
34. [Business Decisions](#34-business-decisions)
35. [Recommended Implementation Order](#35-recommended-implementation-order)
36. [Risks](#36-risks)

---

## 1. Executive Summary

The repository contains a partially-implemented team management foundation. Three roles (OWNER, ADMIN, VIEWER) and two user statuses (ACTIVE, DISABLED) are defined and enforced at the database level. Dashboard authentication with JWT + HttpOnly refresh sessions is fully implemented. A platform-admin bootstrap API (`POST /api/v1/admin/merchants/:merchant_id/users`) allows creating users of any role. One self-service endpoint exists: `PATCH /api/v1/dashboard/users/:user_id/status` (OWNER-only, disables/enables members of the same merchant).

**Critical gaps for self-service team management:**

1. **No user creation by merchant OWNERs.** The only user creation path requires `X-Admin-Key`. An OWNER cannot add a team member without platform operator assistance.
2. **No invitation system.** There is no `merchant_user_invitations` table, no invitation token, no email sending capability, and no invitation acceptance flow anywhere in the codebase.
3. **No role change API.** `MerchantUserRepository` has no `UpdateRole` method. `DashboardUserService` has no `UpdateRole` method. There is no endpoint for changing a user's role.
4. **No email infrastructure.** No SMTP, no mailer, no transactional email provider. An invitation flow requiring email delivery cannot be implemented without first adding this infrastructure.
5. **Stale role risk.** Role is embedded in the JWT at login time. `RequireDashboardAuth` checks user status from DB on every request but does NOT reload the role. If a user's role changes in the DB, the old role persists in active JWTs until they expire (default 15 minutes).
6. **No per-merchant OWNER constraint.** The database permits a merchant with zero OWNERs. There is no constraint preventing the last OWNER from being disabled.

---

## 2. Current Merchant User Architecture

### 2.1 Database Schema

**Migration:** `migrations/000014_create_merchant_users.up.sql`

```sql
CREATE TABLE IF NOT EXISTS merchant_users (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id     UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email           VARCHAR(254) NOT NULL,
    password_hash   TEXT         NOT NULL,
    role            VARCHAR(20)  NOT NULL,
    status          VARCHAR(20)  NOT NULL DEFAULT 'ACTIVE',
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_merchant_users_role   CHECK (role   IN ('OWNER', 'ADMIN', 'VIEWER')),
    CONSTRAINT chk_merchant_users_status CHECK (status IN ('ACTIVE', 'DISABLED'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_merchant_users_email
    ON merchant_users (email);

CREATE INDEX IF NOT EXISTS idx_merchant_users_merchant_created
    ON merchant_users (merchant_id, created_at DESC);
```

**Sessions table:**

```sql
CREATE TABLE IF NOT EXISTS dashboard_sessions (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_user_id    UUID         NOT NULL REFERENCES merchant_users(id) ON DELETE CASCADE,
    refresh_token_hash  VARCHAR(64)  NOT NULL,
    expires_at          TIMESTAMPTZ  NOT NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_dashboard_sessions_token_hash
    ON dashboard_sessions (refresh_token_hash);
```

### 2.2 Key Schema Facts

| Property | Value |
|---|---|
| Primary key | `id` UUID |
| Foreign key | `merchant_id → merchants(id) ON DELETE CASCADE` |
| Email uniqueness | **GLOBAL** (not per-merchant) — `uq_merchant_users_email` |
| Password hashing | Argon2id (PHC format in `TEXT` column) |
| Roles | `OWNER`, `ADMIN`, `VIEWER` — enforced by CHECK constraint |
| Statuses | `ACTIVE`, `DISABLED` — enforced by CHECK constraint |
| Session scoping | Session table has NO `merchant_id` column — scoped to user only |
| Soft delete | Not implemented — no `deleted_at` column |
| Invitation table | Does not exist |

### 2.3 Domain Model

**File:** `internal/model/dashboard_user.go`

```go
type MerchantUser struct {
    ID           uuid.UUID
    MerchantID   uuid.UUID
    Email        string
    PasswordHash string    // json:"-" — never serialised
    Role         DashboardUserRole
    Status       DashboardUserStatus
    LastLoginAt  *time.Time
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

`IsActive()` returns `true` only for `DashboardUserStatusActive`.

---

## 3. Current User Creation Flow

### 3.1 Creation Paths

There are exactly two paths that create a `merchant_users` row:

**Path 1 — Phase 6 Onboarding (admin-only)**
- Route: `POST /api/v1/admin/onboarding/merchants`
- Auth: `X-Admin-Key`
- File: `internal/service/onboarding_service.go` → `OnboardMerchant()`
- Creates: merchant + OWNER user + Phase 5C API key in one transaction
- Role assigned: always `DashboardUserRoleOwner` (hardcoded)
- Status assigned: always `DashboardUserStatusActive`

**Path 2 — Admin User Bootstrap (admin-only)**
- Route: `POST /api/v1/admin/merchants/:merchant_id/users`
- Auth: `X-Admin-Key`
- File: `internal/service/dashboard_user_service.go` → `CreateUser()`
- Creates: single user row
- Role assigned: any role from request body (`OWNER`, `ADMIN`, `VIEWER`)
- Status assigned: always `DashboardUserStatusActive`

### 3.2 Answers to Creation Questions

| Question | Answer |
|---|---|
| Who can create a user? | Only platform admin (X-Admin-Key) |
| Which roles can be assigned? | Any: OWNER, ADMIN, VIEWER (Path 2); OWNER only (Path 1) |
| Can user be created without admin? | No |
| Can OWNER create another user? | **No — no such endpoint exists** |
| Can ADMIN create another user? | No |
| Can VIEWER create another user? | No |
| Is there an invitation flow? | **No** |
| Is email uniqueness global or per-tenant? | **Global** |
| Is password required at creation? | Yes — `min=8,max=128` binding validation |
| Is email verification required? | No — no verification infrastructure |

### 3.3 Password Hashing at Creation

**File:** `internal/service/jwt_util.go` — `hashPassword()`

- Algorithm: Argon2id
- Memory: 64 MiB
- Time: 1 iteration
- Threads: 4
- Key length: 32 bytes
- Salt: 16 random bytes (crypto/rand)
- Format: `$argon2id$v=19$m=65536,t=1,p=4$<salt_b64>$<hash_b64>`

---

## 4. Current Role Model

### 4.1 Role Definitions

**File:** `internal/model/dashboard_user.go`

```go
DashboardUserRoleOwner  DashboardUserRole = "OWNER"
DashboardUserRoleAdmin  DashboardUserRole = "ADMIN"
DashboardUserRoleViewer DashboardUserRole = "VIEWER"
```

Comments in the model describe the intended semantics:
- **OWNER**: full access — user management, API keys, webhooks, payments, refunds, settlements, reconciliation
- **ADMIN**: operational access — payments, refunds, config, settlements, reconciliation. Cannot manage users or perform destructive account-level operations
- **VIEWER**: read-only access to the dashboard

### 4.2 Role Enforcement Mechanism

Role enforcement uses two layers:

**Layer 1 — `RequireRole` middleware** (`internal/middleware/dashboard_auth.go`):
```go
func RequireRole(roles ...model.DashboardUserRole) gin.HandlerFunc
```
Reads `user.Role` from context (the DB-loaded user, not the JWT claim). Returns 403 `INSUFFICIENT_ROLE` if not in allowed set.

**Layer 2 — Service-level check** (`internal/service/dashboard_user_service.go`):
```go
if callerRole != model.DashboardUserRoleOwner {
    return nil, ErrInsufficientRole
}
```
`UpdateUserStatus` explicitly checks `callerRole == OWNER`. This is a second line of defense beyond the middleware.

---

## 5. Authorization Matrix

Based on route registrations in `cmd/server/main.go` and handler/service code:

| Capability | OWNER | ADMIN | VIEWER | Evidence |
|---|:---:|:---:|:---:|---|
| Login / logout / refresh | ✅ | ✅ | ✅ | `POST /api/v1/auth/login` — no role check |
| View own profile (`/auth/me`) | ✅ | ✅ | ✅ | `RequireDashboardAuth` only |
| View dashboard overview | ✅ | ✅ | ✅ | `GET /dashboard/overview` — no `RequireRole` |
| List payments | ✅ | ✅ | ✅ | `GET /dashboard/payments` — no `RequireRole` |
| View payment details | ✅ | ✅ | ✅ | `GET /dashboard/payments/:id` — no `RequireRole` |
| Create payment | ✅ | ✅ | ❌ | `POST /dashboard/payments` — `RequireRole(OWNER, ADMIN)` |
| Cancel payment | ✅ | ✅ | ❌ | `POST /dashboard/payments/:id/cancel` — `RequireRole(OWNER, ADMIN)` |
| List refunds | ✅ | ✅ | ✅ | `GET /dashboard/refunds` — no `RequireRole` |
| Create refund | ✅ | ✅ | ❌ | `POST /dashboard/payments/:id/refunds` — `RequireRole(OWNER, ADMIN)` |
| List API keys | ✅ | ✅ | ✅ | `GET /dashboard/api-keys` — no `RequireRole` |
| Create API key | ✅ | ✅ | ❌ | `POST /dashboard/api-keys` — `RequireRole(OWNER, ADMIN)` |
| Revoke API key | ✅ | ✅ | ❌ | `POST /dashboard/api-keys/:id/revoke` — `RequireRole(OWNER, ADMIN)` |
| Get webhook config | ✅ | ✅ | ✅ | `GET /dashboard/webhook-config` — no `RequireRole` |
| Upsert webhook config | ✅ | ✅ | ❌ | `PUT /dashboard/webhook-config` — `RequireRole(OWNER, ADMIN)` |
| Rotate webhook secret | ✅ | ✅ | ❌ | `POST /dashboard/webhook-config/rotate` — `RequireRole(OWNER, ADMIN)` |
| Disable webhook | ✅ | ✅ | ❌ | `DELETE /dashboard/webhook-config` — `RequireRole(OWNER, ADMIN)` |
| List webhook deliveries | ✅ | ✅ | ✅ | `GET /dashboard/webhooks` — no `RequireRole` |
| Retry webhook delivery | ✅ | ✅ | ❌ | `POST /dashboard/webhooks/:id/retry` — `RequireRole(OWNER, ADMIN)` |
| List reconciliation mismatches | ✅ | ✅ | ✅ | `GET /dashboard/reconciliation/mismatches` — no `RequireRole` |
| View reconciliation mismatch | ✅ | ✅ | ✅ | `GET /dashboard/reconciliation/mismatches/:id` — no `RequireRole` |
| Get merchant settings | ✅ | ✅ | ✅ | `GET /dashboard/settings` — no `RequireRole` |
| List team members | ✅ | ✅ | ✅ | `GET /dashboard/users` — no `RequireRole` |
| Disable/enable team member | ✅ | ❌ | ❌ | `PATCH /dashboard/users/:id/status` — `RequireRole(OWNER)` + service check |
| **Change member role** | **N/A** | **N/A** | **N/A** | **Endpoint does not exist** |
| **Invite team member** | **N/A** | **N/A** | **N/A** | **Endpoint does not exist** |

**Summary of authorization gaps:**
- VIEWER can list team members (may be intentional or oversight — NEEDS BUSINESS DECISION)
- VIEWER can list API keys (read-only — may be intentional)
- No role management endpoint exists at all
- No invitation endpoint exists at all

---

## 6. Tenant Isolation Findings

### 6.1 How Merchant Identity Is Established

For all `/api/v1/dashboard/*` routes, merchant identity is **always derived from the authenticated user**, never from the request:

**File:** `internal/middleware/dashboard_auth.go`
```go
user, err := userRepo.GetByID(c.Request.Context(), userID)
// ...
c.Set(model.ContextKeyDashboardUser, user)
```

The handler then reads `caller.MerchantID` from this context user. No dashboard handler accepts `merchant_id` as a request body field or URL parameter.

### 6.2 List Users — Isolation Analysis

**File:** `internal/handler/dashboard_user_handler.go` → `ListUsers()`

```go
caller := dashboardUserFromContext(c)
users, err := h.userSvc.ListUsers(c.Request.Context(), caller.MerchantID)
```

**Repository query** (`internal/repository/merchant_user_repository.go`):
```sql
SELECT ... FROM merchant_users WHERE merchant_id = $1 ORDER BY created_at DESC
```

`WHERE merchant_id = $1` is correctly scoped. A Merchant A user cannot list Merchant B users. ✅

### 6.3 Update User Status — Isolation Analysis

**File:** `internal/service/dashboard_user_service.go` → `UpdateUserStatus()`

```go
// Load target user.
target, err := s.userRepo.GetByID(ctx, targetUserID)
// ...
// Merchant isolation: target must belong to the caller's merchant.
if target.MerchantID != callerMerchantID {
    return nil, ErrCrossmerchantAccess
}
```

The service loads the target user by ID (no merchant scope in the repository query), then **explicitly verifies** `target.MerchantID == callerMerchantID` in the service layer.

If `ErrCrossmerchantAccess` is returned, the handler maps it to **404** (not 403), to avoid confirming the existence of users in other tenants. ✅

**File:** `internal/handler/dashboard_user_handler.go`:
```go
case errors.Is(err, service.ErrCrossmerchantAccess):
    response.NotFound(c, response.CodeDashboardUserNotFound, "User not found")
```

### 6.4 Repository Layer — GetByID

**File:** `internal/repository/merchant_user_repository.go` → `GetByID()`:
```sql
SELECT ... FROM merchant_users WHERE id = $1
```

This query has **no `merchant_id` predicate**. Isolation is enforced at the service layer, not the repository layer. This is a known pattern in the codebase — the service is responsible for checking ownership after the load. As long as every service that calls `GetByID` performs the ownership check, this is safe.

**Risk:** If a future service method calls `GetByID` and forgets to check `target.MerchantID == callerMerchantID`, it would be an IDOR vulnerability. The current `UpdateUserStatus` handles this correctly. Any new operations (role change, re-enable, etc.) must follow the same pattern.

### 6.5 IDOR Summary

| Operation | Isolation Mechanism | Finding |
|---|---|---|
| `GET /dashboard/users` | `WHERE merchant_id = $1` in repo | ✅ Correct |
| `PATCH /dashboard/users/:id/status` | Service-layer `target.MerchantID != callerMerchantID` | ✅ Correct, returns 404 |
| Future: change role | Not yet implemented | ⚠️ Must implement same pattern |
| Future: re-enable user | Not yet implemented | ⚠️ Must implement same pattern |

---

## 7. OWNER Protection

### 7.1 Current OWNER Constraints

| Question | Answer |
|---|---|
| Can there be multiple OWNERs? | **Yes — no database constraint prevents it** |
| Is exactly one OWNER required? | **No — not enforced anywhere** |
| Can OWNER be disabled by another OWNER? | **Yes — no guard against it** |
| Can OWNER disable themselves? | **No — `ErrSelfDisable` guard in service** |
| Can OWNER be deleted? | Not via any API (no delete endpoint) |
| Can OWNER change their own role? | **No endpoint to change any role** |
| Can OWNER change another OWNER's role? | **No endpoint to change any role** |
| Can ADMIN modify OWNER? | ADMIN cannot change status (`ErrInsufficientRole`). No role-change endpoint. |
| Can OWNER "leave" the merchant? | Self-disable is blocked by `ErrSelfDisable` |
| What happens if the only OWNER is disabled? | No guard — the merchant would have no OWNER |
| DB constraint protecting OWNER count? | **None** |

### 7.2 Self-Disable Guard

**File:** `internal/service/dashboard_user_service.go`:
```go
if callerUserID == targetUserID && status == model.DashboardUserStatusDisabled {
    return nil, ErrSelfDisable
}
```

This prevents an OWNER from disabling themselves, but does NOT prevent an OWNER from disabling all other OWNERs (leaving themselves as the only OWNER, then being unable to do anything).

### 7.3 Last-OWNER Protection — MISSING

**Finding:** There is no "last OWNER" guard. If Merchant A has one OWNER and two ADMINs, the OWNER can disable each ADMIN (which is permitted). There is no restriction on disabling the only remaining OWNER. The OWNER cannot disable themselves (self-disable guard), but if there are two OWNERs, Owner A can disable Owner B even if Owner B is the only remaining active OWNER.

This is a **NEEDS BUSINESS DECISION** item.

---

## 8. ADMIN Capabilities

### 8.1 Current Route-Level ADMIN Access

ADMIN has exactly the same capabilities as OWNER except:

| Restricted to OWNER only | Evidence |
|---|---|
| `PATCH /dashboard/users/:id/status` | `middleware.RequireRole(model.DashboardUserRoleOwner)` in `main.go` |

All other dashboard routes that use `RequireRole` accept both OWNER and ADMIN:
- Create payment, cancel payment, create refund
- Create API key, revoke API key
- Upsert webhook config, rotate webhook secret, disable webhook
- Retry webhook delivery

### 8.2 ADMIN Cannot

Based on current route registrations:
- Cannot change user status (OWNER-only)
- Cannot change user roles (no endpoint exists)
- Cannot invite users (no endpoint exists)
- Cannot manage merchant lifecycle (admin-key-only)

### 8.3 ADMIN Service-Level Authorization

The service `UpdateUserStatus` has an explicit second-level check:
```go
if callerRole != model.DashboardUserRoleOwner {
    return nil, ErrInsufficientRole
}
```

This is defense-in-depth — even if the route-level `RequireRole` middleware were bypassed, the service would reject ADMIN.

---

## 9. VIEWER Capabilities

### 9.1 What VIEWER Can Access

VIEWER can access every route that does NOT have `RequireRole` middleware:

| Route | Access |
|---|---|
| `GET /dashboard/overview` | ✅ |
| `GET /dashboard/payments` | ✅ |
| `GET /dashboard/payments/:id` | ✅ |
| `GET /dashboard/refunds` | ✅ |
| `GET /dashboard/refunds/:id` | ✅ |
| `GET /dashboard/api-keys` | ✅ (read-only) |
| `GET /dashboard/webhook-config` | ✅ (read-only) |
| `GET /dashboard/webhooks` | ✅ (read-only) |
| `GET /dashboard/webhooks/:id` | ✅ |
| `GET /dashboard/reconciliation/mismatches` | ✅ |
| `GET /dashboard/reconciliation/mismatches/:id` | ✅ |
| `GET /dashboard/settings` | ✅ |
| `GET /dashboard/users` | ✅ (**can see all team members**) |

### 9.2 What VIEWER Cannot Do

VIEWER is blocked from all mutating operations by `RequireRole(OWNER, ADMIN)` or `RequireRole(OWNER)`:
- Cannot create/cancel payments, create refunds
- Cannot create/revoke API keys
- Cannot configure webhooks
- Cannot disable/enable team members

### 9.3 VIEWER Gap: Team Listing

`GET /dashboard/users` has no `RequireRole` middleware. **All authenticated dashboard users can list team members**, including VIEWERs. Whether VIEWER should be able to see team email addresses is a **NEEDS BUSINESS DECISION**.

---

## 10. Dashboard Authentication

### 10.1 Login Flow

**File:** `internal/service/auth_service.go` → `Login()`

1. Email normalised: `strings.ToLower(strings.TrimSpace(email))`
2. Email format validated via `net/mail.ParseAddress`
3. User looked up by email: `userRepo.GetByEmail()`
4. If not found: dummy Argon2id verification run (constant-time anti-enumeration)
5. Password verified: `verifyPassword(req.Password, user.PasswordHash)` — Argon2id, constant-time
6. User status checked AFTER password verification (prevents user-enumeration via timing)
7. If DISABLED: `ErrUserDisabled` returned
8. JWT access token issued (HS256, 15 min TTL by default)
9. Opaque refresh token generated (32 random bytes, hex-encoded)
10. Refresh token hash stored in `dashboard_sessions`
11. `last_login_at` updated (best-effort)
12. Refresh token returned to handler; handler sets it as HttpOnly cookie

### 10.2 JWT Claims

**File:** `internal/service/jwt_util.go`

```go
type jwtClaims struct {
    Subject    string `json:"sub"`   // MerchantUser UUID
    MerchantID string `json:"mid"`   // Merchant UUID
    Role       string `json:"role"`  // DashboardUserRole string
    IssuedAt   int64  `json:"iat"`
    ExpiresAt  int64  `json:"exp"`
    JTI        string `json:"jti"`   // unique token ID
}
```

**Role IS embedded in the JWT.** However, `RequireDashboardAuth` reads the role from the DB-loaded user (`user.Role`), not from the JWT claims. The JWT role claim is present but not used for authorization in the middleware.

### 10.3 RequireDashboardAuth — Per-Request Checks

**File:** `internal/middleware/dashboard_auth.go`

On every request:
1. Bearer token extracted from `Authorization` header
2. JWT signature and expiry verified (`authSvc.VerifyAccessToken`)
3. `userID` parsed from `claims.Subject`
4. User loaded from DB: `userRepo.GetByID(ctx, userID)`
5. `user.IsActive()` checked — DISABLED users rejected immediately
6. Merchant loaded from DB: `merchantRepo.GetByID(ctx, user.MerchantID)`
7. `merchant.IsActive()` checked — SUSPENDED/INACTIVE merchants rejected

**What is checked per-request from DB:**
- User status (ACTIVE / DISABLED) ✅
- Merchant status (ACTIVE / SUSPENDED / INACTIVE) ✅

**What is NOT re-checked from DB per-request:**
- User role — role is read from `user.Role` (DB-loaded user object, ✅ fresh)
- Actually: because `GetByID` is called every request, `user.Role` IS fresh from DB

**Correction:** Role IS fresh on every request because the full user row is loaded. The role in the JWT claim (`claims.Role`) is present but redundant for authorization — the middleware does not use it.

### 10.4 Stale Role Window

**Finding:** Role IS loaded fresh from DB on every request via `GetByID`. This means role changes take effect immediately on the NEXT request after the change, without requiring a new login or session invalidation. There is NO stale role issue for active requests.

However, **the JWT itself carries the role**. If a client caches the JWT and uses `claims.Role` directly (e.g., for client-side UI decisions), the cached role in the JWT will not reflect changes until the token expires or is refreshed. This is a client-side concern, not a server-side security issue.

### 10.5 Session Invalidation Triggers

| Trigger | Mechanism |
|---|---|
| User logs out | `sessionRepo.Delete(session.ID)` |
| User is DISABLED | `sessionRepo.DeleteByUserID(userID)` in `UpdateUserStatus` |
| Merchant suspended/deactivated | `sessionRepo.DeleteByMerchantID(merchantID)` in `UpdateMerchantStatus` |
| Session expires | `session.IsExpired()` check on refresh; cleanup via `DeleteExpired` |
| Refresh token rotation | Old session deleted before new one created |

---

## 11. User Lifecycle

### 11.1 Current Lifecycle States

```
[Created by admin] → ACTIVE
ACTIVE → DISABLED   (OWNER changes another user's status)
DISABLED → ACTIVE   (OWNER re-enables user)
```

The self-enable path (DISABLED → ACTIVE) is supported by `UpdateUserStatus` but the route uses `PATCH /dashboard/users/:user_id/status` with body `{"status": "ACTIVE"}`. No dedicated "re-enable" endpoint.

### 11.2 Current Lifecycle Gaps

- **No delete** — no `DELETE /dashboard/users/:id` endpoint; no `DeleteUser` in repository
- **No role change** — no `UpdateRole` in repository or service
- **No password reset** — no forgot-password flow, no reset token table
- **No invitation** — no pending state before ACTIVE
- **No OWNER-last guard** — disabling the last OWNER is technically possible (though constrained by self-disable guard)

### 11.3 Session Effect on Disable

When a user is DISABLED:
1. `sessionRepo.DeleteByUserID(targetUserID)` revokes all refresh sessions immediately
2. Active JWT access tokens remain valid until TTL expiry (default 15 min)
3. On next `RequireDashboardAuth` check: `user.IsActive()` returns false → 401

The window between disabling and full lockout is bounded by the JWT access token TTL.

---

## 12. Invitation Infrastructure

**Finding: No existing self-service invitation infrastructure found.**

Searched the entire repository for: `invitation`, `invite`, `token`, `accept`, `pending`, `email_verif`, `SMTP`, `mailer`, `sendgrid`, `ses`, `mailgun`.

Results:
- No `merchant_user_invitations` table (no migration, no model, no repository)
- No `InvitationService` interface or implementation
- No invitation token generation logic
- No email sending capability
- No invitation acceptance endpoint
- No invitation status constants (PENDING, ACCEPTED, EXPIRED, REVOKED)

**Conclusion:** Phase 8 must build the entire invitation infrastructure from scratch if an invitation-based flow is desired.

---

## 13. Email Infrastructure

**Finding: No email sending infrastructure exists.**

Searched for: `smtp`, `mail.Send`, `sendgrid`, `ses`, `mailgun`, `postmark`, `email template`, `html/template` in service layer.

The only email-related code found is:
- `net/mail.ParseAddress` — used for email FORMAT validation only, not sending
- `emailDomain()` helper in `auth_service.go` — logs only the domain part of an email for privacy

**Current email-related files:**
- `internal/service/auth_service.go` — email validation via `net/mail`
- `internal/service/dashboard_user_service.go` — email normalisation and validation
- No mailer service, no SMTP client, no email template system

**Impact on Phase 8:**
- An invitation flow that sends emails via a link requires building an email sending layer first
- Alternatively, Phase 8 can support a "passwordless invite link" flow that does NOT require email delivery — the OWNER copies the invite link and sends it manually
- Or Phase 8 can use a simpler model: OWNER sets the initial password directly on behalf of the invitee

---

## 14. Password Flow

### 14.1 Current Password Handling

| Concern | Current State |
|---|---|
| Hashing algorithm | Argon2id (PHC format) |
| Salt | 16 random bytes per user (crypto/rand) |
| Memory cost | 64 MiB |
| Time cost | 1 iteration |
| Parallelism | 4 threads |
| Key length | 32 bytes |
| Minimum length | 8 characters (`binding:"min=8"`) |
| Maximum length | 128 characters (`binding:"max=128"`) |
| Password change API | **Does not exist** |
| Password reset API | **Does not exist** |
| Forgot-password flow | **Does not exist** |
| Session invalidation on password change | N/A — no password change exists |

### 14.2 Invited User Password Problem

If Phase 8 introduces an invitation system, a newly invited user needs a way to set their password. Current options based on the existing architecture:

**Option A — Admin sets password:** Admin provides a temporary password in the invite request body. User changes it after login. (No password-change endpoint exists — would need to be added.)

**Option B — Invitation token sets password:** The invitation acceptance endpoint accepts a new password. No email delivery required — just a token-based flow.

**Option C — No invitation — direct creation:** OWNER creates the user with a temporary password and communicates it out-of-band. User logs in and... cannot change password (no password-change endpoint). This option is incomplete without a password-change endpoint.

---

## 15. Email Uniqueness

**Finding: Email is GLOBALLY unique, not per-merchant.**

**Migration comment** (`migrations/000014_create_merchant_users.up.sql`):
```
-- Email uniqueness is GLOBAL (not per-merchant) for the following reasons:
--   1. Auth lookup goes email → user → merchant in one index scan.
--   2. Prevents the same person from accidentally owning seats in two separate merchants.
--   3. Password-reset flows (future) do not need a merchant discriminator.
```

**Repository enforcement:**
- `uq_merchant_users_email` — unique index on `(email)` only, no `merchant_id`
- `GetByEmail` queries `WHERE email = $1` — no merchant scope

**Service enforcement:**
- `ErrMerchantUserEmailExists` is returned when unique constraint fires
- `ErrEmailAlreadyExists` in service maps this to `409 EMAIL_ALREADY_EXISTS`

**Implication for team management:** If `user@example.com` is already an OWNER of Merchant A, they CANNOT be invited to Merchant B. The system does not support one person belonging to multiple merchants. This is a fundamental architectural constraint.

---

## 16. Multi-Merchant User Analysis

**Finding: The architecture assumes one email = one merchant.**

Evidence:
1. `uq_merchant_users_email` — global unique index prevents the same email in two merchant rows
2. Login flow: `GetByEmail` returns a single user — there is no mechanism to select which merchant to log into
3. JWT claims include `mid` (merchantID) — the token is bound to exactly one merchant
4. `RequireDashboardAuth` loads `merchant_id` from `user.MerchantID` — one merchant per session

**Implication:** Phase 8 invitation system must either:
- Reject invitations to emails that already have an account (current behavior), OR
- Introduce a multi-merchant user model (major architectural change — OUT OF SCOPE for Phase 8)

The simpler path is to enforce: **one email = one merchant** for the duration of Phase 8.

---

## 17. Session Model

### 17.1 Session Schema

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | Session identifier |
| `merchant_user_id` | UUID FK → `merchant_users(id)` | User scope |
| `refresh_token_hash` | VARCHAR(64) | SHA-256 hex of plaintext token |
| `expires_at` | TIMESTAMPTZ | Session expiry |
| `created_at` | TIMESTAMPTZ | Creation timestamp |
| `last_used_at` | TIMESTAMPTZ (nullable) | Last use timestamp |

**Note: No `merchant_id` column on sessions.** Sessions are user-scoped only. Merchant-wide operations (e.g., `DeleteByMerchantID`) join through `merchant_users`.

### 17.2 Session Repository Methods

**File:** `internal/repository/dashboard_session_repository.go`

| Method | Description |
|---|---|
| `Create` | Insert new session |
| `GetByRefreshTokenHash` | Look up session by token hash |
| `GetByUserID` | List sessions for one user |
| `Delete` | Delete one session by ID |
| `DeleteByUserID` | Delete all sessions for one user |
| `DeleteByMerchantID` | Delete all sessions for all users of a merchant (via JOIN) |
| `DeleteExpired` | Cleanup expired sessions |
| `UpdateLastUsedAt` | Best-effort last-use tracking |

### 17.3 Session Revocation Capabilities

| Scope | Method | Used When |
|---|---|---|
| One session | `Delete(id)` | Logout |
| All sessions for one user | `DeleteByUserID(userID)` | User disabled |
| All sessions for one merchant | `DeleteByMerchantID(merchantID)` | Merchant suspended/deactivated |
| Expired sessions | `DeleteExpired()` | Cleanup worker |

**For Phase 8 role changes:** If a user's role changes, there is currently no trigger to revoke their sessions. Since role is reloaded from DB on every request, this is not a security issue — the new role takes effect immediately on the next request.

---

## 18. Role Change Behavior

### 18.1 Current State

There is no role change API. `MerchantUserRepository` has no `UpdateRole` method. `DashboardUserService` has no role-change operation.

### 18.2 Stale Role Analysis (If Role Change Were Added)

Because `RequireDashboardAuth` calls `userRepo.GetByID()` on every request and reads `user.Role` from the returned struct, any role change in the DB takes effect on the **very next HTTP request** from that user. No session revocation is required for role enforcement to be current.

However, the **JWT itself carries the old role** in the `role` claim. If any code path reads `claims.Role` instead of `user.Role`, it would see the old role for up to `ACCESS_TOKEN_TTL` (default 15 min). Inspection of `RequireDashboardAuth` and `RequireRole` shows they both use the context user (`user.Role`) not the JWT claim. The JWT `role` field is currently redundant for server-side authorization.

**Recommendation for Phase 8:** When implementing role change, document that no session invalidation is required for role enforcement. However, optionally revoke and reissue sessions to ensure the JWT `role` claim is refreshed for any clients that use it for UI rendering.

---

## 19. User Disable Behavior

### 19.1 Current Behavior When User Is DISABLED

1. `UpdateUserStatus(…, DISABLED)` is called
2. Service calls `sessionRepo.DeleteByUserID(targetUserID)` — all refresh sessions deleted immediately
3. `userRepo.UpdateStatus(id, DISABLED)` — row updated in DB
4. Any future `POST /api/v1/auth/refresh` call: session not found → 401
5. Any future `POST /api/v1/auth/login` call: `!user.IsActive()` → `ErrUserDisabled` → 401
6. Outstanding JWT access tokens: still valid until `exp` (up to 15 min window)
7. Any request using an outstanding JWT: `RequireDashboardAuth` calls `userRepo.GetByID()` → `user.IsActive()` returns false → 401 `USER_DISABLED`

**Result:** Disabled user is fully locked out immediately (refresh revoked) with a maximum residual window of `ACCESS_TOKEN_TTL` for in-flight JWTs.

### 19.2 Re-Enable Behavior

Re-enabling (DISABLED → ACTIVE):
1. `UpdateUserStatus(…, ACTIVE)` is called
2. Service does NOT revoke sessions (there are none — they were deleted on disable)
3. Status updated in DB
4. User can now log in again via `POST /api/v1/auth/login`

No sessions are restored — user must log in fresh after being re-enabled.

---

## 20. Admin vs Self-Service Boundary

### 20.1 Current Admin-Only Operations

| Operation | Route | Reason to Keep Admin-Only |
|---|---|---|
| Onboard new merchant | `POST /admin/onboarding/merchants` | Platform provisioning — admin scope |
| Create merchant row directly | `POST /merchants` | Low-level bootstrap |
| Read merchant by ID | `GET /merchants/:id` | Admin audit tool (Phase 7) |
| Suspend/activate/deactivate merchant | `PATCH /admin/merchants/:id/status` | Lifecycle management |
| Import settlement | `POST /admin/settlements/import` | Platform operation |
| Run reconciliation | Various admin settlement routes | Platform operation |
| Bootstrap dashboard user (any role) | `POST /admin/merchants/:id/users` | Initial OWNER bootstrap |

### 20.2 What Should Move to Self-Service in Phase 8

| Operation | Proposed Self-Service Route | Required Role |
|---|---|---|
| Invite new team member | `POST /dashboard/users/invite` | OWNER |
| Accept invitation | `POST /api/v1/invitations/:token/accept` (unauthenticated) | N/A |
| Change member role | `PATCH /dashboard/users/:id/role` | OWNER |
| List team members | `GET /dashboard/users` | Any (already exists) |
| Disable/enable member | `PATCH /dashboard/users/:id/status` | OWNER (already exists) |

### 20.3 What Should Remain Admin-Only

- Creating the first OWNER (bootstrapping a new merchant) — stays `POST /admin/merchants/:id/users`
- Overriding user status outside normal self-service flow (e.g., compliance enforcement)
- Merchant lifecycle (suspend, deactivate)

---

## 21. Proposed API Surface

The following routes are recommended for Phase 8. They follow the existing `/api/v1/dashboard/users` naming convention used in the codebase.

### 21.1 User Invitation

**POST /api/v1/dashboard/users/invite**

- Authentication: `RequireDashboardAuth`
- Required role: `OWNER` (NEEDS BUSINESS DECISION — should ADMIN also invite?)
- Request: `{ "email": "string", "role": "ADMIN|VIEWER" }`
- Response 201: invitation object (ID, email, role, expires_at, invited_by)
- Response 409: email already registered
- Response 422: disallowed role (e.g., inviting OWNER) — NEEDS BUSINESS DECISION
- Merchant isolation: invitation scoped to `caller.MerchantID`

**GET /api/v1/invitations/:token** (unauthenticated)

- Validates token, returns invitation metadata (email, role, merchant name)
- Response 200: `{ "email": "...", "role": "...", "merchant_name": "..." }`
- Response 404: invalid or expired token

**POST /api/v1/invitations/:token/accept** (unauthenticated)

- Request: `{ "password": "string" }`
- Creates `merchant_users` row with ACTIVE status and provided password
- Marks invitation as accepted
- Response 201: `DashboardUserResponse`
- Response 404: invalid or expired token
- Response 409: email already registered (race condition)

### 21.2 Role Management

**PATCH /api/v1/dashboard/users/:user_id/role**

- Authentication: `RequireDashboardAuth`
- Required role: `OWNER`
- Request: `{ "role": "ADMIN|VIEWER" }` (OWNER cannot be assigned via self-service — NEEDS BUSINESS DECISION)
- Response 200: updated `DashboardUserResponse`
- Merchant isolation: service checks `target.MerchantID == caller.MerchantID`
- Self-role-change: NEEDS BUSINESS DECISION (should OWNER be able to downgrade themselves?)

### 21.3 Existing Endpoints (No Change Needed)

- `GET /api/v1/dashboard/users` — already implemented
- `PATCH /api/v1/dashboard/users/:user_id/status` — already implemented

---

## 22. Invitation Data Model Evaluation

### 22.1 Proposed Schema

```sql
CREATE TABLE merchant_user_invitations (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id  UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    email        VARCHAR(254) NOT NULL,           -- normalised (lowercase, trimmed)
    role         VARCHAR(20)  NOT NULL CHECK (role IN ('ADMIN', 'VIEWER')),
    token_hash   VARCHAR(64)  NOT NULL,           -- SHA-256 hex of opaque token
    invited_by   UUID         NOT NULL REFERENCES merchant_users(id),
    expires_at   TIMESTAMPTZ  NOT NULL,
    accepted_at  TIMESTAMPTZ,                     -- NULL until accepted
    revoked_at   TIMESTAMPTZ,                     -- NULL until revoked
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_invitations_token_hash ON merchant_user_invitations (token_hash);
CREATE INDEX idx_invitations_merchant_email ON merchant_user_invitations (merchant_id, email, accepted_at, revoked_at);
```

### 22.2 Design Decisions

| Decision | Recommendation | Rationale |
|---|---|---|
| `merchant_id` mandatory? | Yes | Required for tenant isolation |
| Token stored hashed? | Yes (SHA-256, same as refresh tokens) | Plaintext token never stored |
| Email normalised? | Yes (lowercase + trim, same as user creation) | Consistent with existing email handling |
| Single-use? | Yes — set `accepted_at` on acceptance; reject if not NULL | Prevents replay |
| Expiry? | Yes — recommended 48–72 hours | NEEDS BUSINESS DECISION |
| Duplicate pending invitations? | One active invitation per (merchant_id, email) — reject new if pending | NEEDS BUSINESS DECISION |
| Role changeable before acceptance? | No (simplest path) | Can always revoke and re-invite |
| Survives merchant suspension? | Token remains in DB; acceptance endpoint can check merchant status | NEEDS BUSINESS DECISION |
| OWNER invitable via this flow? | Recommend: No (admin-only) | Prevents privilege escalation |

### 22.3 Invitation Lifecycle

```
[Created] → PENDING (accepted_at IS NULL, revoked_at IS NULL, expires_at > NOW())
PENDING → ACCEPTED  (accepted_at set, merchant_users row created)
PENDING → EXPIRED   (expires_at < NOW() — no DB change, checked at acceptance time)
PENDING → REVOKED   (revoked_at set)
```

---

## 23. Invitation Security

### 23.1 Threat Analysis

| Threat | Mitigation Required |
|---|---|
| Token leakage (email interception) | Short expiry (48h), HTTPS only |
| Token replay after acceptance | Mark `accepted_at` on first use; reject if already set |
| Token enumeration | 32-byte random token = 256-bit entropy; SHA-256 stored; not guessable |
| Cross-tenant acceptance | `merchant_id` in invitation row; acceptance endpoint loads invitation and checks `merchant_id` |
| Invitation fixation (attacker pre-creates invitation for victim's email) | Check if email already has an account before acceptance; require fresh token per invite |
| Expired invitation reuse | Check `expires_at > NOW()` AND `accepted_at IS NULL` AND `revoked_at IS NULL` |
| Revoked invitation reuse | Check `revoked_at IS NULL` |
| Email ownership assumption | No email verification — a person with access to the email link can accept. The OWNER is responsible for sending to the correct person. |
| Role escalation via invitation | Invitation schema should not allow `role = 'OWNER'`; restrict to ADMIN and VIEWER |
| Race condition on acceptance | `accepted_at` update should use `UPDATE ... WHERE accepted_at IS NULL RETURNING id` |

### 23.2 Security Requirements

1. Token MUST be 32 random bytes (crypto/rand), hex-encoded (64 chars)
2. Token MUST be stored as SHA-256 hash only
3. Token MUST be single-use (check `accepted_at IS NULL` on acceptance)
4. Token MUST expire (configurable, default 48–72 hours)
5. Token MUST be bound to a specific `merchant_id`
6. Acceptance MUST verify the invited email does not already have an active account
7. Acceptance endpoint MUST check merchant status (reject if SUSPENDED/INACTIVE)
8. Invitation creation MUST be role-gated (OWNER only, or OWNER+ADMIN — NEEDS BUSINESS DECISION)
9. Role in invitation MUST be validated (no OWNER invitations via self-service)

---

## 24. Role Boundary Evaluation

### 24.1 Proposed Role Boundaries for Phase 8

Based on the existing authorization model and SaaS best practices:

| Capability | OWNER | ADMIN | VIEWER | Notes |
|---|:---:|:---:|:---:|---|
| Invite ADMIN | ✅ | NEEDS DECISION | ❌ | Should ADMIN be able to invite other ADMINs? |
| Invite VIEWER | ✅ | NEEDS DECISION | ❌ | Should ADMIN be able to invite VIEWERs? |
| Invite OWNER | ❌ (admin-only bootstrap) | ❌ | ❌ | OWNERs created by platform admin only |
| Change ADMIN → VIEWER | ✅ | ❌ | ❌ | OWNER-only role management |
| Change VIEWER → ADMIN | ✅ | ❌ | ❌ | OWNER-only role management |
| Change OWNER → * | NEEDS DECISION | ❌ | ❌ | Can OWNER demote themselves or another OWNER? |
| Change * → OWNER | ❌ | ❌ | ❌ | Promotion to OWNER via self-service is high risk |
| Disable ADMIN | ✅ | ❌ | ❌ | Already implemented |
| Disable VIEWER | ✅ | ❌ | ❌ | Already implemented |
| Disable OWNER | NEEDS DECISION | ❌ | ❌ | Last-OWNER protection needed |
| Re-enable any user | ✅ | ❌ | ❌ | Already implemented |
| Revoke invitation | ✅ | NEEDS DECISION | ❌ | Should ADMIN revoke their own invites? |
| List team members | ✅ | ✅ | NEEDS DECISION | Currently no role restriction |

---

## 25. Merchant Lifecycle Interaction

### 25.1 Current Enforcement by Phase 7 Middleware

`RequireDashboardAuth` checks `merchant.IsActive()` on every request. This means:

| Merchant Status | Effect on Team Management |
|---|---|
| ACTIVE | All team management endpoints accessible (subject to role) |
| SUSPENDED | All dashboard endpoints blocked → `401 MERCHANT_INACTIVE` |
| INACTIVE | All dashboard endpoints blocked → `401 MERCHANT_INACTIVE` |

All Phase 8 team management endpoints will inherit this enforcement automatically via `RequireDashboardAuth`.

### 25.2 Invitation Behavior Under Merchant Status

Phase 8 must decide:

| Scenario | Recommended Behavior |
|---|---|
| Create invitation when merchant ACTIVE | ✅ Allowed |
| Create invitation when merchant SUSPENDED | ❌ Blocked by `RequireDashboardAuth` automatically |
| Accept invitation when merchant SUSPENDED | NEEDS BUSINESS DECISION — acceptance endpoint is unauthenticated; must manually check merchant status |
| Accept invitation when merchant INACTIVE | NEEDS BUSINESS DECISION — should permanently cancelled invitations be blocked? |
| Pending invitation when merchant is suspended | Invitation row remains; can be accepted when merchant is reinstated (if allowed) |

---

## 26. User Lifecycle Matrix

| User State | Can Log In | Can Use Active JWT | Can Be Invited Again | Can Be Re-enabled | Sessions Status |
|---|:---:|:---:|:---:|:---:|---|
| ACTIVE | ✅ | ✅ | N/A (already exists) | N/A | Active |
| DISABLED | ❌ (`ErrUserDisabled`) | ❌ (per-request DB check) | NEEDS DECISION | ✅ (OWNER can re-enable) | Revoked on disable |
| Pending invitation | N/A (no account yet) | N/A | NEEDS DECISION (duplicate pending) | N/A | No sessions |
| Expired invitation | N/A (no account yet) | N/A | ✅ (re-invite) | N/A | No sessions |
| Revoked invitation | N/A (no account yet) | N/A | NEEDS DECISION | N/A | No sessions |
| Accepted invitation | ACTIVE (normal flow) | ✅ | N/A | N/A | Created at login |

---

## 27. IDOR / Security Findings

### 27.1 Current Findings

| Finding | Severity | File | Details |
|---|---|---|---|
| `GetByID` in repo has no `merchant_id` predicate | Low (mitigated) | `merchant_user_repository.go` | Service layer must always check `target.MerchantID`. Currently done correctly. Risk: future callers may forget. |
| `ErrCrossmerchantAccess` returns 404 | ✅ Good | `dashboard_user_handler.go` | Correctly hides existence of cross-tenant users. |
| VIEWER can list team members (emails) | Medium | `main.go` | No `RequireRole` on `GET /dashboard/users`. Depends on business decision. |
| Role in JWT may be stale for client UI | Low | `jwt_util.go` | JWT `role` claim is not refreshed on role change. Server-side authorization uses DB-loaded role — secure. Client-side UI may show stale role until JWT refresh. |

### 27.2 Required Checks for New Phase 8 Operations

Every new operation (invite, change role, revoke invitation) MUST follow:

1. Load caller from context (`dashboardUserFromContext(c)`)
2. Caller merchant identity from `caller.MerchantID` — never from request body/URL
3. Load target resource by ID
4. Verify `target.MerchantID == caller.MerchantID` in service layer
5. Return 404 (not 403) on cross-merchant access (`ErrCrossmerchantAccess` → 404)
6. `RequireRole` middleware at route level AND service-level role check (defense in depth)

---

## 28. Database Findings

### 28.1 What Exists

| Table | Status | Notes |
|---|---|---|
| `merchant_users` | ✅ Complete | Supports OWNER/ADMIN/VIEWER, ACTIVE/DISABLED |
| `dashboard_sessions` | ✅ Complete | Supports all revocation patterns |

### 28.2 What Is Missing for Phase 8

| Element | Purpose | Migration Needed |
|---|---|---|
| `merchant_user_invitations` table | Invitation flow | Yes — new migration |
| `UpdateRole` method on repo | Role change | No migration (column exists); just new repo method |
| `UpdateRole` on `MerchantUserRepository` interface | Role change | Code change only |
| `invited_by` FK on `merchant_users` | Track who created each user | Optional — NEEDS BUSINESS DECISION |
| Per-OWNER count constraint | Prevent zero-OWNER merchant | Complex trigger or application-level — NEEDS DECISION |
| `last_password_changed_at` on `merchant_users` | Password audit | New migration if desired |

### 28.3 No Role Migration Required

The `merchant_users.role` column already accepts `OWNER`, `ADMIN`, `VIEWER`. Changing a role is a pure `UPDATE merchant_users SET role = $1 WHERE id = $2` — no schema change needed.

---

## 29. Frontend Findings

No frontend code exists in this repository. The backend is an API-only service. Frontend is a separate (unavailable) repository.

Swagger documentation (`swagger/docs.go`, `swagger/swagger.yaml`) is the primary API contract for frontend consumers. Phase 8 will need to add Swagger annotations to all new endpoints.

The existing `DashboardUserResponse` DTO already includes `role` and `status` fields — the frontend can use these for UI rendering. No DTO changes are needed for the read side; new endpoints will need new request/response types.

---

## 30. Test Coverage

### 30.1 Currently Covered

| Area | Coverage | Files |
|---|---|---|
| Login / logout / refresh | ✅ Good | `auth_service_test.go`, `dashboard_auth_handler_test.go` |
| JWT sign/verify | ✅ Good | `jwt_util_test.go` |
| User creation (admin) | ✅ Good | `dashboard_user_service_test.go`, `dashboard_auth_handler_test.go` |
| List users | ✅ Merchant isolation tested | `dashboard_user_service_test.go` |
| Disable/enable user | ✅ Role checks, cross-merchant, self-disable | `dashboard_user_service_test.go`, `dashboard_auth_handler_test.go` |
| Session revocation on disable | ✅ | `dashboard_user_service_test.go` |
| Dashboard auth middleware | ✅ User status, token validity | `auth_middleware_test.go` |
| Merchant status check in dashboard auth | ✅ (added in Phase 7) | `auth_middleware_test.go` |

### 30.2 Missing Tests Required for Phase 8

| Test | What It Covers |
|---|---|
| `TestInviteUser_OwnerSuccess` | OWNER can invite ADMIN/VIEWER |
| `TestInviteUser_AdminCannotInvite` | ADMIN blocked from inviting (if OWNER-only) |
| `TestInviteUser_DuplicatePending` | Duplicate pending invitation rejected |
| `TestInviteUser_EmailAlreadyRegistered` | Existing email rejected |
| `TestAcceptInvitation_Success` | Token accepted, user created |
| `TestAcceptInvitation_Expired` | Expired token rejected |
| `TestAcceptInvitation_AlreadyAccepted` | Replay rejected |
| `TestAcceptInvitation_Revoked` | Revoked token rejected |
| `TestAcceptInvitation_CrossTenant` | Invitation merchant isolation |
| `TestUpdateUserRole_OwnerSuccess` | OWNER can change ADMIN → VIEWER |
| `TestUpdateUserRole_AdminBlocked` | ADMIN cannot change roles |
| `TestUpdateUserRole_CrossMerchantReturns404` | Role change cross-tenant returns 404 |
| `TestUpdateUserRole_SelfRoleChange` | OWNER self-demotion behavior (NEEDS DECISION) |
| `TestLastOwnerGuard` | Disabling last OWNER blocked (if implemented) |
| `TestRoleChangeEffectiveImmediately` | Next request after role change uses new role |

---

## 31. MUST HAVE

Items required to safely introduce self-service team management:

### M1 — Self-Service User Invitation (Non-Email Path)
Allow OWNER to invite a new user by email + role. Generate an opaque invitation token. Return the token in the API response (OWNER copies and shares the link manually). No email delivery required.
- **Files needed:** new migration, `InvitationRepository`, `InvitationService`, `InvitationHandler`, route registration
- **Evidence gap:** No invitation infrastructure exists anywhere

### M2 — Invitation Acceptance Endpoint
Unauthenticated endpoint that accepts an invitation token + new password, creates the `merchant_users` row, marks the invitation as accepted.
- **Evidence gap:** No acceptance flow exists

### M3 — Role Change API
`PATCH /api/v1/dashboard/users/:user_id/role` — OWNER-only.
- **Files needed:** `UpdateRole` in `MerchantUserRepository`, `UpdateUserRole` in `DashboardUserService`, handler method
- **Evidence gap:** No `UpdateRole` in repository or service

### M4 — Password Change API
Users must be able to change their own password after accepting an invitation or logging in with a temporary credential.
- **Files needed:** `UpdatePasswordHash` in `MerchantUserRepository`, `ChangePassword` in `DashboardUserService`, handler route
- **Evidence gap:** No password change endpoint exists

### M5 — Invitation Table Migration
New migration for `merchant_user_invitations` table.
- **Evidence gap:** Table does not exist

### M6 — Last-OWNER Protection
Prevent disabling the last active OWNER of a merchant.
- **Files needed:** Service-level check in `UpdateUserStatus` — count active OWNERs before allowing disable
- **Evidence gap:** No such guard exists

---

## 32. SHOULD HAVE

Useful but can follow the core Phase 8 implementation:

### S1 — Email Delivery for Invitations
Send the invitation link to the invitee's email automatically. Requires introducing an email sending infrastructure (SMTP client or third-party provider).

### S2 — Invitation Revocation API
`DELETE /api/v1/dashboard/users/invitations/:id` — OWNER (or inviting user) can cancel a pending invitation.

### S3 — List Pending Invitations
`GET /api/v1/dashboard/users/invitations` — view pending invitations for the merchant.

### S4 — Invitation Resend
`POST /api/v1/dashboard/users/invitations/:id/resend` — generate a new token for a pending invitation (extends expiry).

### S5 — Audit Logging for Role and Status Changes
Structured log entries (or a new `audit_log` table) for: invitation created, accepted, revoked; role changed; user disabled/enabled.

### S6 — `invited_by` FK on `merchant_users`
Add `invited_by UUID REFERENCES merchant_users(id)` column to track who provisioned each user.

---

## 33. OUT OF SCOPE

| Item | Reason |
|---|---|
| Multi-merchant user accounts | Major architecture change; email uniqueness is global by design |
| SSO / SAML / SCIM | No identity provider infrastructure |
| MFA / 2FA | No second-factor infrastructure |
| Billing / seat limits | No billing infrastructure |
| Subscription tiers | No billing infrastructure |
| Per-role permission customisation (RBAC) | Current model is hardcoded role hierarchy |
| Sandbox vs production environments | No environment separation exists |
| Per-merchant Midtrans credentials | Provider architecture change |
| Password complexity rules | Minor feature; current min=8 is acceptable for Phase 8 |
| Account deletion (hard delete) | No `deleted_at` column; complex to retrofit |
| Admin override of merchant team | Platform admin can already use `POST /admin/merchants/:id/users` |

---

## 34. Business Decisions

Items that require explicit product/business decisions before implementation:

| # | Question | Options |
|---|---|---|
| BD1 | Can there be multiple OWNERs? | Yes (current) or restrict to exactly 1 |
| BD2 | Can OWNER be disabled if they are the last OWNER? | Block (recommended) or allow (risky) |
| BD3 | Can OWNER change their own role to ADMIN or VIEWER? | Allow (lose OWNER) or block (self-demotion) |
| BD4 | Can OWNER invite another OWNER? | Block (use admin bootstrap only) or allow |
| BD5 | Can ADMIN invite team members (ADMIN/VIEWER)? | Allow or restrict to OWNER only |
| BD6 | Can ADMIN change another ADMIN's role? | Allow or restrict to OWNER only |
| BD7 | Can VIEWER list team members (see emails)? | Allow (current) or restrict |
| BD8 | Is email verification required for invited users? | No (simpler) or yes (requires email infrastructure) |
| BD9 | Is email delivery required in Phase 8? | No (manual link sharing) or yes (requires email infrastructure) |
| BD10 | How long do invitations remain valid? | 24h, 48h, 72h, 7d — NEEDS DECISION |
| BD11 | Are duplicate pending invitations allowed? | Block (one active per email+merchant) or allow |
| BD12 | What happens to pending invitations when merchant is suspended? | Block acceptance or allow |
| BD13 | What happens to pending invitations when merchant is INACTIVE? | Block acceptance permanently |
| BD14 | Should role changes invalidate existing sessions? | Not required (server reads role from DB) — but optionally revoke for JWT freshness |
| BD15 | Should disabling a user invalidate all sessions? | **Already implemented: Yes** |
| BD16 | Can an invitation be resent? | Yes (new token, extend expiry) or must revoke and re-invite |
| BD17 | Can an invitation be revoked? | Yes (recommended) or no |
| BD18 | What happens if the invited email already has an account in another merchant? | Block (current global uniqueness) or allow multi-tenant (out of scope) |
| BD19 | Should OWNER be able to promote ADMIN → OWNER? | Block (admin bootstrap only) or allow via self-service |
| BD20 | Should password change require the current password? | Yes (standard practice) or no |

---

## 35. Recommended Implementation Order

Based on dependency analysis:

### Step 1 — `UpdateRole` Repository + Service
Add `UpdateRole(ctx, id, role)` to `MerchantUserRepository` interface and `pgMerchantUserRepository`. Add `UpdateUserRole` to `DashboardUserService`. No migration required.
- **Files:** `internal/repository/merchant_user_repository.go`, `internal/service/dashboard_user_service.go`

### Step 2 — Role Change Handler + Route
Add `PATCH /api/v1/dashboard/users/:user_id/role` handler (OWNER-only).
- **Files:** `internal/handler/dashboard_user_handler.go`, `cmd/server/main.go`

### Step 3 — Last-OWNER Protection
Add last-OWNER guard to `UpdateUserStatus` and `UpdateUserRole`. Count active OWNERs before allowing disable or demotion.
- **Files:** `internal/service/dashboard_user_service.go`
- Requires business decision on BD1, BD2, BD3.

### Step 4 — `merchant_user_invitations` Migration
New migration file for the invitations table.
- **Files:** new `migrations/000015_create_invitations.up.sql`

### Step 5 — Invitation Repository
`InvitationRepository` interface + `pgInvitationRepository` implementation.
- **Files:** `internal/repository/invitation_repository.go`

### Step 6 — Invitation Service
`InvitationService` with `CreateInvitation`, `GetByToken`, `AcceptInvitation`, `RevokeInvitation`.
- **Files:** `internal/service/invitation_service.go`

### Step 7 — Invitation Handlers + Routes
- `POST /api/v1/dashboard/users/invite` (authenticated, OWNER-only)
- `GET /api/v1/invitations/:token` (unauthenticated)
- `POST /api/v1/invitations/:token/accept` (unauthenticated)
- **Files:** `internal/handler/invitation_handler.go`, `cmd/server/main.go`

### Step 8 — Password Change API
Add `UpdatePasswordHash` to `MerchantUserRepository`. Add `ChangePassword` to a user service. Add `PATCH /api/v1/auth/password` endpoint.
- **Files:** `internal/repository/merchant_user_repository.go`, service, handler, routes

### Step 9 — Tests
Unit tests for all new service methods and handler tests for all new routes.

### Step 10 — Swagger
Update Swagger annotations and regenerate `swagger/docs.go`.

### Dependency Graph

```
Step 1 (UpdateRole repo + service)
    └── Step 2 (role change handler)
            └── Step 3 (last-OWNER guard — depends on BD1,BD2,BD3)

Step 4 (migration)
    └── Step 5 (invitation repo)
            └── Step 6 (invitation service)
                    └── Step 7 (invitation handlers)

Step 8 (password change) — independent; needed before Step 7 is useful

Step 9 (tests) — after each step
Step 10 (swagger) — after Step 7
```

Steps 1–3 and Steps 4–7 can be developed in parallel by separate developers.

---

## 36. Risks

### R1 — Global Email Uniqueness Blocks Invited Users
If a person already has a dashboard account at Merchant A, they cannot be invited to Merchant B. This is a known architectural constraint.
**Mitigation:** Document this to product. Defer multi-merchant support to a later phase.

### R2 — No Email Infrastructure — Invitation UX
Without email delivery, the OWNER must manually copy and share invitation links. This is operationally awkward.
**Mitigation:** Build the invitation API without email delivery (Phase 8), then add email delivery in a follow-up phase (Phase 8B or Phase 9).

### R3 — Stale JWT Role for Client-Side UI
If a user's role changes, their JWT `role` claim still shows the old role until the JWT expires or is refreshed. Server-side authorization is correct (DB-loaded role). Client-side UI may show wrong role for up to `ACCESS_TOKEN_TTL`.
**Mitigation:** Document that clients should use `/auth/me` or `/auth/refresh` after a role change event. Server-side behavior is correct and safe.

### R4 — Last-OWNER Disaster
If BD2 is resolved as "allow disabling the last OWNER", a merchant could end up with no active OWNER and no way to add team members via self-service. Recovery would require platform admin intervention.
**Mitigation:** Strongly recommend implementing last-OWNER guard (M6) before any self-service role/status management goes live.

### R5 — Race Condition on Invitation Acceptance
Two concurrent acceptance requests with the same token could both succeed if the `accepted_at` update is not atomic.
**Mitigation:** Use `UPDATE merchant_user_invitations SET accepted_at = NOW() WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL RETURNING id`. If zero rows returned, reject with 409.

### R6 — Invitation Token in URL Logs
The invitation token appears in the URL (`/api/v1/invitations/:token`). Web server access logs will record the token.
**Mitigation:** Use POST body for token on acceptance (not URL path). Or accept the URL path risk given the short expiry window.

### R7 — ADMIN Cannot Manage Team
If BD5 is resolved as "OWNER-only can invite", ADMIN users have no path to expand the team. In organizations where the OWNER is not always available, this is a workflow bottleneck.
**Mitigation:** Consider allowing ADMIN to invite VIEWER-level users (but not ADMIN-level). This limits privilege escalation risk.

### R8 — Existing Test Mock Compatibility
Any new methods added to `MerchantUserRepository` interface (e.g., `UpdateRole`, `UpdatePasswordHash`) will require updates to all mock implementations in the test suite. This is a maintenance cost.
**Mitigation:** Accepted cost of interface extension. All mocks are in test files; the compiler will flag missing implementations.

---

*Audit completed: 2026-09-22. No application code was modified during this audit. Only `docs/phase-8-self-service-team-management-audit.md` was created.*
