# Phase 8D.6 — Final Security Regression and Release Hardening

## Scope and baseline

Phase 8D.6 is a regression and release gate for Phases 7 through 8D.5. It does
not introduce MFA, password reset, Redis/Kafka, a new provider architecture,
an audit platform, or unrelated refactors.

The baseline was recorded before 8D.6 changes:

- branch: `master`
- commit: `578d4be migrations: add database migrations for Phases 4 through 14`
- worktree: extensive pre-existing modified and untracked Phase 7–8D.5 work
- latest migration before this phase: `000018`
- development database: `version=18`, `dirty=false`, `audit_logs` present

No reset, checkout, stash, clean, or commit was performed. Existing untracked
files were preserved.

## Concrete 8D.6 hardening changes

The regression review found and fixed these concrete issues:

1. `APP_ENV` is normalized and restricted to `development`, `test`, or
   `production`; unknown or ambiguous values fail startup.
2. Production requires an explicit trusted-proxy policy (`none` or an IP/CIDR
   list); unset production proxy trust is no longer accepted.
3. Production rejects the known mock-webhook placeholder and requires a
   non-default secret of at least 32 bytes.
4. Production requires a JWT secret of at least 32 bytes and rejects short
   non-empty admin keys.
5. Production rejects `WEBHOOK_REQUIRE_HTTPS=false`.
6. Production Midtrans base URLs must be explicitly configured absolute HTTPS
   URLs; the development sandbox fallback is rejected.
7. When email is enabled, production invitation dashboard URLs must use HTTPS.
8. Expiry worker intervals and batch sizes must be positive.
9. Docker Compose no longer hardcodes HTTP webhook mode and now passes payment
   provider, mock-secret, and webhook-delivery settings from the environment.
10. Simulator transition failures no longer return or log raw service/database
    errors to HTTP callers.
11. Client-supplied request IDs are normalized before logging, response
    echoing, audit correlation, and rate-limit context propagation.
12. The Swagger annotation for the admin-protected merchant GET now declares
    `AdminKeyAuth`; generated Swagger was regenerated successfully.
13. Merchant lifecycle transitions are revalidated under the PostgreSQL row
    lock; concurrent incompatible transitions have one winner.
14. Merchant suspension/deactivation, user disablement, and password changes
    revoke dashboard sessions in the same PostgreSQL transaction as the
    security mutation (with fail-closed compatibility behavior for other repos).
15. Midtrans webhook parsing now requires a transaction ID, positive integer
    amount, and valid three-letter currency; malformed signed events cannot
    bypass transaction amount/currency checks.
16. Production rejects the mock payment provider and registers only the selected
    provider webhook parser.
17. Production requires `DB_SSLMODE=verify-full` so the development plaintext
    database fallback cannot silently activate in production.
18. Startup and readiness now verify a clean schema at or beyond migration 18;
    the container healthcheck uses readiness rather than liveness alone.
19. Outbound webhook transport, secret-resolution, and request-build failures
    persist/log only stable diagnostic categories rather than raw internal text.

The corresponding regression tests are in:

- `internal/config/phase_8d6_security_test.go`
- `internal/handler/simulator_handler_test.go`

## Verified regression matrix

| Area | Evidence/result |
|---|---|
| Dashboard authentication | Login, invalid password, disabled user, inactive/suspended merchant, JWT expiry, logout, `/me`, and session lifecycle suites pass. |
| Merchant lifecycle concurrency | PostgreSQL regression test proves two competing ACTIVE→SUSPENDED/INACTIVE requests produce one success and one stable transition error; final state is never an invalid transition. |
| Session invalidation | PostgreSQL tests prove merchant suspension, user disablement, and password change remove refresh sessions in the mutation transaction; compatibility paths fail closed on revoke errors. |
| Refresh rotation | PostgreSQL and race tests verify one winner among 20 concurrent refreshes, old-token rejection, replacement single-use, rollback, revocation, disabled user, and suspended merchant. |
| OWNER concurrency | PostgreSQL/race tests preserve at least one active OWNER for demotion, disable, and mixed races; cross-tenant mutations fail. |
| Team authorization | OWNER/ADMIN/VIEWER role/status/password/invitation/API-key policy and cross-tenant tests pass. |
| Merchant lifecycle | Active, suspended, inactive, session invalidation, reactivation behavior, and audit integration are covered by existing service/middleware/handler tests. |
| API credentials | Canonical hashed keys, one-time plaintext disclosure, revoke/rotate, list secrecy, and tenant isolation pass. |
| Legacy credentials | Window-off rejection, migration, compatibility, disablement, idempotency, concurrency, and uniform failure behavior pass. |
| Invitations | Token hashing, role policy, duplicate/expiry/replay, acceptance, atomic outbox enqueue, and token/log redaction pass. |
| Email outbox | Retry/permanent classification, stale recovery, concurrent workers, bounded errors, and terminal retention behavior pass. |
| Simulator | Disabled, anonymous, wrong-key, foreign-merchant, tenant read/write, terminal-state, production-disable, and raw-error regression tests pass. |
| SSRF | IPv4/IPv6 special ranges, mapped addresses, metadata/localhost, malformed hosts, mixed DNS answers, DNS rebinding, redirects, proxy bypass, TLS policy, and public destinations pass. |
| HTTP hardening | Declared oversized requests return 413; chunked valid oversized bodies are capped with stable validation errors; timeout/body configuration tests pass. |
| Rate limiting | Login, admin, API, independent keys, 429/Retry-After, account dimensions, and refresh loser behavior pass. The limiter remains in-process. |
| Inbound webhooks | Signature verification, malformed payload handling, duplicate events, amount/currency checks, Midtrans strict amount/currency/transaction-ID validation, refund events, and safe error mapping pass. |
| Outbound webhooks | HTTPS policy, SSRF, DNS rebinding, redirect refusal, timeout, bounded/sanitized response diagnostics, retry/dead-letter behavior, and secret non-leakage pass. |
| Payments/idempotency | Payment state/tenant rules, same-key replay, modified-body conflict, concurrent reservation, provider-once behavior, refunds, and handler mappings pass. |
| Audit | Event coverage, actor/request/IP capture, rollback coupling, concurrent OWNER audit, tenant isolation, metadata limits, and secret scans pass. |

## Database and runtime verification

### Clean migration

A disposable PostgreSQL 16 container was migrated from `000001` through
`000018` using the repository migration tool. Result:

```text
version=18
dirty=false
audit_logs=present
audit indexes=5
audit check constraints=4
```

### Existing development database

Verified without changing migration state:

```text
schema_migrations.version=18
schema_migrations.dirty=false
audit_logs=present
```

Sensitive-column checks found no API-key rows with missing hashes, no refresh
sessions with non-SHA-256-length hashes, and no invitation rows with missing or
non-SHA-256-length token hashes. Legacy plaintext columns remain only for
compatibility and are not created for new merchants.

### Docker and endpoints

- `docker compose config --quiet`: pass
- `docker compose up -d --build`: pass
- migration container: exit 0
- application container: healthy
- `/health`: HTTP 200
- `/health/ready`: HTTP 200
- runtime image: non-root user, static binary, CA/timezone data
- startup and readiness both reject a missing, old, or dirty migration state
- production configuration rejects `DB_SSLMODE=disable`, mock providers, weak secrets, unset proxy trust, and HTTP webhook mode

## Original finding reconciliation

| ID | Finding | Original severity | Current status | Evidence | Residual risk | Release blocker |
|---|---|---:|---|---|---|---|
| F1 | Simulator exposure | High | Fixed, deployment-conditional | `config.Load`, simulator allowlist, authenticated tenant scoping, production rejection, provider-parser containment, and simulator tests | Development deployments may explicitly enable it; production mock payment provider is rejected | No |
| F2 | SSRF / DNS rebinding | Critical | Fixed | `internal/ssrf`, guarded dialer, redirect refusal, proxy disablement, TLS preservation, SSRF tests | Live Internet DNS is not used in unit tests | No |
| F3 | Hardcoded webhook encryption key | High | Fixed for production | Production key is required; development fallback is explicit and rejected in production | Secret manager/rotation is still an operational responsibility | No |
| F4 | Plaintext legacy credentials | High | Fixed for new issuance; compatibility retained | Creation frozen, hashed Phase 5C keys, production legacy default closed, migration/disable tests | Existing legacy rows remain until migrated/disabled | No |
| F5 | Development Docker configuration | High | Partially fixed | Compose no longer hardcodes HTTP webhook mode, passes provider/security settings, and production config fails closed for required values; strict APP_ENV/secret/proxy/provider/DB-TLS checks added | TLS terminator and production secret injection remain deployment prerequisites | No, conditional on documented production deployment |
| F6 | Missing rate limiting | High | Fixed for defined login/admin/API surfaces | Login/admin/API limiters, independent keys, 429 and Retry-After tests | In-process only; inbound provider webhooks are not separately limited | No |
| F7 | Missing body limit | High | Fixed | Global declared/chunked body cap and binding tests | Limits are configuration-controlled and must remain positive | No |
| F8 | Last OWNER race | Critical | Fixed | PostgreSQL row-lock/count transaction and race tests | Audit adds only a same-transaction insert | No |
| F9 | Missing audit trail | High | Fixed | `audit_logs`, centralized service, covered mutation events, rollback/isolation tests | No read UI/API; retention/DB immutability remain follow-up | No |
| F10 | Webhook response persistence | High | Fixed for outbound diagnostics | 8 KiB cap, printable sanitization, stable transport/secret error categories, SSRF-safe errors, strict Midtrans amount/currency validation, and tests | Provider webhook payload/signature tables intentionally retain provider evidence | No |
| F11 | Simulator raw errors | Medium | Fixed in 8D.6 | Simulator returns a stable message and logs only a safe failure category; regression test rejects SQLSTATE leakage | Detailed diagnostics are intentionally not exposed through simulator logs/responses | No |
| F12 | Environment parsing | High | Fixed/strengthened | Strict legacy parsing, normalized APP_ENV, explicit production proxy/HTTPS/secret checks | Some non-critical boolean settings still use compatibility fallback parsing | No |
| F13 | Refresh rotation non-atomicity | Critical | Fixed | Atomic in-place CAS/session transaction, concurrent refresh tests, and lifecycle/password session-revocation transactions | No refresh-token family/reuse-history detection | No |
| F14 | Password policy | Medium | Accepted risk | Existing 8–128 character validation and Argon2id storage | No complexity/breach-list policy | No |
| F15 | API-key auth DoS amplification | Medium | Mitigated | Argon2id verification plus API IP/merchant rate limits | In-process limits and single-node deployment remain assumptions | No |
| F16 | Assorted lower-priority findings | Mixed | Partially fixed/reconciled | Prior phase tests, lifecycle/session transaction tests, provider validation, startup/readiness schema gate, and this release sweep | Swagger exposure, live SMTP/provider coverage, and some deployment hardening remain follow-up | No |

## Residual-risk register

| Risk | Severity | Mitigation | Release impact | Future action |
|---|---|---|---|---|
| In-process rate limiting | Medium | Fixed windows, IP/account/merchant keys, 429 behavior | Non-blocking; multi-instance deployments need per-instance review | Distributed limiter only if multi-instance scaling is required |
| No refresh-token family/reuse history | Medium | Atomic rotation and replay rejection | Non-blocking for current scope | Session-family design in a later roadmap phase |
| Audit retention/immutability/read access | Low–Medium | Append-only repository API, no update/delete endpoint, indefinite retention | Non-blocking; operational/forensic tradeoff | Decide retention, DB privilege model, authorized read API |
| Shared admin key has no operator identity | Low–Medium | ADMIN actor and audit failures are recorded without inventing identity | Non-blocking | Adopt real operator identity if required |
| API-key actor lacks key UUID | Low | Merchant-level attribution and target credential IDs | Non-blocking | Add verified key identity to audit context if needed |
| Password length-only policy | Medium | Argon2id, min/max validation, no plaintext persistence | Non-blocking unless policy requires complexity | Add complexity/breach controls separately |
| Argon2id authentication amplification | Medium | API rate limits and bounded request handling | Non-blocking under current deployment assumptions | Capacity/DoS testing and possibly distributed limiting |
| Public Swagger and deployment surface | Low–Medium | Documentation contains no secret values; runtime schema gate and Compose migration ordering are verified | Non-blocking if network/API exposure is controlled | Gate Swagger and restrict operational endpoints at the edge |
| Plain HTTP listener/TLS termination depends on deployment | High if misconfigured | Production config requires explicit proxy/HTTPS/DB-TLS settings; image remains non-root | Conditional deployment prerequisite, not a verified code bypass | Require TLS termination and a certificate-verified database endpoint in deployment policy |
| Inbound provider webhook load | Low–Medium | Signature verification and bounded body | Non-blocking | Add provider-specific ingress limiting/network policy if exposed publicly |
| Refund/settlement adapters remain mock-shaped for non-mock providers | Medium if those production flows are advertised | Production refuses the mock payment provider; payment webhook parser selection is explicit | Conditional scope boundary, not an in-scope payment-webhook bypass | Implement provider-specific refund/settlement integrations before advertising those flows in production |

## Follow-up classification

### Required before release

No additional in-scope code defect was found after the 8D.6 fixes. Production
deployment must provide the documented external prerequisites: explicit
`APP_ENV=production`, a non-mock payment provider, strong secrets,
`TRUSTED_PROXIES=none` or an exact proxy list, HTTPS webhook/provider/dashboard
settings, `DB_SSLMODE=verify-full`, and an appropriate TLS termination
network policy. Refund/settlement provider adapters remain a separately scoped
integration decision.

### Future hardening

- distributed rate limiting for multi-instance deployments;
- refresh-token family/reuse history;
- audit read authorization, retention, and database-level immutability;
- real operator identity for admin actions and key-specific API attribution;
- password complexity/breach policy;
- migration checksum/upgrade automation and Swagger gating;
- live SMTP/provider contract tests and inbound webhook ingress limiting.

### Explicitly out of scope

MFA, password reset, admin key rotation, Redis/Kafka, external SIEM/audit
platform, email-token redesign, session-family redesign, provider architecture
changes, transaction redesign, and major frontend changes.

## Release gate

**SECURITY REGRESSION COMPLETE — NO IN-SCOPE RELEASE BLOCKER FOUND**

This conclusion is conditional on the documented production deployment
prerequisites above and does not claim absolute security.
