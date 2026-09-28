# Phase 8D.5 — Security Audit Logging

## Scope

Phase 8D.5 adds a durable security audit trail for authentication boundaries and
security-sensitive state changes. It is intentionally not a general application
logging framework. Operational `slog` messages remain operational logs; the
records in `audit_logs` are the tenant-aware security history.

## Storage

Migration `000018_create_audit_logs` creates `audit_logs` with:

- server-generated UUID `id`;
- nullable `merchant_id` for tenant events and global/admin events;
- nullable `actor_user_id` (the authenticated dashboard user, when one exists);
- constrained `actor_type` (`DASHBOARD_USER`, `ADMIN`, `API_KEY`, `SYSTEM`, or
  `UNAUTHENTICATED`);
- stable `action`, optional `target_type`, and optional `target_id`;
- bounded `request_id` and the `ClientIP()` value selected by the existing
  trusted-proxy configuration;
- structured JSONB `metadata`, limited to 64 KiB and required to be an object;
- database-generated `created_at`.

The table intentionally has no foreign keys to business rows. Audit history must
survive deletion of a merchant, user, invitation, or credential reference. The
application repository exposes insertion and tenant-scoped chronological reads
only; it exposes no update or delete operation. There is no audit UI or public
read endpoint in this phase.

Indexes support tenant history, actor investigation, action investigation, and
recent global events. No metadata GIN index is created because no metadata query
API exists.

## Event and actor policy

Implemented transactional security events are:

- `MERCHANT_CREATED`
- `MERCHANT_STATUS_CHANGED`
- `USER_CREATED`
- `USER_ROLE_CHANGED`
- `USER_STATUS_CHANGED`
- `PASSWORD_CHANGED`
- `INVITATION_CREATED`
- `INVITATION_ACCEPTED`
- `API_KEY_CREATED`
- `API_KEY_ROTATED`
- `API_KEY_REVOKED`
- `LEGACY_CREDENTIAL_MIGRATED`
- `LEGACY_CREDENTIAL_DISABLED`
- `WEBHOOK_CONFIG_CHANGED`

Failed shared-key admin authentication is recorded as
`ADMIN_AUTH_FAILED` with only a reason category and route path. It is outside a
business transaction and does not change the existing 401 response or
rate-limit behavior. Failed dashboard/API authentication is not expanded in this
phase; existing auth responses and logs remain unchanged.

Dashboard requests carry the database-loaded dashboard user ID. Admin requests
use `ADMIN` with a null user ID because the existing shared admin key has no
per-operator identity. API-key-authenticated requests use `API_KEY` with the
merchant tenant; the raw key and key hash are never copied into the event.
Public invitation acceptance uses `UNAUTHENTICATED` because the invitee has no
pre-existing identity.

Request IDs and IPs come from the existing request-ID middleware and Gin
`ClientIP()`. Forwarded headers are not parsed independently. The request logger
uses route templates rather than raw paths so invitation bearer tokens are not
written to ordinary access logs.

## Secret-safe metadata

Callers construct small explicit metadata maps. The audit package rejects:

- non-object JSON, invalid JSON, excessive nesting/key counts, and metadata over
  64 KiB;
- keys containing password, token, secret, authorization, cookie, body, header,
  signature, or plaintext terms (including common compound forms);
- unsafe nested keys; and
- obvious credential-shaped string values such as `whsec_...`, `sk_...`,
  bearer/basic authorization values, or `password=`, `token=`, and `secret=`
  assignments.

Business events store IDs, roles, statuses, safe reason categories, and a
webhook hostname where useful. They never store passwords, password hashes, API
keys, legacy credentials, credential hashes, refresh/session tokens,
invitation tokens or hashes, webhook secrets, cookies, authorization headers, or
request/response bodies. Webhook URLs are reduced to a hostname; query strings,
userinfo, ports, and paths are not copied.

## Failure and transaction policy

Security-sensitive successful mutations attach an event to the same PostgreSQL
transaction that performs the mutation. The audit insert occurs before COMMIT.
An audit validation or insert error therefore rolls back the business mutation,
including the Phase 8D.4 merchant-row/OWNER lock transactions and the Phase
8D.3 legacy credential transition.

A repeated legacy disable or a same-status merchant request is an idempotent
no-op and does not produce a false transition event. A failed admin
authentication is best-effort because it has no business transaction: the audit
error is logged using safe dimensions and the original 401 is preserved.

## Explicit phase boundaries

This phase does not add a generic audit read API/UI. It also does not turn
background delivery workers or session housekeeping into a second event
stream: invitation creation/acceptance and the requested security mutations are
covered, while invitation lazy-expiry, delivery retry/dead-letter transitions,
and session login/refresh/logout housekeeping remain represented by their
existing operational/domain records. Failed dashboard/API authentication is
not expanded beyond the explicitly required failed admin event. These are
intentional Phase 8D.5 boundaries, not silent replacements for the existing
security controls.

## Retention

No retention period is currently specified by the product/security policy.
Audit records are retained indefinitely. There is no cleanup worker and no
automatic deletion. A future retention decision must be a separate, controlled
operation; email-outbox retention values must not be reused for audit history.

## Operational verification

The normal test suite covers metadata validation and repository API shape.
PostgreSQL-backed tests run when `TEST_DATABASE_URL` is set and verify insert,
tenant-scoped reads, transaction rollback/commit, metadata limits, and the
Phase 8D.4 concurrency regressions. Migration verification should report
`schema_migrations.version=18` and `dirty=false` after applying migration
`000018`.
