# Phase 11.1 — Logout and Access-JWT Semantics

## Decision

This project uses **Model A: bounded stateless access JWTs**.

Logout invalidates the refresh session and prevents refresh continuation. It does not add an access-JWT blacklist, so an already-issued access JWT remains usable until its `exp` claim.

The default maximum residual access-token validity after logout is `AUTH_ACCESS_TOKEN_TTL` (15 minutes). Deployments may configure a different positive duration, and that configured duration is the security bound.

This is intentional and matches the original Phase 8 authentication design and the existing Phase 7/8 security audits. The access token is a short-lived bearer credential; the refresh token is the continuation credential stored in an HttpOnly cookie.

## Guarantees

Logout guarantees:

1. The current dashboard path revokes the logical dashboard session row.
2. The refresh cookie is cleared by the server.
3. A new access token cannot be obtained through the current dashboard's revoked refresh session.
4. The frontend clears its memory-only access token and authenticated query/cache state.
5. Repeated logout is safe and idempotent.

Logout does **not** guarantee:

1. Immediate rejection of a previously issued access JWT.
2. Cancellation of a request that already authenticated or is in flight.
3. Revocation of a copied bearer token before its natural `exp`.

User and merchant lifecycle checks are separate and remain immediate: a disabled user or INACTIVE/SUSPENDED merchant is rejected on the next protected request even while an old access JWT is within its TTL.

## Stable session identity

New access JWTs contain a non-secret `sid` claim identifying the logical `dashboard_sessions` row. The `sid`:

- remains stable across refresh-token rotation;
- is not an authorization source by itself;
- is accepted only inside a correctly signed access JWT;
- is paired with the signed subject when the session is deleted.

The dashboard sends its memory-only access bearer on logout. The backend uses the signed `sid` plus subject for a user-scoped atomic session delete. A legacy/header-only caller can still use the refresh-hash path, but that compatibility path cannot identify a session after its old hash has already rotated.

No schema migration is required: the existing `dashboard_sessions.id` and `merchant_user_id` columns are sufficient.

## Concurrency semantics

| Scenario | Contract |
|---|---|
| Request vs logout | A request that reaches middleware after logout may still authenticate while the user/merchant remain ACTIVE, because Model A does not consult access-JWT revocation. A request already in a handler is not cancelled. |
| Concurrent logout | Both requests may return success; one deletes the row and the other observes an already-revoked session. |
| Refresh vs logout | With the current dashboard's stable-sid path, if logout linearizes first, refresh fails and issues no replacement. If refresh linearizes first, logout deletes the same stable session row, so the returned access token has no usable refresh continuation. A legacy hash-only caller cannot identify a row after its old hash has already rotated. |
| Logout then refresh | The invalidated refresh token fails with `401`. |
| Logout then old access JWT | The old JWT remains accepted until `exp`, then fails as expired. |
| Logout then new access JWT | A token issued before logout remains bounded by `exp`; a token cannot be obtained after logout through the revoked session. |
| Disabled user / inactive merchant | Existing database lifecycle checks reject old access JWTs on the next protected request and refresh rotation fails. |
| Password change | All refresh sessions are revoked; already-issued access JWTs follow the same bounded Model A rule. |

## Security properties preserved

- Access tokens remain memory-only in the browser.
- Refresh tokens remain HttpOnly and are never read by JavaScript.
- Logout does not log bearer tokens, refresh tokens, hashes, or session IDs.
- User/merchant IDs used for session deletion come from the signed token and existing database key relationship, never from a request body or tenant selector.
- PostgreSQL refresh CAS rotation remains unchanged.
- The dashboard uses a module-memory auth transition epoch so a delayed refresh response cannot repopulate state after logout, terminal 401, or account switching.
- No Redis, Kafka, global JWT blacklist, or new distributed dependency was added.
