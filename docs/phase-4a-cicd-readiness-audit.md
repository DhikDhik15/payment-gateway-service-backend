# GO-SAAS Phase 4A — CI/CD Readiness Audit

**Audit date:** 2026-10-20  
**Scope:** `pay-gate-backend` and `pay-gate-dashboard` (separate repositories)  
**Audit type:** Read-only audit. No CI/CD implementation, deployment, commit, or push.  
**Report file:** `docs/phase-4a-cicd-readiness-audit.md` (new)  
**Change constraint:** No application source, workflow, Docker, Compose, environment, dependency, or lockfile file was modified. This report is the only new artifact.

---

## 1. Executive Summary

The two repositories are separate, public GitHub repositories with active GitHub Actions CI workflows and no CD workflow. The frontend CI is green remotely. The backend CI is red: all three runs on record failed, and a clean checkout of the current backend HEAD cannot even build because the generated Swagger package is ignored by Git and absent from the checkout. The backend Docker CI job additionally fails on a clean checkout because the development Compose file requires `DB_USER` and `DB_PASSWORD` before it will parse.

A critical security issue was also found: the backend tracks `.env.stage` in a **public** repository. The file contains non-empty, non-placeholder values for secret-like keys including `DB_PASSWORD`, `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `ADMIN_API_KEY`, `MOCK_WEBHOOK_SECRET`, and `STAGING_DB_PASSWORD`. Values were not printed or copied into this report. This must be treated as an active credential-exposure incident, not merely a repository-hygiene finding.

Phase 3 verified the browser payment flow locally with headless Chrome over CDP. That was a real browser E2E pass, but the harness, fixture, credentials, and artifacts live under `/tmp` and are not committed or automated. Phase 2 API E2E remains a separate, earlier API-level result. Neither is a CI E2E claim.

**Overall verdict: NOT READY — BLOCKED.** Do not begin CD implementation until the credential exposure and the backend clean-checkout/Compose blockers are resolved.

---

## 2. Overall Readiness Verdict

| Area | Verdict | Basis |
|---|---|---|
| Frontend CI | **READY** | Remote GitHub Actions run on `d25a232` passed install, typecheck, lint, tests, and production build |
| Backend CI | **NOT READY** | Remote runs failed; clean checkout fails `go vet`, `go test`, and `go build`; Docker Compose job fails |
| Browser E2E | **LOCAL PASS / CI NOT READY** | Phase 3 browser E2E passed locally; harness and fixture are not committed or automated |
| Security and secrets | **BLOCKED** | `.env.stage` with secret-like values is tracked in a public repository |
| Docker and deployment | **PARTIAL** | Backend Dockerfile/Compose are sound in design; clean image build is blocked; frontend has no container or web-server config |
| CD | **NOT READY** | No deployment workflow, registry, environment, or staging/production infrastructure |
| **Overall** | **NOT READY — BLOCKED** | Blockers B-1, B-2, and B-3 below must be closed first |

The frontend portion is independently CI-capable. The backend is not. A green frontend run does not compensate for a red backend or for a public credential leak.

---

## 3. Repositories Inspected

| Repository | Local path | Remote (credentials stripped) | Visibility | Branch | HEAD | Working tree |
|---|---|---|---|---|---|---|
| Backend | `/run/media/dhika/Dhika/go-pay-gate/pay-gate-backend` | `https://github.com/DhikDhik15/payment-gateway-service-backend.git` | Public | `main` | `fd49157` — `add env mobile final verification` (2026-10-19) | 4 modified, 2 untracked files; all pre-existing and preserved |
| Frontend | `/run/media/dhika/Dhika/pay-gate-dashboard` | `https://github.com/DhikDhik15/payment-gateway-service-dashboard.git` | Public | `main` | `d25a232` — `maintenance frontend` (2026-10-16) | 7 modified, 7 untracked files; all pre-existing and preserved |

The repositories are **not** a monorepo. They have separate Git histories, workflows, package/build systems, and release boundaries. The backend module path (`github.com/dhikaarta/pay-gate-backend`) differs from the GitHub repository path; older documents also use the stale repository names `dhikaarta/pay-gate-backend` and `dhikaarta/pay-gate-dashboard`. This does not break builds, but it is a documentation inconsistency.

The GitHub repository API reported both repositories as `private: false`, `default_branch: main`, and `archived: false`. The `main` branch API reported `protected: false` for both repositories.

No user changes were reset, stashed, cleaned, overwritten, committed, or pushed.

---

## 4. Existing CI/CD State

### 4.1 Workflows present

| Repository | File | Trigger | Jobs | Remote state |
|---|---|---|---|---|
| Backend | `.github/workflows/ci.yml` | `push`/`pull_request` to `main` | `test`, `docker` | Active; **red** |
| Frontend | `.github/workflows/ci.yml` | `push`/`pull_request` to `main` | `ci` | Active; **green** |

No GitLab CI, Azure Pipelines, Jenkins, CircleCI, Travis, Drone, or other CI configuration was found in either repository. No CD/deployment workflow, registry login, environment deployment, release workflow, or approval gate exists.

### 4.2 Remote backend evidence

The public GitHub Actions API shows **three backend runs, all failed**:

| Run | Created (UTC) | HEAD | Result | Failing steps |
|---|---|---|---|---|
| `39218775505` | 2026-10-19 07:52:08 | `fd49157` | failure | `test` job: **Run go vet**; `docker` job: **Validate Docker Compose model** |
| `38882792677` | 2026-10-16 09:46:50 | `cd703bf` | failure | Same two steps |
| `38849639021` | 2026-10-16 04:14:57 | `48cba6f` | failure | Same two steps |

In the latest run, the `test` job stopped at `go vet`; unit tests and the binary build were skipped. The `docker` job stopped at Compose model validation; the image build was skipped. The raw logs could not be downloaded because the job-log endpoint requires repository admin rights (`403`), so the exact remote error text was inferred from the failing step names and reproduced locally from the same commit.

### 4.3 Remote frontend evidence

The public API shows one frontend run:

| Run | Created (UTC) | HEAD | Result |
|---|---|---|---|
| `38849642456` | 2026-10-16 04:14:59 | `d25a232` | **success** |

The run passed `npm ci`, `npm run typecheck`, `npm run lint`, `npm test`, and `npm run build`. This is the strongest evidence that the committed frontend HEAD is CI-capable.

### 4.4 Branch protection and gating

`main` is unprotected in both repositories. A red backend CI run therefore does not prevent direct pushes or merges. There are no required status checks, no required pull-request review, and no environment protection rules. Existing CI is advisory rather than gating, which is especially serious while the backend workflow is red.

### 4.5 Documentation versus reality

Some existing reports describe CI as locally validated or say remote execution was not verified. The remote API now resolves that ambiguity: frontend is green; backend is red. The backend failure is not a transient runner issue; the same two steps failed in every recorded run. The report `docs/phase-4c-deployment-architecture-specification.md` states that CI “passes locally” but does not establish a clean-checkout or remote pass. That claim should not be used as CD readiness evidence.

---

## 5. Frontend CI Readiness

### 5.1 Toolchain and package manager

| Item | Finding |
|---|---|
| Package manager | npm |
| Lockfile | `package-lock.json`, `lockfileVersion: 3`; committed |
| Install command | `npm ci` (lockfile-based, reproducible) |
| Node engine | `"node": ">=20.19.0"` in `package.json` |
| CI Node | Node 22 via `actions/setup-node@v4` |
| Local Node used in this audit | `v22.22.1`, npm `9.2.0` |
| Test runner | `bash scripts/run-tests.sh` → esbuild bundles each `*.test.ts(x)` → `node --test` |

The frontend does not use pnpm or Yarn. There is no `.nvmrc` or `packageManager` field; CI pins Node 22, but local environments can drift within the broad engine range.

### 5.2 Actual commands and results

| Gate | Command | Remote CI | Notes |
|---|---|---|---|
| Install | `npm ci` | **PASS** | Uses committed lockfile |
| Typecheck | `npm run typecheck` (`tsc -b --pretty false`) | **PASS** | No backend required |
| Lint | `npm run lint` (`eslint .`) | **PASS** | Reads source only |
| Unit tests | `npm test` | **PASS** | Remote green; local current tree **266/266 pass** |
| Production build | `npm run build` (`tsc -b && vite build`) | **PASS** | Build-time environment only |

The local `266/266` result includes the current untracked test files in the frontend working tree. The remote run covers the committed HEAD and therefore does not include those untracked files. This is a normal consequence of the pre-existing dirty working tree, not a CI failure.

### 5.3 Build-time environment and secrets

- `VITE_API_BASE_URL` is a build-time value. The CI workflow sets `VITE_API_BASE_URL: http://localhost:8081` as a non-secret placeholder used only for build validation.
- `.env.production` intentionally leaves `VITE_API_BASE_URL` empty so a production build fails closed until the deployment injects a real origin.
- `VITE_ENABLE_SIMULATOR` defaults to `false`; all `VITE_*` values are public browser configuration.
- No backend, database, browser, or external service is required for the current frontend CI.
- The workflow does **not** upload `dist/`, so the localhost-placeholder build is not a deployable artifact. If CD later reuses this workflow, the placeholder must not be promoted.

### 5.4 Gaps

- No `timeout-minutes`; a hung job can run for the default maximum.
- No `concurrency` block; redundant pushes are not cancelled.
- No coverage report, JUnit output, or artifact upload.
- No browser E2E job.
- No frontend Dockerfile, nginx/Caddy configuration, or SPA-fallback hosting configuration in the repository.
- The production host must rewrite `/login`, `/pay/*`, `/dashboard/*`, and `/accept-invitation` to `index.html`; that responsibility is documented but not implemented here.

**Frontend CI verdict: READY for CI as currently scoped.** It is not a deployment artifact pipeline.

---

## 6. Backend CI Readiness

### 6.1 Toolchain and dependency inputs

| Item | Finding |
|---|---|
| Go version | `go 1.25` in `go.mod`; local toolchain `go1.25.0 linux/amd64` |
| Dependency lock | `go.sum` present (185 lines); no `vendor/` directory |
| CI Go version | `actions/setup-go@v5` with `go-version-file: go.mod`; resolved successfully in the remote run |
| Entry point | `cmd/server/main.go` |
| Test framework | Standard library `testing`; 110 backend test files |
| Build tags/vendor | None observed |

### 6.2 Actual commands and results

| Gate | Command | Remote CI | Clean HEAD checkout | Current working tree |
|---|---|---|---|---|
| Formatting | `gofmt -l .` | “PASS” (gate is non-failing) | no output | no output |
| Static analysis | `go vet ./...` | **FAIL** | **FAIL** | PASS |
| Unit tests | `go test -race -count=1 ./...` | skipped | **FAIL** | PASS (PostgreSQL tests skipped) |
| Binary build | `go build -ldflags="-w -s" -o bin/server ./cmd/server` | skipped | **FAIL** | PASS |
| Compose validation | `docker compose -f docker-compose.yml config --quiet` | **FAIL** | **FAIL** | PASS only with local `.env` |
| Docker image build | `docker compose ... build app` | skipped | **BLOCKED** by build failure | not re-built in this audit |

### 6.3 Clean-checkout blocker

The generated Swagger package is ignored by Git:

- `.gitignore:35-37` ignores `swagger/docs.go`, `swagger/swagger.json`, and `swagger/swagger.yaml`.
- `git ls-files swagger` returns **zero** tracked files.
- `cmd/server/main.go:23` imports `_ "github.com/dhikaarta/pay-gate-backend/swagger"`.
- A clean `git archive HEAD` checkout therefore has no `swagger/` package.

From that clean checkout:

```
go vet ./...   → exit 1:
cmd/server/main.go:23:2: no required module provides package
github.com/dhikaarta/pay-gate-backend/swagger

go test -count=1 ./... → exit 1 (cmd/server setup failed)
go build ./cmd/server → exit 1 (same missing package)
```

This exactly explains why the remote `go vet` step failed while the local working tree passed: locally, the ignored `swagger/` directory exists and is populated; CI does not receive it.

The Dockerfile would hit the same failure. It copies the source at `Dockerfile:17` and builds it at `Dockerfile:20-21`; a clean checkout has no Swagger package. A clean Docker build was not executed in this audit, but the equivalent Go build already fails deterministically.

### 6.4 Compose CI blocker

`docker-compose.yml:80-81` declares:

```
DB_USER: ${DB_USER:?DB_USER is required}
DB_PASSWORD: ${DB_PASSWORD:?DB_PASSWORD is required}
```

The backend workflow does not set either variable. On a clean GitHub checkout there is no `.env` file (it is ignored), so Compose interpolation aborts before the image build:

```
error while interpolating services.app.environment.DB_PASSWORD:
required variable DB_PASSWORD is missing a value
error while interpolating services.app.environment.DB_USER:
required variable DB_USER is missing a value
exit 1
```

Supplying dummy, non-secret values (`DB_USER=dummy DB_PASSWORD=dummy`) makes `docker compose ... config --quiet` exit 0. This isolates the failure to missing CI configuration, not to invalid Compose syntax.

### 6.5 Test scope

- `go test -race -count=1 ./...` passes on the current working tree with no database configured.
- PostgreSQL-backed tests are explicitly skipped when `TEST_DATABASE_URL` is unset, for example `internal/repository/order_repository_pg_test.go:21-23`, `password_reset_pg_test.go:28-30`, and `phase_8d4_concurrency_test.go:26-28`.
- `make test-integration` requires `TEST_DATABASE_URL` and a disposable database migrated through the latest schema; `migrations/` has 25 migration pairs.
- CI currently runs only the non-PostgreSQL path. It cannot catch data-layer, migration, locking, or transaction regressions.

### 6.6 Other gaps

- `make fmt-check` correctly fails on unformatted files, but CI does not use it. See finding M-1.
- `golangci-lint` is referenced by `make lint` but is not installed or run in CI.
- No coverage report or threshold.
- No `go mod tidy` or module-integrity check.
- No artifact upload of the built binary.
- No service containers, database migrations, or integration-test job.

**Backend CI verdict: NOT READY.** A red main branch, a clean-checkout build failure, and a deterministic Compose failure are blocking.

---

## 7. Browser E2E Automation Readiness

### 7.1 What was actually verified

Phase 2 was an **API E2E** verification and remains a separate historical result. Phase 3 was a **browser E2E** verification executed locally:

- Browser: Google Chrome `155.0.8059.39`, `--headless=new`
- Transport: Chrome DevTools Protocol at `127.0.0.1:9223`
- Driver: a small Node 22 harness using built-in `WebSocket` and `fetch`
- Servers: local frontend and backend already running
- Scenarios: login, create payment, hosted payment popup, public QRIS page, success/failure transitions, terminal-state rejection, dashboard reflection, paid reload, 404, API-unavailable handling, console/network checks
- Result: **six scenarios PASS** in Phase 3

That pass is credible as a local browser result. It is not evidence that browser E2E runs in CI.

### 7.2 Reproducibility status

| Aspect | Status | Evidence |
|---|---|---|
| Harness committed | **NO** | Harness, scenario scripts, screenshots, JSON evidence, and credentials are under `/tmp/opencode/phase3-browser/` and `/tmp/opencode/phase2-e2e-1792464265/` |
| Merchant fixture reproducible | **NO** | Reuses a disposable Phase 2 merchant from a local credentials file; no checked-in seed |
| Transaction isolation | **PARTIAL** | Unique order IDs were used, but there is no ephemeral database or automated teardown |
| CI workflow | **NO** | No E2E workflow; no browser launch step; no service orchestration |
| Artifact retention | **NO** | Artifacts are outside the repository and not uploaded |
| Manual setup required | **YES** | Start PostgreSQL, migrate, start backend/frontend, create/obtain merchant, launch Chrome on a fixed CDP port |
| Deterministic expiry | **PARTIAL** | Phase 3 used a +31-minute browser clock offset; backend may still return `PENDING` until the 30-second expiry worker runs |

### 7.3 Expiry-worker risk

`PAYMENT_EXPIRY_ENABLED=true` and `PAYMENT_EXPIRY_INTERVAL=30s` in the verified local configuration. The frontend can render `EXPIRED` from `expired_at` before the backend worker updates the stored status. Any CI assertion that expects the API and UI to agree immediately after the expiry boundary will be flaky.

Recommended future controls: use a short test-only expiry interval, poll with a bounded timeout, and inject a deterministic clock into the frontend test; keep expiry enabled. Do not disable expiry to make assertions easier. The Phase 3 report already records this as a known limitation.

### 7.4 Verdict

**Browser E2E: local PASS, CI readiness BLOCKED.** It cannot be claimed as CI-ready while the harness, fixture, and startup procedure are outside version control.

---

## 8. Environment and Secret Management

### 8.1 Configuration sources

| Source | Status | Notes |
|---|---|---|
| Backend `.env` | Ignored, not tracked | Local development only |
| Backend `.env.elk` | Ignored, not tracked | Local observability |
| Backend `.env.example` | Tracked | Mostly placeholders; `DB_PASSWORD` is non-empty (see M-7) |
| Backend `.env.stage` | **Tracked, public** | Contains secret-like values; see B-1 |
| Frontend `.env`, `.env.development`, `.env.production` | Not tracked | Only public `VITE_*` values; `.env.production` intentionally leaves the API origin empty |
| Frontend `.env.example` | Tracked | Public, non-secret placeholders |

### 8.2 Critical exposure: tracked `.env.stage`

The backend repository is public, and `.env.stage` is present at HEAD `fd49157`. It was introduced in commit `cd703bf` on 2026-10-16 and is still tracked. The file has 72 keys. The following secret-like keys are present with non-empty values; **values were deliberately not read into this report**:

| Key | Status | Exposure consequence |
|---|---|---|
| `DB_PASSWORD` | Present, non-empty, high-entropy-looking | Database access if reachable |
| `STAGING_DB_PASSWORD` | Present, non-empty | Staging database access |
| `AUTH_JWT_SECRET` | Present, non-empty | Session/token forgery if reused |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | Present, non-empty | Decryption of stored webhook secrets |
| `MOCK_WEBHOOK_SECRET` | Present, non-empty | Webhook signature forgery against mock flows |
| `ADMIN_API_KEY` | Present, non-empty | Administrative API access if enabled |

The values are not plausible placeholders: they are substantially longer than the placeholder strings and do not contain obvious placeholder words. They must be treated as compromised regardless of whether they were intended only for local staging. Deleting the file in a new commit is insufficient because the public Git history retains it. Rotate first, then purge history with an approved history-rewrite process, and assume any system that accepted those values was exposed.

Whether GitHub secret scanning or push protection alerted on this file is not visible in the public API and was not verified. No repository-level secret-scanning or push-protection configuration is committed.

### 8.3 Stage profile is not safe for a dashboard stage

The same `.env.stage` also has a configuration problem independent of secret exposure:

| Setting | Value | Concern |
|---|---|---|
| `APP_ENV` | `test` | Production-grade validation is not applied |
| `FRONTEND_PUBLIC_URL` | `http://127.0.0.1:18080` | Points at a backend origin, not the frontend SPA origin |
| `CORS_ALLOWED_ORIGINS` | includes `127.0.0.1:18080`, `localhost:18080`, `localhost:35597` | Mixed local origins, not a deployable stage origin |
| `PAYMENT_EXPIRY_ENABLED` | `false` | Expiry is disabled, contrary to required staging behavior |
| `RATE_LIMIT_ENABLED` | `false` | Auth/API protection disabled |
| `WEBHOOK_REQUIRE_HTTPS` | `false` | Insecure webhook destinations permitted |
| `DB_SSLMODE` | `disable` | No TLS to PostgreSQL |

This profile may have been intended for a local Flutter/mobile staging scenario, but it must not be used for a dashboard staging environment or any internet-facing deployment.

### 8.4 FRONTEND_PUBLIC_URL validation by environment

Current backend behavior (`internal/config/config.go:698-722`):

- The value is required when `PAYMENT_PROVIDER=mock`.
- It must be an absolute URL.
- It must have no userinfo, query, or fragment.
- It must use `http` or `https`.
- There is **no environment-aware HTTPS requirement** and no rejection of loopback/private hosts. Unlike `CORS_ALLOWED_ORIGINS` (`internal/config/config.go:865-872`), production does not require HTTPS here.

Recommended validation contract for future phases:

| Environment | Required validation |
|---|---|
| Development | `http://localhost:<frontend-port>` allowed; no credentials/query/fragment; exact CORS match |
| Test/CI | Explicit loopback frontend origin allowed only for test; expiry enabled; exact CORS; no real secrets |
| Staging | Public HTTPS frontend origin; reject loopback, `localhost`, `127.0.0.1`, and private IPs; production-like `APP_ENV`; exact HTTPS CORS; expiry enabled |
| Production | Public HTTPS frontend origin; reject local/private hosts; no userinfo/query/fragment; exact HTTPS CORS; mock provider and simulator disabled; expiry enabled unless an approved policy says otherwise |

`localhost` and `127.0.0.1` must remain distinct; no wildcard credentialed CORS is acceptable.

### 8.5 CI secret handling

The existing workflows do not reference repository secrets. Both set top-level `permissions: contents: read`. There is no `pull_request_target`, no registry login, no deployment credential, and no secret printed in workflow YAML. That is good.

However, there are no GitHub Environments, no environment-scoped secrets, no OIDC configuration, and no required reviewers. When CD is added, secrets must be environment-scoped and injected only after approval, never exposed to pull-request jobs from forks.

### 8.6 `.env.example` note

`.env.example` is tracked and mostly contains placeholders. `DB_PASSWORD` is a non-empty seven-character value with no obvious placeholder marker. It is not equal to the stage or local values, but it should be replaced with an explicit placeholder or empty value so operators cannot mistake it for a usable credential. SMTP entries appear example-like, but should be verified by the owner.

---

## 9. Test Fixture Readiness

| Test layer | Current mechanism | CI readiness |
|---|---|---|
| Backend unit tests | In-memory fakes and standard Go tests; 110 test files | **Ready**; run without external services |
| Backend PostgreSQL integration | Tests skip unless `TEST_DATABASE_URL` is set; require a disposable DB migrated through the latest schema | **Not wired into CI** |
| Backend migrations | 25 migration pairs; one-shot `migrate` Compose service exists | **Ready to use in a CI service-container job** |
| Frontend unit tests | esbuild + `node:test`; API calls mocked; no backend required | **Ready**; remote green |
| API E2E | Phase 2 result; external/local fixture | **Historical only**; not automated |
| Browser E2E | Phase 3 CDP harness; disposable Phase 2 merchant; `/tmp` credentials | **Not reproducible**; no seed or teardown |
| Browser transaction isolation | Unique order IDs in Phase 3 | **Partial**; no ephemeral DB per run |

The browser fixture is the weakest point. A CI-ready E2E needs a checked-in seed/bootstrap that creates a merchant, user, and API credentials deterministically, plus a disposable PostgreSQL database per run. Credentials must be generated per run or injected as ephemeral test secrets, never committed. The Phase 2 fixture file under `/tmp` is sensitive and must not be copied into the repository or report.

The 30-second expiry worker must be accounted for in fixture and assertion design. The backend state can legitimately lag the frontend’s timestamp-derived display by up to one worker interval.

---

## 10. Docker and Deployment Readiness

### 10.1 Backend container

Strengths:

- Multi-stage Dockerfile at `Dockerfile:1-53`.
- Non-root runtime user (`USER appuser`, line 46).
- Health check against `/health/ready` (lines 50-51).
- Migrations copied into the image (line 44).
- Compose runs the app with `read_only: true`, `cap_drop: ALL`, `no-new-privileges`, and a `tmpfs` `/tmp` (`docker-compose.yml:14-20`).
- One-shot `migrate` service runs before the app (`docker-compose.yml:144-146`, `152-182`).
- `.dockerignore` excludes `.env` and `.env.*` except `.env.example`, so environment secrets are not copied into the build context.

Blockers and gaps:

- Clean image build fails because the Swagger package is absent from a clean checkout (B-2).
- Compose validation/build fails on a clean CI runner because `DB_USER` and `DB_PASSWORD` are required (B-3).
- There is no CI-specific Compose file or app-only build path.
- There is no image registry, tag-by-SHA policy, image scan, SBOM, or signature.
- `docker-compose.stage.yml` provisions only a local PostgreSQL instance; it does not deploy the backend or frontend.
- No production Compose/Helm/Terraform/Ansible definition is present.

### 10.2 Frontend deployment

The frontend has no `Dockerfile`, `nginx.conf`, `Caddyfile`, compose file, Vercel/Netlify/Firebase config, or other hosting/SPA-fallback configuration. The repository-only deployment output is `dist/`, which is Git-ignored. A production host must serve `dist/` with history fallback and inject a real `VITE_API_BASE_URL` at build time.

### 10.3 CD

No CD workflow exists. There is no deployment environment, no registry, no approval gate, no migration job in CD, no smoke test, no rollback automation, and no post-deploy verification. `docs/phase-4c-deployment-architecture-specification.md` describes a target architecture (VPS, private PostgreSQL, TLS ingress, static frontend host, container registry), but that architecture has not been provisioned or automated.

**Docker/deployment verdict: PARTIAL.** The backend container design is a good foundation, but it is not currently buildable from a clean checkout and there is no CD path.

---

## 11. Free-Tier Constraints Requiring Verification

The repositories are on GitHub and are public at audit time. GitHub’s official billing documentation states that GitHub Actions is free for public repositories using standard GitHub-hosted runners. That removes build-minute charges for the current public setup, but it does not remove operational limits or account-plan constraints.

| Constraint | What must be verified before Phase 4C/4E | Current audit position |
|---|---|---|
| Build minutes | Public standard runners are free per GitHub docs; private-repository minutes depend on plan | Do not assume if repositories become private |
| Concurrency | Maximum concurrent jobs for the account/plan; backend and frontend plus E2E could overlap | Verify before adding parallel E2E |
| Artifact storage/retention | Artifact and cache storage allowances, retention settings, and screenshot/log size | No artifacts are currently uploaded |
| Private-repository restrictions | If visibility changes, minutes/storage/cache quotas and billed overages apply | Repositories are public today |
| Browser automation resources | Chrome launch/download time, memory, CPU, and disk for Chrome + frontend + backend + PostgreSQL | Not measured in CI; verify on `ubuntu-latest` |
| Service containers | PostgreSQL service containers are available on Linux runners, but consume job time and require migration/seed setup | Supported in principle; not configured |
| Caching | Go module cache and npm cache currently used; cache size/eviction policy is account-dependent | Existing workflows use built-in caches |
| Larger runners | Not required for the current small suite; do not enable paid larger runners without approval | Not used |

Official references checked on 2026-10-20:

- GitHub Actions billing: <https://docs.github.com/en/billing/concepts/product-billing/github-actions>
- GitHub Actions limits: <https://docs.github.com/en/actions/reference/limits>

Exact quotas and prices can change and are plan-dependent. They must be rechecked against the actual GitHub account before implementation. No account, plan, paid service, or quota was changed during this audit.

---

## 12. Findings

### BLOCKER

**B-1 — Secret-like values are tracked in `.env.stage` in a public repository.**  
Evidence: `.env.stage` is tracked at HEAD `fd49157`; repository API reports `private: false`; 72 keys; non-empty `DB_PASSWORD`, `STAGING_DB_PASSWORD`, `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `MOCK_WEBHOOK_SECRET`, and `ADMIN_API_KEY`. Values were not printed.  
Impact: Anyone with the public repository can read the values. If any are reused, the database, JWT sessions, admin API, webhook signatures, or encrypted webhook secrets may be compromised. Git history preserves them after deletion.  
Required action: Treat as an incident. Rotate every exposed credential first. Then purge the file from history with an approved process, add secret scanning/push protection, and verify no deployment used those values. Do not merely delete it in a new commit.

**B-2 — A clean backend checkout cannot build, vet, or test.**  
Evidence: `.gitignore:35-37` ignores `swagger/docs.go`, `swagger/swagger.json`, and `swagger/swagger.yaml`; `git ls-files swagger` is empty; `cmd/server/main.go:23` imports the package. From `git archive HEAD`, `go vet`, `go test`, and `go build` all exit 1 with “no required module provides package …/swagger”. The remote `go vet` step failed on all three runs.  
Impact: Backend CI is red by construction, Docker image builds from a clean checkout cannot succeed, and release artifacts cannot be reproduced from the repository.  
Required action: Either commit the generated Swagger package (and stop ignoring it), or generate it in CI and Docker builds with a pinned, reproducible `swag` invocation before `go vet`/`go test`/`go build`. Verify with a clean `git archive` checkout.

**B-3 — The backend Docker CI job fails on a clean checkout because Compose requires database credentials.**  
Evidence: `.github/workflows/ci.yml:53-54` runs `docker compose -f docker-compose.yml config --quiet`; `docker-compose.yml:80-81` requires `DB_USER` and `DB_PASSWORD`. The workflow sets neither. Reproduced with a clean environment: exit 1, “required variable DB_PASSWORD is missing a value”; dummy values make it exit 0. Remote run `39218775505` failed at this exact step.  
Impact: The Docker job never reaches the image build, so Dockerfile/context regressions are not validated.  
Required action: Add a CI-specific Compose file or explicitly set disposable non-secret dummy DB values for validation. Never point CI at real staging/production credentials. After B-2 is fixed, build the image and run a container smoke test.

### HIGH

**H-1 — `main` is unprotected and failing CI is not a required check.**  
Evidence: GitHub branch API reports `protected: false` for both repositories. Backend CI is red on all runs, yet direct pushes to `main` are allowed.  
Impact: Broken code can land without review, and a future CD trigger on `main` could deploy unverified revisions.  
Required action: After CI is green, enable branch protection or rulesets with required checks, required review, no force-push, and no direct bypass for routine changes. Do not enable a CD trigger before this.

**H-2 — Browser E2E is not committed, reproducible, or automated.**  
Evidence: Phase 3 harness, credentials, screenshots, and JSON evidence live under `/tmp`; no E2E directory or workflow exists in either repository; fixture is a reusable Phase 2 merchant rather than a checked-in seed.  
Impact: The local browser PASS cannot be reproduced by another operator or enforced on changes.  
Required action: Commit a deterministic harness and seed/bootstrap, run it against an ephemeral stack in CI, and upload logs/screenshots with bounded retention. Keep API E2E and browser E2E as separate jobs/results.

**H-3 — The staging environment profile is unsafe for dashboard staging.**  
Evidence: `APP_ENV=test`; `FRONTEND_PUBLIC_URL=http://127.0.0.1:18080` (backend origin); expiry disabled; rate limiting disabled; `WEBHOOK_REQUIRE_HTTPS=false`; `DB_SSLMODE=disable`; CORS includes localhost/127.0.0.1 ports.  
Impact: Payment links point at the wrong origin, expiry convergence is not exercised, and production-like protections are absent.  
Required action: Create a separate, correct staging configuration with a public HTTPS frontend origin, expiry enabled, rate limiting enabled, HTTPS webhooks, TLS database connections, and exact HTTPS CORS. Never reuse this mobile-local profile for the dashboard.

**H-4 — No CD pipeline or deployable frontend artifact exists.**  
Evidence: Only `ci.yml` exists in each repository; no registry login, image push, environment, deployment job, frontend Dockerfile, or SPA-fallback host configuration.  
Impact: Deployment is manual and undocumented at the automation layer; there is no promotion, approval, rollback, or smoke-test path.  
Required action: Implement Phase 4E after CI and E2E are green. Start with staging only, use GitHub Environments and short-lived credentials, and require approval before any production deployment.

**H-5 — Backend integration tests are skipped in CI.**  
Evidence: PostgreSQL tests skip without `TEST_DATABASE_URL`; CI has no PostgreSQL service, migrations, or integration job; `make test-integration` exists but is unused.  
Impact: Data-layer, transaction, migration, and concurrency regressions can merge while CI is green (once B-2 is fixed).  
Required action: Add a Linux CI job with an ephemeral PostgreSQL service, run migrations, set `TEST_DATABASE_URL`, and run the integration suite. Keep it separate from the fast unit job.

**H-6 — No repository-level secret-scanning/push-protection configuration or dependency/security review is present.**  
Evidence: No `.github/dependabot.yml`, secret-scanning configuration, CodeQL workflow, dependency-review workflow, or security policy was found. Whether GitHub’s platform-level public-repository scanning is active was not visible in this audit. The public `.env.stage` exposure demonstrates the gap.  
Impact: Credential leaks and vulnerable dependencies may not be detected before merge.  
Required action: Confirm whether platform secret scanning is active; enable push protection and add Dependabot/Renovate and dependency review. Scan container images/SBOMs in Phase 4D/4E.

### MEDIUM

**M-1 — The backend formatting gate cannot fail.**  
Evidence: `.github/workflows/ci.yml:31-32` runs `gofmt -l .`. `gofmt -l` prints unformatted paths but exits 0. A deliberately unformatted temp file printed its path and returned exit 0. The local `Makefile:66-67` already implements the correct check.  
Impact: Unformatted code can pass CI, and the green step gives false assurance.  
Required action: Replace with `test -z "$(gofmt -l .)"` or call `make fmt-check`.

**M-2 — Large build artifacts and a malformed scratch file are tracked.**  
Evidence: `server` is a tracked 36 MiB binary at HEAD; a tracked file named `pect --format '{{range .Config.Env}}{{println .}}{{end}}' opencode-orders-pg-8a | grep -E '^POSTGRES_(USER|DB)='` also exists. The scratch file contains no password/token keys but is clearly accidental.  
Impact: Repository bloat, confusing clone contents, and an unnecessary artifact in the Docker build context.  
Required action: Remove both from version control in a future cleanup, add ignore rules, and ensure the Docker build context excludes scratch/temp files.

**M-3 — GitHub Actions are pinned to mutable major tags; base images are not digest-pinned.**  
Evidence: `actions/checkout@v4`, `actions/setup-go@v5`, `actions/setup-node@v4`; Docker base images use tags such as `golang:1.25-alpine`, `alpine:3.20`, and `migrate/migrate:v4.18.1`.  
Impact: Supply-chain drift or a compromised tag can change CI/build behavior without a repository change. Checkout credentials are also persisted by default.  
Required action: Pin actions to commit SHAs (with Dependabot updates), set `persist-credentials: false` where possible, and pin production image digests after verification.

**M-4 — Frontend workflow lacks timeout and concurrency; backend concurrency does not deduplicate pushes.**  
Evidence: Frontend `.github/workflows/ci.yml` has no `timeout-minutes` or `concurrency`. Backend uses `ci-${{ github.head_ref || github.run_id }}`; on a push, `head_ref` is empty and `run_id` is unique, so consecutive push runs are not cancelled.  
Impact: Redundant or hung runs consume runner capacity and delay feedback.  
Required action: Add bounded timeouts and a branch/SHA-based concurrency group in both workflows.

**M-5 — No coverage, test reports, or build artifacts are retained.**  
Evidence: Both workflows run plain test commands; no coverage tool, JUnit output, or `actions/upload-artifact` step exists.  
Impact: Regressions are visible only in raw logs; there is no trend, no coverage gate, and no artifact for release verification.  
Required action: Add coverage where meaningful, produce machine-readable reports, and upload bounded artifacts for release/E2E evidence.

**M-6 — The frontend CI build is not a deployable artifact.**  
Evidence: CI sets `VITE_API_BASE_URL=http://localhost:8081` and does not upload `dist/`; `.env.production` intentionally leaves the origin empty.  
Impact: A future CD job that reuses this build would ship an app pointed at localhost.  
Required action: Keep the placeholder for validation only, or build a separate deployment artifact with the environment-specific HTTPS origin. Never promote the CI validation build.

**M-7 — `.env.example` contains a non-empty `DB_PASSWORD` and example SMTP values.**  
Evidence: `DB_PASSWORD` is a non-empty seven-character value without an obvious placeholder marker; SMTP fields are example-like but not all explicitly templated.  
Impact: Operators may reuse a sample value, and the file is less clearly a template than it should be.  
Required action: Replace `DB_PASSWORD` with an empty or explicit placeholder and normalize all SMTP examples.

**M-8 — Node version is pinned only in CI.**  
Evidence: `package.json` allows `>=20.19.0`; CI uses Node 22; no `.nvmrc`, `.node-version`, or `packageManager` field exists.  
Impact: Local Node versions can diverge from CI, especially around the custom esbuild test runner.  
Required action: Add a Node version file or `packageManager` field and document the supported LTS line.

### LOW

**L-1 — No Dependabot/Renovate, CODEOWNERS, or security policy.**  
Impact: Dependency and ownership drift is manual. Required action: Add these in Phase 4D, after CI is green.

**L-2 — Documentation claims are stale or use old repository names.**  
Evidence: Existing reports refer to `dhikaarta/pay-gate-backend` / `dhikaarta/pay-gate-dashboard`; `docs/phase-4c-deployment-architecture-specification.md` says remote CI execution was not verified.  
Impact: Operators may trust outdated readiness claims. Required action: Update references and distinguish local from remote CI evidence.

**L-3 — `.dockerignore` does not exclude scratch/temp files.**  
Evidence: `.dockerignore` excludes `.env*`, `.git`, `bin/`, `server`, docs, and editor directories, but not the malformed `pect …` file or a `tmp/` directory.  
Impact: Build context bloat and accidental inclusion of unrelated files in the Docker daemon context. Required action: Add explicit exclusions during cleanup.

**L-4 — No artifact retention policy for future E2E evidence.**  
Impact: Browser screenshots/logs could exceed free storage or retain sensitive data too long. Required action: Set explicit retention and redact headers/cookies before upload.

---

## 13. Recommended Phase 4B Implementation Plan

**Objective:** Make backend CI trustworthy from a clean checkout and close the credential exposure before any CD work.

1. **Contain the secret incident (do first).**
   - Rotate `DB_PASSWORD`, `STAGING_DB_PASSWORD`, `AUTH_JWT_SECRET`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `MOCK_WEBHOOK_SECRET`, `ADMIN_API_KEY`, and any other values from `.env.stage` that could be live.
   - Remove `.env.stage` from version control and purge it from history with an approved rewrite process.
   - Enable secret scanning and push protection; verify no other tracked file contains credentials.
   - Do not print or copy the values during remediation.
2. **Restore clean-checkout reproducibility.**
   - Choose one: commit the generated `swagger/docs.go` (and JSON/YAML if desired), or add a pinned `swag init` step to CI and the Docker build.
   - Verify from `git archive HEAD` that `gofmt`, `go vet`, `go test`, and `go build` pass with no local untracked files.
3. **Fix the Compose CI job.**
   - Add `docker-compose.ci.yml` or set disposable dummy `DB_USER`/`DB_PASSWORD` values in the workflow.
   - Ensure the app-only image build does not require a live database.
   - Add a container smoke test against `/health/ready` where feasible.
4. **Fix the formatting gate.**
   - Replace `gofmt -l .` with `make fmt-check` or `test -z "$(gofmt -l .)"`.
5. **Add integration coverage.**
   - Add a Linux job with a PostgreSQL service container.
   - Run migrations, set `TEST_DATABASE_URL`, and run `make test-integration`.
   - Keep this separate from the fast unit job.
6. **Harden CI.**
   - Pin actions to SHAs; add timeouts and concurrency; set `persist-credentials: false`.
   - Add `golangci-lint` (pinned) if it can be installed reproducibly; otherwise document that `go vet` is the only static-analysis gate.
   - Add coverage and a bounded build artifact if useful.
7. **Clean repository artifacts.**
   - Remove the tracked `server` binary and malformed scratch file; extend ignore rules.
   - Replace `.env.example` `DB_PASSWORD` with a placeholder.
8. **Verify and gate.**
   - Confirm backend CI is green on a pull request from a clean branch.
   - Enable branch protection/required checks after green.

**Exit criteria:** A clean clone builds; backend CI is green; Compose validation and image build pass with non-secret CI values; integration tests run against ephemeral PostgreSQL; `.env.stage` is no longer present in current history or the working tree; exposed credentials are rotated.

---

## 14. Recommended Phase 4C Implementation Plan

**Objective:** Harden frontend CI and make browser E2E reproducible without weakening auth, CORS, or expiry.

1. **Harden the existing frontend workflow.**
   - Add `timeout-minutes` and `concurrency`.
   - Pin actions to SHAs; set `persist-credentials: false`.
   - Keep `npm ci` and all five current gates.
   - Do not upload the localhost-placeholder `dist/` as a deployable artifact.
2. **Commit a deterministic browser E2E harness.**
   - Place it in a documented `e2e/` or `scripts/e2e/` directory.
   - Prefer a maintained browser driver for CI reliability, or port the verified CDP harness; do not add dependencies in Phase 4A.
   - Capture console errors, failed requests, screenshots, and JSON evidence with header/cookie redaction.
3. **Add the E2E runtime.**
   - Use an ephemeral PostgreSQL, backend with mock provider/test config, frontend preview server, and headless Chrome.
   - Add health checks and bounded waits; fail with logs/screenshots on timeout.
4. **Make the fixture reproducible.**
   - Add a seed/bootstrap that creates a merchant, user, and API credentials.
   - Use unique transactions per run and tear down the database.
   - Never commit real credentials; generate ephemeral test values.
5. **Make expiry deterministic.**
   - Use a short test-only expiry interval and poll with a bounded timeout, or inject a clock.
   - Keep expiry enabled. Do not disable it to simplify tests.
   - Add a server-side assertion that terminal transitions are rejected after expiry.
6. **Add a frontend deployment artifact (optional in 4C, required before CD).**
   - Add `Dockerfile` + nginx/Caddy SPA fallback or document a static-host configuration.
   - Build with a real HTTPS API origin only in the deployment pipeline.

**Exit criteria:** Frontend CI remains green with timeout/concurrency; browser E2E runs from a clean checkout with a committed fixture; API E2E and browser E2E are separate; no credentials or secrets are stored in the harness.

---

## 15. Recommended Phase 4D Integration Plan

**Objective:** Prove the two separate repositories work together on every relevant change, not just in isolation.

1. **Choose one integration orchestrator.** Use the frontend repository or a small dedicated E2E repository. Build the backend from a pinned commit and the frontend from a pinned commit.
2. **Trigger the integration run.** Use `workflow_run` after both CI workflows succeed, a scheduled run, or a `repository_dispatch` from the backend. Avoid long-lived cross-repo tokens; prefer a GitHub App or short-lived credentials.
3. **Bring up an ephemeral stack.** PostgreSQL service container → backend migrations → backend mock/test config → frontend preview/build → headless browser.
4. **Run contract checks.**
   - Backend-returned `payment_url` uses the configured frontend origin and `/pay/:identifier`.
   - Public payment endpoint requires no dashboard `Authorization` or cookie.
   - CORS allows only the exact frontend origin; `localhost` and `127.0.0.1` remain distinct.
   - Status transitions and terminal-state rejection agree between API and UI.
   - Expiry behavior is deterministic and does not disable the worker.
5. **Run the Phase 2 API E2E and Phase 3 browser E2E as separate jobs.** Keep their results and artifacts distinct.
6. **Gate both repositories.** Once the integration workflow is stable, add its required check to branch protection/rulesets in both repositories.
7. **Add security gates.** Secret scanning, dependency review, and container scanning run on pull requests; no PR job receives deployment secrets.

**Exit criteria:** A pull request in either repository can trigger a reproducible cross-repo integration run; the local-only `/tmp` fixture is no longer required; no real payment provider, money, or external webhook is used.

---

## 16. Recommended Phase 4E CD-Readiness Plan

**Objective:** Design and implement staging-first CD with approvals, promotion, and rollback. Do not deploy to production in this phase without explicit approval.

1. **Provision external prerequisites outside the repositories.**
   - Private PostgreSQL with TLS (`DB_SSLMODE=verify-full`), separate staging/production databases.
   - Container registry.
   - TLS-terminating ingress/reverse proxy.
   - Static frontend host with SPA history fallback.
   - Domain and DNS.
2. **Create GitHub Environments.**
   - `staging` and `production` with required reviewers, environment-scoped secrets, and deployment branch restrictions.
   - No production secrets available to pull-request jobs.
3. **Add a CD workflow.**
   - Trigger on a release tag or manual `workflow_dispatch`, not every push.
   - Build once, tag by commit SHA, push to the registry, record the digest.
   - Run migrations as a one-shot job before app rollout.
   - Deploy backend, deploy frontend, wait for health checks, run smoke tests.
   - Support rollback to the previous image digest and database backup restore procedure.
4. **Use short-lived credentials.**
   - Prefer OIDC federation for registry/cloud access over long-lived keys.
   - Store only environment-scoped deployment credentials; never commit them.
5. **Correct the URL/secrets matrix.**
   - Backend runtime: `APP_ENV=production`, `FRONTEND_PUBLIC_URL` = HTTPS frontend origin, exact HTTPS CORS, expiry enabled, mock provider/simulator disabled, TLS database.
   - Frontend build: `VITE_API_BASE_URL` = HTTPS backend API origin, `VITE_ENABLE_SIMULATOR=false`.
6. **Verify free-tier capacity.** Confirm concurrency, artifact retention, service-container availability, and browser runtime limits before enabling E2E in CD.
7. **Stop at staging.** Record production promotion as a separate, approved action; do not deploy production during Phase 4E without authorization.

**Exit criteria:** A staging deployment is reproducible from a clean commit, health/smoke checks pass, rollback is tested, secrets are environment-scoped, and production remains behind a manual approval gate.

---

## 17. Exact Files That Would Be Changed in Future Phases

These are concrete proposed paths, not changes made in Phase 4A. Names may be adjusted only by an explicit implementation decision.

| Phase | Repo | File | Purpose |
|---|---|---|---|
| 4B | Backend | `.github/workflows/ci.yml` | Fix `gofmt` gate, add Compose CI env, generate/commit Swagger, pin actions, add integration job |
| 4B | Backend | `.gitignore` | Stop ignoring committed Swagger or clarify generated-output handling |
| 4B | Backend | `swagger/docs.go` | Commit generated API package (option A) |
| 4B | Backend | `swagger/swagger.json` | Commit generated specification (option A; optional) |
| 4B | Backend | `swagger/swagger.yaml` | Commit generated specification (option A; optional) |
| 4B | Backend | `docker-compose.ci.yml` (new) | App-only/dummy-DB Compose validation and smoke test |
| 4B | Backend | `Makefile` | Add a single `ci` target using `fmt-check`, `vet`, `test`, `build`; optional integration target |
| 4B | Backend | `.dockerignore` | Exclude scratch/temp files; keep environment secrets out |
| 4B | Backend | `.env.stage` | Remove from tracking and purge history; do not reuse the current values |
| 4B | Backend | `.env.example` | Replace non-empty `DB_PASSWORD` and normalize SMTP placeholders |
| 4B | Backend | `server` | Remove tracked 36 MiB build artifact |
| 4B | Backend | Malformed tracked scratch file (exact name in M-2) | Remove accidental tracked file |
| 4B | Backend | `docs/phase-4b-backend-ci-remediation.md` (new) | Record remediation and clean-checkout evidence |
| 4C | Frontend | `.github/workflows/ci.yml` | Add timeout/concurrency, SHA pins, credential persistence hardening, optional coverage/artifact |
| 4C | Frontend | `.github/workflows/e2e.yml` (new) | Browser E2E job with PostgreSQL/backend/frontend/Chrome |
| 4C | Frontend | `e2e/run-e2e.mjs` (new) | Committed CDP/Playwright harness entry point |
| 4C | Frontend | `e2e/seed.sh` (new) | Ephemeral merchant/user/transaction fixture bootstrap |
| 4C | Frontend | `e2e/scenarios/login-create-pay.mjs` (new) | Primary browser flow |
| 4C | Frontend | `e2e/scenarios/expiry-and-errors.mjs` (new) | Expiry, terminal-state, 404, and API-failure checks |
| 4C | Frontend | `Dockerfile` (new) | Deployable static SPA image, or documented alternative |
| 4C | Frontend | `nginx.conf` (new) | SPA history fallback and security headers |
| 4C | Frontend | `package.json`, `package-lock.json` | Only if a browser-test dependency is approved; no change in 4A |
| 4C | Frontend | `docs/phase-4c-frontend-e2e-automation.md` (new) | Record E2E reproduction and frontend deployment artifact |
| 4D | Frontend | `.github/workflows/cross-repo-e2e.yml` (new) | Cross-repo pinned-commit stack and contract/browser tests |
| 4D | Frontend | `docker-compose.e2e.yml` (new) | Ephemeral PostgreSQL/backend/frontend stack |
| 4D | Frontend | `e2e/seed-e2e.sql` (new) | Reproducible merchant/user/transaction fixtures |
| 4D | Both | `.github/dependabot.yml` (new) | Dependency update automation |
| 4D | Both | `.github/workflows/dependency-review.yml` (new) | Dependency review and vulnerability gate |
| 4D | Backend | `internal/config/config.go` | Only if environment-aware `FRONTEND_PUBLIC_URL` validation is approved; not required for CI repair |
| 4D | Backend | `internal/config/mock_payment_base_url_test.go` | Regression tests for the approved URL-validation change |
| 4D | Both | `docs/phase-4d-cross-repo-integration.md` (new) | Integration architecture, triggers, and evidence |
| 4E | Backend | `.github/workflows/cd-staging.yml` (new) | Build/push/migrate/deploy/rollback staging-first CD |
| 4E | Backend | `docker-compose.prod.yml` (new) | Production-like deployment definition if Compose is chosen |
| 4E | Frontend | `.github/workflows/deploy-frontend-staging.yml` (new) | Static frontend build/deploy with environment-specific API origin |
| 4E | Either | `scripts/deploy.sh` (new) | Auditable deploy helper |
| 4E | Either | `scripts/rollback.sh` (new) | Auditable rollback helper |
| 4E | Both | `docs/phase-4e-staging-cd.md` (new) | Environment matrix, approval, smoke, rollback, and operational runbook |

Repository settings (branch protection, environments, required reviewers) are not files, but they are required changes in Phase 4D/4E.

---

## 18. Commands and Evidence Used for the Audit

All commands were read-only with respect to application source and CI/CD configuration. Temporary copies were created under `/tmp/opencode/phase4a/`.

### 18.1 Repository discovery

```bash
git rev-parse --show-toplevel
git branch --show-current
git status --short
git log -1 --oneline
git remote -v                 # remote userinfo redacted before display
git ls-files '.env*' '.github/*' 'Dockerfile*' '*compose*' 'Makefile'
```

Result: two separate GitHub repositories, both `main`, both public; backend HEAD `fd49157`; frontend HEAD `d25a232`; pre-existing dirty working trees preserved.

### 18.2 Clean-checkout backend reproduction

```bash
rm -rf /tmp/opencode/phase4a/head-tree
mkdir -p /tmp/opencode/phase4a/head-tree
git archive HEAD | tar -x -C /tmp/opencode/phase4a/head-tree
cd /tmp/opencode/phase4a/head-tree

go vet ./...
go test -count=1 ./...
go build -o /tmp/opencode/phase4a/head-server ./cmd/server
```

Result:

- `go vet ./...` → exit 1, missing `github.com/dhikaarta/pay-gate-backend/swagger`
- `go test -count=1 ./...` → exit 1, `cmd/server` setup failed; other packages passed
- `go build ...` → exit 1, same missing package

### 18.3 Compose clean-environment reproduction

```bash
mkdir -p /tmp/opencode/phase4a/compose-clean
cp docker-compose.yml /tmp/opencode/phase4a/compose-clean/

env -i PATH="$PATH" HOME="$HOME" \
  docker compose --project-directory /tmp/opencode/phase4a/compose-clean \
  -f /tmp/opencode/phase4a/compose-clean/docker-compose.yml config --quiet
```

Result: exit 1; `DB_USER` and `DB_PASSWORD` are required. Adding dummy non-secret values:

```bash
env -i PATH="$PATH" HOME="$HOME" DB_USER=dummy DB_PASSWORD=dummy \
  docker compose --project-directory /tmp/opencode/phase4a/compose-clean \
  -f /tmp/opencode/phase4a/compose-clean/docker-compose.yml config --quiet
```

Result: exit 0. This confirms the CI failure is missing configuration, not invalid Compose syntax.

### 18.4 Formatting-gate proof

```bash
printf 'package x\nfunc  X(){}\n' > /tmp/opencode/phase4a/bad.go
gofmt -l /tmp/opencode/phase4a/bad.go
echo "gofmt_exit=$?"
```

Result: the path was printed and `gofmt_exit=0`. A formatting gate using `gofmt -l` alone therefore cannot fail a job.

```bash
cd /run/media/dhika/Dhika/go-pay-gate/pay-gate-backend
gofmt -l .
make fmt-check
```

Result: no files listed; `make fmt-check` exit 0 on the current tree. The repository is formatted today, but the CI invocation is still non-failing by construction.

### 18.5 Backend current-tree tests

```bash
env -i PATH="$PATH" HOME="$HOME" \
  GOCACHE=/tmp/opencode/phase4a/gocache GOTOOLCHAIN=auto \
  go test -race -count=1 ./...
```

Result: all packages passed; PostgreSQL tests were skipped because `TEST_DATABASE_URL` was not set. This is the correct clean-unit-test behavior, but it is not a clean-checkout test.

### 18.6 Frontend tests

```bash
cd /run/media/dhika/Dhika/pay-gate-dashboard
npm test
```

Result: **266 tests, 266 pass, 0 fail, 0 skipped**. This includes current untracked test files in the working tree.

### 18.7 GitHub remote evidence

Public GitHub API calls (no authentication, no secret values):

```text
GET /repos/DhikDhik15/payment-gateway-service-backend
GET /repos/DhikDhik15/payment-gateway-service-dashboard
GET /repos/DhikDhik15/payment-gateway-service-backend/actions/workflows
GET /repos/DhikDhik15/payment-gateway-service-dashboard/actions/workflows
GET /repos/DhikDhik15/payment-gateway-service-backend/actions/runs
GET /repos/DhikDhik15/payment-gateway-service-dashboard/actions/runs
GET /repos/DhikDhik15/payment-gateway-service-backend/actions/runs/39218775505/jobs
GET /repos/DhikDhik15/payment-gateway-service-backend/branches/main
GET /repos/DhikDhik15/payment-gateway-service-dashboard/branches/main
```

Findings:

- Both repositories: `private: false`, default branch `main`.
- Both branches: `protected: false`.
- Backend: one active workflow, three runs, all `failure`; latest `39218775505` failed `go vet` and Compose validation.
- Frontend: one active workflow, one run `38849642456`, `success` at `d25a232`.

A request for raw job logs returned:

```text
403 Must have admin rights to Repository.
```

Therefore the exact remote log text was not read; the failing step names and local clean-checkout reproduction were used.

### 18.8 Documentation references

Official GitHub documentation checked on 2026-10-20:

- <https://docs.github.com/en/billing/concepts/product-billing/github-actions>
- <https://docs.github.com/en/actions/reference/limits>

Local evidence files:

- `docs/phase-3-local-browser-e2e-verification.md` (Phase 3 browser method and limitations)
- `docs/phase-2-backend-integration-verification.md` (prior API E2E and `.env.stage` concern)
- `.github/workflows/ci.yml` (both repositories)
- `Dockerfile`, `docker-compose.yml`, `Makefile`, `.gitignore`, `.dockerignore`
- `internal/config/config.go` (`FRONTEND_PUBLIC_URL`, CORS, environment validation)
- `package.json`, `package-lock.json`, `scripts/run-tests.sh`
- `internal/repository/*_pg_test.go` (PostgreSQL skip behavior)

---

## 19. Explicit Limitations and Unverified Claims

- **Remote CI logs were not readable.** GitHub returned `403 Must have admin rights` for job logs. The failing steps are known from the public jobs API; the detailed error text was reproduced locally, not copied from remote logs.
- **A clean Docker image build was not executed.** The equivalent clean-tree `go build` fails on the missing Swagger package; the Dockerfile performs the same build. This is a deterministic inference, not an executed `docker build` result.
- **Frontend typecheck, lint, and build were not rerun in this audit.** The committed frontend HEAD passed all of them in remote CI; Phase 3 also recorded local passes. The local 266-test run covers the current working tree, which includes untracked tests not present in the remote run.
- **Browser E2E was not rerun in this audit.** The Phase 3 result is treated as historical local evidence. No claim is made that it ran in CI.
- **No secret values were read into the report.** `.env.stage` values were inspected only enough to determine presence, length class, and placeholder status. The report does not reproduce them. Rotation status and whether any value was already used outside local staging are unknown.
- **GitHub secret-scanning alerts were not inspected.** That API requires authentication and repository permissions. Whether GitHub already alerted on the public `.env.stage` is unverified.
- **Account plan and exact free-tier quotas were not verified.** Public repository status and official documentation were checked; the account’s private-repository allowances, concurrency ceiling, artifact retention settings, and runner capacity were not.
- **No staging or production environment was inspected or deployed.** There is no evidence that a staging host exists or that any deployment pipeline has run.
- **No application behavior was re-certified.** This is a CI/CD-readiness audit. Phase 2 API E2E and Phase 3 browser E2E remain the functional evidence, with their documented limitations.
- **No claim is made that the frontend or backend is production-ready.** Frontend CI readiness is not deployment readiness; backend clean-checkout repair is not production deployment approval.
- **The `.env.example` `DB_PASSWORD` value was classified conservatively.** It is non-empty and not an obvious placeholder, but it was not printed or compared against external systems. The owner should verify it.
- **Repository visibility can change.** If either repository becomes private, GitHub Actions billing, artifact, cache, and concurrency constraints change and must be re-evaluated.

---

## 20. Confirmation That No Application or CI/CD Configuration Was Modified

Confirmed:

- The only file created by this audit is `docs/phase-4a-cicd-readiness-audit.md`.
- No application source file was changed.
- No CI/CD workflow, Dockerfile, Compose file, Makefile, `.gitignore`, `.dockerignore`, environment file, dependency manifest, or lockfile was changed.
- No file was reset, stashed, cleaned, overwritten, committed, or pushed.
- All pre-existing changes in both working trees were preserved.
- No service was deployed, no container was started for deployment, and no real payment provider, money, or external webhook was used.
- No secret, token, cookie, password, or credential value is reproduced in this report.

**Final summary:** Frontend CI is ready and green remotely. Backend CI is not ready: it is red on all recorded runs, cannot build from a clean checkout, and fails Compose validation. The public `.env.stage` secret exposure is a blocker that must be handled as an incident before any further CI/CD work. Browser E2E passed locally but is not reproducible or automated. CD does not exist. The recommended provider is GitHub Actions, based on the repository remotes and existing active workflows; free-tier limits must be verified against the actual account before implementation.
