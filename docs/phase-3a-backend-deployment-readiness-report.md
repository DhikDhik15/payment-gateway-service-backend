# GO-SAAS Phase 3A — Backend Deployment Readiness Audit Report

**Repository:** `github.com/dhikaarta/pay-gate-backend`
**Branch:** `main` (working tree contains pre-existing uncommitted changes — all preserved)
**Audit type:** Audit only. No implementation, no deployment, no workflow changes.
**Report file:** `docs/phase-3a-backend-deployment-readiness-report.md` (new; does not overwrite prior reports)
**Acceptance:** Audit based on actual repository files; existing CI unchanged; no code modified; no secrets exposed.

---

## 1. Executive Summary

The backend is **deployment-capable but not production-ready out of the box**. The container image, health checks, graceful shutdown, fail-closed config validation, and one-shot migration pattern are well implemented. However, production deployment requires external infrastructure (private PostgreSQL, TLS-terminating ingress, secret injection) and explicit operator configuration. No application code or CI workflow changes are required to proceed; the remaining work is operational provisioning and a CD workflow.

**Overall readiness: READY WITH GAPS** — the artifact builds and starts correctly given a valid environment; production deployment is gated on external prerequisites (database, secrets, ingress) and a CD pipeline.

---

## 2. Current Architecture

Based on actual repository files:

### 2.1 Application packaging
- **Entry point:** `cmd/server/main.go` (package `main`).
- **Go version:** `go 1.25` (matches `go.mod`; Dockerfile uses `golang:1.25-alpine` builder).
- **Dockerfile:** Multi-stage (builder → `alpine:3.20` runtime), non-root `appuser`, `read_only` rootfs in Compose, `COPY migrations ./migrations`, `EXPOSE 8080`, healthcheck on `localhost:8080/health/ready`, `ENTRYPOINT ["./server"]`.
- **.dockerignore:** Excludes `.env`, `.env.*` (except `!.env.example`), `.git`, `bin/`, `docs/`, `README.md`, editor dirs. Secrets are not copied into image layers.
- **docker-compose.yml:** Two services — `app` (depends_on `migrate` with `service_completed_successfully`) and `migrate` (one-shot `migrate/migrate:v4.18.1`, `restart: "no"`). PostgreSQL is external (not provisioned by Compose). Host port mapping `8081:8080`.
- **CI workflow:** `.github/workflows/ci.yml` exists (created in Phase 2). Two jobs: `test` (gofmt, vet, race tests, build) and `docker` (Compose config validation + image build). No push, no deploy, no secrets.

### 2.2 Database and migrations
- **Driver:** `github.com/jackc/pgx/v5 v5.7.2`.
- **Migrations:** 25 versioned SQL pairs in `migrations/` (`000001` … `000025`), each with `.up.sql` and `.down.sql`, managed by `golang-migrate`.
- **Migration tool:** NOT a Go dependency; provided via the `migrate/migrate:v4.18.1` Docker image.
- **Schema version gate:** `pkg/database/postgres.go` defines `ExpectedMigrationVersion = 22`. `CheckSchema()` queries `schema_migrations` and fails startup if `dirty=true` or `version < 22`.
- **Startup dependency:** `cmd/server/main.go` creates the pool (with ping), then calls `CheckSchema` before listening. Failure → `os.Exit(1)` (fail closed). The app does **not** auto-migrate; migrations run separately before app startup.
- **Readiness:** `GET /health/ready` requires reachable PostgreSQL + clean schema at/above minimum version; returns 503 otherwise. `GET /health` is liveness only.

### 2.3 Runtime workers
Payment expiry, outbound merchant webhook delivery, email outbox delivery, optional terminal email cleanup — all interval-polling, bounded-batch, context-cancellation-aware.

### 2.4 Security posture (observed)
- Container runs as `appuser` (non-root), `no-new-privileges`, `cap_drop ALL`, `read_only` rootfs, `tmpfs: /tmp`.
- No `.env` in image (`.dockerignore`).
- Structured JSON logging; startup explicitly avoids logging credentials.
- `TRUSTED_PROXIES` required in production; `DB_SSLMODE=verify-full` required in production; mock provider rejected in production.

---

## 3. Production Readiness Checklist

| Area | Item | Status | Evidence |
|------|------|--------|----------|
| **Image** | Multi-stage build, static binary | PASS | Dockerfile stages 1→2, `CGO_ENABLED=0` |
| **Image** | Non-root runtime user | PASS | `USER appuser` |
| **Image** | No secrets in layers | PASS | `.dockerignore` excludes `.env`, `.env.*` |
| **Image** | Healthcheck | PASS | `HEALTHCHECK ... /health/ready` |
| **Image** | Graceful shutdown | PASS | main.go: SIGINT/SIGTERM drain (HTTP 10s + workers 10s + pool close) |
| **Config** | Fail-closed validation | PASS | config.Load rejects invalid production values (mock provider, verify-full, weak secrets, unset TRUSTED_PROXIES) |
| **Config** | No auto-migration | PASS | App validates schema only; migrate is separate one-shot job |
| **DB** | Migration tooling present | PASS | `migrate/migrate:v4.18.1` image in Compose |
| **DB** | Schema version gate | PASS | `ExpectedMigrationVersion = 22`, dirty/version checks |
| **Ops** | Liveness + readiness endpoints | PASS | `/health`, `/health/ready` |
| **Ops** | Structured JSON logs | PASS | `slog.NewJSONHandler` |
| **Security** | Production rejects dev defaults | PASS | config.Load production gates (mock, verify-full, secrets) |
| **CI** | Workflow exists | PASS | `.github/workflows/ci.yml` |
| **CD** | CD workflow | GAP | No CD workflow; no registry push; no deploy |
| **Infra** | Private PostgreSQL | GAP | Must be provisioned externally (not in Compose) |
| **Infra** | TLS termination | GAP | External ingress/proxy required (app listens on plain HTTP) |
| **Secrets** | Secret injection mechanism | GAP | Must be supplied by deployment platform/secret manager |

---

## 4. Blockers Classified by Severity

| ID | Blocker | Severity | Evidence |
|----|---------|----------|----------|
| CD-001 | No CD workflow exists | High | Only `.github/workflows/ci.yml` present; no registry push, image promotion, or deploy stages |
| CD-002 | No container registry configured | Medium | No registry credentials, image tagging strategy, or provenance in repository |
| CD-003 | PostgreSQL must be provisioned externally | High | Compose does not provision PostgreSQL; `DB_HOST`/`DB_USER`/`DB_PASSWORD`/`DB_NAME` required at runtime; migration job needs `MIGRATION_DATABASE_URL` or `DB_*` |
| CD-004 | TLS termination must be external | Medium | App listens on plain HTTP behind ingress (runbook §7); no TLS config in Dockerfile/Compose |
| CD-005 | Production secrets must be injected externally | High | `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `DB_PASSWORD`, provider keys — none in repo; must come from deployment platform/secret manager |
| CD-006 | Migration `down` is destructive and guarded | Medium | `make migrate-down` requires `ALLOW_DESTRUCTIVE_DOWN=true`; down paths (e.g. migration 17) not guaranteed to restore exact prior schema; rollback prefers image rollback, not `down` |
| CD-007 | Mock provider rejected in production | Medium (config, not blocker) | `PAYMENT_PROVIDER=mock` fails `config.Load` in production; `PAYMENT_PROVIDER=midtrans` requires `MIDTRANS_BASE_URL` + `MIDTRANS_SERVER_KEY`; refund/settlement adapters remain mock-shaped (fail closed with 503) |
| CD-008 | No metrics exporter | Low | Runbook §10: metrics are a future enhancement, not a release prerequisite |
| CD-009 | `TRUSTED_PROXIES` required in production | Medium (config) | `config.Load` rejects unset `TRUSTED_PROXIES` when `APP_ENV=production` |

**No Critical blockers** exist in the repository itself. All severity-High items are external infrastructure/CD pipeline gaps, not code defects.

---

## 5. Recommended Deployment Architecture

Per `docs/phase-9-production-runbook.md` §1 (the documented operating model):

```
Client
  |
  | HTTPS (public TLS terminated externally)
  v
TLS-terminating ingress / reverse proxy / load balancer
  |
  | HTTP on a private interface
  v
PayGate API container (non-root, port 8080)
  |--------------------------- PostgreSQL (TLS, verify-full, private network)
  |--------------------------- Payment provider (HTTPS, non-mock, non-sandbox)
  |--------------------------- SMTP relay (TLS when enabled)
  |--------------------------- Merchant webhook destinations (HTTPS/SSRF policy)
```

**Deployment option recommendation:** Single VPS with Docker Compose (Option A) is the simplest production-deployment strategy consistent with the existing architecture:
- The repository already provides `docker-compose.yml` with a one-shot migration job and external PostgreSQL.
- Adding a TLS-terminating reverse proxy (e.g. nginx, Traefik, or Caddy) on the same VPS, bound to `127.0.0.1:8080` or a private interface, satisfies the runbook's TLS-boundary requirement.
- Managed container platforms (Option B) are viable but introduce platform-specific secret/ingress concepts not present in the repository; they are a reasonable follow-up once the Compose-based path is proven.

No implementation is performed in this audit phase.

---

## 6. Safe Migration Strategy

Based on `docs/phase-9-production-runbook.md` §5 and `docker-compose.yml`:

### 6.1 Migration execution
1. **One-shot job before app startup.** Compose `migrate` service runs `migrate -path /migrations -database <URL> up` with `restart: "no"`; `app` has `depends_on: migrate` with `condition: service_completed_successfully`. A failed migration blocks app startup.
2. **Explicit database URL.** Production/staging must inject `MIGRATION_DATABASE_URL` (complete URL with URL-encoded credentials). The Compose fallback assembled from `DB_*` variables is development-only.
3. **Fail-closed schema gate.** App startup calls `database.CheckSchema()`: fails if `schema_migrations` missing, `dirty=true`, or `version < ExpectedMigrationVersion (22)`. `/health/ready` returns 503 if schema is not ready.

### 6.2 Upgrade sequence (runbook §5.2)
1. Confirm current image and schema version.
2. Take a verified backup.
3. Review new migration files and compatibility requirements.
4. Run `make migrate-up` with explicit `DATABASE_URL`/`DB_URL`.
5. Verify `version`/`dirty` state and required tables/indexes.
6. Deploy the new image.
7. Wait for readiness (`/health/ready` 200) and run smoke tests.
8. Keep the previous image available for rollback.

### 6.3 Rollback
- **Application rollback preferred** when migration is backward-compatible: roll back the image, restore previous configuration, verify readiness.
- **Do not automatically run destructive `down` migrations.** `make migrate-down` is guarded by `ALLOW_DESTRUCTIVE_DOWN=true`; down paths (e.g. migration 17) are not guaranteed to restore the exact prior schema.
- If a migration is irreversible, use a forward fix or an approved backup restore procedure.

### 6.4 What was NOT done (audit scope)
- No connection to or modification of any database.
- No production migrations executed.

---

## 7. Required Environment Configuration

Classified from `config.go`, `docker-compose.yml`, `.env.example`, and `docs/phase-9-production-runbook.md` §2. Secret values are classified but **not printed**.

### 7.1 Required in production (must be injected externally)
| Variable | Production requirement |
|----------|----------------------|
| `APP_ENV` | Exactly `production` |
| `DB_HOST` | Non-empty private PostgreSQL host |
| `DB_PORT` | Integer 1–65535 |
| `DB_USER` | Non-empty runtime/migration-capable user |
| `DB_PASSWORD` | Non-empty secret |
| `DB_NAME` | Non-empty database name |
| `DB_SSLMODE` | `verify-full` |
| `MIGRATION_DATABASE_URL` | Explicit complete URL for migration jobs |
| `AUTH_JWT_SECRET` | Non-empty, ≥32 bytes, not dev fallback |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | Exactly 32 bytes (64 hex or base64), not dev fallback |
| `TRUSTED_PROXIES` | Explicit `none` or IP/CIDR list |
| `PAYMENT_PROVIDER` | Non-mock (e.g. `midtrans`) |
| `MIDTRANS_BASE_URL` | Explicit non-sandbox absolute HTTPS URL without userinfo |
| `MIDTRANS_SERVER_KEY` | Non-empty when provider is `midtrans` |
| `WEBHOOK_REQUIRE_HTTPS` | `true` |
| `MOCK_WEBHOOK_SECRET` | Non-default, ≥32 bytes (compatibility parser) |

### 7.2 Required only when a feature is enabled
| Feature | Variables |
|---------|-----------|
| Email delivery | `EMAIL_ENABLED=true`, `SMTP_HOST`, `SMTP_FROM`, valid `SMTP_PORT`, positive `SMTP_TIMEOUT`, `SMTP_TLS` (not `none` in prod); `SMTP_USERNAME`/`SMTP_PASSWORD` per relay policy |
| Invitation links | `DASHBOARD_BASE_URL` (HTTPS in production when email enabled) |
| Admin routes | `ADMIN_API_KEY` (≥32 bytes if non-empty) |
| Simulator | `PAYMENT_SIMULATOR_ENABLED=true` + `SIMULATOR_MERCHANT_ID` (rejected in production) |
| Legacy credential migration | `LEGACY_API_CREDENTIALS_ENABLED=true` (deliberate, time-bounded) |

### 7.3 Optional (documented positive defaults)
`CORS_ALLOWED_ORIGINS`, token TTLs, `IDEMPOTENCY_TTL`, provider timeout, expiry settings, rate limits, worker intervals/batch sizes.

### 7.4 Development-only values (must NOT be used in production)
`DB_SSLMODE=disable`, mock provider, simulator, dev fallback encryption/JWT values, plaintext SMTP, local CORS/dashboard URLs, `APP_BIND_HOST` unrestricted.

### 7.5 Deprecated
`MOCK_PAYMENT_BASE_URL` — retained as a deprecated compatibility alias for `FRONTEND_PUBLIC_URL`; new deployments use `FRONTEND_PUBLIC_URL`.

### 7.6 Secret-handling rules (from runbook §3)
- Provision secrets in deployment platform or external secret manager; inject into app and migration job at runtime.
- Keep `.env` out of source control, image layers, CI logs, and shell history.
- Never print secret values during startup, health checks, migration status, or incident commands.
- Restore secrets separately from database backups.

---

## 8. Proposed CD Workflow Stages

Design only — not implemented. Extends the existing `.github/workflows/ci.yml` (unchanged).

### Stage A: Build (existing CI `docker` job)
- Validate Compose model (`docker compose config --quiet`).
- Build application image (`docker compose build app`).
- No push, no secrets.

### Stage B: Image promotion (new)
- Tag image with commit SHA and/or semver tag.
- Push to container registry (registry credentials from GitHub repository/environment secrets — never in workflow file).
- Record image digest for provenance.

### Stage C: Migration (new, run before deploy)
- One-shot `migrate/migrate:v4.18.1` job against the target environment's `MIGRATION_DATABASE_URL`.
- Run `migrate up`; verify `schema_migrations.version`/`dirty=false`.
- Fail the deployment if migration fails (do not proceed to app deploy).

### Stage D: Deploy (new)
- Roll out new image (rolling update or blue/green depending on platform).
- Wait for `/health/ready` to return 200.
- Run smoke checks (runbook §13).

### Stage E: Rollback (new, manual/on-failure)
- Roll back to previous image.
- Restore previous configuration.
- Verify readiness.
- Do not run automatic destructive `down` migrations.

### CI/CD separation
- Keep `ci.yml` (PR/push checks: test + docker build) unchanged.
- Add a separate `cd.yml` (release/deployment: promotion, migration, deploy, rollback) triggered by tags or manual dispatch.
- CD workflow needs deployment credentials (registry, target environment) — injected via GitHub secrets, never committed.

---

## 9. Rollback Strategy

Based on runbook §5.3:

1. **Preferred:** Application rollback — roll back the container image to the previous known-good tag, restore the previous configuration, verify `/health/ready`.
2. **Schema:** Do **not** run automatic `down` migrations. Migrations are forward-only in normal operations. If a migration is irreversible, use a forward fix or an approved backup restore.
3. **Data:** Restore from backup only into a disposable database first; never restore over production without approval. Restore secrets separately from the database dump.
4. **Grace period:** Allow at least the Compose `stop_grace_period` (20 seconds) during rollout/rollback so workers drain and the pool closes cleanly.

---

## 10. Security Considerations

| Area | Finding | Risk |
|------|---------|------|
| Secrets in tracked files | `.env` not tracked; `.env.example` contains placeholders only (SMTP placeholders set in Phase 2) | Low |
| Secrets in image layers | `.dockerignore` excludes `.env`, `.env.*`, `.git` | Low |
| Container privileges | `no-new-privileges`, `cap_drop ALL`, `read_only` rootfs, non-root user | Low — well hardened |
| Database port exposure | Compose does not publish a database port; PostgreSQL external | Low |
| Production config validation | `config.Load` rejects mock provider, `verify-full` violations, weak/unset secrets, unset `TRUSTED_PROXIES` | Low — fail closed |
| TLS termination | External to Go process; app listens on plain HTTP behind ingress | Medium — operator must configure ingress correctly |
| Logging sensitive payment data | runbook §10: no raw bodies, credentials, cookies, tokens, webhook secrets, or DB connection strings in logs | Low — documented contract |
| Refund/settlement production readiness | Non-mock refund/settlement adapters fail closed with 503 | Medium — do not advertise as production-ready |
| Inbound webhook rate limiting | No separate inbound provider rate limiter; edge/firewall limits recommended | Low–Medium |
| `TRUSTED_PROXIES` | Required explicitly in production | Medium — misconfiguration affects ClientIP/rate limits/audit |

---

## 11. Explicit Implementation Plan for the Next Phase

The next phase (CD implementation) should proceed in this order:

1. **Provision external infrastructure:** private PostgreSQL (certificate-verified TLS), container registry, TLS-terminating ingress/reverse proxy, secret manager or deployment-platform secret injection.
2. **Create `.github/workflows/cd.yml`:** image promotion (tag + push), one-shot migration job, deploy stage with readiness check, manual rollback procedure. Do not add registry credentials to the workflow file.
3. **Validate in staging:** run the full migration + deploy + smoke-check sequence (`docs/phase-9-production-runbook.md` §13) against a staging environment using `APP_ENV=production` and separate staging credentials.
4. **Production rollout:** backup → migrate → deploy → smoke check → keep previous image available.
5. **Document RPO/RTO and backup ownership** (runbook §6: operator/DBA decisions, not code).

### Acceptance criteria for the next phase
- CD workflow builds, tags, and pushes the image without exposing secrets.
- Migration runs as a one-shot job before app startup and fails the deployment on error.
- App starts only when `schema_migrations` is clean and at/above the expected version.
- `/health/ready` returns 200 after rollout.
- Rollback to previous image + config is tested or rehearsed.
- No secrets printed in workflow logs or stored in the repository.

---

## 12. Files Inspected

| File | Purpose in audit |
|------|-----------------|
| `Dockerfile` | Multi-stage build, runtime user, healthcheck, entry point |
| `.dockerignore` | Secret/image-layer exclusion |
| `docker-compose.yml` | App + one-shot migrate services, external PostgreSQL, env injection |
| `docker-compose.phase-11-verify.yml` | Disposable PostgreSQL pattern (reference only) |
| `go.mod` | Go 1.25, dependencies (pgx v5.7.2, gin, etc.) |
| `cmd/server/main.go` | Startup, config load, DB pool + CheckSchema, graceful shutdown |
| `internal/config/config.go` | `config.Load`, production fail-closed validation, env resolution |
| `pkg/database/postgres.go` | `NewPool`, `Ping`, `CheckSchema`, `ExpectedMigrationVersion` |
| `internal/handler/health_handler.go` | `/health` (liveness), `/health/ready` (readiness) |
| `Makefile` | Build/test/migrate/docker targets |
| `.github/workflows/ci.yml` | Existing CI (unchanged, preserved) |
| `docs/phase-9-production-runbook.md` | Production operating model, config contract, migration/rollback/smoke-check procedures |
| `docs/phase-9-production-checklist.md` | Production readiness checklist |
| `README.md` | Quick start, environment configuration |
| `migrations/` | 25 versioned SQL migration pairs |
| `.gitignore` / `.env.example` | Secret-exclusion tracking, example config |

---

## 13. Files Created

- `docs/phase-3a-backend-deployment-readiness-report.md` (this report — new, does not overwrite prior reports)

## 14. Commands Executed

No repository-modifying or database-modifying commands were executed during this audit. Only read-only inspection:
- `git status --short`, `git branch --show-current`, `git log --oneline -3`
- `git ls-files .env` (confirmed `.env` is not tracked)
- File reads via the read tool (no command execution)

No destructive database operations, no migrations, no deployment, no image push were performed.

---

## 15. Preservation Confirmation

- All pre-existing uncommitted changes (`.env.example`, `cmd/server/main.go`, docs, Phase 8E/10L files, prior verification reports, new migrations) were preserved untouched.
- Existing CI workflow (`.github/workflows/ci.yml`) is unchanged.
- No application code was modified.
- No deployment occurred.
- No secrets were exposed.
