# Phase 9 — Production Runbook

## 1. Scope and operating model

Phase 9 converts the security-hardened application into a documented and
recoverable production-operable service. It does not introduce Redis, Kafka,
distributed locking, MFA, a new payment provider, or a frontend release.

The application is a stateless HTTP API with a PostgreSQL database and several
bounded background workers:

```text
Client
  |
  | HTTPS
  v
TLS-terminating ingress / reverse proxy / load balancer
  |
  | HTTP on a private interface
  v
PayGate API container (non-root, port 8080)
  |--------------------------- PostgreSQL (TLS, private network)
  |--------------------------- payment provider (HTTPS)
  |--------------------------- SMTP relay (TLS when enabled)
  |--------------------------- merchant webhook destinations (HTTPS/SSRF policy)
```

TLS termination is external to the Go process. The Go server intentionally
listens on plain HTTP behind the configured TLS boundary; do not expose that
listener directly to the public Internet. Docker Compose binds the application
to `127.0.0.1` for a host-local reverse proxy. A deployment where the proxy is
another container or VM must bind only to the appropriate private interface and
must preserve the trusted-proxy policy.

The current implementation has no metrics exporter. Operational visibility is
provided through structured JSON logs, health/readiness probes, database state,
and the bounded worker/delivery tables. Metrics are a future enhancement, not a
release prerequisite for this phase.

## 2. Production configuration contract

Configuration is loaded from environment variables. `.env` is a development
convenience only and is ignored by Git. Production values should be injected by
the deployment platform or an external secret manager.

### Required in production

| Variable | Default | Production requirement / allowed values | Security or operational implication | Example |
|---|---|---|---|---|
| `APP_ENV` | `development` | Exactly `production` after normalization; only `development`, `test`, and `production` are accepted | Activates fail-closed production checks | `production` |
| `APP_PORT` | `8080` | Integer `1–65535`; normally `8080` behind ingress | Invalid values fail before listening | `8080` |
| `DB_HOST` | none in app config | Non-empty private PostgreSQL host | Must not be a public database listener | `db.internal.example` |
| `DB_PORT` | `5432` | Integer `1–65535` | Invalid values fail startup | `5432` |
| `DB_USER` | none | Non-empty runtime/migration-capable user as approved by DBA | Never place in logs or command history | `paygate_runtime` |
| `DB_PASSWORD` | none | Non-empty secret | Inject externally; never commit or print | `<external-secret>` |
| `DB_NAME` | none | Non-empty database name | Restore and migration targets must be explicit | `payment_gateway` |
| `DB_SSLMODE` | `disable` in development | Must be `verify-full` | Certificate-verified PostgreSQL TLS is mandatory | `verify-full` |
| `MIGRATION_DATABASE_URL` | Compose fallback only | Explicit complete URL for production/staging migration jobs | Prevents URL-special-character password corruption; inject as a secret | `postgres://<encoded-user>:<encoded-password>@<host>:5432/<db>?sslmode=verify-full` |
| `AUTH_JWT_SECRET` | development fallback | Non-empty, at least 32 bytes | Protects dashboard access tokens | `<external-secret>` |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | development fallback | Exactly 32 bytes encoded as 64 hex characters or valid base64 | Required to decrypt merchant webhook secrets | `<external-secret>` |
| `TRUSTED_PROXIES` | unset in development | Explicit `none` or comma-separated IP/CIDR list | Controls `ClientIP`, rate limits, audit IP, and request logs | `none` |
| `PAYMENT_PROVIDER` | `mock` | Non-mock provider; currently `midtrans` | Mock provider is rejected in production | `midtrans` |
| `MIDTRANS_BASE_URL` | sandbox URL | Explicit non-default absolute HTTPS URL without userinfo | Prevents accidental sandbox-only production traffic | `https://api.provider.example` |
| `MIDTRANS_SERVER_KEY` | empty | Non-empty when `PAYMENT_PROVIDER=midtrans` | External provider credential | `<external-secret>` |
| `MOCK_WEBHOOK_SECRET` | development placeholder | Non-default, at least 32 bytes in production | Required by the current compatibility parser configuration; never reuse the placeholder | `<external-secret>` |
| `WEBHOOK_REQUIRE_HTTPS` | `true` in production | Must remain true; explicit false is rejected | Merchant destinations cannot use plaintext HTTP | `true` |
| `APP_NAME` | `payment-gateway` | Optional stable application name | Appears in startup metadata, not a secret | `payment-gateway` |

`ADMIN_API_KEY` is optional in the sense that an empty value disables admin
routes. If admin/onboarding/settlement operations are required, set a random
value of at least 32 bytes. The same rule applies to `MIDTRANS_CLIENT_KEY` when
a future provider integration uses it; it is loaded but not used by the current
Midtrans adapter.

### Required only when a feature is enabled

| Feature | Required variables | Validation / operational behavior |
|---|---|---|
| Email delivery | `EMAIL_ENABLED=true`, `SMTP_HOST`, `SMTP_FROM`, valid `SMTP_PORT`, positive `SMTP_TIMEOUT`, `SMTP_TLS` | `SMTP_USERNAME`/`SMTP_PASSWORD` are required by the selected relay/auth policy. Production rejects `SMTP_TLS=none`. |
| Invitation links | `DASHBOARD_BASE_URL` | Must be an absolute URL; production requires HTTPS when email is enabled. |
| Simulator | `PAYMENT_SIMULATOR_ENABLED=true`, valid `SIMULATOR_MERCHANT_ID` | Only development/test; production startup rejects it. |
| Legacy credential migration | `LEGACY_API_CREDENTIALS_ENABLED=true` | Deliberate, time-bounded operator decision. New legacy issuance remains permanently frozen. |
| Admin routes | `ADMIN_API_KEY` | Empty disables routes. Production non-empty values must be at least 32 bytes. |
| Midtrans provider | `PAYMENT_PROVIDER=midtrans`, `MIDTRANS_BASE_URL`, `MIDTRANS_SERVER_KEY` | Provider-specific refund/settlement adapters are not implemented; do not advertise those operations as production-ready. |
| Email worker | `EMAIL_WORKER_ENABLED` and positive worker settings | Worker can be disabled deliberately; default is enabled and bounded. |
| Email cleanup | `EMAIL_CLEANUP_ENABLED=true` and positive retention settings | Defaults off; only `SENT` and `DEAD` rows are eligible. |
| Webhook delivery | `WEBHOOK_DELIVERY_ENABLED` and positive delivery settings | Worker retries and dead-letters bounded deliveries. |

### Optional settings

`CORS_ALLOWED_ORIGINS`, `AUTH_ACCESS_TOKEN_TTL`, `AUTH_REFRESH_TOKEN_TTL`,
`INVITATION_TOKEN_TTL`, `IDEMPOTENCY_TTL`, `PAYMENT_PROVIDER_TIMEOUT`,
`PAYMENT_EXPIRY_ENABLED`, `PAYMENT_EXPIRY_INTERVAL`, `PAYMENT_EXPIRY_BATCH_SIZE`,
`RATE_LIMIT_ENABLED`, `RATE_LIMIT_LOGIN_PER_MINUTE`, `RATE_LIMIT_ADMIN_PER_MINUTE`,
`RATE_LIMIT_API_PER_MINUTE`, and worker intervals/batch sizes have documented
positive defaults. Set them explicitly in staging/production when capacity or
policy requires, and validate them before rollout.

### Development/test-only values

`DB_SSLMODE=disable`, the mock payment provider, the simulator, development
fallback encryption/JWT values, plaintext SMTP, and local CORS/dashboard URLs
are not production deployment values. `APP_ENV=test` is useful for automated
tests but is not a substitute for the production fail-closed contract.

## 3. Secret injection

The repository intentionally has no cloud-specific secret manager integration.
The supported integration point is the process environment:

1. Provision secrets in the deployment platform or an external secret manager.
2. Inject them into the app and one-shot migration job at runtime.
3. Keep `.env` out of source control, image layers, CI logs, and shell history.
4. Restrict access to the migration/runtime database roles and provider keys.
5. Restore secrets separately from database backups; a database dump does not
   contain the correct runtime secret values.
6. Rotate the webhook encryption key only through a planned key-version/rotation
   procedure; the current schema has no key ID/version field.
7. If a database backup is exposed, treat it as sensitive: it can contain
   invitation email bodies/tokens, webhook evidence, payment/refund attempts,
   and refresh-session hashes.

Never print secret values during startup, health checks, migration status, or
incident commands. Prefer redacted status output and operator-side secret
rotation.

## 4. Preflight and fail-closed validation

Before deploying, validate the exact environment with the application image or
an equivalent configuration test. Production startup rejects:

- missing/invalid `APP_ENV`, ports, timeouts, body limits, worker intervals, or
  rate-limit values;
- weak/missing JWT, admin, mock-webhook, or webhook encryption secrets;
- `DB_SSLMODE` other than `verify-full`;
- unset/ambiguous `TRUSTED_PROXIES` or invalid proxy entries;
- mock payment provider or implicit Midtrans sandbox endpoint;
- insecure provider/dashboard/webhook URLs;
- simulator enabled in production;
- invalid SMTP settings when email is enabled;
- dirty, missing, or too-old database migrations at startup/readiness.

A configuration failure is safer than starting with a development fallback.
Do not “fix” a failed production preflight by copying development values into
production.

## 5. Database and migration operations

### Initial deployment

1. Provision PostgreSQL on a private network with certificate-verified TLS.
2. Create separate staging/production database users and databases.
3. Take and verify a backup before the first migration if the database contains
   any data.
4. Run the one-shot migration job with an explicit `DATABASE_URL`/`DB_URL`.
5. Confirm `schema_migrations.version=18` and `dirty=false`.
6. Start the application and wait for `/health/ready` to return `200`.
7. Run the smoke checks in Section 12.

Example commands:

```bash
export DATABASE_URL='postgres://<user>:<password>@<db-host>:5432/<db-name>?sslmode=verify-full'
make migrate-status
make migrate-up
```

The migration URL is sensitive because it may contain a password. Use a protected
process environment and avoid command tracing. When Docker Compose is used,
inject `MIGRATION_DATABASE_URL` as one complete URL with URL-encoded
credentials; the development-only fallback assembled from `DB_*` variables is
not a production secret-injection pattern.

### Upgrade deployment

1. Confirm the current image and schema version.
2. Take a verified backup.
3. Review the new migration files and compatibility requirements.
4. Run `make migrate-up` using the approved migration identity.
5. Verify version/dirty state and required tables/indexes.
6. Deploy the new image.
7. Wait for readiness and run smoke tests.
8. Keep the previous image available for application rollback.

### Rollback

Application rollback is preferred when the migration is backward-compatible:
roll back the image, restore the previous configuration, and verify readiness.

Do not automatically run destructive `down` migrations. `make migrate-down` is
guarded by `ALLOW_DESTRUCTIVE_DOWN=true` and still requires explicit approval.
Migration 17 and other down paths are not guaranteed to restore the exact prior
schema. If a migration is irreversible, use a forward fix or an approved
backup restore procedure.

## 6. Backup and restore

### Backup

Use PostgreSQL-native logical backup tooling or the DBA's managed equivalent.
A logical custom-format backup is suitable for a restore drill:

```bash
umask 077
pg_dump \
  --format=custom \
  --no-owner \
  --no-privileges \
  --file="$BACKUP_FILE" \
  "$DATABASE_URL"
```

Backups contain sensitive business data. Encryption at rest, access logging,
retention, geographic storage, and deletion are operator/DBA responsibilities.
The repository does not define an RPO or RTO; those are operator decisions.

### Restore drill

Never restore over production without approval. Restore into a disposable
database first:

```bash
createdb "$RESTORE_DB"
pg_restore \
  --exit-on-error \
  --single-transaction \
  --no-owner \
  --no-privileges \
  --dbname="$RESTORE_DATABASE_URL" \
  "$BACKUP_FILE"

psql "$RESTORE_DATABASE_URL" -Atc \
  'SELECT version, dirty FROM schema_migrations;'
```

Verify at least:

- `schema_migrations.version=18` and `dirty=false`;
- representative merchant, user, transaction, refund, and payment-attempt rows;
- `idempotency_keys`, `dashboard_sessions`, invitations, and outbox state;
- `audit_logs` existence and representative non-secret metadata;
- application database connectivity and `/health/ready` using the restored
  database;
- separate restoration/injection of all runtime secrets;
- post-restore provider reconciliation and duplicate webhook/idempotency
  handling.

A restore can reactivate old refresh-session hashes and contains invitation
tokens in email outbox bodies. Revoke/rotate browser sessions and follow the
incident process if a backup was exposed or restored into an untrusted
environment.

**Phase 9 evidence:** a disposable PostgreSQL database was migrated through
`000001`–`000018`, populated with non-secret fixture rows across merchant,
user, transaction, idempotency, invitation, email-outbox, and audit tables,
dumped, dropped/recreated, and restored. The result was
`version=18`, `dirty=false`; representative counts were `1|1|1|1|1|1`, and the
application schema/readiness check passed. This was not a production restore.

## 7. TLS, proxy, and network model

- Terminate public TLS at the ingress/reverse proxy/load balancer.
- Forward only to the private application listener.
- Set `TRUSTED_PROXIES=none` for direct exposure or provide the exact proxy
  IP/CIDRs. Never trust arbitrary client `X-Forwarded-For` values.
- Use HTTPS PostgreSQL with certificate verification (`DB_SSLMODE=verify-full`).
- Use HTTPS provider endpoints and HTTPS merchant webhook destinations.
- Use SMTP `starttls` or `implicit` in production; plaintext SMTP is rejected.
- Restrict PostgreSQL, SMTP, provider, and internal admin access with network
  policy/firewall rules. Do not publish PostgreSQL publicly.
- Configure DNS, certificates, and provider webhook allowlists outside the
  application and verify them in staging.

The same `ClientIP()` result is used by rate limiting, audit IP capture, and
request logging. The trusted-proxy regression test covers trusted, untrusted,
and `none` modes.

## 8. Health, readiness, and shutdown

### Health

`GET /health` means the process is alive. It does not prove that a dependency is
available and does not expose dependency details.

### Readiness

`GET /health/ready` requires:

1. a reachable PostgreSQL pool;
2. a readable `schema_migrations` table;
3. `dirty=false`;
4. schema version at least the release minimum (currently 18).

A failed readiness check returns `503` with a stable non-sensitive code. The
container healthcheck uses readiness, so a database/schema failure prevents a
container from being considered healthy. The image healthcheck timeout is 10
seconds, covering the bounded database ping/schema checks.

### Shutdown

`SIGINT`/`SIGTERM` triggers:

1. HTTP listener shutdown and bounded in-flight request drain (10-second
   context), while workers remain available for current work;
2. worker context cancellation and a bounded worker drain window (10 seconds
   total);
3. deferred database pool close and process exit.

A listener failure uses the same coordinated shutdown path rather than exiting
from a goroutine and bypassing pool/worker cleanup.

A dependency that ignores cancellation is logged as a shutdown timeout; the
process does not wait indefinitely. Operators should allow at least the
configured Compose `stop_grace_period` (20 seconds) during rollout.

## 9. Workers and bounded operations

The following workers use interval polling and bounded batches:

- payment expiry;
- outbound merchant webhook delivery;
- email outbox delivery;
- optional terminal email cleanup.

Each worker logs start/stop/error categories, respects context cancellation, and
survives ordinary batch failures for a later tick. Stale `PROCESSING` recovery,
retry backoff, terminal/dead states, and cleanup eligibility are repository
invariants covered by tests. There is no unbounded goroutine-per-request
worker. Capacity has not been benchmarked; validate batch sizes and intervals
against expected traffic and database capacity.

## 10. Logging and observability

Structured JSON logs include safe startup/shutdown categories, request ID,
method, route template, status, latency, client IP, worker counts, and stable
failure categories. Do not add raw bodies, credentials, cookies, tokens,
webhook secrets, or database connection strings.

Operators should monitor:

- container restarts and readiness failures;
- HTTP 5xx rate and latency;
- database connection/pool/storage failures and migration state;
- payment creation/provider failures and idempotency conflicts;
- inbound signature failures and outbound delivery retries/dead letters;
- email pending depth, retries, dead records, and worker failures;
- admin authentication failures, rate-limit events, cross-tenant denials, and
  SSRF rejections.

There is no current Prometheus/OpenTelemetry exporter. If metrics are added
later, keep labels bounded and never use request IDs, tokens, emails, URLs, or
provider response bodies as labels.

## 11. Provider, webhook, and SMTP operations

### Payment provider

Production must use an explicit non-mock provider, an explicit non-sandbox
HTTPS endpoint, and injected credentials. The current Midtrans adapter covers
payment create/cancel/status HTTP calls and inbound webhook parsing.

Refund and settlement adapters remain mock-shaped. The dashboard refund path
and admin settlement-import path now fail closed with `503` when the configured
provider is not mock; do not represent Midtrans refunds or settlements as
production-ready. Implement and test provider-specific adapters before
enabling those operations.

Validate provider account/environment separation in staging. Verify a real
sandbox signature/webhook contract, timeout ambiguity, duplicate event handling,
and reconciliation before a production rollout. Do not use production provider
credentials in staging.

### Webhooks

Inbound provider webhooks require a public HTTPS endpoint, signature
verification, bounded bodies, and edge/network protection. The application has
no separate inbound provider rate limiter; configure ingress/firewall limits.

Outbound merchant webhooks require HTTPS (production), valid DNS/certificates,
merchant endpoint availability, SSRF-safe destination validation, bounded
timeouts/retries, and monitoring of `FAILED`/`DEAD` deliveries. Never weaken the
SSRF dialer to accommodate a private merchant endpoint.

### SMTP/email

When enabled, configure SMTP host/port/auth/from/TLS, HTTPS dashboard URL, and
separate staging credentials. Validate SPF/DKIM/DMARC and mailbox delivery in
staging. A local fake SMTP test is not proof of live relay/auth/deliverability.
If email is disabled, the no-op sender intentionally drains outbox rows to
`SENT`; this must be understood before enabling a production deployment.

## 12. Staging model

Staging should use `APP_ENV=production` to exercise the same fail-closed
configuration, Docker runtime, TLS boundary, trusted-proxy configuration,
migration gate, health/readiness behavior, and worker lifecycle.

It must use separate:

- PostgreSQL database and credentials;
- payment provider account/environment and credentials;
- merchant webhook endpoints and DNS;
- SMTP account/relay and sending domain;
- JWT/admin/webhook encryption secrets;
- backup/storage location;
- synthetic, non-production data.

Never point staging at a production database or use production secrets. Keep a
staging configuration template and record the operator who approved each
environment.

## 13. Deterministic smoke test

Use a disposable/synthetic staging tenant. Do not put real secrets in a shell
history or ticket. The production-mode smoke uses the approved non-mock
provider account and a provider-specific signature/webhook contract; the local
mock flow is a separate `APP_ENV=development` test, not a production-mode
substitute.

1. Start the image and wait for migration success.
2. `curl -fsS https://<public-host>/health` through the TLS boundary.
3. `curl -fsS https://<public-host>/health/ready`.
4. Verify admin authentication failure with a deliberately wrong key and
   confirm a redacted `401` plus safe audit category.
5. Onboard a synthetic merchant with the admin key.
6. Log in as the synthetic OWNER through the dashboard API.
7. Create a canonical API key and confirm the plaintext is shown only once.
8. Create a payment with an idempotency key using the approved staging
   Midtrans sandbox account.
9. Retrieve the payment and verify merchant scoping.
10. Submit a correctly signed provider webhook and verify the state change.
11. Configure a controlled merchant webhook target and verify delivery status.
12. Create an invitation and verify one outbox row without logging its token.
13. Verify a redacted audit event exists for the successful mutation.
14. Logout and verify refresh single-use/replay behavior.
15. Repeat health/readiness checks after worker activity.

A deterministic mock-provider flow may be run separately in development/test
mode. Do not claim live-provider or live-SMTP success unless that controlled
environment was actually used.

## 14. Failure and recovery drills

| Failure | Expected behavior | Recovery verification |
|---|---|---|
| PostgreSQL unavailable | Startup fails or readiness returns `503`; liveness may remain alive | Restore DB/network, confirm readiness |
| Wrong/old/dirty schema | Startup/readiness fails before serving traffic | Run approved migration/restore, verify version/dirty |
| Invalid production env | Startup exits before listening | Correct injected config and restart |
| Provider unavailable | Payment/provider operation returns stable failure; retries/timeouts remain bounded | Provider recovery test and reconciliation |
| SMTP unavailable | Outbox retries; terminal/dead rows are observable | Requeue/repair procedure reviewed by operator |
| Merchant webhook unavailable | Delivery retries, then dead state; response diagnostics bounded | Merchant endpoint recovery and manual retry |
| Container restart | Migration dependency and readiness gate re-establish state | Verify no duplicate idempotency/refund effects |
| SIGTERM | Workers cancel within bound, HTTP drains, pool closes | Inspect logs and exit code |
| Invalid proxy header | Untrusted forwarded value ignored | Confirm rate-limit/audit/request IP consistency |

Safe requeue and duplicate provider operations must be approved procedures; do
not blindly reset terminal delivery/outbox rows.

## 15. Capacity/resource review

The current code has fixed database pool defaults (25 max, 5 min, five-minute
lifetime, one-minute idle) and configurable HTTP/worker batch/timeouts. Capacity
has not been benchmarked. Production limits must be validated against expected
traffic, PostgreSQL connection limits, provider/SMTP latency, webhook fan-out,
rate-limiter memory, audit growth, outbox growth, and backup windows.

## 16. Operational risk register

| Risk | Severity | Mitigation | Production impact | Future action |
|---|---|---|---|---|
| In-process rate limiting | Medium | Fixed windows, bounded table, IP/account/merchant keys | Multi-instance limits are per process | Distributed limiter only when scaling requires it |
| No refresh-token family detection | Medium | Atomic rotation/replay rejection | Reuse-history visibility is absent | Later session-family design |
| Indefinite audit retention | Low–Medium | Append-only application repository, no cleanup worker | Storage growth and access governance | Retention/immutability policy |
| Shared admin identity | Low–Medium | ADMIN actor and failure audit | No human operator attribution | Real operator identity |
| API-key actor specificity | Low | Merchant and target-key metadata | Key-level actor detail is limited | Add verified key UUID context |
| Password complexity | Medium | Argon2id and length bounds | No breach/complexity policy | Policy decision |
| Argon2id capacity | Medium | Rate limits and bounded requests | CPU exhaustion under hostile load | Capacity test/scale controls |
| Public Swagger | Low–Medium | No secret examples; edge restriction available | Operational surface is broader than necessary | Production gate/edge ACL |
| Inbound webhook ingress limiting | Low–Medium | Signature/body limits | Edge-level abuse controls are external | Add ingress limiter |
| Dashboard route rate limiting | Low–Medium | Authenticated tenant scoping and edge/WAF policy | Expensive dashboard queries/mutations rely on edge policy | Add bounded in-process or edge policy when traffic warrants |
| Stale worker recovery backlog | Low–Medium | Context cancellation and bounded delivery/cleanup loops | Large crash backlogs can create long database transactions | Bound/review stale-recovery queries under load |
| Live provider/SMTP validation | Medium | Deterministic fakes and documented staging flow | Production dependency behavior is not proven here | Controlled staging contract test |
| Refund/settlement provider adapters | High if advertised | Non-mock refund path fails closed; settlement remains scoped | Those operations are not production-complete | Implement provider adapters |
| Migration checksums/upgrade automation | Low | Version/dirty gate and explicit migration job | Drift/checksum protection is incomplete | Add checksums and CI migration verification |
| TLS/secret/backup ownership | High if omitted | Contract and checklists | External infrastructure remains operator-owned | Assign owners and conduct drills |
| Backup retention/RPO/RTO | Operator decision | Procedure and restore drill | Recovery objectives are not defined by code | DBA/operator policy |
| Restore session/outbox handling | Medium | Separate secret injection and post-restore review | Old session hashes/tokens may be sensitive | Incident/revocation procedure |
| Capacity benchmarking | Medium | Bounded defaults | No evidence for production scale | Load/benchmark test |

## 17. Verification evidence

The repository release gate should record:

```bash
gofmt -l .
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./... -count=1
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -race ./... -count=1
make swagger
docker compose config --quiet
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8081/health
curl -fsS http://127.0.0.1:8081/health/ready
```

The Phase 9 disposable schema drill also rejected an old version (`17`),
a dirty version (`18/dirty=true`), and a missing `schema_migrations` table;
each readiness/schema check failed as required.

Never include real secret values in evidence, logs, or documentation.
