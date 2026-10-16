# GO-SAAS Phase 4C — Deployment Architecture Specification

**Repository:** `github.com/dhikaarta/pay-gate-backend` (backend) + `github.com/dhikaarta/pay-gate-dashboard` (frontend, separate repo)
**Branch:** `main` (working tree contains pre-existing uncommitted changes — all preserved)
**Document type:** Design and documentation only. No implementation, no deployment, no source changes.
**Report file:** `docs/phase-4c-deployment-architecture-specification.md` (new)

---

## 1. Executive Summary

This specification defines the target deployment architecture for the GO-SAAS Payment Gateway Simulator, based on verified evidence from both the backend and frontend repositories.

**Current state:** Both applications build, test, and run locally. CI workflows exist and pass locally (remote GitHub Actions execution not yet verified). No production infrastructure (VPS, database, container registry, domain, CD workflow) has been provisioned.

**Target architecture:** A single VPS running the Go backend as a Docker container (with Docker Compose for local orchestration patterns), a separately provisioned private PostgreSQL, a TLS-terminating reverse proxy/ingress, a static frontend host serving the React/Vite SPA, and a container registry for image storage. GitHub Actions provides CI; a CD workflow is designed but not yet implemented.

**Readiness:** The application is architecture-compatible with production deployment. All gaps are external infrastructure and operational provisioning, not code defects. No code changes are required.

---

## 2. Verified Current Architecture

All claims below are verified against actual repository files (source, configuration, Dockerfiles, Compose, CI workflows, and existing Phase 3/4 audit reports).

### 2.1 Backend — Go API

| Component | Finding | Evidence |
|-----------|---------|----------|
| Entry point | `cmd/server/main.go` (package `main`) | Source |
| Go version | `go 1.25` (matches `go.mod`) | `go.mod:3`, Dockerfile |
| Framework | Gin v1.10.0 | `go.mod` |
| Database driver | `github.com/jackc/pgx/v5 v5.7.2` | `go.mod` |
| Build | Multi-stage Dockerfile (builder `golang:1.25-alpine` → runtime `alpine:3.20`) | `Dockerfile` |
| Runtime user | Non-root `appuser`, `read_only` rootfs in Compose, `no-new-privileges`, `cap_drop ALL` | `Dockerfile`, `docker-compose.yml` |
| Listen port | Container `8080`; Compose publishes `8081:8080` | `Dockerfile:48`, `docker-compose.yml:22` |
| Health endpoints | `GET /health` (liveness), `GET /health/ready` (readiness: DB + schema) | `internal/handler/health_handler.go`, `cmd/server/main.go:493-494` |
| Graceful shutdown | SIGINT/SIGTERM drain (HTTP 10s + workers 10s + pool close) | `cmd/server/main.go` |
| Stateless | No in-memory session state; access tokens in frontend memory, refresh cookie HttpOnly | `src/lib/auth/service.ts` |
| Workers | Payment expiry, outbound webhook delivery, email outbox, optional email cleanup — interval-polling, bounded-batch, context-cancellation-aware | `cmd/server/main.go` |

### 2.2 Backend — Database and Migrations

| Component | Finding | Evidence |
|-----------|---------|----------|
| Database | PostgreSQL (external to Compose; not provisioned by it) | `docker-compose.yml` comments |
| Migration tool | `golang-migrate` v4 via `migrate/migrate:v4.18.1` Docker image (NOT a Go dependency) | `docker-compose.yml:154` |
| Migration files | 25 versioned SQL pairs in `migrations/` (`000001` … `000025`), each with `.up.sql` and `.down.sql` | `migrations/` directory |
| Migration execution | One-shot Compose `migrate` service (`restart: "no"`) runs `migrate -path /migrations -database <URL> up` before `app` starts | `docker-compose.yml:153-182` |
| Schema gate | `CheckSchema()` fails startup if `schema_migrations` missing, `dirty=true`, or `version < ExpectedMigrationVersion (22)` | `pkg/database/postgres.go:66-88` |
| Auto-migration | **None.** App validates schema only; does not run migrations. | `cmd/server/main.go` |
| Connection pool | Max 25, min 5, 5-min max lifetime, 1-min max idle, 10s connect timeout | `pkg/database/postgres.go` |

### 2.3 Frontend — React/Vite SPA

| Component | Finding | Evidence |
|-----------|---------|----------|
| Build tool | Vite | `package.json`, `vite.config.ts` |
| Build output | `dist/` directory | `package.json:12` (`vite build`) |
| Framework | React 18.3.1, React Router 7.18.4 | `package.json` |
| TypeScript | `~5.6.2` | `package.json` |
| Public payment route | `/pay/:identifier` → `PublicPaymentPage` (unauthenticated) | `src/routes/index.tsx:73` |
| Dashboard routes | `/dashboard/…` behind `ProtectedRoute` (Bearer JWT) | `src/routes/index.tsx:89-110` |
| API client base URL | `VITE_API_BASE_URL` (build-time, bundled into browser build) | `src/lib/api/client.ts:13-26` |
| Simulator flag | `VITE_ENABLE_SIMULATOR` (compiled out of production builds) | `src/lib/simulator/config.ts:7-9` |
| Auth | Bearer access token in frontend memory; HttpOnly refresh cookie never read/written by JS | `src/lib/auth/service.ts:5-6` |
| Build command | `tsc -b && vite build` | `package.json:12` |
| Test command | `bash scripts/run-tests.sh` (255 tests) | `package.json:15` |

### 2.4 Existing CI Workflows

Both repositories have `.github/workflows/ci.yml`:

| Attribute | Backend | Frontend |
|-----------|---------|----------|
| Triggers | `push`/`pull_request` to `main` | `push`/`pull_request` to `main` |
| Runner | `ubuntu-latest` | `ubuntu-latest` |
| Permissions | `contents: read` | `contents: read` |
| Concurrency | `ci-${{ github.head_ref || github.run_id }}` (cancel-in-progress) | None defined |
| Go toolchain | Go 1.25 (`go-version-file: go.mod`) | N/A |
| Node toolchain | N/A | Node 22 (`actions/setup-node@v4`) |
| Dependency cache | `setup-go` cache | `setup-node` cache + `npm ci` |
| Jobs | `test` (gofmt, vet, race test, build) + `docker` (Compose config + image build) | `ci` (typecheck, lint, test, build) |
| Secrets | None | None |

### 2.5 Frontend-to-Backend URL Contract (Verified)

| Relationship | Backend variable | Frontend variable | Build-time or runtime? | Source |
|--------------|-----------------|-------------------|------------------------|--------|
| API origin for dashboard calls | N/A (serves `/api/v1/...`) | `VITE_API_BASE_URL` | Build-time (Vite bundles it) | `src/lib/api/client.ts:13-26` |
| Customer payment page origin | `FRONTEND_PUBLIC_URL` | N/A | Runtime (read by backend `config.Load`) | `internal/config/config.go:699` |
| `payment_url` returned to customer | Constructed as `{FRONTEND_PUBLIC_URL}/pay/{providerTxId}` | N/A | Runtime | `internal/service/mock_payment_provider.go:89` |
| CORS for dashboard API calls | `CORS_ALLOWED_ORIGINS` | N/A | Runtime | `internal/middleware/cors.go` |
| Invitation link origin | `DASHBOARD_BASE_URL` | N/A | Runtime | `internal/config/config.go` |

**Key distinction:** `FRONTEND_PUBLIC_URL` (backend runtime config) and `VITE_API_BASE_URL` (frontend build-time config) are independent. The frontend and backend may run on separate origins. The frontend does not reconstruct the payment URL; it uses the backend-returned value as opaque.

---

## 3. Discrepancies Between Reports and Actual Files

| Report | Claim | Actual file evidence | Discrepancy? |
|--------|-------|---------------------|--------------|
| Phase 3A | `ExpectedMigrationVersion = 22` | `pkg/database/postgres.go:23` confirms `ExpectedMigrationVersion = 22` | No |
| Phase 3A | `docker-compose.yml` has `migrate` service with `restart: "no"` | `docker-compose.yml:153-156` confirms | No |
| Phase 3A | App listens on plain HTTP behind TLS boundary | `docs/phase-9-production-runbook.md:28-33` confirms; Dockerfile has no TLS config | No |
| Phase 4A | Backend CI `docker` job builds image without push/deploy | `.github/workflows/ci.yml:46-57` confirms | No |
| Phase 4A | Frontend CI uses Node 22, `npm ci` | `.github/workflows/ci.yml` (frontend) confirms `node-version: '22'`, `npm ci` | No |
| Phase 4B | `VITE_API_BASE_URL` is build-time, fails closed in production if unset | `src/lib/api/client.ts:16-18` confirms | No |
| Phase 4B | `payment_url = {FRONTEND_PUBLIC_URL}/pay/{providerTxId}` | `internal/service/mock_payment_provider.go:89` confirms | No |
| Phase 4B | `DB_SSLMODE=verify-full` required in production | `internal/config/config.go:335` confirms | No |
| Phase 4B | `TRUSTED_PROXIES` required explicitly in production | `internal/config/config.go:587-589` confirms | No |

**No discrepancies found.** All Phase 3/4 report claims are consistent with actual repository files.

---

## 4. Proposed Target Architecture

```
Client (browser)
  |
  | HTTPS (public TLS terminated externally)
  v
TLS-terminating ingress / reverse proxy / load balancer
  |
  | HTTP on a private interface
  v
+---------------------------------------------------------------+
| PayGate API container (non-root, port 8080)                   |
|                                                               |
|  /api/v1/...  (merchant dashboard API, X-API-Key / JWT auth) |
|  /api/v1/mock-payments/:identifier  (public JSON projection) |
|  /health, /health/ready                                       |
|                                                               |
| |--------------------------- PostgreSQL (TLS verify-full)   |
| |--------------------------- Payment provider (HTTPS)       |
| |--------------------------- SMTP relay (TLS when enabled)  |
| |--------------------------- Merchant webhook destinations  |
+---------------------------------------------------------------+
  ^
  |
Static frontend SPA (served from FRONTEND_PUBLIC_URL origin)
  | serves /pay/:identifier (public payment page)
  | serves /dashboard/... (authenticated dashboard, VITE_API_BASE_URL calls API)
  |
  | Build artifact: dist/ (Vite)
  | VITE_API_BASE_URL injected at build time (HTTPS API origin)
  | VITE_ENABLE_SIMULATOR=false in production
```

**Deployment option: Single VPS with Docker Compose** — This is the simplest production-deployment strategy consistent with the existing architecture (Phase 3A §5). The repository already provides `docker-compose.yml` with a one-shot migration job and external PostgreSQL. A TLS-terminating reverse proxy (nginx, Traefik, or Caddy) on the same VPS, bound to `127.0.0.1` or a private interface, satisfies the runbook's TLS-boundary requirement.

**Managed container platforms** are viable but introduce platform-specific secret/ingress concepts not present in the repository; they are a reasonable follow-up once the Compose-based path is proven.

**No exact hosting prices are quoted.** Cost and operational complexity are assessed qualitatively.

---

## 5. Component Responsibilities

| Component | Responsibility | Implemented? | Source |
|-----------|---------------|--------------|--------|
| Frontend SPA | Serve `/pay/:identifier` (public), `/dashboard/...` (auth); call backend API via `VITE_API_BASE_URL` | Yes (build artifact `dist/`) | `src/routes/`, `src/lib/api/client.ts` |
| Backend API | Merchant payment lifecycle, public mock-payment JSON projection, health/readiness, auth, webhooks | Yes | `cmd/server/main.go`, `internal/handler/` |
| PostgreSQL | Transaction storage, idempotency, audit, sessions, webhook deliveries | External (not provisioned by Compose) | `docker-compose.yml`, `migrations/` |
| Migration job | One-shot `migrate up` before app startup | Yes (Compose `migrate` service) | `docker-compose.yml:153-182` |
| Reverse proxy / ingress | Public TLS termination, forward to private backend listener | Not implemented | External |
| Container registry | Store tagged backend images | Not implemented | External |
| Secret management | Inject `DB_PASSWORD`, `AUTH_JWT_SECRET`, etc. at runtime | Not implemented | External |
| Frontend host | Serve static `dist/` with SPA history fallback | Not implemented | External |
| GitHub Actions CI | gofmt, vet, test, build, Docker validation | Yes (existing `ci.yml`) | `.github/workflows/ci.yml` |
| GitHub Actions CD | Image promotion, migration, deploy, rollback | Not implemented | Designed in Phase 3A §8 |

---

## 6. URL and Environment Configuration Matrix

| Variable | Set by | Build-time or runtime? | Repository | Production requirement |
|----------|--------|------------------------|------------|-----------------------|
| `VITE_API_BASE_URL` | Frontend build pipeline | Build-time (bundled in browser) | Frontend | Must be production HTTPS API origin |
| `VITE_ENABLE_SIMULATOR` | Frontend build pipeline | Build-time | Frontend | Must be `false` in production |
| `FRONTEND_PUBLIC_URL` | Backend runtime env | Runtime | Backend | Must be production frontend origin serving `/pay/:identifier` |
| `CORS_ALLOWED_ORIGINS` | Backend runtime env | Runtime | Backend | Must include production frontend origin (explicit HTTPS) |
| `DASHBOARD_BASE_URL` | Backend runtime env | Runtime | Backend | Must be HTTPS in production when email enabled |
| `APP_ENV` | Backend runtime env | Runtime | Backend | Must be `production` |
| `DB_*` | Backend runtime env | Runtime | Backend | Private PostgreSQL, `DB_SSLMODE=verify-full` |
| `PAYMENT_PROVIDER` | Backend runtime env | Runtime | Backend | Must not be `mock` in production |
| `MIDTRANS_BASE_URL`/`MIDTRANS_SERVER_KEY` | Backend runtime env | Runtime | Backend | Explicit non-sandbox HTTPS URL + key |
| `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `DB_PASSWORD`, etc. | Secret management | Runtime | Backend | Injected externally, never committed |

**Build-time vs runtime:** `VITE_API_BASE_URL` and `VITE_ENABLE_SIMULATOR` are baked into the frontend at build time and cannot be changed without rebuilding. All `DB_*`, `APP_ENV`, `FRONTEND_PUBLIC_URL`, `CORS_ALLOWED_ORIGINS`, and secret variables are backend runtime configuration injected via environment.

---

## 7. Database and Migration Strategy

### 7.1 Database isolation and connectivity
- PostgreSQL must be provisioned externally on a private network (not publicly exposed).
- Separate staging and production databases and credentials.
- `DB_SSLMODE=verify-full` required in production (certificate-verified TLS).
- Connection pool defaults: max 25, min 5, 5-min max lifetime, 1-min max idle (not benchmarked; must be validated).

### 7.2 TLS requirements
- PostgreSQL: certificate-verified TLS (`DB_SSLMODE=verify-full`).
- Backend listener: plain HTTP behind TLS-terminating ingress; must not be exposed directly to public Internet.
- Provider endpoints: HTTPS, non-sandbox in production.
- Merchant webhook destinations: HTTPS required in production (`WEBHOOK_REQUIRE_HTTPS=true`).
- SMTP: STARTTLS or implicit TLS when email enabled.

### 7.3 Migration sequencing
1. Provision PostgreSQL on private network with certificate-verified TLS.
2. Create separate staging/production database users and databases.
3. Take and verify a backup before the first migration (if database contains data).
4. Run one-shot migration job with explicit `DATABASE_URL`/`DB_URL` (or `MIGRATION_DATABASE_URL` for Compose when credentials contain URL-special characters).
5. Confirm `schema_migrations.version` and `dirty=false`.
6. Start application and wait for `/health/ready` to return 200.
7. Run smoke checks.

### 7.4 Backup requirements
- PostgreSQL-native logical backup (`pg_dump --format=custom`) before first migration and before upgrades.
- Backups contain sensitive business data; encryption at rest, access logging, retention, and geographic storage are operator responsibilities.
- Secrets are **not** in database dumps; restore secrets separately.
- Restore into a disposable database first; never restore over production without approval.

### 7.5 Readiness verification
- `GET /health/ready` returns 200 only when PostgreSQL is reachable and schema is clean at/above `ExpectedMigrationVersion (22)`.
- Container healthcheck uses readiness; a database/schema failure prevents the container from being considered healthy.

### 7.6 Migration failure handling
- A failed migration blocks app startup (Compose `depends_on: condition: service_completed_successfully`).
- Do not automatically run destructive `down` migrations. `make migrate-down` is guarded by `ALLOW_DESTRUCTIVE_DOWN=true`; down paths (e.g. migration 17) are not guaranteed to restore the exact prior schema.
- If a migration is irreversible, use a forward fix or an approved backup restore procedure.

### 7.7 Application rollback
- **Application rollback preferred** when migration is backward-compatible: roll back the container image, restore previous configuration, verify readiness.
- Keep the previous image available for rollback.
- Database rollback is **not** recommended automatically; use forward fix or backup restore for irreversible migrations.

### 7.8 Staging verification
- Staging must use `APP_ENV=production` to exercise the same fail-closed configuration.
- Staging must use separate PostgreSQL, provider account/environment, webhook endpoints, SMTP, JWT/admin/secrets, backup location, and synthetic data.
- Never point staging at a production database or use production secrets.

---

## 8. Security Requirements

| Area | Requirement | Source |
|------|-------------|--------|
| Secret injection | Secrets (`DB_PASSWORD`, `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `MIDTRANS_SERVER_KEY`, `MOCK_WEBHOOK_SECRET`, `ADMIN_API_KEY`, `SMTP_USERNAME/PASSWORD`, `MIGRATION_DATABASE_URL`) injected via deployment platform or external secret manager at runtime; never committed, never in image layers, never in CI logs | `docs/phase-9-production-runbook.md:3` |
| GitHub Actions permissions | `contents: read` only; no deployment credentials, no package write access, no production secrets in workflow files | `.github/workflows/ci.yml` (both repos) |
| Container registry credentials | Injected via GitHub repository/environment secrets; never in workflow files | Phase 3A §8 |
| Trusted proxy configuration | `TRUSTED_PROXIES` must be explicitly `none` or IP/CIDR list in production; `config.Load` rejects unset value | `internal/config/config.go:587-589` |
| HTTPS and secure cookies | Public TLS terminated at ingress; backend listens on plain HTTP on private interface; refresh cookie set with `Secure` and `SameSite`; frontend served over HTTPS | `docs/phase-9-production-runbook.md:7`, `src/lib/auth/service.ts` |
| CORS | Explicit origins only; never `*` with credentials; `CORS_ALLOWED_ORIGINS` must include production frontend origin | `internal/middleware/cors.go` |
| Frontend security headers | SPA served over HTTPS; secure contexts for cookies | `src/lib/api/client.ts:22` |
| Sensitive logging | Structured JSON logs; no raw bodies, credentials, cookies, tokens, webhook secrets, or DB connection strings | `docs/phase-9-production-runbook.md:10` |
| Database network access | PostgreSQL on private network, not publicly exposed; restricted by network policy/firewall | `docs/phase-9-production-runbook.md:7` |
| SSRF protection | Loopback/metadata/private destinations blocked at dial boundary | `internal/ssrf/ssrf.go` |
| Rate limiting | `RATE_LIMIT_ENABLED=false` rejected in production; in-process fixed windows | `internal/config/config.go:558` |
| Simulator | `PAYMENT_PROVIDER=mock` and `PAYMENT_SIMULATOR_ENABLED=true` rejected in production | `internal/config/config.go:537,557` |

---

## 9. Deployment Workflow Design

Design only — not implemented. Extends the existing `.github/workflows/ci.yml` (unchanged).

### 9.1 CI workflow (existing, unchanged)
- **Backend:** `test` job (gofmt, vet, race test, build) + `docker` job (Compose config validation, image build).
- **Frontend:** `ci` job (typecheck, lint, test, build).
- Both on `push`/`pull_request` to `main`, `ubuntu-latest`, `contents: read`.

### 9.2 CD workflow (proposed, not implemented)
Separate `.github/workflows/cd.yml`, triggered by tags or manual dispatch:

| Stage | Action | Details |
|-------|--------|---------|
| A: Build | Validate Compose model, build backend image | Existing CI `docker` job |
| B: Image promotion | Tag image with commit SHA/semver, push to registry, record digest | Registry credentials from GitHub secrets |
| C: Migration | One-shot `migrate/migrate:v4.18.1` job against target `MIGRATION_DATABASE_URL`; verify `version`/`dirty=false`; fail deployment on migration failure | Before app deploy |
| D: Deploy | Roll out new image; wait for `/health/ready` 200; run smoke checks | Rolling update or blue/green |
| E: Rollback | Roll back to previous image, restore configuration, verify readiness; no automatic destructive `down` migrations | Manual/on-failure |

**CI/CD separation:** Keep `ci.yml` (PR/push checks) unchanged. Add `cd.yml` (release/deployment) with deployment credentials injected via GitHub secrets, never committed.

---

## 10. Rollback Strategy

| Scenario | Action |
|----------|--------|
| Application issue after deploy | Roll back container image to previous known-good tag; restore previous configuration; verify `/health/ready`. |
| Schema migration issue | Do **not** run automatic `down` migrations. Use forward fix or approved backup restore. |
| Data corruption | Restore from backup into disposable database first; verify `schema_migrations.version`/`dirty=false`; restore secrets separately. |
| Graceful shutdown | Allow at least Compose `stop_grace_period` (20 seconds) during rollout/rollback so workers drain and pool closes. |

---

## 11. Infrastructure Prerequisites

| Prerequisite | Status | Notes |
|--------------|--------|-------|
| Private PostgreSQL (TLS verify-full) | Not provisioned | External to repository; required for production |
| TLS-terminating ingress/reverse proxy | Not provisioned | External; app listens on plain HTTP behind it |
| Container registry | Not configured | Required for image storage and promotion |
| Secret management | Not configured | Deployment platform or external secret manager |
| Static frontend hosting | Not provisioned | Must serve `dist/` with SPA history fallback at `FRONTEND_PUBLIC_URL` |
| Persistent storage for product images | Not provisioned | Docker named volume or persistent disk |
| GitHub Actions CD workflow | Not implemented | Described in Phase 3A §8; not yet created |
| Remote GitHub Actions CI verification | Not performed | `gh` CLI unavailable; local validation passes |

---

## 12. Ordered Implementation Roadmap

| # | Phase | Dependencies | Acceptance criteria |
|---|-------|--------------|---------------------|
| 1 | Verify remote GitHub Actions CI | Push workflow files to `main` or open test PR | Both `test` and `docker`/`ci` jobs pass on GitHub Actions |
| 2 | Select hosting providers and deployment regions | None (decision) | Provider/region selected; cost and operational complexity assessed qualitatively |
| 3 | Provision staging infrastructure | #2 | Staging VPS, PostgreSQL, ingress, frontend host, registry, secret management provisioned |
| 4 | Configure PostgreSQL and secrets | #3 | Migration runs successfully; `/health/ready` returns 200; all required production secrets injected |
| 5 | Implement backend CD | #1, #3 | `.github/workflows/cd.yml` created; image promotion, migration, deploy stages work in staging |
| 6 | Implement frontend CD | #1, #3 | Frontend build with production `VITE_API_BASE_URL`; static artifact deployed to frontend host |
| 7 | Test payment flows in staging | #5, #6 | Create payment → open `payment_url` → public API lookup → simulate success/fail → dashboard consistency |
| 8 | Validate backup and rollback | #3, #7 | Backup/restore drill in disposable database; image rollback tested or rehearsed |
| 9 | Define production release criteria | #7, #8 | Release criteria documented; operator/DBA/security owners assigned |

---

## 13. Explicit Unresolved Decisions

| Decision | Why unresolved | Who must decide |
|----------|---------------|-----------------|
| Hosting provider and region | Not specified in repository; operator choice | Infrastructure operator |
| Container registry provider | Not specified in repository | Infrastructure operator |
| Secret management solution | Not specified in repository | Security/operations team |
| Frontend hosting provider | Not specified in repository | Frontend/operations team |
| TLS certificate provider | Not specified in repository | Operations team |
| Database backup schedule and retention (RPO/RTO) | Not defined in repository; operator decision | DBA/operator |
| CD workflow trigger (tags vs. manual dispatch) | Design choice | Release engineering team |
| Staging vs. production promotion gates | Process decision | Release engineering team |

---

## 14. Files Inspected

| File | Repository | Purpose |
|------|-----------|---------|
| `docs/phase-3a-backend-deployment-readiness-report.md` | Backend | Prior audit report |
| `docs/phase-4a-ci-verification-report.md` | Backend | Prior CI verification report |
| `docs/phase-4b-backend-local-to-production-report.md` | Backend | Prior local-to-production report |
| `docs/phase-4b-frontend-local-to-production-report.md` | Frontend | Prior local-to-production report |
| `docs/phase-9-production-runbook.md` | Backend | Production operating model |
| `docs/phase-9-production-checklist.md` | Backend | Production readiness checklist |
| `internal/config/config.go` | Backend | Configuration loading and production validation |
| `internal/middleware/cors.go` | Backend | CORS policy |
| `internal/ssrf/ssrf.go` | Backend | SSRF protection |
| `pkg/database/postgres.go` | Backend | Connection pool, schema check, expected migration version |
| `cmd/server/main.go` | Backend | Startup, health routes, graceful shutdown |
| `internal/handler/health_handler.go` | Backend | `/health` and `/health/ready` |
| `internal/service/mock_payment_provider.go` | Backend | `payment_url` construction |
| `Dockerfile` | Backend | Multi-stage build, non-root runtime, healthcheck |
| `docker-compose.yml` | Backend | App + migrate services, environment injection |
| `.env.example` | Backend | Development configuration template |
| `go.mod` | Backend | Go 1.25, dependencies |
| `.env.example`, `.env.development`, `.env.production` | Frontend | Frontend environment templates |
| `package.json` | Frontend | Scripts, engines, dependencies |
| `src/lib/api/client.ts` | Frontend | `VITE_API_BASE_URL` build-time config |
| `src/lib/simulator/config.ts` | Frontend | `VITE_ENABLE_SIMULATOR` |
| `src/lib/auth/service.ts` | Frontend | Auth, HttpOnly refresh cookie |
| `src/routes/index.tsx` | Frontend | Route table (`/pay/:identifier`) |
| `.github/workflows/ci.yml` | Both repos | CI workflows |

---

## 15. Files Created

- `docs/phase-4c-deployment-architecture-specification.md` (this report — new)

---

## 16. Files Modified

**None.** No application source code, CI workflow, Docker configuration, migration, or configuration files were modified by this audit. All pre-existing user changes in both repositories were preserved.

---

## 17. Commands Executed

No repository-modifying or database-modifying commands were executed. Only read-only file inspection was performed (reading existing reports and source files).

---

## 18. Overall Readiness

**Architecture specification complete.** The target architecture is suitable for the existing implementation. All gaps are external infrastructure and operational provisioning. No code changes are required. The ordered implementation roadmap (Section 12) provides the path from current state to production deployment.

**Decisions still required before implementation:** Hosting provider/region, container registry, secret management solution, frontend hosting provider, TLS certificate provider, backup RPO/RTO, CD workflow trigger, and staging vs. production promotion gates (Section 13).
