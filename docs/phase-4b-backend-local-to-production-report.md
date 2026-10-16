# GO-SAAS Phase 4B — Backend: Local-to-Production Configuration Audit

**Repository:** `github.com/dhikaarta/pay-gate-backend`
**Branch:** `main` (working tree contains pre-existing uncommitted changes — all preserved)
**Audit type:** Audit and documentation only. No implementation, no deployment, no source changes.
**Report file:** `docs/phase-4b-backend-local-to-production-report.md` (new)

---

## 1. Overall Readiness

**Backend: READY WITH CONFIGURATION REQUIRED**

The application builds, starts, and passes all local validation. Moving from `localhost` to production requires external infrastructure (private PostgreSQL, TLS-terminating ingress, secret injection) and explicit operator configuration. No code changes are required to support production deployment; the remaining work is operational provisioning.

---

## 2. Configuration Inventory

All backend configuration is loaded from environment variables via `internal/config/config.go` (`config.Load()`). `.env` is a development convenience only, is gitignored, and is **not tracked**. Production values must be injected by the deployment platform or an external secret manager.

Secret values are classified but **never printed** in this report.

| # | Variable | Component | Required? | Secret? | Dev-specific? | Different in prod? | Source file |
|---|----------|-----------|-----------|---------|---------------|--------------------|-------------|
| 1 | `APP_NAME` | App name (logs, startup metadata) | No | No | No | No | `internal/config/config.go`, `docker-compose.yml` |
| 2 | `APP_ENV` | Configuration validation gate | Yes | No | Yes (default `development`) | **YES** — must be `production` | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 3 | `APP_PORT` | HTTP listener port | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 4 | `APP_BIND_HOST` | Listener bind interface | No | No | Yes (loopback for dev recovery) | Maybe | `internal/config/config.go`, `.env.example` |
| 5 | `FRONTEND_PUBLIC_URL` | Browser-facing origin for `payment_url` | Yes (when `PAYMENT_PROVIDER=mock`) | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 6 | `MOCK_PAYMENT_BASE_URL` | Deprecated alias for `FRONTEND_PUBLIC_URL` | No | No | No | No | `internal/config/config.go`, `.env.example` |
| 7 | `PAYMENT_PROVIDER` | Provider selection | No | No | Yes (default `mock`) | **YES** — must not be `mock` in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 8 | `PAYMENT_PROVIDER_TIMEOUT` | Provider call timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 9 | `MIDTRANS_BASE_URL` | Midtrans provider endpoint | Yes (when `provider=midtrans` in prod) | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 10 | `MIDTRANS_SERVER_KEY` | Midtrans authentication | Yes (when `provider=midtrans`) | **YES** | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 11 | `MIDTRANS_CLIENT_KEY` | Midtrans client key | No | **YES** | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 12 | `IDEMPOTENCY_TTL` | Idempotency key expiry | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 13 | `PAYMENT_EXPIRY_ENABLED` | Expiry worker on/off | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 14 | `PAYMENT_EXPIRY_INTERVAL` | Expiry worker poll interval | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 15 | `PAYMENT_EXPIRY_BATCH_SIZE` | Expiry worker batch size | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 16 | `MOCK_WEBHOOK_SECRET` | Mock webhook HMAC signing | Yes (in prod, ≥32 bytes) | **YES** | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 17 | `ADMIN_API_KEY` | Admin/ops API key | No (empty disables routes) | **YES** | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 18 | `SETTLEMENT_RECON_STALE_AFTER` | Reconciliation stale threshold | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 19 | `WEBHOOK_DELIVERY_ENABLED` | Outbound webhook worker | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 20 | `WEBHOOK_DELIVERY_INTERVAL` | Webhook worker poll interval | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 21 | `WEBHOOK_DELIVERY_BATCH_SIZE` | Webhook worker batch size | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 22 | `WEBHOOK_DELIVERY_TIMEOUT` | Webhook delivery timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 23 | `WEBHOOK_MAX_ATTEMPTS` | Webhook max delivery attempts | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 24 | `WEBHOOK_DELIVERY_STALE_AFTER` | Webhook stale threshold | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 25 | `WEBHOOK_SECRET_ENCRYPTION_KEY` | Webhook secret at-rest encryption | Yes (in prod) | **YES** | Yes (dev fallback exists) | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 26 | `WEBHOOK_REQUIRE_HTTPS` | Webhook destination TLS policy | Yes (in prod) | No | Yes (default true in prod) | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 27 | `AUTH_JWT_SECRET` | Dashboard JWT signing | Yes (in prod, ≥32 bytes) | **YES** | Yes (dev fallback exists) | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 28 | `AUTH_ACCESS_TOKEN_TTL` | Access token lifetime | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 29 | `AUTH_REFRESH_TOKEN_TTL` | Refresh/session lifetime | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 30 | `INVITATION_TOKEN_TTL` | Invitation token validity | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 31 | `PASSWORD_RESET_TOKEN_TTL` | Password-reset token validity | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 32 | `DEV_RECOVERY_ENABLED` | Dev-only account recovery | No | No | **YES** (development only) | **YES** — must be disabled in prod | `internal/config/config.go`, `.env.example` |
| 33 | `DEV_RECOVERY_KEY` | Dev recovery key | No (if disabled) | **YES** | **YES** | **YES** — must not be set in prod | `internal/config/config.go`, `.env.example` |
| 34 | `APP_BIND_HOST` | Listener bind interface | No | No | Yes (loopback for dev recovery) | Maybe | `internal/config/config.go`, `.env.example` |
| 35 | `DASHBOARD_BASE_URL` | Dashboard SPA origin (invitation links) | Yes (when `EMAIL_ENABLED=true`) | No | No | **YES** — must be HTTPS in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 36 | `EMAIL_ENABLED` | Outbound email on/off | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 37 | `SMTP_HOST` | SMTP server | Yes (when email enabled) | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 38 | `SMTP_PORT` | SMTP port | Yes (when email enabled) | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 39 | `SMTP_USERNAME` | SMTP auth user | Yes (when SMTP auth required) | **YES** | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 40 | `SMTP_PASSWORD` | SMTP auth password | Yes (when SMTP auth required) | **YES** | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 41 | `SMTP_FROM` | Default sender | Yes (when email enabled) | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 42 | `SMTP_TLS` | SMTP transport security | Yes (when email enabled) | No | No | **YES** — production rejects `none` | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 43 | `SMTP_TIMEOUT` | Per-send SMTP timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 44 | `EMAIL_WORKER_*` (5 vars) | Email outbox worker | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 45 | `EMAIL_CLEANUP_*` (5 vars) | Email retention cleanup | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 46 | `CORS_ALLOWED_ORIGINS` | CORS policy for dashboard | No | No | Yes (localhost default) | **YES** — must be explicit HTTPS in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 47 | `PRODUCT_IMAGE_DIR` | Persistent image storage path | No | No | No | Maybe (should be persistent volume) | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 48 | `PRODUCT_IMAGE_MAX_BYTES` | Max image upload size | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 49 | `PRODUCT_IMAGE_MAX_DIMENSION` | Max image dimension | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 50 | `PRODUCT_IMAGE_MAX_PIXELS` | Max image pixel count | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 51 | `DB_HOST` | PostgreSQL host | **YES** | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 52 | `DB_PORT` | PostgreSQL port | **YES** | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 53 | `DB_USER` | PostgreSQL user | **YES** | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 54 | `DB_PASSWORD` | PostgreSQL password | **YES** | **YES** | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 55 | `DB_NAME` | PostgreSQL database name | **YES** | No | No | **YES** | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 56 | `DB_SSLMODE` | PostgreSQL TLS mode | **YES** | No | Yes (default `disable`) | **YES** — must be `verify-full` in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 57 | `MIGRATION_DATABASE_URL` | Explicit migration connection URL | Yes (in prod/staging) | **YES** | No | **YES** | `docker-compose.yml`, `.env.example` |
| 58 | `PAYMENT_SIMULATOR_ENABLED` | Payment simulator on/off | No | No | **YES** (development only) | **YES** — rejected in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 59 | `SIMULATOR_MERCHANT_ID` | Simulator-allowed merchant UUID | No (if simulator disabled) | No | **YES** | **YES** — simulator rejected in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 60 | `LEGACY_API_CREDENTIALS_ENABLED` | Legacy plaintext auth window | No | No | Yes | **YES** — fail-safe closed in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 61 | `RATE_LIMIT_ENABLED` | Rate limiting on/off | No | No | No | **YES** — must be enabled in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 62 | `RATE_LIMIT_LOGIN_PER_MINUTE` | Login rate limit | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 63 | `RATE_LIMIT_ADMIN_PER_MINUTE` | Admin rate limit | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 64 | `RATE_LIMIT_API_PER_MINUTE` | Merchant API rate limit | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 65 | `HTTP_MAX_BODY_BYTES` | Request body cap | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 66 | `HTTP_READ_HEADER_TIMEOUT` | Slow-header timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 67 | `HTTP_READ_TIMEOUT` | Read timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 68 | `HTTP_WRITE_TIMEOUT` | Write timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 69 | `HTTP_IDLE_TIMEOUT` | Idle keep-alive timeout | No | No | No | Maybe | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |
| 70 | `TRUSTED_PROXIES` | X-Forwarded-For trust policy | Yes (in prod) | No | Yes (unset allowed in dev) | **YES** — must be explicit in prod | `internal/config/config.go`, `docker-compose.yml`, `.env.example` |

**Secret variables (never print, never commit):** `DB_PASSWORD`, `MIDTRANS_SERVER_KEY`, `MIDTRANS_CLIENT_KEY`, `MOCK_WEBHOOK_SECRET`, `ADMIN_API_KEY`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `AUTH_JWT_SECRET`, `DEV_RECOVERY_KEY`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `MIGRATION_DATABASE_URL`.

**Development-only variables (must NOT be used in production):** `DB_SSLMODE=disable`, `PAYMENT_PROVIDER=mock`, `PAYMENT_SIMULATOR_ENABLED=true`, `DEV_RECOVERY_ENABLED=true`, dev fallback encryption/JWT values, `CORS_ALLOWED_ORIGINS=http://localhost:5173`, `DASHBOARD_BASE_URL=http://localhost:5173`.

---

## 3. Localhost and URL Dependencies

| # | Location | Value | Classification | Notes |
|---|----------|-------|----------------|-------|
| 1 | `.env.example:11` | `FRONTEND_PUBLIC_URL=http://localhost:5173` | Expected development configuration | Frontend Vite dev server origin; must be replaced with production frontend origin |
| 2 | `.env.example:169` | `DASHBOARD_BASE_URL=http://localhost:5173` | Expected development configuration | Invitation link origin; must be HTTPS in prod |
| 3 | `.env.example:228` | `CORS_ALLOWED_ORIGINS=http://localhost:5173` | Expected development configuration | Must be explicit HTTPS origins in prod |
| 4 | `.env.example:80` | `DB_HOST=host.docker.internal` | Expected development configuration | Docker Compose internal host; not valid outside Docker |
| 5 | `docker-compose.yml:34` | `FRONTEND_PUBLIC_URL: ${FRONTEND_PUBLIC_URL:-http://localhost:5173}` | Expected development configuration | Compose default; production must override |
| 6 | `docker-compose.yml:105` | `CORS_ALLOWED_ORIGINS: ${CORS_ALLOWED_ORIGINS:-http://localhost:5173}` | Expected development configuration | Compose default; production must override |
| 7 | `docker-compose.yml:78` | `DB_HOST: ${DB_HOST:-host.docker.internal}` | Expected development configuration | Compose default; production must override |
| 8 | `docker-compose.yml:22` | `ports: "8081:8080"` | Expected development configuration | Host port mapping; production may differ |
| 9 | `swagger/docs.go:8952` | `Host: "localhost:8081"` | Example/documentation only | Generated Swagger metadata; not runtime-critical |
| 10 | `internal/config/config_dev_recovery_test.go` | `127.0.0.1` | Expected development configuration | Test-only; enforces loopback binding for dev recovery |
| 11 | `internal/config/config_bind_host_test.go` | `127.0.0.1` | Expected development configuration | Test-only; validates bind host parsing |
| 12 | `internal/middleware/cors.go:22` | `CORS_ALLOWED_ORIGINS=http://localhost:5173` | Example/documentation only | Comment example |
| 13 | `internal/config/config_simulator_test.go` | `http://localhost:5173` | Expected development configuration | Test-only |
| 14 | `internal/service/mock_payment_provider_test.go` | `http://localhost:5173/pay/...` | Expected development configuration | Test-only; validates payment URL construction |
| 15 | `internal/middleware/cors_test.go` | `http://localhost:5173` | Expected development configuration | Test-only |
| 16 | `internal/ssrf/ssrf.go` | `localhost`, `127.0.0.1`, `0.0.0.0` | Potential production risk (mitigated) | SSRF protection intentionally blocks loopback/metadata destinations — this is correct production behavior |
| 17 | Various `*_test.go` files | `127.0.0.1`, `localhost` | Expected development configuration | Test-only (test servers, local SMTP, local DB) |

**No automatic replacement of localhost values is recommended.** Each production deployment must consciously replace development origins with the actual production frontend/dashboard origins.

---

## 4. Frontend-to-Backend Integration

The frontend repository (`pay-gate-dashboard`) is separate. Integration points verified from backend source:

### 4.1 `VITE_API_BASE_URL` (frontend) → Backend API origin
- **Backend role:** The backend does not read `VITE_API_BASE_URL`; it is a frontend build-time variable bundled into the browser build.
- **Backend expectation:** The backend serves API routes under `/api/v1/...` on `APP_PORT` (default `8080` in container, `8081` on Docker host).
- **Production requirement:** Frontend `VITE_API_BASE_URL` must point at the backend API origin (e.g. `https://api.example.com`), must use HTTPS in production, and is validated at frontend build time (`src/lib/api/client.ts`).

### 4.2 `FRONTEND_PUBLIC_URL` → `payment_url`
- **Backend role:** `resolveFrontendPublicURL()` (`internal/config/config.go:699`) reads `FRONTEND_PUBLIC_URL` (with deprecated `MOCK_PAYMENT_BASE_URL` fallback), normalizes trailing slash, validates it is an absolute URL without userinfo/query/fragment.
- **Usage:** `MockPaymentProvider.CreatePayment()` constructs `payment_url = {FRONTEND_PUBLIC_URL}/pay/{providerTransactionId}` (e.g. `http://localhost:5173/pay/MOCK-TXN-abc123`).
- **Production requirement:** Must point at the production frontend origin serving `/pay/:identifier`. Must be set explicitly in production; required when `PAYMENT_PROVIDER=mock`.

### 4.3 `CORS_ALLOWED_ORIGINS`
- **Backend role:** `internal/middleware/cors.go` — explicit origin list, never `*` when credentials allowed, sets `Access-Control-Allow-Credentials: true` only for matching origins.
- **Production requirement:** Must be explicit HTTPS origins (e.g. `https://dashboard.example.com`). Empty = no cross-origin access.

### 4.4 Public payment route
- **Backend role:** `GET /api/v1/mock-payments/:identifier` and `GET /pay/:identifier` (`cmd/server/main.go:497,519`), unauthenticated, returns `publicMockPayment` JSON (`reference, amount, currency, payment_method, status, expired_at, provider_transaction_id, qris_payload`).
- **Production requirement:** The frontend SPA route `/pay/:identifier` must be served by the frontend origin (`FRONTEND_PUBLIC_URL`); the backend JSON projection is a separate route on the API origin. Do not assume same-origin deployment.

### 4.5 API authentication
- **Backend role:** `POST /api/v1/payments` and merchant endpoints require `X-API-Key` (compound `pk_<hex>:sk_<hex>` Phase 5C format, or bare legacy `pk_<hex>` while migration window is open).
- **Production requirement:** API keys injected externally; legacy credential migration window (`LEGACY_API_CREDENTIALS_ENABLED`) is fail-safe closed in production.

### 4.6 Cookie behavior
- **Backend role:** The backend does not set cookies. Dashboard authentication uses Bearer access tokens (in frontend memory) and HttpOnly refresh cookies (set by backend auth endpoints). The refresh cookie is HttpOnly and never read/written by JavaScript.
- **Production requirement:** Refresh cookie must be set over HTTPS with `Secure` and `SameSite` attributes; the backend auth endpoints must be reachable over the TLS-terminating ingress.

---

## 5. Database and Migration Requirements

### 5.1 Required PostgreSQL configuration
- **Host/Port/User/Password/Name:** All required at startup (`requiredEnv` in `config.Load()`).
- **SSLMode:** `disable` in development; **`verify-full` required in production** (certificate-verified TLS).
- **Connection pool:** `NewPool()` (`pkg/database/postgres.go:28`) — max 25 conns, min 5, 5-min max lifetime, 1-min max idle, 10s connect timeout. Ping at startup.
- **Capacity:** Not benchmarked; must be validated against expected traffic.

### 5.2 Migration execution mechanism
- **Tool:** `golang-migrate` v4, provided via `migrate/migrate:v4.18.1` Docker image (NOT a Go dependency).
- **Directory:** `migrations/` — 25 versioned SQL pairs (`000001` … `000025`), each with `.up.sql` and `.down.sql`.
- **Execution:** One-shot Compose `migrate` service runs `migrate -path /migrations -database <URL> up` with `restart: "no"`.
- **URL:** Production/staging must inject `MIGRATION_DATABASE_URL` (complete URL with URL-encoded credentials). Compose fallback assembled from `DB_*` variables is development-only.

### 5.3 Startup schema validation
- `CheckSchema()` (`pkg/database/postgres.go:69`) queries `schema_migrations` and fails startup if:
  - Table missing or unreadable
  - `dirty = true`
  - `version < ExpectedMigrationVersion` (currently 22)
- `cmd/server/main.go` calls `CheckSchema` after pool creation, before listening. Failure → `os.Exit(1)` (fail closed).
- The app does **not** auto-migrate.

### 5.4 Backup prerequisites
- PostgreSQL-native logical backup (`pg_dump --format=custom`) recommended.
- Backup before first migration and before upgrades.
- Backups contain sensitive business data (invitations, webhook evidence, payment attempts, refresh-session hashes); encryption at rest and access control are operator responsibilities.
- Secrets are **not** in the database dump; restore secrets separately.

### 5.5 Risks when deploying a new application version
- New binary may require schema at/above `ExpectedMigrationVersion`; old schema → startup failure.
- Migration must run before app startup (Compose `depends_on: condition: service_completed_successfully`).
- Down migrations are destructive and guarded (`ALLOW_DESTRUCTIVE_DOWN=true`); not guaranteed to restore exact prior schema.
- Rollback prefers image rollback, not `down` migrations.

### 5.6 Safe migration sequencing
1. Provision PostgreSQL on private network with certificate-verified TLS.
2. Create separate staging/production database users and databases.
3. Take and verify a backup before first migration (if database contains data).
4. Run one-shot migration job with explicit `DATABASE_URL`/`DB_URL` (or `MIGRATION_DATABASE_URL`).
5. Confirm `schema_migrations.version` and `dirty=false`.
6. Start application and wait for `/health/ready` to return 200.
7. Run smoke checks.

---

## 6. Production Security Prerequisites

| Area | Requirement | Status | Source |
|------|-------------|--------|--------|
| HTTPS termination | External to Go process; app listens on plain HTTP behind TLS boundary | Not implemented | `docs/phase-9-production-runbook.md:7` |
| App listener exposure | Must not be exposed directly to public Internet; private/loopback-only | Not implemented | `docs/phase-9-production-runbook.md:7` |
| PostgreSQL TLS | `DB_SSLMODE=verify-full` required in production | Config enforced | `internal/config/config.go:335` |
| Trusted proxies | `TRUSTED_PROXIES` must be explicitly `none` or IP/CIDR list in prod | Config enforced | `internal/config/config.go:587` |
| Webhook HTTPS | `WEBHOOK_REQUIRE_HTTPS` must remain true in prod | Config enforced | `internal/config/config.go:594` |
| Provider endpoint | Must be explicit non-sandbox HTTPS URL without userinfo in prod | Config enforced | `internal/config/config.go:369` |
| Secrets | Injected externally; never committed, never in image, never logged | Config enforced | `internal/config/config.go` (production gates) |
| CORS | Explicit origins; never `*` with credentials | Config enforced | `internal/middleware/cors.go` |
| SSRF | Loopback/metadata/private destinations blocked at dial boundary | Implemented | `internal/ssrf/ssrf.go` |
| Rate limiting | `RATE_LIMIT_ENABLED=false` rejected in production | Config enforced | `internal/config/config.go:558` |
| Simulator | `PAYMENT_SIMULATOR_ENABLED=true` rejected in production | Config enforced | `internal/config/config.go:537` |
| Dev recovery | Rejected outside development; requires loopback binding | Config enforced | `internal/config/config.go:577` |
| Health/readiness | `/health` (liveness), `/health/ready` (DB + schema, 503 on failure) | Implemented | `internal/handler/health_handler.go` |
| Container security | Non-root user, `no-new-privileges`, `cap_drop ALL`, `read_only` rootfs | Implemented | `Dockerfile`, `docker-compose.yml` |
| Logging | Structured JSON; no credentials/bodies/tokens in logs | Implemented | `cmd/server/main.go`, `docs/phase-9-production-runbook.md:10` |
| Graceful shutdown | SIGINT/SIGTERM drain (HTTP 10s + workers 10s + pool close) | Implemented | `cmd/server/main.go` |

---

## 7. Deployment Readiness Checklist

### Already implemented (in repository)
- Multi-stage Dockerfile, non-root runtime user, healthcheck, graceful shutdown
- Fail-closed configuration validation for production
- One-shot migration job separate from app startup
- Schema version/dirty gate at startup and readiness
- `/health` and `/health/ready` endpoints
- SSRF protection, rate limiting, CORS, trusted proxies
- Structured JSON logging without secrets
- CI workflow (`.github/workflows/ci.yml`): gofmt, vet, test, build, Docker validation

### Configuration required later (operator must supply)
- `APP_ENV=production`, `DB_*`, `DB_SSLMODE=verify-full`, `DB_PASSWORD`, `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `MIDTRANS_BASE_URL`, `MIDTRANS_SERVER_KEY`, `MOCK_WEBHOOK_SECRET`, `TRUSTED_PROXIES`, `FRONTEND_PUBLIC_URL`, `CORS_ALLOWED_ORIGINS`, `DASHBOARD_BASE_URL`, `ADMIN_API_KEY` (if admin routes needed), SMTP vars (if email enabled), `WEBHOOK_SECRET_ENCRYPTION_KEY`

### Infrastructure required later
- Private PostgreSQL (certificate-verified TLS, not publicly exposed)
- TLS-terminating ingress/reverse proxy/load balancer
- Container registry (for image storage)
- Secret management (deployment platform or external secret manager)
- Persistent storage for product images (Docker named volume or persistent disk)

### Application changes potentially required
- None identified. The application is architecture-compatible with production deployment given correct configuration.

### Must be verified before production
- `APP_ENV=production` accepted by config validation
- All required production secrets injected and valid
- `TRUSTED_PROXIES` explicitly set
- `PAYMENT_PROVIDER` not `mock`; provider endpoint non-sandbox HTTPS
- `DB_SSLMODE=verify-full` with valid certificate
- Migration runs successfully against target database
- `/health/ready` returns 200 after rollout
- Frontend `VITE_API_BASE_URL` points at backend API origin over HTTPS
- `FRONTEND_PUBLIC_URL` points at production frontend origin
- `CORS_ALLOWED_ORIGINS` includes production frontend origin

### Actual blockers (must be resolved before production)
- **No production PostgreSQL provisioned** (external to repository)
- **No TLS-terminating ingress configured** (external to repository)
- **No production secrets injected** (external to repository)
- **No container registry configured** (external to repository)
- **Remote GitHub Actions not yet verified** (Phase 4A finding)

### Optional improvements (not blockers)
- Metrics exporter (Prometheus/OpenTelemetry) — documented as future enhancement
- Migration checksums/upgrade automation — documented as future action
- Distributed rate limiting — only when scaling requires it

---

## 8. Future Deployment Design (Proposed — Not Implemented)

**Target architecture:**

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
  |--------------------------- PostgreSQL (TLS verify-full, private network)
  |--------------------------- Payment provider (HTTPS, non-mock)
  |--------------------------- SMTP relay (TLS when email enabled)
  |--------------------------- Merchant webhook destinations (HTTPS/SSRF)
```

| Component | Recommendation |
|-----------|---------------|
| Static frontend hosting | Frontend SPA served from production origin (`FRONTEND_PUBLIC_URL`); builds from `pay-gate-dashboard` repository |
| Go backend container | Multi-stage Docker image, non-root, port 8080, behind ingress |
| External PostgreSQL | Private network, certificate-verified TLS, separate staging/prod databases |
| Container registry | Store tagged images; image digest for provenance |
| GitHub Actions CI/CD | Existing `ci.yml` (unchanged); new CD workflow for promotion/migration/deploy (not yet implemented) |
| HTTPS | External TLS termination; app receives plain HTTP on private interface |
| Secret management | Deployment platform or external secret manager; inject at runtime |
| Migration execution | One-shot `migrate/migrate:v4.18.1` job before app startup |
| Health checks | `/health/ready` used by container healthcheck and load balancer |
| Rollback | Image rollback preferred; no automatic destructive `down` migrations |

**This is a proposed design only. Nothing is provisioned or deployed.**

---

## 9. Files Inspected

| File | Purpose |
|------|---------|
| `internal/config/config.go` | Configuration loading and production validation |
| `internal/middleware/cors.go` | CORS policy implementation |
| `internal/ssrf/ssrf.go` | SSRF protection at dial boundary |
| `pkg/database/postgres.go` | Connection pool, schema check, expected migration version |
| `cmd/server/main.go` | Startup, config load, DB check, graceful shutdown |
| `internal/handler/health_handler.go` | `/health` and `/health/ready` |
| `Dockerfile` | Multi-stage build, non-root runtime, healthcheck |
| `docker-compose.yml` | App + migrate services, environment injection |
| `.env.example` | Development configuration template |
| `go.mod` | Go 1.25, dependencies |
| `docs/phase-9-production-runbook.md` | Production operating model and config contract |
| `docs/phase-9-production-checklist.md` | Production readiness checklist |
| `README.md` | Quick start and environment configuration |

---

## 10. Files Created

- `docs/phase-4b-backend-local-to-production-report.md` (this report — new)

---

## 11. Files Modified

**None.** No application source, configuration, Docker, migration, or CI files were modified by this audit.

---

## 12. Commands Executed

No repository-modifying or database-modifying commands were executed. Only read-only file inspection was performed.

---

## 13. Recommended Next Phase

Proceed to deployment infrastructure planning:
1. Provision external infrastructure (private PostgreSQL, container registry, TLS ingress, secret management).
2. Create a CD workflow (image promotion → one-shot migration → deploy → readiness check → manual rollback).
3. Validate in staging with `APP_ENV=production` and separate staging credentials.
4. Production rollout: backup → migrate → deploy → smoke check → keep previous image available.
