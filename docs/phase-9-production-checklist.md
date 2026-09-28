# Phase 9 — Production Readiness Checklist

Use this checklist for each production or staging deployment. Record evidence
and owner in the change ticket. Never paste secret values into the checklist.

## Configuration

- [ ] `APP_ENV=production` is explicit and accepted by configuration validation.
- [ ] `APP_PORT` is a valid port in the expected private range.
- [ ] JWT secret is injected externally and is at least 32 bytes.
- [ ] Admin key is injected externally, at least 32 bytes, or admin routes are intentionally disabled.
- [ ] Webhook encryption key is injected externally and decodes to exactly 32 bytes.
- [ ] Payment provider is explicit and not `mock`.
- [ ] Provider endpoint is explicit, non-sandbox, HTTPS, and has no userinfo.
- [ ] Provider credentials are injected externally.
- [ ] Compatibility `MOCK_WEBHOOK_SECRET` is a non-default 32-byte secret if the loader requires it.
- [ ] `DB_SSLMODE=verify-full` and the database certificate verifies.
- [ ] Trusted proxies are explicitly `none` or an exact IP/CIDR list.
- [ ] Simulator is disabled.
- [ ] Dashboard base URL is correct and HTTPS when email is enabled.
- [ ] Any configured production CORS origin is explicit HTTPS (or CORS is intentionally empty).
- [ ] Merchant webhook HTTPS requirement is enabled.
- [ ] SMTP host/port/auth/from/TLS are configured when email is enabled.
- [ ] Worker intervals, batch sizes, timeouts, and rate limits are intentional.
- [ ] `RATE_LIMIT_ENABLED=true` in production; disabling it is rejected.

## Database

- [ ] PostgreSQL is private and not publicly exposed.
- [ ] A pre-deployment backup exists and its restore procedure is approved.
- [ ] Migration command uses an explicit `DATABASE_URL`/`DB_URL` (and `MIGRATION_DATABASE_URL` for Compose when credentials contain URL-special characters).
- [ ] Migration version is `18`.
- [ ] `schema_migrations.dirty=false`.
- [ ] `audit_logs`, `email_outbox`, invitations, sessions, merchant users,
      payments, idempotency, and webhook tables exist.
- [ ] Application startup schema check passes.
- [ ] `/health/ready` passes after rollout.
- [ ] Connection pool limits are reviewed against database capacity.

## Network and TLS

- [ ] Public TLS terminates at ingress/reverse proxy/load balancer.
- [ ] Application HTTP listener is private or loopback-only.
- [ ] PostgreSQL uses certificate-verified TLS.
- [ ] Provider and merchant webhook destinations use HTTPS.
- [ ] SMTP uses STARTTLS or implicit TLS.
- [ ] Forwarded headers are trusted only from configured proxies.
- [ ] DNS, certificates, and provider allowlists have been verified.
- [ ] Outbound network policy permits only required dependencies.

## Runtime

- [ ] Image runs as the non-root `appuser`.
- [ ] Image contains no `.env` or injected secret.
- [ ] Container has read-only filesystem/capability restrictions where supported.
- [ ] Healthcheck uses `/health/ready`.
- [ ] Migration runs as a separate one-shot job before app startup.
- [ ] `SIGTERM` drains workers and HTTP within the stop grace period.
- [ ] Expiry, webhook, email, and cleanup workers start/stop as configured.
- [ ] Worker errors are visible without request bodies or credentials.

## Security preservation

- [ ] Anonymous simulator access is denied.
- [ ] Foreign-merchant simulator access is denied.
- [ ] Private/loopback/metadata SSRF destinations are denied.
- [ ] DNS rebinding and redirect-to-private protections pass.
- [ ] Refresh token remains single-use and replay is rejected.
- [ ] Inactive merchant and disabled user authentication is denied.
- [ ] Cross-tenant access is denied.
- [ ] OWNER invariant remains enforced under concurrency.
- [ ] Login/admin/API rate limits remain enabled and tested.
- [ ] Required mutations produce audit events.
- [ ] Audit metadata contains no passwords, keys, tokens, cookies, or bodies.
- [ ] Legacy credential issuance remains frozen.
- [ ] Canonical API credentials remain hashed.
- [ ] HTTP body and timeout limits remain positive.
- [ ] Swagger exposure has an explicit edge/network decision.

## Business smoke

- [ ] Synthetic merchant onboarding succeeds.
- [ ] Synthetic OWNER login succeeds.
- [ ] API credential is created and plaintext is shown only once.
- [ ] Payment creation/retrieval succeeds in the configured staging mode.
- [ ] Idempotency replay and modified-body conflict behave correctly.
- [ ] Provider webhook signature and state transition succeed.
- [ ] Merchant webhook delivery reaches a controlled test endpoint.
- [ ] Invitation creation creates an outbox row without logging its token.
- [ ] Audit event exists for a successful mutation.
- [ ] Logout/refresh/replay behavior is verified.
- [ ] Health and readiness remain successful after smoke activity.
- [ ] Non-mock refund/settlement limitations are explicitly acknowledged.
- [ ] Non-mock refund creation and settlement import fail closed until real adapters exist.

## Recovery

- [ ] Backup method, encryption, retention, and access owner are documented.
- [ ] Restore drill completed in a disposable database.
- [ ] Restored schema version/dirty state verified.
- [ ] Representative merchant/payment/idempotency/session/invitation/outbox/audit
      data verified.
- [ ] Runtime secrets restored separately from the database dump.
- [ ] Application rollback procedure tested or rehearsed.
- [ ] Destructive migration rollback is not automatic.
- [ ] Post-restore provider reconciliation and session revocation plan exists.
- [ ] RPO/RTO owners have made explicit decisions.

## Observability

- [ ] Startup, migration, worker, readiness, and shutdown log categories reviewed.
- [ ] HTTP 5xx/latency and restart counts monitored.
- [ ] Database failures, pool pressure, storage, and migration state monitored.
- [ ] Provider failures, webhook retries/dead letters monitored.
- [ ] Email queue depth/retries/dead records monitored.
- [ ] Authentication failures, rate limits, authorization denials, and SSRF rejections monitored.
- [ ] Metrics exporter decision recorded; no unbounded/high-cardinality labels
      introduced without review.

## Sign-off

- [ ] Operator owner: ____________________
- [ ] DBA/recovery owner: ____________________
- [ ] Security reviewer: ____________________
- [ ] Staging evidence attached without secrets: ____________________
- [ ] Production evidence attached without secrets: ____________________
- [ ] Release decision: ____________________
