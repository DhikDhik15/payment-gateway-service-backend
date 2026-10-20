# Phase 4B.1 — Staging Secret Rotation Readiness Audit

**Date:** 2026-10-20  
**Repository:** `DhikDhik15/payment-gateway-service-backend`  
**Branch / HEAD:** `main` / `fd49157`  
**Incident status:** **OPEN — six staging secret categories remain compromised; no credential has been rotated and no Git history has been rewritten.**  
**Audit type:** Read-only staging rotation readiness audit  
**Final decision:** **READY AFTER PREPARATION** — not ready for immediate execution. The webhook-encryption-key step is the hard gate and is **BLOCKED until the preparation gates in §12 are met**.

**Handling note:** This report contains key names, non-secret staging configuration, code references, and procedures only. It contains no secret values, no hashes of secret values, no tokens, no cookies, no encryption keys, and no complete environment-file excerpt. No database was connected to, no authentication was attempted, no session was revoked, no webhook was sent, no container was started or stopped, no migration was run, and nothing was rotated or committed.

---

## 1. Executive summary

The public `.env.stage` exposure covered six populated secret categories: `DB_PASSWORD`, `STAGING_DB_PASSWORD`, `AUTH_JWT_SECRET`, `ADMIN_API_KEY`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, and `MOCK_WEBHOOK_SECRET`. Phase 4B-0 removed the file from the current Git index and added local ignore/template controls, but the values remain in public Git history and must still be treated as compromised.

This audit finds that the current staging profile is local-only and mock-only:
- the application is configured as `APP_ENV=test`;
- the application binds to `127.0.0.1:18080`;
- the PostgreSQL target is `127.0.0.1:55433` / `go_saas_m2_staging`;
- the payment provider is `mock`, with no populated Midtrans keys;
- external merchant webhook delivery, email delivery, expiry processing, and rate limiting are disabled in this profile.

No production host, production database, or production provider endpoint was found in the inspected non-secret host/URL settings. That is evidence for this staging profile, not proof about every possible external integration.

The rotation readiness is uneven:
- `DB_PASSWORD` / `STAGING_DB_PASSWORD`: a manual PostgreSQL role-password rotation is possible, but the staging database is currently offline and changing `POSTGRES_PASSWORD` alone does **not** rotate an existing role password.
- `AUTH_JWT_SECRET`: rotating it invalidates access JWTs after restart, but does **not** invalidate opaque refresh sessions. A separate, approved mass session deletion is required.
- `ADMIN_API_KEY`: only one key is supported. There is no dual-key overlap and no rotation endpoint. Rotation is a hard cutover with an application restart.
- `WEBHOOK_SECRET_ENCRYPTION_KEY`: there is no key version, no key ID, and no decrypt-old/encrypt-new migration path. Replacing the key makes every existing `merchant_webhook_configs.encrypted_secret` unreadable. The supported recovery is to reissue every merchant webhook signing secret through the existing rotate endpoints, not merely re-encrypt the old secrets.
- `MOCK_WEBHOOK_SECRET`: consumable by the inbound mock webhook parser and by internal mock-payment/dashboard self-webhooks. Rotation requires a restart and updates to local harnesses; old signatures are rejected.

**Overall conclusion:** a safe, staging-only rotation can be prepared, but it must not start until the inventory, backup, reissue-authority, startup-procedure, session-revocation, and change-coordination gates are complete. The existing webhook-key situation is the reason this audit cannot return **READY FOR CONTROLLED STAGING ROTATION**.

---

## 2. Scope, method, and limitations

### 2.1 Scope

In scope:
- Git status and preservation of existing work.
- The Phase 4B-0 incident report.
- The actual local staging profile, using only allowlisted non-secret keys.
- Configuration loading, database connection construction, JWT/session behavior, admin-key enforcement, webhook encryption/storage, merchant webhook reissue, mock webhook verification, and audit/logging behavior.
- A staging-only rotation sequence, rollback considerations, validation checklist, and operator requirements.

Out of scope:
- Rotating or revoking any credential.
- Starting or stopping any service or container.
- Connecting to PostgreSQL or executing SQL.
- Revoking sessions or testing authentication.
- Sending or retrying webhooks.
- Migrations, deployment, production access, Git history cleanup, commits, pushes, or force-pushes.

### 2.2 Method and evidence limitations

The staging database container is not running, port `55433` is not listening, and port `18080` is not listening. Therefore:
- `merchant_webhook_configs` row count is **UNKNOWN**.
- `dashboard_sessions` row count is **UNKNOWN**.
- `audit_logs` row count and earliest/latest timestamps are **UNKNOWN**.
- Whether any existing ciphertext was produced by the current key is **UNKNOWN**, although the key is populated and the schema exists.
- The effective log retention on disk is **UNKNOWN**.

No attempt was made to start the database or application to resolve these unknowns, because that would be state-changing and outside this read-only phase.

The repository reference to `docs/mobile-local-staging-execution.md` in `docker-compose.stage.yml:5` points to a file that is absent from this working tree. The actual staging launch command is therefore **UNKNOWN** and not reproducible from the repository alone.

### 2.3 Classification legend

- **VERIFIED** — directly supported by source, configuration, schema, or observed read-only system state.
- **INFERRED** — a reasoned conclusion from the available evidence, but not directly exercised in this audit.
- **UNKNOWN** — cannot be established without state-changing access, missing documentation, or a database/service that is currently offline.

---

## 3. Current staging identity and isolation

### 3.1 Git and incident state

| Finding | Evidence | Class |
|---|---|---|
| Repository is public and branch is `main` at `fd49157`; `origin/main` is also `fd49157`. | `git status`, `git rev-parse`, Phase 4B-0 report | **VERIFIED** |
| Local containment changes are staged: `.env.stage` deleted from the index, `.env.stage.example` added, `.gitignore` modified. The Phase 4B-1 report is the only new untracked file from this phase. | `git status --short`; §16 | **VERIFIED** |
| `.env.stage` remains present locally with restricted permissions and was not edited by this audit. | `ls -la` metadata; no write to `.env.stage` | **VERIFIED** |
| Six secret categories are populated and remain unrotated. | Phase 4B-0 inventory; allowlisted presence/populated checks | **VERIFIED** |
| `.env.stage` remains in public Git history; local index removal does not remove it from history. | Phase 4B-0 report | **VERIFIED** |

### 3.2 Staging configuration (non-secret values only)

The following values were read from allowlisted non-secret keys only. Secret values were not emitted.

| Key | Observed value or status | Class |
|---|---|---|
| `APP_ENV` | `test` | **VERIFIED** |
| `APP_BIND_HOST` | `127.0.0.1` | **VERIFIED** |
| `APP_PORT` | `18080` | **VERIFIED** |
| `FRONTEND_PUBLIC_URL` | `http://127.0.0.1:18080` | **VERIFIED** |
| `CORS_ALLOWED_ORIGINS` | `http://127.0.0.1:18080,http://localhost:18080,http://localhost:35597` | **VERIFIED** |
| `DASHBOARD_BASE_URL` | empty | **VERIFIED** |
| `PAYMENT_PROVIDER` | `mock` | **VERIFIED** |
| `PAYMENT_SIMULATOR_ENABLED` | `false` | **VERIFIED** |
| `PAYMENT_EXPIRY_ENABLED` | `false` | **VERIFIED** |
| `RATE_LIMIT_ENABLED` | `false` | **VERIFIED** |
| `WEBHOOK_REQUIRE_HTTPS` | `false` | **VERIFIED** |
| `WEBHOOK_DELIVERY_ENABLED` | `false` | **VERIFIED** |
| `EMAIL_ENABLED` | `false` | **VERIFIED** |
| `EMAIL_WORKER_ENABLED` | `false` | **VERIFIED** |
| `DB_HOST` | `127.0.0.1` | **VERIFIED** |
| `DB_PORT` | `55433` | **VERIFIED** |
| `DB_NAME` | `go_saas_m2_staging` | **VERIFIED** |
| `DB_USER` | `go_saas_m2_staging` | **VERIFIED** |
| `DB_SSLMODE` | `disable` | **VERIFIED** |
| `STAGING_DB_PORT` | `55433` | **VERIFIED** |
| `STAGING_DB_NAME` | `go_saas_m2_staging` | **VERIFIED** |
| `STAGING_DB_USER` | `go_saas_m2_staging` | **VERIFIED** |
| `STAGING_DB_HOST` | absent | **VERIFIED** |
| `MIGRATION_DATABASE_URL` | empty | **VERIFIED** |
| `DATABASE_URL` | empty | **VERIFIED** |
| `TEST_DATABASE_URL` | empty | **VERIFIED** |
| `TRUSTED_PROXIES` | `none` | **VERIFIED** |
| `MIDTRANS_BASE_URL` | `https://app.sandbox.midtrans.com` | **VERIFIED** |
| `MIDTRANS_SERVER_KEY`, `MIDTRANS_CLIENT_KEY` | empty | **VERIFIED** |
| `AUTH_ACCESS_TOKEN_TTL` | `15m` | **VERIFIED** |
| `AUTH_REFRESH_TOKEN_TTL` | `168h` | **VERIFIED** |

### 3.3 Isolation evidence

| Finding | Evidence | Class |
|---|---|---|
| The staging Compose file provisions only PostgreSQL, binds it to `127.0.0.1:${STAGING_DB_PORT:-55433}`, and uses a dedicated named network and volume. | `docker-compose.stage.yml:1-51` | **VERIFIED** |
| The staging database container is not present/running; port `55433` is not listening. Only the named volume `go-saas-m2-staging-pgdata` remains. | `docker ps -a`, `docker volume ls`, `ss -ltnp` | **VERIFIED** |
| The staging backend is not running; port `18080` is not listening. | `ss -ltnp`; process list | **VERIFIED** |
| A separate protected backend container `pay-gate-app` is running on `0.0.0.0:8081`; it was not accessed or changed. | `docker ps -a`, `ss -ltnp` | **VERIFIED** |
| No inspected staging host/URL setting points to a production host. The database and browser origins are loopback; the provider is mock; the Midtrans base URL is sandbox and its keys are empty. | §3.2 | **VERIFIED** (for inspected keys) |
| Whether every possible external integration key (`SMTP_*`, provider keys, future URLs) is empty or non-production is not fully proven. | `SMTP_HOST` empty; reviewed allowlist; no full secret inspection | **UNKNOWN** residual |
| `FRONTEND_PUBLIC_URL` points at the backend’s own port (`18080`), not the SPA origin. This is a known staging-profile defect and may break dashboard/payment-link validation. | §3.2; Phase 2 report R-2 | **VERIFIED** value; impact **INFERRED** |
| The staging startup procedure is not documented in the repository. `docker-compose.stage.yml` references a missing document, and `config.Load()` loads only `.env` via `godotenv.Load()`, not `.env.stage`. | `docker-compose.stage.yml:5`; `internal/config/config.go:308-314`; missing file | **VERIFIED** absence; startup method **UNKNOWN** |
| As a result, a plain `make run` or `./bin/server` without an operator-exported environment would not load `.env.stage` and must not be treated as a staging start. | `Makefile:36-38`; `config.Load()` behavior | **INFERRED** |

### 3.4 Staging controls that are intentionally disabled

`APP_ENV=test` bypasses production-only checks. In this profile:
- rate limiting is disabled (`RATE_LIMIT_ENABLED=false`);
- outbound merchant webhook delivery is disabled (`WEBHOOK_DELIVERY_ENABLED=false`);
- inbound/outbound email is disabled (`EMAIL_ENABLED=false`, `EMAIL_WORKER_ENABLED=false`);
- expiry processing is disabled (`PAYMENT_EXPIRY_ENABLED=false`);
- simulator is disabled (`PAYMENT_SIMULATOR_ENABLED=false`);
- webhook URL HTTPS enforcement is disabled (`WEBHOOK_REQUIRE_HTTPS=false`);
- database TLS is disabled (`DB_SSLMODE=disable`);
- the server is loopback-only.

These settings are acceptable only for isolated local staging. They must not be copied into a shared or production profile.

---

## 4. Verified configuration and consumers, by secret category

| Secret | Staging status | Consumers / behavior | Code evidence | Class |
|---|---|---|---|---|
| `DB_PASSWORD` | populated; value withheld | Required at startup; assembled into the PostgreSQL DSN; used by the pgx pool; changing it requires an application restart. | `internal/config/config.go:270-305`, `:597-611`, `:634-642`; `cmd/server/main.go:110-118` | **VERIFIED** |
| `STAGING_DB_PASSWORD` | populated; value withheld | Consumed by `docker-compose.stage.yml` as `POSTGRES_PASSWORD` for the staging PostgreSQL container. The Go application does not read this name directly. | `docker-compose.stage.yml:29-32` | **VERIFIED** |
| `AUTH_JWT_SECRET` | populated; value withheld | Injected into `AuthConfig.JWTSecret`; used to sign and verify HS256 access JWTs. Access JWT TTL is 15 minutes. | `internal/config/config.go:431-452`; `cmd/server/main.go:273-283`; `internal/service/jwt_util.go:31-116`; `internal/service/auth_service.go:429-431` | **VERIFIED** |
| `ADMIN_API_KEY` | populated; value withheld | Injected into `AdminConfig.APIKey`; enforced by `AdminAuth` for `/api/v1/admin` and admin-only merchant create/get routes. | `internal/config/config.go:338-341`, `:829-835`; `internal/middleware/admin_auth.go:23-59`; `cmd/server/main.go:616-645` | **VERIFIED** |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | populated; value withheld | Parsed into `WebhookDelivery.EncryptionKey`; used to AES-256-GCM encrypt/decrypt merchant webhook signing secrets. | `internal/config/config.go:415-429`, `:667-668`; `internal/config/config.go:1179-1191`; `internal/service/webhook_crypto.go:30-99` | **VERIFIED** |
| `MOCK_WEBHOOK_SECRET` | populated; value withheld | Used by the mock inbound parser for HMAC-SHA256 verification and by internal mock-payment/dashboard self-webhook signing. | `internal/config/config.go:845-853`; `cmd/server/main.go:237-241`, `:454-458`; `internal/service/webhook_parser.go:97-133`, `:179-187` | **VERIFIED** |

### 4.1 DB credential relationship

`DB_USER` and `STAGING_DB_USER` are both `go_saas_m2_staging`, and `DB_NAME` and `STAGING_DB_NAME` are both `go_saas_m2_staging`. This strongly suggests the application role and the container’s bootstrap role are the same PostgreSQL role.

Whether `DB_PASSWORD` and `STAGING_DB_PASSWORD` currently hold the same value cannot be established without comparing secret values, which this audit did not do. The safe assumption is that they are the same credential set and must be rotated together. This relationship is **INFERRED**; the individual values remain **UNKNOWN** and undisclosed.

### 4.2 Migration URL

`MIGRATION_DATABASE_URL`, `DATABASE_URL`, and `TEST_DATABASE_URL` are empty in the staging profile. The Compose migration job can fall back to a URL assembled from `DB_*` variables, while `make migrate-up` requires an explicit `DB_URL`/`DATABASE_URL`/`TEST_DATABASE_URL`. No staging-specific migration wrapper exists in the repository.

| Finding | Evidence | Class |
|---|---|---|
| `MIGRATION_DATABASE_URL` is empty. | §3.2 | **VERIFIED** |
| The Compose migration fallback constructs a URL from `DB_USER`, `DB_PASSWORD`, `DB_HOST`, `DB_PORT`, `DB_NAME`, and `DB_SSLMODE`. | `docker-compose.yml:152-178` | **VERIFIED** |
| `make migrate-up` requires an explicit `DB_URL` or `DATABASE_URL`. | `Makefile:157-165` | **VERIFIED** |
| The actual staging migration command used previously is not documented. | missing `docs/mobile-local-staging-execution.md`; no staging target | **UNKNOWN** |

---

## 5. Database credential rotation mechanism

### 5.1 Mechanism

There is no application endpoint or background process that rotates the database password. The application reads `DB_HOST`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, and `DB_SSLMODE` at startup, constructs a DSN, and creates a fixed pgx pool. The rotation mechanism is therefore an external PostgreSQL administrative action followed by an application restart.

Relevant code:
- `internal/config/config.go:270-305` — `DatabaseConfig` and `DSN()`.
- `internal/config/config.go:597-611` — `requiredEnv` for `DB_HOST`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`.
- `cmd/server/main.go:104-118` — pool creation using `cfg.Database.DSN()`.
- `docker-compose.stage.yml:29-32` — PostgreSQL bootstrap environment.

### 5.2 Critical PostgreSQL behavior

The staging Compose file passes `STAGING_DB_PASSWORD` to the official PostgreSQL image as `POSTGRES_PASSWORD`. That variable is a **bootstrap/init** setting. Because the named volume `go-saas-m2-staging-pgdata` already exists, changing only the Compose environment and restarting the container is not sufficient to change the existing role password. The role must be changed with an administrative password change in PostgreSQL, then both environment names must be updated to the new value.

This behavior is **INFERRED** from the official PostgreSQL image design and the presence of an existing volume; it is not exercised in this read-only audit. It must be confirmed by the operator against the actual staging database before relying on it.

### 5.3 Rotation sequence and dependencies

1. Keep the staging application stopped. The staging backend is already not running.
2. Start only the staging PostgreSQL service in isolation, if needed, using the existing staging Compose project; do not touch the protected `pay-gate-app` on `8081`.
3. Take a full staging database backup (for example, a consistent `pg_dump` to a restricted local path) and record the backup location and checksum without printing credentials.
4. Confirm the role, database, and migration version with read-only queries.
5. Change the PostgreSQL role password through an administrative session. Do not put the password in shell history, process arguments, logs, or repository files. Prefer a secret-manager/stdin or interactive mechanism.
6. Update `DB_PASSWORD` and `STAGING_DB_PASSWORD` together in the local `.env.stage` (never committed) and in the approved secret store. If the two values represent the same role, they must match.
7. Update `MIGRATION_DATABASE_URL` only if it is going to be used; it is currently empty. If it is populated, rotate any embedded password and verify URL encoding. Prefer an explicitly supplied staging `DATABASE_URL` over command-line secrets.
8. Start the application with the new database credential and verify `/health` and `/health/ready`.
9. Verify the old database password is rejected by a new connection attempt (without printing either password).
10. Confirm `schema_migrations` is clean and at the expected version; do not run migrations unless a separate, approved need exists.

**Dependencies:** database service available; backup completed; application stopped; both env names updated; any migration URL updated; a recovery path for role-password lockout prepared.

**Recovery:** do not restore the old compromised password. If the new password locks out normal access, use local container/OS-level administrative access or a verified backup recovery path, then set a fresh password and update both environment names.

### 5.4 Database findings

| Finding | Class |
|---|---|
| Password rotation is an external `ALTER ROLE`-style operation; no application rotation API exists. | **VERIFIED** |
| Application restart is required after the env change. | **VERIFIED** |
| `STAGING_DB_PASSWORD` changes the container bootstrap env, not an existing role password. | **INFERRED** |
| `DB_PASSWORD` and `STAGING_DB_PASSWORD` are intended for the same role/database names. | **VERIFIED** names; value relationship **INFERRED** |
| The staging database is currently offline, so the rotation cannot be executed or inventoried yet. | **VERIFIED** |
| The actual staging migration command and database backup procedure are undocumented. | **UNKNOWN** |

---

## 6. JWT access tokens and refresh-session revocation

### 6.1 Access-token behavior

The dashboard uses HS256 JWTs with a single signing secret:
- `internal/service/jwt_util.go:31-33` fixes the header to HS256.
- `internal/service/jwt_util.go:42-52` defines `sub`, `mid`, `role`, `iat`, `exp`, `jti`, and optional `sid`.
- `internal/service/jwt_util.go:56-116` signs and verifies the signature and expiry.
- `internal/middleware/dashboard_auth.go:33-105` verifies the token and then loads the current user and merchant, rejecting disabled users/inactive merchants.

There is no key ID (`kid`), key version, issuer/audience check, or dual-secret verification. Replacing `AUTH_JWT_SECRET` and restarting causes all previously issued access tokens to fail signature verification immediately. Old access tokens cannot be selectively retained.

### 6.2 Refresh-session behavior

Refresh tokens are separate from JWTs:
- `internal/service/jwt_util.go:120-143` generates 32 random bytes as an opaque hex token and stores only its SHA-256 hash.
- `migrations/000014_create_merchant_users.up.sql:39-61` defines `dashboard_sessions` with `refresh_token_hash`, `expires_at`, and indexes.
- `internal/service/auth_service.go:332-394` refreshes by looking up the hash and does not use `AUTH_JWT_SECRET`.
- `internal/repository/dashboard_session_repository.go:130-148` retrieves a session by hash.
- `internal/repository/dashboard_session_repository.go:405-436` supports deletion by user, by merchant, and by expiry.

Consequences:
- Rotating `AUTH_JWT_SECRET` **does not** revoke refresh tokens.
- With the current `AUTH_REFRESH_TOKEN_TTL=168h`, unrevoiced refresh sessions can remain usable for up to seven days.
- Deleting sessions does **not** invalidate already-issued access JWTs; those remain valid until their 15-minute expiry unless the JWT secret is also rotated.
- The safest sequence is therefore: stop the application, rotate the JWT secret, delete all refresh sessions, then start with the new secret.

### 6.3 Session-revocation capabilities

| Capability | Evidence | Class |
|---|---|---|
| Logout revokes one session by refresh-token hash or by the stable `sid` + user. | `internal/service/auth_service.go:270-314` | **VERIFIED** |
| `DeleteByUserID` exists and is used by user lifecycle/password changes. | `internal/service/dashboard_user_service.go:128-135`, `:350-355`, `:542-546`; `internal/repository/dashboard_session_repository.go:405-412` | **VERIFIED** |
| `DeleteByMerchantID` exists and is used when a merchant is suspended/deactivated. | `internal/service/merchant_service.go:199-201`; `internal/repository/dashboard_session_repository.go:414-428` | **VERIFIED** |
| There is no global “revoke all dashboard sessions” API route. | Route inventory in `cmd/server/main.go`; only login/logout/refresh/me and lifecycle actions found | **VERIFIED** |
| `DeleteExpired` exists but is not scheduled by any worker in the inspected code. | `internal/repository/dashboard_session_repository.go:430-437`; no call in `cmd/server/main.go` | **VERIFIED** |
| Mass revocation requires an approved database operation such as deleting all `dashboard_sessions` rows, after backup. | Derived from schema and repository capabilities | **INFERRED** |

### 6.4 Required JWT/session procedure

1. Take the database backup.
2. Stop the staging application.
3. Generate a new `AUTH_JWT_SECRET` with at least 32 random bytes and store it in the approved secret store.
4. Update the environment and restart only after the database credential rotation is complete.
5. Delete all `dashboard_sessions` rows through an approved, audited operator action. Do not inspect or print refresh-token hashes.
6. Start the application with the new JWT secret.
7. Verify an old access token is rejected with 401, a new login succeeds, and an old refresh token is rejected with 401.
8. Notify dashboard users that they must sign in again and clear stale browser cookies.

**Recovery:** do not restore the old JWT secret. If new token issuance fails, correct the configuration and issue another new secret. Session deletion is intentionally one-way; users can sign in again.

---

## 7. Admin API key rotation behavior

### 7.1 Enforcement

`AdminAuth` uses one configured key and a constant-time comparison:
- `internal/middleware/admin_auth.go:16-23` documents the shared-key design and its limitations.
- `internal/middleware/admin_auth.go:30-38` returns 503 when the key is empty.
- `internal/middleware/admin_auth.go:40-53` rejects missing/wrong keys with 401.
- `internal/middleware/admin_auth.go:55-58` marks successful requests as an admin actor.
- `internal/middleware/admin_auth.go:62-97` records `ADMIN_AUTH_FAILED` audit events for failures.

The key protects the `/api/v1/admin` group and admin-only merchant create/get routes (`cmd/server/main.go:544-545`, `:616-645`). There is no admin-key rotation endpoint, key ID, dual-key overlap, or old-key grace period.

### 7.2 Rotation sequence

1. Generate a new key of at least 32 random bytes (the production minimum).
2. Schedule a maintenance window because old and new keys cannot both be valid.
3. Update `ADMIN_API_KEY` in the local `.env.stage` and approved secret store.
4. Restart the application; the old key is rejected immediately.
5. Update all admin/ops clients and scripts atomically.
6. Verify the old key returns 401 and the new key succeeds on a non-mutating admin endpoint (for example a read-only `GET /api/v1/admin/settlements` or `GET /api/v1/merchants/:id` with a known staging ID).
7. Record the change manually; the application does not emit a dedicated “admin key rotated” audit event.

### 7.3 Admin findings

| Finding | Class |
|---|---|
| Only one admin key is supported; no dual-key overlap exists. | **VERIFIED** |
| Old key rejection after restart is immediate. | **VERIFIED** |
| Empty `ADMIN_API_KEY` disables admin routes with 503. | **VERIFIED** |
| Production enforces a 32-byte minimum; `APP_ENV=test` does not. | **VERIFIED** |
| Admin authentication failures are audited without the key value. | **VERIFIED** |
| Successful admin key rotation is not separately audited by the application. | **VERIFIED** |

---

## 8. Webhook encryption key and stored data format

### 8.1 Crypto and storage format

- `internal/service/webhook_crypto.go:30-48` accepts a 32-byte key as 64 hex characters or standard base64.
- `internal/service/webhook_crypto.go:50-69` encrypts with AES-256-GCM.
- Ciphertext format is `base64(nonce || ciphertext || tag)`; there is no version byte, key ID, associated data, or algorithm marker.
- `internal/service/webhook_crypto.go:72-99` decrypts with one key only; a wrong key returns `ErrDecryptFailed`.
- `migrations/000010_create_merchant_webhook_configs.up.sql:6-19` stores `encrypted_secret` as a single `TEXT` column; there is no `key_version` column.
- `internal/config/config.go:1179-1191` parses the encryption key.
- `internal/service/merchant_webhook_dispatcher.go:229-238` loads the merchant config and decrypts its secret before signing a delivery.
- `internal/service/merchant_webhook_dispatcher.go:138-144` marks a delivery permanently failed with the safe category `webhook secret unavailable` if decryption fails.

### 8.2 Rotation capability

There is **no** decrypt-old/encrypt-new migration path in the code:
- no key ring, key version, `kid`, or previous-key fallback;
- no re-encryption command or migration;
- no admin endpoint that rewrites all stored ciphertext;
- no way to make existing rows readable under a replacement key without the old key.

Replacing `WEBHOOK_SECRET_ENCRYPTION_KEY` directly would make every existing `encrypted_secret` row unreadable to the application. With `WEBHOOK_DELIVERY_ENABLED=false`, that would not immediately produce outbound failures, but it would break merchant webhook recovery and any later delivery attempt until every row is reissued.

### 8.3 Security conclusion on re-encryption

Even if a decrypt-old/encrypt-new tool were built, merely preserving the old merchant webhook signing secrets would not remediate the incident: the old encryption key and the database password were both exposed, so the old merchant signing secrets must be considered decryptable by an attacker. The correct recovery is to generate **new merchant webhook signing secrets** for every merchant and overwrite the old ciphertext. Re-encryption alone is insufficient.

### 8.4 Webhook-key findings

| Finding | Class |
|---|---|
| AES-256-GCM with a single 32-byte key is used. | **VERIFIED** |
| Ciphertext is `base64(nonce || ciphertext || tag)` with no version/key ID. | **VERIFIED** |
| No decrypt-old/encrypt-new or dual-key support exists. | **VERIFIED** |
| Direct key replacement makes existing ciphertext unreadable. | **VERIFIED** |
| The number of existing `merchant_webhook_configs` rows is unknown because the staging DB is offline. | **UNKNOWN** |
| Existing merchant signing secrets must be reissued, not just re-encrypted. | **INFERRED** from the joint DB/key exposure; conservative and required |
| The current staging delivery worker is disabled, reducing immediate outbound impact. | **VERIFIED** |

---

## 9. Merchant webhook signing-secret reissue

### 9.1 Existing reissue endpoints

There are two supported application routes:

1. **Dashboard:** `POST /api/v1/dashboard/webhook-config/rotate`
   - Requires a dashboard JWT and `OWNER` or `ADMIN` role.
   - Route: `cmd/server/main.go:832-835`.
   - Handler: `internal/handler/dashboard_webhook_handler.go:87-112`.

2. **Merchant API:** `POST /api/v1/merchants/:id/webhook/rotate`
   - Requires merchant authentication.
   - Route: `cmd/server/main.go:560-572`.
   - Handler: `internal/handler/merchant_webhook_handler.go:96-125`.

### 9.2 What `RotateSecret` does

`internal/service/merchant_webhook_service.go:137-181`:
- finds the existing config;
- generates a fresh 32-byte `whsec_` signing secret;
- encrypts it with the current `encKey`;
- records an audit event `WEBHOOK_CONFIG_CHANGED` with `change=SECRET_ROTATED` and only the destination host;
- calls `UpdateSecret`, which overwrites `encrypted_secret` and `updated_at`;
- returns the new plaintext secret once in the response.

It does **not** decrypt the old ciphertext. That means it can still work after the encryption key has been replaced, as long as the application can load the row and update it. The old signing secret is not needed.

### 9.3 Operational constraint

There is no admin bulk reissue endpoint and no list-all-webhook-configs admin API. Reissue requires per-merchant authorization:
- a dashboard `OWNER`/`ADMIN` login for each merchant, or
- merchant API credentials for each merchant.

The staging database can provide the merchant IDs and config count, but it cannot provide plaintext dashboard passwords or merchant API keys. The operator must confirm that each staging merchant can be reissued, or prepare an approved one-off reissue tool. That tool does not exist in this repository.

### 9.4 Reissue procedure after key replacement

1. Keep the application stopped or keep `WEBHOOK_DELIVERY_ENABLED=false`.
2. Complete database backup and inventory.
3. Replace the encryption key in the environment/secret store.
4. Start the application with delivery still disabled.
5. Enumerate every `merchant_webhook_configs` row by `merchant_id` and status using read-only SQL; do not select or print `encrypted_secret`.
6. For every row, invoke the dashboard or merchant rotate endpoint with the appropriate staging authorization.
7. Capture each new plaintext secret exactly once and deliver it to the merchant/harness through the approved channel. Never log it.
8. Verify every row has a new `updated_at` after the rotation start and no row remains on the old ciphertext.
9. Optionally verify each row decrypts under the new key with a tool that does not print plaintext; do not dump ciphertext or secrets.
10. Only then consider enabling delivery, and only against a local loopback receiver. Do not send real external webhooks.

### 9.5 If reissue cannot cover every row

Do not enable delivery. Either:
- keep the affected merchant config disabled with explicit approval, or
- prepare and test a one-off controlled reissue tool before changing the key.

Deleting webhook configs is destructive and must not be done without explicit human approval.

---

## 10. Mock webhook secret consumers and verification tests

### 10.1 Consumers

- `internal/config/config.go:845-853` loads `MOCK_WEBHOOK_SECRET`.
- `cmd/server/main.go:237-241` registers `NewMockWebhookParser` when the provider is `mock`.
- `internal/service/webhook_parser.go:97-133` verifies HMAC-SHA256 over the raw body and compares in constant time.
- `internal/handler/webhook_handler.go:45-73` exposes `POST /api/v1/webhooks/providers/:provider` with no merchant authentication; the provider signature is the authenticator.
- `internal/handler/mock_payment_handler.go:102-104` and `internal/handler/dashboard_payment_handler.go:331-334` self-sign mock transitions with the configured secret.
- `internal/handler/simulator_handler.go:292-295` also self-signs when the simulator is used.

The mock provider is active in staging because `PAYMENT_PROVIDER=mock`. `PAYMENT_SIMULATOR_ENABLED=false` disables the simulator path, but the public mock-payment routes and inbound mock webhook parser remain part of the mock provider surface.

### 10.2 Verification coverage

Existing tests cover:
- valid mock signatures;
- invalid signatures;
- missing signatures;
- a correctly formatted signature produced with the wrong secret;
- production rejection of the known default/short mock secret.

Evidence: `internal/service/webhook_expiry_test.go:205-254`; `internal/handler/webhook_handler_test.go:187-214`, `:344-357`; `internal/config/phase_8d6_security_test.go:312-324`.

### 10.3 Rotation procedure

1. Generate a new `MOCK_WEBHOOK_SECRET` of at least 32 random bytes.
2. Update the local `.env.stage` and approved secret store.
3. Restart the application.
4. Update every local mobile/harness/script that signs mock webhooks.
5. Verify a valid new signature is accepted and an old/wrong signature is rejected with 401.
6. Do not expose the staging listener beyond loopback, and do not use real provider credentials.

### 10.4 Mock findings

| Finding | Class |
|---|---|
| The mock parser is registered only when the configured provider is `mock`; staging is `mock`. | **VERIFIED** |
| The inbound mock webhook route is unauthenticated and relies on the HMAC secret. | **VERIFIED** |
| Internal mock/dashboard/simulator paths self-sign with the same secret. | **VERIFIED** |
| Rotating the secret requires restarting the app and updating external harnesses. | **VERIFIED** |
| `APP_ENV=test` does not enforce the production 32-byte minimum. | **VERIFIED** |
| Existing tests cover valid, invalid, missing, and wrong-secret signatures. | **VERIFIED** |
| Exposure of the secret permits forged mock webhook events; the loopback bind limits reachability. | **INFERRED** |

---

## 11. Audit logging and retention

### 11.1 Security audit trail

`migrations/000018_create_audit_logs.up.sql:1-54` defines an append-only `audit_logs` table with no foreign keys and no UPDATE/DELETE APIs. Known actions include:
- `MERCHANT_CREATED`, `MERCHANT_STATUS_CHANGED`;
- `USER_CREATED`, `USER_ROLE_CHANGED`, `USER_STATUS_CHANGED`, `PASSWORD_CHANGED`;
- `INVITATION_CREATED`, `INVITATION_ACCEPTED`;
- `API_KEY_CREATED`, `API_KEY_ROTATED`, `API_KEY_REVOKED`;
- `LEGACY_CREDENTIAL_MIGRATED`, `LEGACY_CREDENTIAL_DISABLED`;
- `WEBHOOK_CONFIG_CHANGED`;
- `ADMIN_AUTH_FAILED`.

`WEBHOOK_CONFIG_CHANGED` metadata is limited to fields such as `change`, `enabled`, and `destination_host`; it does not include the signing secret. `ADMIN_AUTH_FAILED` records a reason and route path, not the submitted key.

There is no audit read API in the inspected route table. Investigation requires an approved read-only database query or export. There is no cleanup worker or retention setting for `audit_logs`.

### 11.2 Application logs

The application writes JSON logs to stdout (`cmd/server/main.go:61-64`). The request logger records:
- request ID;
- HTTP method;
- route template (not the raw path);
- status;
- latency;
- client IP.

It does not log headers, bodies, cookies, tokens, or query strings (`cmd/server/main.go:1110-1130`). Startup logs record application name, environment, and port, not secret values (`cmd/server/main.go:75-79`). Authentication logs avoid token/session identifiers.

### 11.3 Log retention

| Finding | Evidence | Class |
|---|---|---|
| `audit_logs` is append-only and has no application delete/update path. | `migrations/000018`; `internal/repository/audit_log_repository.go:37-75`; no delete/update calls found | **VERIFIED** |
| No audit-retention worker or cleanup configuration exists. | repository/service scan | **VERIFIED** absence |
| Audit rows remain until the database/volume/backups are deleted or manually purged by an operator. | schema and volume presence | **INFERRED** |
| ELK Logstash tails Docker container JSON logs only. | `elk/logstash/pipeline/logstash.conf:1-45`; `docker-compose.elk.yml:50-54` | **VERIFIED** |
| The staging backend is a host process, not a container; its stdout is not automatically collected by the Docker-log Logstash pipeline. | `docker-compose.stage.yml:4-5`; no staging app service | **INFERRED** |
| ELK containers are not running at audit time. | `docker ps -a` | **VERIFIED** |
| No Elasticsearch ILM/index-retention policy is defined in the repository. | ELK config scan | **VERIFIED** absence |
| Effective application-log retention is not defined. | no policy; host-process logging | **UNKNOWN** |

### 11.4 Rotation-specific audit gaps

The application does not emit audit events for:
- database role-password changes;
- `AUTH_JWT_SECRET` rotation;
- mass deletion of `dashboard_sessions`;
- `ADMIN_API_KEY` rotation;
- `WEBHOOK_SECRET_ENCRYPTION_KEY` rotation;
- `MOCK_WEBHOOK_SECRET` rotation.

The operator must maintain a separate change record containing:
- date/time and operator;
- environment (`local staging` only);
- secret category (not value);
- action and result;
- affected row/user counts;
- validation performed;
- rollback decision.

### 11.5 Logging requirements during rotation

1. Preserve existing `audit_logs` before any database restore or volume reset; export only safe columns.
2. Do not print or log old/new secrets, hashes, tokens, cookies, ciphertext, or full connection URLs.
3. Record the six categories as changed without values.
4. Capture counts before/after for:
   - `merchant_webhook_configs` total and reissued;
   - `dashboard_sessions` before deletion and after (expected zero);
   - `audit_logs` before/after.
5. Capture expected audit events:
   - `WEBHOOK_CONFIG_CHANGED` / `SECRET_ROTATED` for each reissued merchant;
   - `ADMIN_AUTH_FAILED` for the old admin key validation;
   - any lifecycle events if a merchant is suspended/disabled.
6. Keep `WEBHOOK_DELIVERY_ENABLED=false` and `EMAIL_ENABLED=false` during validation.
7. If staging logs are needed in ELK, document a separate log-shipping procedure; do not assume the current ELK pipeline sees host-process logs.

---

## 12. Rotation sequence, dependencies, and required operator actions

### 12.1 Preparation gates — all must be complete before any rotation

| Gate | Requirement | Why |
|---|---|---|
| P-1 | Reconfirm staging scope: `APP_ENV=test`, loopback bind, staging DB/volume names, mock provider, empty Midtrans keys, no production URLs. | Prevent production impact. |
| P-2 | Keep the staging application stopped; do not touch the protected `pay-gate-app` on `8081`. | Avoid mixed old/new credentials and scope creep. |
| P-3 | Start only the staging PostgreSQL service if needed; take a consistent backup to a restricted path; record location/checksum without secret output. | Recovery and audit. |
| P-4 | Obtain read-only inventory: `merchant_webhook_configs` count/status, `dashboard_sessions` count, `audit_logs` count/time range, and migration version/dirty state. Do not select secret columns. | Unknowns drive the webhook and session plan. |
| P-5 | Confirm per-merchant webhook reissue authority or prepare an approved one-off reissue tool. | Required for the webhook-key gate. |
| P-6 | Generate all replacement secrets in an approved secret store, not in the repository, shell history, or command-line arguments. | Prevent re-exposure. |
| P-7 | Document the actual staging start command, ensuring `.env.stage` is loaded explicitly and not confused with `.env`. Fix `FRONTEND_PUBLIC_URL` for dashboard validation. | Reproducibility and correct L1 validation. |
| P-8 | Obtain approval for mass `dashboard_sessions` deletion and schedule the admin-key client cutover. | Session invalidation and hard-cutover coordination. |
| P-9 | Keep `WEBHOOK_DELIVERY_ENABLED=false`, `EMAIL_ENABLED=false`, and `EMAIL_WORKER_ENABLED=false`; do not use external provider or webhook destinations. | No real external side effects. |
| P-10 | Open a manual incident/change log for the rotation. | The app does not audit all rotation actions. |

### 12.2 Recommended order

1. **Preparation:** complete P-1 through P-10.
2. **Database credential:** rotate the PostgreSQL role password; update `DB_PASSWORD` and `STAGING_DB_PASSWORD`; restart/verify database connectivity.
3. **JWT + refresh sessions:** generate new `AUTH_JWT_SECRET`; stop app; delete all `dashboard_sessions`; start with new secret; verify old access/refresh tokens fail.
4. **Admin key:** generate new `ADMIN_API_KEY`; restart; verify old key 401 and new key works; update all clients.
5. **Webhook encryption key + merchant reissue:** keep delivery disabled; replace key; start; reissue every merchant webhook secret; verify all rows updated; only then consider local-only delivery.
6. **Mock secret:** generate new `MOCK_WEBHOOK_SECRET`; restart; update harnesses; verify old signature fails and new signature succeeds.
7. **Validation:** complete §14; preserve audit evidence; do not enable external integrations.
8. **History cleanup:** only after all rotations and validation, and only with separate explicit approval. This audit does not perform or authorize it.

### 12.3 Dependencies and ordering notes

- Database rotation is a prerequisite for session deletion and for any DB inventory work.
- JWT rotation must precede or accompany session deletion if the goal is to eliminate access-token validity immediately.
- Admin-key rotation is independent but has no dual-key overlap; all clients must switch during the window.
- Webhook-key rotation depends on the inventory and reissue authority; do not change the key if you cannot reissue every row.
- Mock-secret rotation depends on updating local harnesses; internal self-signing paths follow the new secret automatically after restart.
- `WEBHOOK_DELIVERY_ENABLED` must remain false until every merchant config is reissued and validated.
- History cleanup depends on all six rotations, validation, a mirror backup, collaborator notification, and force-push approval; it is outside this phase.

### 12.4 Required operator actions (human)

1. Perform the database inventory and backup; do not use production credentials.
2. Execute the PostgreSQL role password change through an approved administrative method.
3. Update the local `.env.stage` and approved secret store; never commit the file.
4. Generate and distribute new JWT, admin, encryption, and mock secrets through secure channels.
5. Approve and execute the mass session deletion SQL after backup.
6. Reissue all merchant webhook signing secrets through the existing endpoints or an approved tool.
7. Update local mobile, dashboard, and test harness signers.
8. Maintain the manual change log and collect safe audit evidence.
9. Keep the incident open until rotation, validation, and history cleanup are complete.

---

## 13. Safe rollback considerations

**General rule:** never restore a compromised credential or old webhook signing secret to “roll back.” Fix forward, keep the environment isolated, and keep outbound delivery/email disabled.

| Area | If something fails | Safe response |
|---|---|---|
| Database password | New password cannot connect or role lockout. | Do not restore the old password. Use local container/OS superuser access or a verified backup recovery, set a fresh password, and update both env names. |
| JWT | New secret breaks token issuance. | Do not restore the old secret. Correct configuration and issue another new secret; users sign in again. |
| Refresh sessions | Some clients fail after deletion. | Do not restore old session rows. Users re-authenticate; access tokens are bounded by the new JWT secret and 15-minute TTL. |
| Admin key | A client was not updated. | Old key remains invalid. Issue another new key if distribution was incomplete; do not re-enable the old key. |
| Webhook encryption key | Rows are unreadable or reissue is incomplete. | Keep `WEBHOOK_DELIVERY_ENABLED=false`; do not enable delivery. Reissue remaining rows or restore the isolated staging backup and re-run with an approved plan. Do not revert to the old key unless the environment is fully isolated and the action is explicitly approved; old secrets remain compromised. |
| Merchant webhook secrets | A merchant/harness cannot verify the new secret. | Reissue again if necessary and update the receiver. Do not restore the old secret. |
| Mock webhook secret | Local harness fails signature verification. | Update the harness signer; do not re-enable the old secret. |
| Backups | Backup contains old ciphertext/credentials. | Store encrypted and access-controlled; destroy per incident policy after recovery. Never restore it into production. |

A database backup taken before rotation contains old credentials and old ciphertext. It is for recovery only and must not become a new long-term exposure.

---

## 14. Staging-only validation checklist

### 14.1 Before rotation

- [ ] `APP_ENV=test`, `APP_BIND_HOST=127.0.0.1`, `APP_PORT=18080`.
- [ ] `DB_HOST=127.0.0.1`, `DB_PORT=55433`, `DB_NAME=go_saas_m2_staging`, `DB_USER=go_saas_m2_staging`; `STAGING_DB_*` match.
- [ ] `MIGRATION_DATABASE_URL`, `DATABASE_URL`, and `TEST_DATABASE_URL` are empty or point only to staging.
- [ ] `PAYMENT_PROVIDER=mock`; `MIDTRANS_SERVER_KEY` and `MIDTRANS_CLIENT_KEY` are empty; `MIDTRANS_BASE_URL` is sandbox or unused.
- [ ] `WEBHOOK_DELIVERY_ENABLED=false`, `WEBHOOK_REQUIRE_HTTPS=false` (local only), `EMAIL_ENABLED=false`, `EMAIL_WORKER_ENABLED=false`, `PAYMENT_EXPIRY_ENABLED=false`, `PAYMENT_SIMULATOR_ENABLED=false`.
- [ ] `TRUSTED_PROXIES=none`; CORS/browser origins are loopback only.
- [ ] Staging DB/network/volume names are distinct from the protected backend; `pay-gate-app` on `8081` is untouched.
- [ ] Database backup exists and is access-controlled.
- [ ] Inventory counts and `schema_migrations` version/dirty state are recorded.
- [ ] Per-merchant reissue authority or an approved one-off tool is confirmed.
- [ ] All replacement secrets exist only in the approved secret store and local `.env.stage` (never Git).
- [ ] `FRONTEND_PUBLIC_URL` is corrected to the actual SPA origin if dashboard/browser validation is in scope.

### 14.2 After each rotation

- [ ] Database: application connects; `/health` and `/health/ready` return 200 on loopback; old DB password is rejected; schema is clean.
- [ ] JWT: old access token returns 401; new login succeeds; old refresh token returns 401; `dashboard_sessions` is empty.
- [ ] Admin: old key returns 401; new key succeeds on a non-mutating admin read; no key appears in logs.
- [ ] Webhook key: all `merchant_webhook_configs` rows have new `updated_at`; no row remains on old ciphertext; every row can be used by the new key without printing secrets; delivery remains disabled until the full set is reissued.
- [ ] Mock: new signatures are accepted; old/wrong signatures return 401; local harnesses are updated.
- [ ] No external provider, SMTP, or webhook calls occurred.
- [ ] Expected audit events exist; manual change log is complete.
- [ ] No secret value, hash, token, cookie, key, or full environment file appears in logs/reports/shell history.

### 14.3 Explicitly not allowed during validation

- Do not send real external webhooks or retry production deliveries.
- Do not enable email or a real payment provider.
- Do not access production or the protected backend on `8081`.
- Do not run migrations unless separately approved and necessary.
- Do not inspect or print `encrypted_secret`, refresh-token hashes, API-key hashes, or credentials.
- Do not expose `18080` beyond loopback.

---

## 15. Unresolved risks and findings register

### 15.1 Findings register

| ID | Finding | Class |
|---|---|---|
| F-01 | Six secret categories are populated and remain unrotated; the incident is still open. | **VERIFIED** |
| F-02 | The staging profile is loopback-only, test-mode, and mock-provider. | **VERIFIED** |
| F-03 | No production host was found in the inspected non-secret staging host/URL values. | **VERIFIED** for inspected keys; residual **UNKNOWN** |
| F-04 | The staging DB is offline; the staging app is not running; encrypted config/session/audit counts are unknown. | **VERIFIED** state; counts **UNKNOWN** |
| F-05 | The staging startup procedure is undocumented and the referenced staging document is missing. | **VERIFIED** absence; actual method **UNKNOWN** |
| F-06 | DB password rotation is external; restart required; no application rotation API. | **VERIFIED** |
| F-07 | Changing `STAGING_DB_PASSWORD`/`POSTGRES_PASSWORD` does not rotate an existing PostgreSQL role. | **INFERRED** |
| F-08 | `DB_PASSWORD` and `STAGING_DB_PASSWORD` target the same role/database names; value equality is unknown. | **VERIFIED** names; **INFERRED** same credential set |
| F-09 | Rotating `AUTH_JWT_SECRET` invalidates access JWTs after restart. | **VERIFIED** |
| F-10 | Rotating `AUTH_JWT_SECRET` does not revoke opaque refresh sessions; they can remain valid up to `168h`. | **VERIFIED** |
| F-11 | No global session-revocation API exists; mass revocation requires approved SQL. | **VERIFIED** absence; SQL **INFERRED** |
| F-12 | Admin auth supports one key only; no dual-key overlap; old key is rejected immediately after restart. | **VERIFIED** |
| F-13 | Webhook encryption has no key version/key ID/dual-key/decrypt-old/encrypt-new support. | **VERIFIED** |
| F-14 | Direct webhook-key replacement makes existing ciphertext unreadable. | **VERIFIED** |
| F-15 | Merchant webhook signing secrets must be reissued, not merely re-encrypted. | **INFERRED** from joint DB/key exposure; required |
| F-16 | Reissue endpoints exist for dashboard OWNER/ADMIN and merchant auth, but there is no admin bulk reissue. | **VERIFIED** |
| F-17 | Mock-secret consumers and valid/invalid/missing/wrong-secret tests exist. | **VERIFIED** |
| F-18 | `audit_logs` is append-only and has no configured retention/cleanup. | **VERIFIED** |
| F-19 | ELK collects Docker container logs only; the host-process staging backend is not automatically collected, and ELK is not running. | **INFERRED** collection; **VERIFIED** not running |
| F-20 | Rotation actions for DB/JWT/admin/encryption/mock secrets are not application-audited; manual logging is required. | **VERIFIED** absence |
| F-21 | The staging profile disables rate limiting, expiry, outbound delivery, email, and HTTPS enforcement; it must remain isolated. | **VERIFIED** |
| F-22 | The protected backend `pay-gate-app` is running on `8081` and was not touched. | **VERIFIED** |
| F-23 | `.env.stage` remains in public Git history; history cleanup is still pending and out of scope. | **VERIFIED** |
| F-24 | The webhook-key step cannot start until inventory, backup, and full reissue coverage are proven. | **VERIFIED** conditional |

### 15.2 Remaining risks

1. **Incident not contained at the credential layer.** Six categories remain compromised in public history.
2. **Webhook data recovery gap.** Existing merchant configs may be unreadable after a key change, and their count is unknown until the database is started.
3. **Merchant reissue burden.** Per-merchant authorization may not be available for every staging merchant; no bulk admin endpoint exists.
4. **Database rotation trap.** Changing only `STAGING_DB_PASSWORD` may leave the old role password valid.
5. **JWT/session split.** JWT rotation alone leaves refresh tokens valid for up to seven days.
6. **Admin hard cutover.** No dual-key overlap means any missed client loses access immediately.
7. **Test-mode secret-strength bypass.** `APP_ENV=test` does not enforce the production minimum lengths; new secrets must be generated to production-grade entropy manually.
8. **No global session revocation.** Mass deletion requires an approved database action and is not application-audited.
9. **Startup ambiguity.** `config.Load()` loads `.env`, not `.env.stage`; a careless start could use the wrong profile.
10. **Frontend URL defect.** `FRONTEND_PUBLIC_URL` points at the backend port, which can invalidate dashboard/payment-link checks.
11. **Staging controls disabled.** Rate limiting, expiry, email, HTTPS enforcement, and outbound delivery are off; this is not a production-security validation.
12. **Audit retention gap.** Audit rows have no defined retention; application logs may not be collected or retained.
13. **Mock surface exposure.** The mock webhook secret can forge events; the loopback bind is the only network boundary.
14. **Protected backend proximity.** `pay-gate-app` on `8081` is running; do not confuse it with staging or access it.
15. **History cleanup risk.** Force-pushing rewritten history remains a separate, destructive, approval-required action after rotation.

---

## 16. Confirmation: no secret disclosure and no state-changing action

**No secret disclosure:**
- No secret value from `.env.stage` was printed, copied into this report, or included in any command output.
- No password hash, refresh-token hash, API-key hash, token, cookie, encryption key, ciphertext, or complete environment-file excerpt was printed.
- The audit emitted only key names, populated/empty status, selected non-secret staging configuration values, source references, and observed read-only system state.
- This report contains no hashes of secret values.

**No state-changing action:**
- No credential was rotated or revoked.
- No database was connected to or modified; no SQL was executed.
- No container or service was started, stopped, restarted, or redeployed.
- No migration was run.
- No authentication, session revocation, or webhook operation was executed.
- No production system was accessed.
- No Git commit, push, force-push, reset, checkout, clean, or history rewrite was performed.
- No application file was changed by this audit. The only file created by this audit is this report; pre-existing user modifications and untracked Phase 2/3/4A/4B-0 documents were preserved.
- The incident remains **OPEN**; `.env.stage` remains in public Git history; history cleanup has not been started.

---

## 17. Final decision

### **READY AFTER PREPARATION**

This is **not** authorization to rotate now. The staging-only procedure is viable for the database, JWT/session, admin, mock, and webhook-reissue steps after the preparation gates in §12 are complete.

The hard gate is the webhook encryption key:

- There is no key version or decrypt-old/encrypt-new support.
- Existing ciphertext becomes unreadable on direct key replacement.
- Existing merchant signing secrets must be reissued because the database password and encryption key were exposed together.
- The staging database is offline, so the number of affected configs is unknown.
- There is no bulk admin reissue endpoint; per-merchant authorization or an approved one-off tool must be confirmed.

Until those webhook conditions are satisfied, the webhook-key step is **BLOCKED**. Do not replace that key and do not enable delivery. If the reissue authority or tool cannot be prepared, keep the webhook-key step blocked and continue with the other five categories in a phased, isolated manner.

**Do not execute the rotation yet. Do not clean Git history. Do not close the incident.**
