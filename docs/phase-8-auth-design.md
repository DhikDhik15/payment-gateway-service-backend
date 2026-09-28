# Phase 8 — Dashboard Authentication Design

## Overview

Phase 8 introduces a dedicated authentication system for the React dashboard. This is completely **separate** from merchant API keys.

```
Dashboard Account (Phase 8)        Merchant API Key (Phase 1–5C)
─────────────────────────────      ─────────────────────────────
email + password                   pk_<hex>:sk_<hex>
    ↓                                  ↓
JWT access token                   Server-to-server API calls
+ HttpOnly refresh cookie          (machine-to-machine integration)
    ↓
React dashboard UI
```

**Never use a merchant API key as a dashboard login credential, and never use a dashboard password to authenticate API calls.**

---

## Authentication Mechanism

**JWT Access Token + HttpOnly Refresh Cookie (Dual-token session design)**

### Why this design?

| Concern | Decision |
|---|---|
| React SPA compatibility | JWTs are easy to use in Authorization headers |
| XSS resistance for refresh token | HttpOnly cookie prevents JavaScript access |
| Short exposure window | 15-minute access token limits blast radius of token theft |
| Session revocation | Server-side refresh-session record provides continuation revocation; access JWT validity remains bounded by TTL |
| Zero external dependencies | HS256 JWT implemented with stdlib crypto only |

### Why not long-lived JWTs only?
A long-lived JWT cannot be revoked without a server-side blocklist. If stolen, the attacker retains access until expiry. The dual-token design limits this: access tokens expire in 15 minutes, and the refresh token is stored HttpOnly (harder to steal via XSS).

### Why not cookies-only?
Cookies-only requires careful CSRF protection. The Bearer token approach for API calls is simpler, better documented, and more familiar in the React/API context.

---

## Token Lifecycle

```
1. Login (POST /api/v1/auth/login)
   ├── Validates email + Argon2id password
   ├── Creates: dashboard_sessions row (stores SHA-256 hash of refresh token)
   ├── Issues: short-lived JWT access token (15m, in JSON body)
   └── Sets:   HttpOnly refresh_token cookie (7d, in Set-Cookie header)

2. Authenticated request
   └── Authorization: Bearer <access_token>
       └── Middleware: validates JWT signature + expiry
           └── Loads user from DB (checks ACTIVE status)

3. Access token expiry (after 15m)
   ├── POST /api/v1/auth/refresh (sends refresh_token cookie automatically)
   ├── Validates refresh token hash against dashboard_sessions
   ├── Atomically CAS-rotates the refresh hash on the same stable session row
   └── Issues: new access token + new refresh cookie

4. Logout (POST /api/v1/auth/logout)
   ├── Reads a valid Bearer access token when available and uses its stable `sid` claim
   ├── Deletes the exact dashboard session (scoped to the token subject)
   ├── Falls back to the refresh-token hash for legacy/header-only clients
   └── Clears refresh_token cookie (MaxAge: -1)

### Logout and access-JWT contract (Phase 11.1)

This project intentionally uses **bounded stateless access JWTs**:

- Logout revokes the refresh session and prevents future refresh continuation.
- The frontend clears its memory-only access token and authenticated query state.
- A previously issued access JWT is **not** added to a blacklist. It remains usable until its `exp` claim (default maximum residual validity: `AUTH_ACCESS_TOKEN_TTL`, normally 15 minutes).
- Logout therefore does **not** promise immediate revocation of a stolen/copyable access bearer token.
- User and merchant lifecycle checks still reject outstanding access JWTs on the next protected request when the user is disabled or the merchant is INACTIVE/SUSPENDED.
- The stable `sid` claim is not a secret; it lets logout remain session-row-safe if refresh rotation races with logout. A refresh that wins may return an access token, but the subsequent logout removes the same logical session row, so no new refresh can be obtained from it.
- A request that authenticates before logout may complete; Model A does not provide cancellation of in-flight requests.

This is a deliberate security trade-off: the HttpOnly refresh token limits continuation theft, while the short access-token lifetime bounds bearer-token exposure.

---

## Access Token Structure (JWT HS256)

Signed with HMAC-SHA256 using `AUTH_JWT_SECRET`.

```json
{
  "sub": "<merchant_user_uuid>",
  "mid": "<merchant_uuid>",
  "role": "OWNER",
  "iat": 1726123456,
  "exp": 1726124356,
  "jti": "<unique_token_id>",
  "sid": "<stable_dashboard_session_id>"
}
```

- `sub` — user UUID (primary identity)
- `mid` — merchant UUID (fast isolation without a DB lookup per request)
- `role` — current role at token issue time
- `jti` — prevents token reuse analysis attacks (unique per token)
- `sid` — stable logical dashboard-session ID; not a secret and not an access credential by itself

**Never include** password, password_hash, API keys, secrets, or provider credentials in JWT claims.

---

## Password Hashing

Passwords are hashed with **Argon2id** using the same parameters as the merchant API key system:

```
time    = 1
memory  = 64 MiB
threads = 4
keyLen  = 32 bytes
saltLen = 16 bytes (random, per-hash)
```

Stored format (PHC-like):
```
$argon2id$v=19$m=65536,t=1,p=4$<salt_base64>$<hash_base64>
```

**Security invariants:**
- Plaintext password is never stored, logged, or returned
- Password hash is never returned in any API response
- `verifyPassword` is constant-time (prevents timing oracle attacks)
- Dummy Argon2id verify runs on unknown-email to prevent user enumeration via response latency

---

## Refresh Token Security

- Generated as 32 cryptographically random bytes → hex-encoded (64 chars)
- Never stored in plaintext — only its **SHA-256 hex hash** is stored
- Delivered to client **once** via `HttpOnly; Secure; SameSite=Strict` cookie
- Cookie path is scoped to `/api/v1/auth` (not the entire site)
- Token rotation: each `/auth/refresh` call atomically replaces the old hash on the same logical session row; the row ID and `sid` remain stable

---

## Session Table (dashboard_sessions)

```sql
CREATE TABLE dashboard_sessions (
    id                  UUID        PRIMARY KEY,
    merchant_user_id    UUID        NOT NULL REFERENCES merchant_users(id) ON DELETE CASCADE,
    refresh_token_hash  VARCHAR(64) NOT NULL,   -- SHA-256 hex
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    last_used_at        TIMESTAMPTZ
);
```

Unique index on `refresh_token_hash` for O(1) lookup. Sessions expire after `AUTH_REFRESH_TOKEN_TTL` (default: 7 days). `DELETE` on user row cascades to sessions.

---

## User Roles

| Role | Allowed Operations |
|---|---|
| `OWNER` | Full access: user management, API keys, webhooks, payments, refunds, settlements, reconciliation |
| `ADMIN` | Operational access: payments, refunds, config, settlements, reconciliation. Cannot manage users |
| `VIEWER` | Read-only dashboard access |

Authorization is **server-side only**. Frontend UI hiding is supplementary, not a security control.

---

## User Status

| Status | Behaviour |
|---|---|
| `ACTIVE` | Normal authentication |
| `DISABLED` | Login rejected (401); refresh sessions revoked immediately on disable |

Status check happens:
1. At login (after password verification; returns `401 USER_DISABLED`)
2. At every authenticated request (middleware re-loads user from DB)
3. On status change to `DISABLED` (all `dashboard_sessions` for that user are deleted)
4. At refresh token use (disabled user's remaining session is deleted)

A disabled user whose access token has not yet expired will be rejected at the next authenticated request because the middleware loads the user from DB on each request.

---

## Merchant Isolation

Every `MerchantUser` row carries a `merchant_id` FK. The middleware stores the full user object (including `merchant_id`) in the Gin context.

Every dashboard-protected endpoint that accesses business data **must** use the caller's `merchant_id` from the authenticated context — never from request body, query params, or URL path when the caller's identity has already been established.

The `UpdateUserStatus` service method enforces this:
```go
if target.MerchantID != callerMerchantID {
    return nil, ErrCrossmerchantAccess
}
```

Cross-merchant attempts return 404 (not 403) to avoid confirming that the target user exists.

---

## Email Uniqueness

Email uniqueness is **globally unique** (not per-merchant) for these reasons:

1. Auth lookup goes `email → user → merchant` in one index scan (no merchant discriminator needed at login time)
2. Prevents the same person accidentally owning seats under multiple merchants
3. Future password-reset flows do not need a merchant discriminator

The database enforces this with a `UNIQUE INDEX` on `merchant_users.email`.

---

## Security Considerations

### Enumeration prevention
- Login returns a generic `401 INVALID_CREDENTIALS` for both wrong password and unknown email
- A dummy Argon2id verification runs for unknown email to equalise latency
- Disabled user returns `401 USER_DISABLED` (same HTTP status as wrong password)
- Cross-merchant user access returns `404` (not `403`)

### Logging
- Passwords are **never logged** anywhere
- Tokens are **never logged** anywhere
- JWT secrets are **never logged** anywhere
- Email is logged at domain level only (`@example.com`) for aggregated monitoring

### Secret management
- `AUTH_JWT_SECRET` must come from environment/config
- In production, an empty `AUTH_JWT_SECRET` causes startup failure
- In development, a warning is emitted and an insecure default is used
- JWT secret must not be the same as `ADMIN_API_KEY`, `DB_PASSWORD`, `MIDTRANS_SERVER_KEY`, or any other credential

### Rate limiting
Login endpoint brute-force protection is **not yet implemented** at the application level. This is a production hardening requirement:

- TODO: Implement per-IP or per-email rate limiting on `POST /api/v1/auth/login`
- Mitigation until then: infrastructure-level rate limiting (nginx, cloud load balancer, WAF)

### CORS
- `Access-Control-Allow-Origin` is set to explicit origins only (never `*`)
- `Access-Control-Allow-Credentials: true` is only set for allowed origins
- Wildcard + credentials is forbidden (browsers reject it anyway)
- Configure via `CORS_ALLOWED_ORIGINS=http://localhost:5173`

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `AUTH_JWT_SECRET` | — | JWT signing secret. Required in production. Generate with `openssl rand -hex 32` |
| `AUTH_ACCESS_TOKEN_TTL` | `15m` | Access token lifetime |
| `AUTH_REFRESH_TOKEN_TTL` | `168h` | Refresh token / session lifetime (7 days) |
| `CORS_ALLOWED_ORIGINS` | `""` | Comma-separated allowed origins. Empty = no CORS |

---

## Limitations (Production Hardening TODO)

1. **No login rate limiting** — must be added before production
2. **No password reset / forgot password** — not in Phase 8 scope
3. **No email verification** — accounts are created by admin bootstrap only
4. **No MFA / TOTP** — not in Phase 8 scope
5. **No session listing / revoke-all** — infrastructure exists (`DeleteByUserID`), UI not wired
6. **No automatic session cleanup** — `DeleteExpired()` exists but no background worker yet
7. **No audit log** — login/logout events are structured-logged but not persisted to DB
8. **Admin bootstrap only** — no self-service registration (intentional design for Phase 8)
