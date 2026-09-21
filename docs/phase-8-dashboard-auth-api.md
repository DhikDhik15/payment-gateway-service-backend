# Phase 8 — Dashboard Auth API Contract

This document is the authoritative contract for the React TypeScript dashboard.

**Base URL:** `http://localhost:8080` (production: configured by deployment)

---

## Authentication Mechanism

The dashboard uses a **dual-token** design:

| Token | Transport | Lifetime | Storage |
|---|---|---|---|
| Access Token (JWT) | `Authorization: Bearer <token>` header | 15 minutes | JavaScript memory (do NOT use localStorage) |
| Refresh Token (opaque) | `HttpOnly; Secure; SameSite=Strict` cookie | 7 days | Managed by browser (automatic) |

The refresh token is automatically sent by the browser on requests to `/api/v1/auth/*` because the cookie path is `/api/v1/auth`. No JavaScript cookie access is needed or possible.

---

## Endpoints

### POST /api/v1/auth/login

Authenticate with email and password.

**Request headers:**
```
Content-Type: application/json
```

**Request body:**
```json
{
  "email": "owner@merchant.com",
  "password": "strongpassword"
}
```

- `email`: required, valid email format, normalised (case-insensitive)
- `password`: required

**Success response (200):**
```json
{
  "success": true,
  "data": {
    "access_token": "<jwt>",
    "token_type": "Bearer",
    "expires_in": 900,
    "user": {
      "id": "<uuid>",
      "merchant_id": "<uuid>",
      "email": "owner@merchant.com",
      "role": "OWNER",
      "status": "ACTIVE",
      "last_login_at": "2026-09-16T07:40:14.953Z",
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  },
  "meta": { "request_id": "req_abc123" }
}
```

A `Set-Cookie` response header will be present:
```
Set-Cookie: refresh_token=<opaque_hex>; Path=/api/v1/auth; HttpOnly; SameSite=Strict; Max-Age=604800
```

The cookie is set automatically by the browser for future `/api/v1/auth/*` requests.

**Error responses:**
```json
{ "success": false, "error": { "code": "INVALID_CREDENTIALS", "message": "Invalid email or password" }, "meta": {...} }
```

| Status | Code | Condition |
|---|---|---|
| 400 | `VALIDATION_ERROR` | Missing or invalid fields |
| 401 | `INVALID_CREDENTIALS` | Wrong email or password |
| 401 | `USER_DISABLED` | Account is disabled |
| 500 | `INTERNAL_ERROR` | Unexpected server error |

**React implementation note:**
- Store `access_token` in React state or a ref (not localStorage)
- Store `expires_in` to schedule a proactive token refresh
- Do not read or write the `refresh_token` cookie in JavaScript

---

### POST /api/v1/auth/logout

Revoke the current session. Always succeeds from the client's perspective.

**Request headers:**
```
Authorization: Bearer <access_token>   (optional, logout works without it)
```

The browser automatically sends the `refresh_token` HttpOnly cookie.

**Success response (200):**
```json
{
  "success": true,
  "data": {},
  "meta": { "request_id": "req_abc123" }
}
```

The response clears the cookie:
```
Set-Cookie: refresh_token=; Path=/api/v1/auth; HttpOnly; SameSite=Strict; Max-Age=-1
```

**React implementation note:**
- After logout: clear the access token from state
- Redirect to the login page
- Do not manually clear the cookie (handled by the server)

---

### GET /api/v1/auth/me

Returns the profile of the currently authenticated user.

**Request headers:**
```
Authorization: Bearer <access_token>
```

**Success response (200):**
```json
{
  "success": true,
  "data": {
    "id": "<uuid>",
    "merchant_id": "<uuid>",
    "email": "owner@merchant.com",
    "role": "OWNER",
    "status": "ACTIVE",
    "last_login_at": "2026-09-16T07:40:14.953Z",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:00Z"
  },
  "meta": { "request_id": "req_abc123" }
}
```

**Error responses:**

| Status | Code | Condition |
|---|---|---|
| 401 | `INVALID_CREDENTIALS` | Missing, invalid, or expired access token |
| 401 | `USER_DISABLED` | Account was disabled after token was issued |

---

### POST /api/v1/auth/refresh

Issue a new access token using the HttpOnly refresh cookie.

The browser sends the `refresh_token` cookie automatically. No request body is needed.

**Request headers:**
```
Content-Type: application/json
```

**Success response (200):** Same shape as login response (new access token + new cookie).

**Error responses:**

| Status | Code | Condition |
|---|---|---|
| 401 | `INVALID_CREDENTIALS` | Refresh token missing, invalid, or expired |

**React implementation note:**
- Call this endpoint proactively when the access token is about to expire (e.g. 1 minute before `expires_in`)
- On 401, redirect to login (session has expired)
- The new access token replaces the old one in state

---

### POST /api/v1/admin/merchants/:merchant_id/users

Create a dashboard user. **Admin bootstrap only — requires `X-Admin-Key` header.**

This endpoint is **not** for the React dashboard UI. It is used by operators to provision initial accounts.

**Request headers:**
```
X-Admin-Key: <admin_key>
Content-Type: application/json
```

**Request body:**
```json
{
  "email": "owner@merchant.com",
  "password": "strongpassword",
  "role": "OWNER"
}
```

- `role`: one of `OWNER`, `ADMIN`, `VIEWER`
- `password`: minimum 8 characters

**Success response (201):**
```json
{
  "success": true,
  "data": {
    "id": "<uuid>",
    "merchant_id": "<uuid>",
    "email": "owner@merchant.com",
    "role": "OWNER",
    "status": "ACTIVE",
    "created_at": "2026-09-16T07:40:14.953Z",
    "updated_at": "2026-09-16T07:40:14.953Z"
  },
  "meta": { "request_id": "req_abc123" }
}
```

The response never includes `password` or `password_hash`.

---

### GET /api/v1/dashboard/users

List all dashboard users for the authenticated user's merchant.

**Request headers:**
```
Authorization: Bearer <access_token>
```

All roles may access this endpoint. Results are automatically scoped to the caller's merchant — no merchant ID parameter is needed.

**Success response (200):**
```json
{
  "success": true,
  "data": [
    {
      "id": "<uuid>",
      "merchant_id": "<uuid>",
      "email": "owner@merchant.com",
      "role": "OWNER",
      "status": "ACTIVE",
      "last_login_at": "2026-09-16T07:40:14.953Z",
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  ],
  "meta": { "request_id": "req_abc123" }
}
```

---

### PATCH /api/v1/dashboard/users/:user_id/status

Change the status of a dashboard user. **OWNER role only.**

**Request headers:**
```
Authorization: Bearer <access_token>
Content-Type: application/json
```

**Request body:**
```json
{
  "status": "DISABLED"
}
```

- `status`: one of `ACTIVE`, `DISABLED`

**Success response (200):** Updated `DashboardUserResponse`.

**Error responses:**

| Status | Code | Condition |
|---|---|---|
| 400 | `INVALID_REQUEST` | Cannot disable own account |
| 401 | `INVALID_CREDENTIALS` | Not authenticated |
| 403 | `INSUFFICIENT_ROLE` | Caller is not OWNER |
| 404 | `DASHBOARD_USER_NOT_FOUND` | User not found or belongs to a different merchant |

---

## Error Response Shape

All errors follow the same envelope:

```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable description",
    "details": { "field": "Specific validation error" }
  },
  "meta": {
    "request_id": "req_abc123"
  }
}
```

### Common error codes

| Code | HTTP | Meaning |
|---|---|---|
| `VALIDATION_ERROR` | 400 | Missing/invalid request fields (details map included) |
| `INVALID_REQUEST` | 400 | Invalid business logic request |
| `INVALID_CREDENTIALS` | 401 | Authentication failed (generic — do not distinguish email/password) |
| `USER_DISABLED` | 401 | Account is disabled |
| `INSUFFICIENT_ROLE` | 403 | Caller lacks the required role |
| `DASHBOARD_USER_NOT_FOUND` | 404 | User not found |
| `EMAIL_ALREADY_EXISTS` | 409 | Email is already registered |
| `INTERNAL_ERROR` | 500 | Unexpected server error |

---

## 401 Behaviour

When a request returns 401:

1. **Missing token:** Show login page
2. **Expired token:** Try `POST /api/v1/auth/refresh`
   - If refresh succeeds → retry the original request with new access token
   - If refresh fails (401) → redirect to login
3. **USER_DISABLED:** Show a message "Your account has been disabled" and clear session

**React implementation pattern:**

```typescript
async function apiCall<T>(url: string, options: RequestInit): Promise<T> {
  const response = await fetch(url, {
    ...options,
    credentials: 'include',  // send refresh cookie automatically
    headers: {
      ...options.headers,
      'Authorization': `Bearer ${accessToken}`,
    },
  });

  if (response.status === 401) {
    const errorData = await response.json();
    if (errorData.error?.code === 'INVALID_CREDENTIALS') {
      // Try to refresh
      const refreshed = await tryRefreshToken();
      if (refreshed) {
        // Retry with new token
        return apiCall(url, options);
      }
    }
    // Redirect to login
    redirectToLogin();
  }

  return response.json();
}
```

---

## 403 Behaviour

A 403 with `INSUFFICIENT_ROLE` means the authenticated user does not have the required role for this operation.

- Show an "Access denied" message
- Do not attempt to retry
- The user's role is available in the `/auth/me` response

---

## User Roles

```typescript
type Role = 'OWNER' | 'ADMIN' | 'VIEWER';
```

| Role | User Management | API Keys | Payments | Refunds | Settlements |
|---|---|---|---|---|---|
| `OWNER` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `ADMIN` | ❌ | ❌ | ✅ | ✅ | ✅ |
| `VIEWER` | ❌ | ❌ | Read only | Read only | Read only |

---

## User Status

```typescript
type Status = 'ACTIVE' | 'DISABLED';
```

- `ACTIVE`: Normal access
- `DISABLED`: Login rejected. If already logged in, next authenticated request returns 401.

---

## Merchant Isolation

Every dashboard user belongs to exactly one merchant. The backend enforces this automatically:

- All data returned by dashboard endpoints is scoped to the authenticated user's merchant
- You never need to pass a `merchant_id` in dashboard API requests
- Attempting to access another merchant's data returns 404

---

## CORS Configuration

The backend supports CORS for the React dashboard via the `CORS_ALLOWED_ORIGINS` environment variable.

For local development:
```
CORS_ALLOWED_ORIGINS=http://localhost:5173
```

The backend responds with:
```
Access-Control-Allow-Origin: http://localhost:5173
Access-Control-Allow-Credentials: true
Access-Control-Allow-Headers: Content-Type, Authorization, X-Request-ID, X-Refresh-Token, X-Admin-Key
```

**React `fetch` configuration:**
```typescript
const response = await fetch('/api/v1/auth/login', {
  method: 'POST',
  credentials: 'include',  // Required for the refresh_token cookie to be sent/received
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ email, password }),
});
```

`credentials: 'include'` is required so that the browser sends and receives the `Set-Cookie` header for the HttpOnly refresh token.

---

## TypeScript Types

```typescript
// User profile
interface DashboardUser {
  id: string;
  merchant_id: string;
  email: string;
  role: 'OWNER' | 'ADMIN' | 'VIEWER';
  status: 'ACTIVE' | 'DISABLED';
  last_login_at?: string | null;  // ISO 8601
  created_at: string;
  updated_at: string;
}

// Login response
interface LoginResponse {
  access_token: string;
  token_type: 'Bearer';
  expires_in: number;   // seconds until access token expires
  user: DashboardUser;
}

// Standard API envelope
interface ApiSuccess<T> {
  success: true;
  data: T;
  meta: { request_id: string };
}

interface ApiError {
  success: false;
  error: {
    code: string;
    message: string;
    details?: Record<string, string>;
  };
  meta: { request_id: string };
}
```

---

## Session Lifecycle Diagram

```
User enters email+password
        │
        ▼
POST /api/v1/auth/login
        │
        ├── 401 → show error
        │
        └── 200 → store access_token in memory
                   browser stores refresh_token cookie (HttpOnly)
                        │
                        ▼
               Subsequent API calls
               Authorization: Bearer <access_token>
                        │
                        ├── 200 → normal operation
                        │
                        └── 401 (expired)
                                │
                                ▼
                        POST /api/v1/auth/refresh
                        (browser sends cookie automatically)
                                │
                                ├── 200 → new access_token, retry
                                │
                                └── 401 → redirect to login
                                          clear access_token from state
                                          server cleared cookie on next request
```
