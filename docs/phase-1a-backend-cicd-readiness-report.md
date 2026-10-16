# GO-SAAS Phase 1A — Backend CI/CD & Deployment Readiness Audit Report

**Repository:** `pay-gate-backend` (`github.com/dhikaarta/pay-gate-backend`)
**Branch:** `main` (working tree contains pre-existing uncommitted changes — all preserved)
**Audit type:** Audit only. No application code, Docker configuration, migrations, or workflow files were modified.
**Report only:** The only newly created file is this report.

---

## 1. Overall Readiness: READY WITH GAPS

The backend is functionally sound and container-ready, but CI/CD is not yet implemented.

| Area | Verdict |
|------|---------|
| Source builds (`go build`) | READY |
| Unit tests (`go test ./...`) | READY (no external services) |
| `go vet` | READY |
| `gofmt` / `make fmt-check` | GAP — 4 untracked Phase 10L files need formatting |
| Docker image build | READY (multi-stage, non-root, healthchecked) |
| Docker Compose (app + migrate + external DB) | READY |
| Database migrations | READY (separate one-shot job; app does not auto-migrate) |
| GitHub Actions CI/CD | GAP — no `.github/workflows` directory exists |
| Production secrets / `.env.example` | GAP — `.env.example` contains non-placeholder SMTP values |

---

## 2. Repository and Go Version Findings

| Item | Finding |
|------|---------|
| Module path | `github.com/dhikaarta/pay-gate-backend` |
| Go version | `go 1.25` (toolchain `go1.25.0 linux/amd64`, matches `go.mod`) |
| Go module cache / vendoring | No `vendor/` directory; module cache mode (`go mod download` in Docker) |
| Entry point | `cmd/server/main.go` (package `main`); also `cmd/server/server_test.go`, `cmd/server/phase_8d5_audit_test.go` |
| Direct dependencies | gin v1.10.0, pgx v5.7.2, godotenv, swaggo/swag, uuid, go-playground/validator, golang.org/x/crypto, golang.org/x/image, plus indirects |
| Test framework | Standard library `testing` + `github.com/jackc/pgx/v5`; no external test runner |
| Linter configured | `golangci-lint` referenced in Makefile (`make lint`), noted "must be installed separately" — not a Go dependency |
| Pre-existing uncommitted changes | 13 modified + 28 untracked files (Phases 8E, 10L, prior verification reports) — all preserved, none touched by this audit |

---

## 3. Existing Build / Test Commands

Defined in `Makefile`:

| Target | Command | External service required? | Notes |
|--------|---------|----------------------------|-------|
| `make test` | `go test -v -race -count=1 ./...` | No | Unit/in-memory tests only. `*_pg_test.go` suites skip when `TEST_DATABASE_URL` is unset. |
| `make test-integration` | `TEST_DATABASE_URL=<url> go test -v -race -count=1 -p=1 ./...` | Yes — PostgreSQL | Requires explicit `TEST_DATABASE_URL`; runs against a disposable/migrated test DB. Never run against user DB. |
| `make vet` | `go vet ./...` | No | Clean on this tree. |
| `make fmt-check` | `gofmt -l .` (fail if non-empty) | No | Currently FAILS — see Gap G-003. |
| `make fmt` | `gofmt -w .` | No | Formats all Go source. |
| `make build` | `CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/server ./cmd/server` | No | Produces static binary. |
| `make all` | `fmt-check vet test build` | No | Read-only release gate. Fails at `fmt-check` today. |
| `make docker-build` | `docker compose -f docker-compose.yml build` | No (Docker daemon required) | Builds app image. |
| `make docker-validate` | `docker compose config --quiet` | No | Validates Compose model. |
| `make migrate-up` | `migrate -path migrations -database $DB_URL up` | Yes — PostgreSQL + `migrate` CLI | Requires explicit `DB_URL`/`DATABASE_URL`. |
| `make migrate-status` | `migrate -path migrations -database $DB_URL version` | Yes — PostgreSQL | |
| `make swagger` | `swag init ... -o ./swagger` | No | Requires `swag` CLI installed separately. Regenerates generated docs. |

### Unit vs. database integration tests

- **Unit tests** (the default `make test`): pure Go, in-memory repositories or stubs. No PostgreSQL, no network, no secrets. Safe to run on any CI runner.
- **Database integration tests**: files matching `*_pg_test.go` (e.g. `dashboard_overview_pg_test.go`, `transaction_list_repository_test.go`, `password_reset_pg_test.go`). Each obtains its pool from `TEST_DATABASE_URL` and calls `t.Skip("TEST_DATABASE_URL not set")` when absent. Safe by design — they never touch a user-managed database unless `TEST_DATABASE_URL` points at one.

---

## 4. Docker Findings

### Dockerfile (`Dockerfile`, multi-stage)

| Aspect | Finding |
|--------|---------|
| Multi-stage | Yes: `builder` (golang:1.25-alpine) → runtime (alpine:3.20) |
| Reproducible build | `GOTOOLCHAIN=local` set; `go mod download` before source copy for layer caching; `-ldflags="-w -s"` |
| Minimal runtime image | alpine:3.20; only zoneinfo + CA certs copied from builder; static binary |
| Non-root execution | Yes: `appuser:appgroup` via `USER appuser` |
| Read-only rootfs | Yes (Compose `read_only: true` + `tmpfs: /tmp`) |
| Security opts | Compose: `no-new-privileges: true`, `cap_drop: ALL`, `init: true`, `stop_grace_period: 20s` |
| Signal handling / graceful shutdown | Yes: `cmd/server/main.go` handles SIGINT/SIGTERM with coordinated drain (HTTP shutdown 10s, worker drain 10s, pool close) |
| Health check | Yes: `HEALTHCHECK ... CMD wget -qO- http://localhost:8080/health/ready` (interval 30s, timeout 10s, start-period 15s, retries 3) |
| Port exposure | `EXPOSE 8080`; Compose publishes `8081:8080` |
| Runtime config via env | Yes: all configuration via environment variables (`config.Load`); no hardcoded credentials |
| Secrets in image layers | No: `.dockerignore` excludes `.env`, `.env.*` (except `!.env.example`), `.git`; image contains no credentials |

### `.dockerignore` coverage

Excludes: `.git`, `.env`, `.env.*` (except `.env.example`), `.env.local`, `bin/`, `server`, `*.test`, `*.out`, `coverage.txt`, `.idea/`, `.vscode/`, `docker-compose.override.yml`, `README.md`, `docs/`.

Minor gaps: `tmp/`, `pay-gate-backend.code-workspace`, and the pre-built `server` binary (covered by `server` line) — `tmp/` is not excluded. Low risk; `tmp/` is empty in the tree.

### docker-compose.yml

| Service | Role |
|---------|------|
| `app` | Go backend; depends_on `migrate` (condition `service_completed_successfully`); PostgreSQL external |
| `migrate` | One-shot `migrate/migrate:v4.18.1` job; `restart: "no"`; runs `migrate up` against `MIGRATION_DATABASE_URL` or a `DB_*`-assembled URL |

PostgreSQL is intentionally external to Compose. The app container does not run migrations itself — it validates schema via `database.CheckSchema` at startup and fails fast if the schema is missing, dirty, or below `ExpectedMigrationVersion` (22).

---

## 5. Database Migration Findings

| Aspect | Finding |
|--------|---------|
| Migration tool | `golang-migrate` v4, invoked via the `migrate/migrate:v4.18.1` Docker image (not a Go dependency) |
| Migration directory | `migrations/` — 25 versioned pairs (`000001` … `000025`), each with `.up.sql` and `.down.sql` |
| How applied | One-shot Compose `migrate` service runs `migrate -path /migrations -database <URL> up` before `app` starts |
| Safe / repeatable | `migrate up` is idempotent (applies only pending versions) |
| App auto-migrate | **No.** Application startup calls `database.CheckSchema`, which requires `schema_migrations.version >= 22` and `dirty=false`. Startup fails otherwise — fail-closed. |
| Startup before DB ready | No — `NewPool` pings the DB and `CheckSchema` queries `schema_migrations`; both fail fast if unavailable |
| Rollback | `make migrate-down` guarded by `ALLOW_DESTRUCTIVE_DOWN=true`; destructive, requires explicit approval. Down paths not guaranteed to restore exact prior schema (e.g. migration 17). Production rollback should prefer image rollback, not `down` migrations. |
| Migration failure handling | Compose `depends_on: condition: service_completed_successfully` blocks `app` until `migrate` succeeds; a failed migration prevents app startup |

No migrations were run against any user database during this audit (per safety rules).

---

## 6. CI Requirements and Dependencies

For a clean CI runner (Linux, amd64):

| Requirement | Satisfied by | Notes |
|-------------|--------------|-------|
| Go toolchain | `go 1.25` | Use `golang:1.25` or `actions/setup-go` with `go-version: 1.25` |
| Dependency module cache | `go.mod` + `go.sum` present | `go mod download` or `go test` populates cache |
| PostgreSQL | Optional | Required only for `*_pg_test.go` suites via `TEST_DATABASE_URL`; unit tests skip without it |
| Docker daemon | Optional | Required only for `docker-build` / `docker-validate` steps |
| `migrate` CLI | Optional | Required only for migration steps; provided by Docker image in Compose |
| `golangci-lint` | Optional | `make lint` target exists but linter not a dependency; not required for CI |
| `swag` | Optional | Required only if regenerating swagger docs (`make swagger`) |
| Network access | For `go mod download` | Module cache can be pre-populated / cached in CI |

### Safe integration test execution in CI

1. Provision a **disposable** PostgreSQL instance (e.g. `postgres:16-alpine` service container in CI).
2. Apply migrations against it with `make migrate-up DB_URL=<disposable URL>`.
3. Run `make test-integration TEST_DATABASE_URL=<disposable URL>`.
4. Tear down the disposable DB; never point `TEST_DATABASE_URL` at a user-managed or production database.
5. `*_pg_test.go` suites skip gracefully when `TEST_DATABASE_URL` is unset, so `make test` can run without PostgreSQL.

---

## 7. Security / Secrets Findings

| Area | Finding | Risk |
|------|---------|------|
| `.gitignore` | Excludes `.env`, `.env.elk`, `*.local` | Good — local env files not tracked |
| `.dockerignore` | Excludes `.env`, `.env.*` (except `.env.example`), `.git` | Good — secrets excluded from image layers |
| `.env.example` | Tracked; **contains non-placeholder SMTP values** (`SMTP_HOST`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM` filled in) | Potential secret leakage into the example file. Values must not be printed or committed to CI logs. Review and replace with placeholders or remove. |
| `.env` | Untracked, gitignored, user-managed | Not inspected or modified per safety rules |
| `.env.elk` | Untracked, mode 600 | ELK secrets; out of scope |
| JWT secret (`AUTH_JWT_SECRET`) | Env-injected; dev fallback only; production requires ≥32 bytes, non-default | Good |
| Admin key (`ADMIN_API_KEY`) | Env-injected; empty disables admin routes | Good |
| Webhook encryption key (`WEBHOOK_SECRET_ENCRYPTION_KEY`) | Env-injected; dev fallback; production requires explicit non-default 32-byte key | Good |
| DB credentials (`DB_USER`/`DB_PASSWORD`) | Env-injected; Compose uses `${DB_USER:?}` / `${DB_PASSWORD:?}` (fail if unset) | Good |
| CORS (`CORS_ALLOWED_ORIGINS`) | Env-injected; production requires HTTPS; default `http://localhost:5173` | Good |
| Trusted proxies (`TRUSTED_PROXIES`) | Env-injected; production requires explicit `none` or IP/CIDR list | Good |
| Secret output | Startup logs intentionally exclude credentials; readiness checks don't print secrets | Good |

### Secret-in-image risk
Low. `.dockerignore` excludes `.env` and `.env.*`. Compose injects runtime secrets via environment at container start, not build time. The one residual item is `.env.example` containing filled SMTP values (see G-004).

---

## 8. Confirmed Gaps / Blockers

| ID | Gap | Severity | Evidence |
|----|-----|----------|----------|
| G-001 | No GitHub Actions workflows exist; no `.github` directory | High (CI/CD not implemented) | `ls .github` → not found |
| G-002 | `make all` fails at `make fmt-check` on the current working tree | Medium | `gofmt -l .` lists 4 untracked Phase 10L files: `internal/model/dashboard_analytics.go`, `internal/repository/dashboard_analytics_pg_test.go`, `internal/repository/dashboard_overview_pg_test.go`, `internal/repository/dashboard_overview_recent_pg_test.go` |
| G-003 | `.env.example` contains non-placeholder SMTP credentials | Medium | `SMTP_HOST=smtp.mailspons.com`, `SMTP_USERNAME=...`, `SMTP_PASSWORD=...`, `SMTP_FROM=support@mail.com` present |
| G-004 | No automated migration checksum/upgrade verification in CI | Low | Runbook mentions adding checksums as a future action (risk register) |
| G-005 | No metrics exporter (Prometheus/OpenTelemetry) | Low | Runbook: "no metrics exporter … future enhancement" |

None of these block source-level CI (`go test`, `go vet`, `go build`), but G-001 and G-002 block a green `make all` / CI release gate until addressed. G-002 is caused by pre-existing untracked files (Phase 10L work) and is not a regression introduced by this audit.

---

## 9. Recommended GitHub Actions Workflow (Design Only — Not Implemented)

Two workflows are recommended, kept in `.github/workflows/`:

### 9a. `ci.yml` — Pull-request / push checks (runs on every PR and push to `main`)

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25', cache: true }
      - run: gofmt -l .        # formatting validation
      - run: go vet ./...
      - run: go test -race -count=1 ./...   # unit tests; _pg_test suites skip without DB
      - run: CGO_ENABLED=0 go build -ldflags="-w -s" -o bin/server ./cmd/server
  # Optional: integration tests against a disposable Postgres service container
  integration:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env: { POSTGRES_USER: ci, POSTGRES_PASSWORD: ci, POSTGRES_DB: ci_test }
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U ci -d ci_test"
          --health-interval 5s --health-timeout 5s --health-retries 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25', cache: true }
      - run: make migrate-up DB_URL="postgres://ci:ci@localhost:5432/ci_test?sslmode=disable"
      - run: TEST_DATABASE_URL="postgres://ci:ci@localhost:5432/ci_test?sslmode=disable" go test -race -count=1 -p=1 ./...
```

### 9b. `release.yml` — Release / deployment gate (runs on tags or manual dispatch)

```yaml
name: Release
on:
  workflow_dispatch:
  push:
    tags: ['v*']
jobs:
  docker:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: docker compose -f docker-compose.yml config --quiet   # validate Compose
      - run: docker compose -f docker-compose.yml build            # validate image build
      # Push image to registry here (registry credentials from repository secrets)
  # Optional: run smoke checks against a staging environment after image promotion
```

Key design points:
- Keep PR checks separate from release/deployment.
- `gofmt -l .` must pass before `make all` is green — resolves G-002 (format the 4 untracked files or exclude them from CI scope).
- Integration tests use a service-container PostgreSQL with `TEST_DATABASE_URL`; never a user-managed DB.
- Secrets (`DB_PASSWORD`, registry credentials) injected via GitHub repository/environment secrets, never in workflow files.
- Do not commit workflows yet — this is a design recommendation only.

---

## 10. Files Changed

No application code, Docker configuration, migrations, or workflow files were modified.

New file created:
- `docs/phase-1a-backend-cicd-readiness-report.md` (this report)

Pre-existing uncommitted changes (13 modified + 28 untracked files) were preserved untouched.

---

## 11. Exact Next Implementation Steps

1. **Format the 4 untracked Phase 10L files** so `make fmt-check` passes (or confirm they are intentionally excluded from CI scope). Run `gofmt -w .` only on those files if formatting them is acceptable; do not reformat unrelated code.
2. **Review `.env.example` SMTP values** (G-003): replace `SMTP_HOST`/`SMTP_USERNAME`/`SMTP_PASSWORD`/`SMTP_FROM` with placeholder values or remove them, so no credentials live in a tracked example file.
3. **Create `.github/workflows/ci.yml`** per the design in §9a (formatting, vet, test, build, optional integration against disposable Postgres).
4. **Create `.github/workflows/release.yml`** per the design in §9b (Compose validation + image build) when a release process is needed.
5. **Add migration checksum/upgrade verification** to CI (G-004) once migrations are stable, per the production runbook risk register.

No changes were made during this audit; the smallest safe next step is to address G-002 (gofmt) and G-003 (`.env.example` SMTP values), then implement `ci.yml`.
