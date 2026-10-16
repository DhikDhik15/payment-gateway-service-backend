# Development-Only Account Recovery (No SMTP)

Local password recovery for developers when SMTP email delivery is
unavailable. **Disabled by default. Never enable in production.**

## When to use it

- You have a local dashboard account but cannot sign in, and the normal
  forgot-password email (`POST /api/v1/auth/password-reset/request` →
  `{DASHBOARD_BASE_URL}/reset-password?token=…`) never arrives because SMTP
  is unreachable in your development environment.
- You need to set a new password directly, without waiting for delivery.

## When NOT to use it

- Never in production, staging, or any shared environment.
- Never as a substitute for the production forgot-password flow.
- Never expose the recovery key to the frontend codebase, URLs, logs, or chat.

## How it works

- `GET /api/v1/auth/dev-recovery/status` → `{ "success": true, "data": { "enabled": true|false } }`.
  Public capability signal so the UI can decide whether to show the
  development controls. Carries no secrets.
- `POST /api/v1/auth/dev-recovery/recover` with header
  `X-Dev-Recovery-Key: <key>` and body
  `{ "email": "owner@example.com", "new_password": "…", "confirm_password": "…" }`.
  - Authorization is the header key only (constant-time compare). An email
    address alone is never sufficient.
  - Password policy is the existing rule (8–128 characters); hashing is the
    existing Argon2id service.
  - Only the identified account is updated; its refresh sessions are revoked.
  - Success returns `{ "message": "Password has been reset. …", "email": "…" }`
    with no hashes, tokens, or secrets. Sign in at login with the new password.

## Enable locally

1. Generate a local-only key (never commit it):
   ```
   export DEV_RECOVERY_KEY="$(openssl rand -hex 32)"
   ```
2. Start the backend with **all** of the following:
   ```
   APP_ENV=development \
   DEV_RECOVERY_ENABLED=true \
   DEV_RECOVERY_KEY="$DEV_RECOVERY_KEY" \
   APP_BIND_HOST=127.0.0.1 \
   APP_PORT=8080 \
   go run ./cmd/server
   ```
   Startup fails unless every value is present and correct:
   - `DEV_RECOVERY_KEY` ≥ 32 characters, no default.
   - `APP_BIND_HOST=127.0.0.1` (loopback-only listener, enforced).
3. Confirm the capability signal:
   ```
   curl http://127.0.0.1:8080/api/v1/auth/dev-recovery/status
   # {"success":true,"data":{"enabled":true},"meta":{...}}
   ```
4. Recover the account:
   ```
   curl -X POST http://127.0.0.1:8080/api/v1/auth/dev-recovery/recover \
     -H 'Content-Type: application/json' \
     -H "X-Dev-Recovery-Key: $DEV_RECOVERY_KEY" \
     -d '{"email":"owner@example.com","new_password":"new-strong-pass-1","confirm_password":"new-strong-pass-1"}'
   ```
5. Sign in with the new password at `POST /api/v1/auth/login`.

Frontend integration (no secret in code): the UI calls `GET …/status`; when
`enabled` is true it shows an additional "Development recovery" section on
the Forgot Password page that collects the key (password input, memory only —
never localStorage/sessionStorage/cookies), the account email, and the new
password + confirmation, then POSTs as above. The key field is cleared from
memory after the request. Production builds never render these controls.

## Disable

```
DEV_RECOVERY_ENABLED=false   # default
# and unset the key:
unset DEV_RECOVERY_KEY
```

Restart the backend. `GET …/status` then reports `{"enabled":false}` and
`POST …/recover` returns `404 RESOURCE_NOT_FOUND` (indistinguishable from a
missing route).

## Why it must never be enabled in production

- It resets a password with a single shared local secret instead of mailbox
  ownership proof. Any holder of the key can take over any eligible account.
- `config.Load` rejects `DEV_RECOVERY_ENABLED=true` when
  `APP_ENV=production` (and when `APP_ENV` is anything other than
  `development`), and the service re-checks `production` on every call.
- Enabling it also forces `APP_BIND_HOST=127.0.0.1`, which is incompatible
  with real deployments.

## Production recovery flow (unchanged)

- `POST /api/v1/auth/password-reset/request {email}` always returns the same
  generic message; only eligible accounts (ACTIVE user, ACTIVE merchant) get
  a single-use opaque token (SHA-256 hash stored, `PASSWORD_RESET_TOKEN_TTL`,
  default 1h) plus a `PASSWORD_RESET` email-outbox row for async SMTP delivery.
- `POST /api/v1/auth/password-reset/confirm {token,password}` consumes the
  token, updates the Argon2id hash, revokes other reset tokens and refresh
  sessions. No session is established.
- Rate limiting (per-IP + per-account) and anti-enumeration behavior are
  untouched by the development endpoint.

## Operational limitations

- No frontend pages ship in this repository (backend only); the dashboard UI
  guidance above is a contract for the external React app, verified by the
  `status` capability endpoint and handler tests, not by checked-in `.tsx`.
- Recovery targets one account per request; no bulk reset.
- Only ACTIVE users of ACTIVE merchants are eligible (same rule as the
  production flow).
- Already-issued access JWTs remain valid until their short TTL (no JWT
  blacklist by design); refresh sessions are revoked immediately.
- Never print the key, passwords, or hashes; server logs contain only
  `user_id`/`merchant_id`.
