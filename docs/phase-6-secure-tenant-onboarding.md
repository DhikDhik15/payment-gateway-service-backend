# Secure Tenant Onboarding Foundation

Atomic admin-only provisioning of a new SaaS tenant (merchant).

> Note: outbound merchant webhooks are documented separately in `merchant-webhooks.md`.
> This document covers **tenant onboarding**, not webhook delivery.

## Endpoint

```
POST /api/v1/admin/onboarding/merchants
Header: X-Admin-Key: <ADMIN_API_KEY>
```

Requires the existing platform admin key (`AdminAuth`). Merchant API keys and dashboard JWTs are rejected.

## Request

```json
{
  "name": "Toko Maju",
  "code": "TOKO001",
  "owner_email": "owner@tokomaju.example",
  "owner_password": "secure-password"
}
```

`merchant_id` is never accepted from the client. The OWNER and API credential are always bound to the merchant created inside the same transaction.

## Success response (201)

```json
{
  "success": true,
  "data": {
    "merchant": {
      "id": "...",
      "name": "Toko Maju",
      "code": "TOKO001",
      "status": "ACTIVE",
      "created_at": "..."
    },
    "owner": {
      "id": "...",
      "email": "owner@tokomaju.example",
      "role": "OWNER"
    },
    "api_credential": {
      "id": "...",
      "key_id": "pk_...",
      "secret": "sk_...",
      "name": "Initial API Key",
      "status": "ACTIVE",
      "created_at": "..."
    }
  },
  "meta": { "request_id": "..." }
}
```

**One-time secret:** `api_credential.secret` is returned only in this response. Store it securely. It is never stored plaintext and cannot be retrieved again.

Password / password hashes are never returned.

## Transaction boundary

Inside one PostgreSQL transaction:

1. `INSERT` into `merchants` (includes legacy `api_key` + hashed `api_secret` for Auth middleware compatibility)
2. `INSERT` into `merchant_users` (role = `OWNER`)
3. `INSERT` into `merchant_api_keys` (Phase 5C credential)

Any failure → `ROLLBACK`. No partial tenant.

Does **not** create webhook configs, provider accounts, or call external APIs.

## Credential model

| Credential | Purpose |
|------------|---------|
| Legacy `merchants.api_key` / hashed `api_secret` | Backward-compatible Auth middleware stage-2 lookup. Legacy plaintext secret is discarded at creation (same as `POST /api/v1/merchants`). |
| Phase 5C `merchant_api_keys` (`pk_…` / `sk_…`) | Primary merchant API credential for new tenants. Secret returned once in onboarding response. |

## Errors

| Status | Code | When |
|--------|------|------|
| 400 | `VALIDATION_ERROR` | Invalid body / email |
| 401 | `ADMIN_UNAUTHORIZED` | Missing/invalid `X-Admin-Key` |
| 409 | `DUPLICATE_MERCHANT_CODE` | Merchant code taken |
| 409 | `EMAIL_ALREADY_EXISTS` | Owner email taken (global unique) |
| 503 | `ADMIN_NOT_CONFIGURED` | `ADMIN_API_KEY` empty |

## Backward compatibility: `POST /api/v1/merchants`

Previously unauthenticated. Now requires `X-Admin-Key` and remains a **low-level merchant-row create** (no OWNER, no Phase 5C key).

Prefer `POST /api/v1/admin/onboarding/merchants` for full tenant provisioning.

## Follow-up (out of scope here)

- Protect or redesign `GET /api/v1/merchants/:id` (currently public; returns non-secret fields only)
- Merchant lifecycle (suspend/activate)
- Sandbox/production isolation
- Per-tenant provider credentials
- SaaS billing / subscription
- Self-service team invitation

## Manual verification

```bash
# Unauthorized
curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST http://localhost:8080/api/v1/admin/onboarding/merchants \
  -H 'Content-Type: application/json' \
  -d '{"name":"A","code":"A1","owner_email":"a@ex.com","owner_password":"password1"}'
# expect 401

# Authorized
curl -s -X POST http://localhost:8080/api/v1/admin/onboarding/merchants \
  -H "Content-Type: application/json" \
  -H "X-Admin-Key: $ADMIN_API_KEY" \
  -d '{"name":"Merchant A","code":"MERCHA","owner_email":"a@ex.com","owner_password":"password1"}'
```

Verify DB rows in `merchants`, `merchant_users`, and `merchant_api_keys` share the same `merchant_id`.

Provider credentials remain global/process-level (intentional Phase boundary).
