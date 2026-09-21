# Merchant API Key Lifecycle (Phase 5C)

This document describes the production merchant API key system introduced in
Phase 5C, including authentication migration from the Phase 1 legacy credential.

## Why merchant API keys exist

Phase 1 merchants authenticate with a single long-lived public API key stored on
the `merchants` table (`api_key`). That works for early development, but
production merchants need:

* Multiple named keys (e.g. staging vs production)
* Secure secret hashing (Argon2id)
* Revocation without deleting the merchant
* Rotation without downtime ambiguity
* Optional expiration
* Auditable `last_used_at`

Phase 5C adds a dedicated `merchant_api_keys` table and management API while
**keeping legacy `merchants.api_key` authentication fully working**.

## Key structure

A Phase 5C credential is a compound value:

```text
<key_id>:<secret>
```

Example shape (placeholders only):

```text
pk_<40-hex-chars>:sk_<64-hex-chars>
```

| Component | Stored? | Purpose |
|-----------|---------|---------|
| `key_id` (`pk_…`) | Yes (plaintext) | Efficient DB lookup |
| `secret` (`sk_…`) | **Never** — only Argon2id hash | Credential proof |
| `secret_hash` | Yes | Verification only |

Entropy:

* `key_id`: 160 bits (`crypto/rand`)
* `secret`: 256 bits (`crypto/rand`)

## Creation

```http
POST /api/v1/merchants/{id}/api-keys
X-API-Key: <legacy-or-existing-key>
Content-Type: application/json

{
  "name": "Production Backend",
  "expires_at": "2027-09-14T00:00:00Z"
}
```

`expires_at` is optional. If provided it must be an RFC3339 timestamp in the future.

**Authorization**: the authenticated merchant must match path `{id}`
(same merchant UUID parameter as `GET /api/v1/merchants/{id}`).

### One-time secret

The create response includes `secret` **exactly once**:

```json
{
  "success": true,
  "data": {
    "id": "uuid",
    "merchant_id": "uuid",
    "name": "Production Backend",
    "key_id": "pk_…",
    "secret": "sk_…",
    "status": "ACTIVE",
    "expires_at": null,
    "created_at": "2026-09-14T03:00:00Z"
  },
  "meta": { "request_id": "req_xxx" }
}
```

After creation:

* `GET` / `LIST` never return `secret` or `secret_hash`
* The database never stores plaintext `secret`
* Logs never include the secret or full `X-API-Key`

Store the returned credential as:

```text
X-API-Key: pk_…:sk_…
```

## Authentication

Protected endpoints continue to use the `X-API-Key` header.

### Migration behavior (two-stage)

```text
X-API-Key
   │
   ├─ looks like "pk_…:sk_…"  →  new merchant_api_keys auth
   │                              (lookup key_id → verify Argon2id →
   │                               check ACTIVE + not expired →
   │                               resolve merchant → update last_used_at)
   │
   └─ otherwise (including bare legacy "pk_…")
                                  →  legacy merchants.api_key auth
```

Important:

* Legacy Phase 1–4 keys are bare `pk_<hex>` values **without** a `:sk_` segment.
* Compound credentials that fail verification return `401 INVALID_API_KEY`
  immediately — they do **not** fall through to legacy auth.
* Auth failures are intentionally generic (no “revoked / expired / unknown”
  distinction on the public auth path).

Example:

```bash
curl http://localhost:8081/api/v1/payments \
  -H "X-API-Key: <MERCHANT_API_KEY>"
```

Use a placeholder in docs and local notes — never commit real secrets.

## Expiration

When `expires_at` is set and the current time is past it, authentication fails
with the same generic `401 INVALID_API_KEY` as any other invalid credential.

Management endpoints still list expired keys so merchants can see and rotate them.

## Revocation

```http
DELETE /api/v1/merchants/{id}/api-keys/{key_id}
```

Soft revoke:

```text
ACTIVE → REVOKED
```

Sets `revoked_at`. The record is retained. Immediately after commit, the
credential can no longer authenticate.

Revoking an already-revoked key returns `409 API_KEY_ALREADY_REVOKED`.

## Rotation

```http
POST /api/v1/merchants/{id}/api-keys/{key_id}/rotate
```

Atomic transaction policy:

```text
BEGIN
  revoke old key (ACTIVE → REVOKED)
  insert new ACTIVE key (new key_id + new secret_hash)
COMMIT
```

After rotation:

* Old credential → immediately invalid
* New `secret` returned **once** in the rotate response
* Name and `expires_at` are inherited from the old key

There is **no** dual-active grace period by default. Clients must switch to the
new credential as soon as rotation succeeds.

## Listing

```http
GET /api/v1/merchants/{id}/api-keys
```

Returns safe metadata only (`key_id`, status, timestamps). Never `secret` or
`secret_hash`.

## Legacy credential compatibility

| Credential | Format | Still works? |
|------------|--------|--------------|
| Phase 1 merchant `api_key` | bare `pk_<hex>` | Yes |
| Phase 5C API key | `pk_<hex>:sk_<hex>` | Yes |

Phase 5C does **not**:

* Delete or invalidate `merchants.api_key`
* Auto-migrate all merchants onto the new table
* Require merchants to rotate immediately

Merchants can keep using the legacy key while adopting named keys gradually.
Bootstrapping the first Phase 5C key is typically done while authenticated with
the legacy key.

## Merchant isolation

All management operations are scoped to the authenticated merchant:

* Create / list / revoke / rotate require path `{id}` == auth merchant
* Cross-merchant attempts return `403 FORBIDDEN` (path mismatch) or
  `404 API_KEY_NOT_FOUND` (foreign key id under own merchant scope)

## Security considerations

* Randomness: `crypto/rand` only
* Hashing: Argon2id with per-secret salt (`golang.org/x/crypto/argon2`)
* Verification: constant-time compare of derived hashes
* Never log secrets, hashes, or full `X-API-Key` values
* `last_used_at` is best-effort metadata — update failure does not fail auth
  and never creates an auth bypass

## Example curl flow

```bash
# 1. Create merchant (returns legacy api_key)
curl -s http://localhost:8081/api/v1/merchants \
  -H 'Content-Type: application/json' \
  -d '{"name":"Merchant Alpha","code":"ALPHA01"}'

# 2. Create a named API key (authenticate with legacy key)
curl -s http://localhost:8081/api/v1/merchants/<MERCHANT_ID>/api-keys \
  -H "X-API-Key: <LEGACY_API_KEY>" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Production Backend"}'

# 3. Call payments with the new compound credential
curl -s http://localhost:8081/api/v1/payments \
  -H "X-API-Key: <KEY_ID>:<SECRET>"

# 4. List keys (secret absent)
curl -s http://localhost:8081/api/v1/merchants/<MERCHANT_ID>/api-keys \
  -H "X-API-Key: <LEGACY_API_KEY>"

# 5. Rotate
curl -s -X POST \
  http://localhost:8081/api/v1/merchants/<MERCHANT_ID>/api-keys/<KEY_UUID>/rotate \
  -H "X-API-Key: <LEGACY_API_KEY>"

# 6. Revoke
curl -s -X DELETE \
  http://localhost:8081/api/v1/merchants/<MERCHANT_ID>/api-keys/<KEY_UUID> \
  -H "X-API-Key: <LEGACY_API_KEY>"
```

## Status values

| Status | Meaning |
|--------|---------|
| `ACTIVE` | Usable for authentication (if not expired) |
| `REVOKED` | Soft-revoked; authentication rejected |

## Error codes (management)

| Code | HTTP | When |
|------|------|------|
| `API_KEY_NOT_FOUND` | 404 | Unknown / wrong-merchant key id |
| `API_KEY_ALREADY_REVOKED` | 409 | Revoke/rotate on revoked key |
| `VALIDATION_ERROR` | 400 | Invalid body / past `expires_at` |
| `FORBIDDEN` | 403 | Path merchant ≠ authenticated merchant |
| `INVALID_API_KEY` | 401 | Missing/invalid auth credential |
