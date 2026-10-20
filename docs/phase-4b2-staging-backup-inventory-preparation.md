# Phase 4B.2 — Staging Backup and Inventory Preparation

**Date:** 2026-10-20  
**Repository:** `DhikDhik15/payment-gateway-service-backend` (public)  
**Branch / HEAD:** `main` / `fd49157`  
**Incident status:** **OPEN — six staging secret categories remain compromised; no credential has been rotated and no Git history has been rewritten.**  
**Phase type:** Read-only preparation. No database connection, SQL execution, backup creation, service start/stop, credential rotation, environment-file change, or repository-history change was performed.  
**Final decision:** **READY AFTER ADDITIONAL PREPARATION** — the procedure is defined, but several human decisions and staging-startup preconditions must be closed before a backup may be taken.

**Handling note:** This report contains non-secret resource identifiers, configuration key names, schema references, aggregate-only query plans, and procedures. It contains no secret values, password hashes, refresh-token hashes, API-key hashes, encrypted secret values, tokens, cookies, encryption keys, complete environment-file excerpts, or database connection strings with credentials. No backup was created, no database was contacted, and no SQL was run.

---

## 1. Executive summary

The staging database is currently **not instantiated**: the staging PostgreSQL container is absent, the staging network is absent, ports `55433` and `18080` are not listening, and no staging backend process is running. The named staging volume `go-saas-m2-staging-pgdata` still exists and Docker reports approximately **50.82 MB** of volume usage. The protected backend container `pay-gate-app` is running on `0.0.0.0:8081` and was not accessed or changed.

No staging-specific backup procedure, backup script, Makefile target, CI job, backup retention policy, RPO, or RTO exists in the repository. The only supported backup/restore guidance is generic PostgreSQL logical-backup guidance in the production runbook and deployment documents. **No staging backup is verified to exist.** The presence of the volume is not proof of a valid or recoverable backup.

A safe staging-only procedure can be prepared:

1. Verify staging identity from non-secret Docker and configuration metadata.
2. Start only the staging PostgreSQL service, under human approval.
3. Take a consistent logical `pg_dump` to a protected path outside the repository, without overwriting any existing file.
4. Encrypt the dump at rest and verify its integrity and table of contents.
5. Restore it into a disposable staging database and validate schema and aggregate counts.
6. Run aggregate-only inventory queries for sessions, webhook configuration, delivery dependencies, audit records, and reissue authority.
7. Stop before any credential rotation, webhook reissue, or database mutation.

The procedure is **not ready to execute** because the staging image is not present locally, the backup destination/encryption/retention are undefined, the restore-drill target and privileges are unconfirmed, the staging startup command is undocumented, and per-merchant webhook reissue authority is not established. These are preparation gates, not missing procedural steps.

**Decision:** **READY AFTER ADDITIONAL PREPARATION.** Once the gates in §13 are closed, this becomes **READY FOR HUMAN-APPROVED BACKUP**. The backup itself remains prohibited in this phase.

---

## 2. Scope, method, and limitations

### 2.1 Scope

In scope:

- Read-only inspection of the incident and rotation-readiness reports.
- Repository documentation, Compose configuration, migrations, and code paths relevant to backup, sessions, webhook configuration, webhook delivery, and audit records.
- Read-only Docker/Git/host metadata needed to identify the staging resources.
- A staging-only backup procedure, protection requirements, integrity verification, restore-validation plan, and aggregate-only inventory query plan.
- Merchant webhook reissue tracking requirements.

Out of scope and not performed:

- Connecting to PostgreSQL or executing SQL.
- Starting, stopping, restarting, or reconfiguring any container or service.
- Creating, overwriting, deleting, or restoring any backup file.
- Creating or modifying Docker volumes, networks, containers, or database state.
- Rotating, revoking, or testing any credential.
- Modifying `.env.stage` or any other environment file.
- Authenticating, logging in, deleting sessions, or reissuing webhook secrets.
- Migrations, deployment, production access, Git commits, pushes, force-pushes, or history rewriting.

### 2.2 Method

Evidence came from:

- Git status and object identity (`git status`, `git rev-parse`).
- Read-only Docker metadata (`docker ps -a`, `docker volume ls`, `docker volume inspect`, `docker system df -v`, `docker compose ls`, `docker image ls`).
- Host listener state (`ss -ltnp`).
- Read-only tool checks (`pg_dump`, `pg_restore`, `psql`, `gpg`, `sha256sum` versions).
- Repository files only: Compose files, migrations, Makefile, CI workflow, configuration loader, repositories, services, handlers, routes, and existing reports.
- Allowlisted non-secret values from `.env.stage`; secret keys were checked only for populated/empty status, never printed.

### 2.3 Limitations and classification

The staging database is offline, so the following remain **UNKNOWN**:

- Actual database name/role inside the volume beyond the configured non-secret names.
- Actual schema version and dirty state.
- Actual row counts for sessions, webhook configs, deliveries, and audit records.
- Whether the volume contains a valid, complete PostgreSQL cluster.
- Actual logical database size.
- Whether the role password in the volume matches the current environment value.
- Whether a webhook encryption key change would leave any row unreadable.
- Whether every merchant can be reissued through an authorized path.

Classification legend:

- **VERIFIED** — directly supported by source, configuration, schema, or observed read-only system state.
- **INFERRED** — a reasoned conclusion from available evidence, not directly exercised.
- **UNKNOWN** — requires state-changing access, a running service, a database connection, or missing human information.

---

## 3. Verified staging resource identifiers and isolation

### 3.1 Git and incident state

| Finding | Evidence | Class |
|---|---|---|
| Branch is `main`; `HEAD` and `origin/main` are both `fd49157`. | `git rev-parse` | **VERIFIED** |
| `.env.stage` is removed from the Git index and is ignored by `.gitignore:20` (`.env.*`). The local file remains at mode `600`, 5,467 bytes, 122 lines, and its content was not edited by this phase. | `git status`, `git check-ignore`, `stat`, `wc` | **VERIFIED** |
| Pre-existing staged changes are still present: `.env.stage` deletion from the index, `.env.stage.example` addition, `.gitignore` modification. | `git status --porcelain` | **VERIFIED** |
| Pre-existing modified source/test files and untracked Phase 2/3/4A/4B-0/4B-1 documents were preserved. | `git status --porcelain` | **VERIFIED** |
| Six secret categories are populated and remain unrotated: `DB_PASSWORD`, `STAGING_DB_PASSWORD`, `AUTH_JWT_SECRET`, `ADMIN_API_KEY`, `WEBHOOK_SECRET_ENCRYPTION_KEY`, `MOCK_WEBHOOK_SECRET`. Presence only; values withheld. | Allowlisted presence check | **VERIFIED** |
| `MIDTRANS_SERVER_KEY`, `MIDTRANS_CLIENT_KEY`, `SMTP_PASSWORD`, and `DEV_RECOVERY_KEY` are empty. | Allowlisted presence check | **VERIFIED** |
| The incident remains open; `.env.stage` remains in public Git history. | Phase 4B-0 and Phase 4B.1 reports | **VERIFIED** |

### 3.2 Configured staging resources

The staging Compose file provisions only PostgreSQL. The backend is a separate host process.

| Resource | Expected identifier | Evidence | Class |
|---|---|---|---|
| Compose file | `docker-compose.stage.yml` | Repository file | **VERIFIED** |
| Compose project name | `go-saas-m2-staging` | Usage comment, `docker-compose.stage.yml:17-19` | **VERIFIED** |
| Service | `postgres` | `docker-compose.stage.yml:22` | **VERIFIED** |
| Container name | `go-saas-m2-staging-db` | `docker-compose.stage.yml:24` | **VERIFIED** |
| Image | `postgres:16-alpine` | `docker-compose.stage.yml:23` | **VERIFIED** |
| Volume | `go-saas-m2-staging-pgdata` | `docker-compose.stage.yml:40`, `:49-51` | **VERIFIED** |
| Network | `go-saas-m2-staging-net` | `docker-compose.stage.yml:42`, `:44-47` | **VERIFIED** |
| Host bind | `127.0.0.1:55433` → container `5432` | `docker-compose.stage.yml:27-28`; `STAGING_DB_PORT=55433` | **VERIFIED** |
| Database name | `go_saas_m2_staging` | `STAGING_DB_NAME`, `DB_NAME` | **VERIFIED** |
| Database role | `go_saas_m2_staging` | `STAGING_DB_USER`, `DB_USER` | **VERIFIED** |
| Application profile | `APP_ENV=test`, `APP_BIND_HOST=127.0.0.1`, `APP_PORT=18080` | Allowlisted `.env.stage` values | **VERIFIED** |
| Provider profile | `PAYMENT_PROVIDER=mock`; Midtrans keys empty; sandbox base URL | Allowlisted values | **VERIFIED** |
| Staging controls | `WEBHOOK_DELIVERY_ENABLED=false`, `WEBHOOK_REQUIRE_HTTPS=false`, `EMAIL_ENABLED=false`, `EMAIL_WORKER_ENABLED=false`, `PAYMENT_EXPIRY_ENABLED=false`, `PAYMENT_SIMULATOR_ENABLED=false`, `RATE_LIMIT_ENABLED=false`, `TRUSTED_PROXIES=none` | Allowlisted values | **VERIFIED** |

### 3.3 Observed runtime state

| Observation | Result | Class |
|---|---|---|
| Staging PostgreSQL container `go-saas-m2-staging-db` | Absent from `docker ps -a` | **VERIFIED** |
| Staging network `go-saas-m2-staging-net` | Absent from `docker network ls` | **VERIFIED** |
| Staging volume `go-saas-m2-staging-pgdata` | Present; local driver; Docker-reported usage approximately 50.82 MB; no attached container | **VERIFIED** |
| Staging host port `55433` | Not listening | **VERIFIED** |
| Staging backend port `18080` | Not listening | **VERIFIED** |
| Staging backend process | No matching process found | **VERIFIED** |
| Protected backend `pay-gate-app` | Running, healthy, `0.0.0.0:8081->8080/tcp` | **VERIFIED** |
| Only running Compose project | `pay-gate-backend` using `docker-compose.yml` | **VERIFIED** |
| Staging image `postgres:16-alpine` | Not present locally | **VERIFIED** |
| Docker Compose | v5.6.0 | **VERIFIED** |

### 3.4 Protected backend separation

The protected backend is a separate stack:

- Container: `pay-gate-app`
- Image: local image `pay-gate-backend-app`
- Network: `pay-gate-network`
- Volume: `pay-gate-product-images` mounted at `/app/storage/product-images`
- Host port: `8081`

The staging volume and staging network are not attached to this protected container. The protected container’s environment was not inspected, because doing so could expose secrets. No request was sent to `8081`.

### 3.5 What is still unknown about the volume

The volume size and name are verified, but the following are **UNKNOWN** until the staging PostgreSQL service is started and queried under approval:

- Whether the volume contains a valid cluster for `go_saas_m2_staging`.
- Whether the configured role password still matches the role in the volume.
- Actual schema version and dirty state.
- Actual table counts.
- Actual logical database size.
- Whether the volume is the only copy of staging data.

The historical M2 report records `schema_migrations` at `25 / 25`, clean, on 2026-10-19. That is historical evidence, not current verification. The current migration directory ends at `000025_password_reset_tokens`, and the application requires at least version 22 (`pkg/database/postgres.go:19-23`, `:78-85`).

---

## 4. How to distinguish staging from production without displaying secret values

### 4.1 Pre-connection checks

Do not connect to any database until all of the following are true:

| Check | Required staging result |
|---|---|
| Compose file | Exactly `docker-compose.stage.yml` |
| Compose project | Exactly `go-saas-m2-staging` |
| Service being started | Only `postgres` |
| Container name | `go-saas-m2-staging-db` |
| Image | `postgres:16-alpine` pinned to a reviewed digest |
| Volume source | `go-saas-m2-staging-pgdata` |
| Volume destination | `/var/lib/postgresql/data` |
| Container network | `go-saas-m2-staging-net` |
| Host port mapping | `127.0.0.1:55433` → `5432/tcp` only |
| Container labels | `com.docker.compose.project=go-saas-m2-staging`, `com.docker.compose.service=postgres` |
| Staging environment profile | `APP_ENV=test`, loopback bind, mock provider |
| Protected backend | `pay-gate-app` remains untouched and separate |
| Other listeners on `55433` | None |

If the container is created with a different volume source, a different network, a non-loopback port mapping, or a project label other than `go-saas-m2-staging`, **stop before any query**. Do not initialize a new cluster and do not write to the volume.

### 4.2 In-database identity checks

Run these only inside the approved backup window, in a read-only transaction. They do not print secrets.

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

SELECT
  current_database() AS database_name,
  current_user AS role_name,
  inet_server_addr() AS server_address,
  inet_server_port() AS server_port,
  pg_is_in_recovery() AS in_recovery,
  current_setting('server_version') AS server_version;

SELECT
  d.datname AS database_name,
  pg_get_userbyid(d.datdba) AS database_owner
FROM pg_database d
WHERE d.datname = current_database();

SELECT
  rolname,
  rolsuper,
  rolcreatedb,
  rolcanlogin,
  rolvaliduntil
FROM pg_roles
WHERE rolname = current_user;

SELECT version, dirty FROM schema_migrations;

ROLLBACK;
```

Expected staging identity:

- `current_database()` = `go_saas_m2_staging`
- `current_user` = `go_saas_m2_staging`
- `pg_is_in_recovery()` = `false`
- Server major version compatible with the PostgreSQL 16 staging image
- `schema_migrations.dirty` = `false`

`inet_server_addr()` may be null when connected over a Unix socket; in that case rely on the verified container/port metadata. Inside the container, `inet_server_port()` is expected to be `5432`; the host mapping is `127.0.0.1:55433`.

### 4.3 Refusal conditions

Refuse to take the backup and escalate if any of the following occurs:

- Database name or role is not the staging name.
- The server is a different major version than the staging image.
- `pg_is_in_recovery()` is true.
- `schema_migrations.dirty` is true.
- `schema_migrations` is missing or unreadable.
- The volume mount or container labels do not match the staging identifiers.
- Any non-loopback database port is exposed.
- The protected backend or its network/volume is involved.
- Any environment value suggests production, a real provider, or a non-loopback database host.
- A container starts and initializes a new cluster because the volume was not found.

### 4.4 What not to use for staging identification

Do not use any of the following:

- Printing `.env.stage`, `.env`, `DATABASE_URL`, `MIGRATION_DATABASE_URL`, or a resolved Compose configuration.
- Unfiltered `docker inspect` (it prints container environment secrets).
- `docker compose config` with `--env-file .env.stage` (it resolves and prints secrets).
- `docker exec ... env` or `printenv`.
- A production credential, production database, or protected backend.
- A hostname or IP that is not the verified loopback staging target.
- Secret values or hashes as identity markers.

Use resource names, labels, ports, database names, role names, schema version, and read-only system state instead.

---

## 5. Existing backup and restore mechanisms

### 5.1 Repository-supported mechanism

The repository’s generic backup guidance is in `docs/phase-9-production-runbook.md:196-258`:

- Use PostgreSQL-native logical backup tooling.
- Take a custom-format dump with `pg_dump --format=custom --no-owner --no-privileges`.
- Backups contain sensitive business data; encryption at rest, access logging, retention, geographic storage, and deletion are operator/DBA responsibilities.
- Restore into a disposable database first; never restore over production without approval.
- Use `pg_restore --exit-on-error --single-transaction --no-owner --no-privileges`.
- Verify `schema_migrations.version` and `dirty=false`.
- Verify representative merchant, user, transaction, refund, payment-attempt, idempotency, session, invitation, outbox, and audit state.
- Restore runtime secrets separately from the database dump.
- A restore can reactivate old refresh-session hashes and can contain invitation tokens in outbox bodies; revoke/rotate sessions and follow the incident process if a backup is exposed or restored into an untrusted environment.

Supporting requirements:

- `docs/phase-4c-deployment-architecture-specification.md:220-224`: PostgreSQL-native logical backup before first migration and upgrades; encryption at rest and access control are operator responsibilities; restore into a disposable database; secrets restored separately.
- `docs/phase-4b-backend-local-to-production-report.md:183-187`: same logical-backup recommendation; backups contain sensitive data.
- `docs/phase-9-production-checklist.md:96-107`: backup method, encryption, retention, and access owner must be documented; restore drill in a disposable database; restored schema version/dirty state verified; runtime secrets restored separately; RPO/RTO owners must decide.

### 5.2 What is missing

| Missing item | Evidence | Class |
|---|---|---|
| Staging-specific backup procedure or script | No backup/restore script in the repository; only ELK scripts exist | **VERIFIED** absence |
| Makefile backup/restore target | `Makefile` has migration targets only | **VERIFIED** absence |
| CI backup job or database service in CI | `.github/workflows/ci.yml` has no database service and no backup/restore step | **VERIFIED** absence |
| Backup destination, encryption recipient, retention, access owner, RPO/RTO for staging | Not defined in repository | **UNKNOWN** |
| Verified staging backup artifact | No staging backup is documented or verified | **UNKNOWN** |
| Automated volume snapshot procedure | Not documented | **UNKNOWN** |

The existence of `go-saas-m2-staging-pgdata` is **not** a backup. It is the live data volume. Copying a raw data directory is not the primary procedure and must not be treated as a substitute for a logical backup.

### 5.3 Available tooling

| Tool | Observed | Implication |
|---|---|---|
| `pg_dump` | PostgreSQL 18.6 | Can dump from a PostgreSQL 16 server; use the matching `pg_restore` 18.6 for the restore drill |
| `pg_restore` | PostgreSQL 18.6 | Use the same client major version as the dump |
| `psql` | PostgreSQL 18.6 | Read-only identity/inventory queries and restore validation |
| `createdb` / `dropdb` | Available | Disposable restore database creation/removal, subject to privileges |
| `gpg` | GnuPG 2.4.8; keyring currently has zero public and zero secret keys | Symmetric encryption with a passphrase from the approved secret store, or provision an approved recipient key |
| `sha256sum` | Available | Backup integrity checksum |
| `age` | Not available | Do not plan on `age` unless installed and approved |
| Docker Compose | v5.6.0 | Staging service orchestration |
| `postgres:16-alpine` image | Not present locally | A pull is required before starting; pin and approve the digest |

If host/client version compatibility is rejected by policy, use the container’s PostgreSQL 16 `pg_dump` and `pg_restore` instead. Do not mix a dump produced by one major version with a `pg_restore` from an older major version.

### 5.4 What a staging backup will contain

A full logical database dump will include sensitive data, including:

- `merchant_users.password_hash` (Argon2id hashes)
- `dashboard_sessions.refresh_token_hash` (SHA-256 hashes of opaque refresh tokens)
- `merchant_api_keys.secret_hash`
- `merchants.api_key` and `merchants.api_secret` where legacy plaintext credentials still exist
- `merchant_webhook_configs.encrypted_secret` (old AES-256-GCM ciphertext)
- `merchant_webhook_deliveries.payload`, `endpoint_url`, and delivery errors
- `webhook_events.payload` and `signature`
- `email_outbox` bodies, which may contain invitation tokens
- `audit_logs` metadata, request IDs, and IP addresses
- Payment, refund, settlement, order, product, and idempotency data

Therefore, the backup file is itself incident-sensitive and must be encrypted and access-controlled. A backup does **not** include the runtime secrets (JWT, admin key, webhook encryption key, mock secret, database password); restore those separately from the approved secret store.

### 5.5 No backup is verified to exist

This preparation did not find a staging backup file, backup schedule, backup manifest, or restore record in the repository. The Phase 9 restore drill described in `docs/phase-9-production-runbook.md:253-258` was a disposable test environment, not a current staging backup. **Do not claim that a staging backup exists.**

---

## 6. Inventory tables, columns, and dependencies

### 6.1 Inventory table map

| Area | Table | Relevant non-secret columns | Purpose |
|---|---|---|---|
| Schema state | `schema_migrations` | `version`, `dirty` | Confirm migration state and restore fidelity |
| Tenant scope | `merchants` | `id`, `status`, `legacy_credential_state`, `created_at`, `updated_at` | Merchant status and reissue scope |
| Dashboard users | `merchant_users` | `id`, `merchant_id`, `role`, `status`, `created_at` | Reissue authorization coverage |
| Dashboard sessions | `dashboard_sessions` | `id`, `merchant_user_id`, `expires_at`, `created_at`, `last_used_at` | Session inventory and revocation planning |
| Webhook configuration | `merchant_webhook_configs` | `id`, `merchant_id`, `status`, `description`, `created_at`, `updated_at` | Reissue inventory; **never select `encrypted_secret`** |
| Webhook delivery outbox | `merchant_webhook_deliveries` | `id`, `merchant_id`, `config_id`, `event_id`, `event_type`, `transaction_id`, `attempt_count`, `status`, `next_attempt_at`, `last_attempt_at`, `processing_at`, `delivered_at`, `last_http_status`, `created_at`, `updated_at` | Delivery dependency and backlog inventory |
| Inbound provider webhooks | `webhook_events` | `id`, `provider`, `event_id`, `event_type`, `transaction_id`, `status`, `processed_at`, `created_at` | Webhook subsystem context; not encrypted by the merchant key |
| Audit trail | `audit_logs` | `id`, `merchant_id`, `actor_user_id`, `actor_type`, `action`, `target_type`, `target_id`, `created_at` | Security history and reissue audit |
| Merchant API keys | `merchant_api_keys` | `id`, `merchant_id`, `name`, `status`, `expires_at`, `revoked_at`, `created_at` | Reissue authorization coverage; **never select `secret_hash`** |
| Legacy merchant credentials | `merchants` | `legacy_credential_state`, `legacy_credential_disabled_at` | Understand legacy credential exposure in the backup |

Schema evidence:

- `migrations/000001_create_merchants.up.sql`: merchant columns and status.
- `migrations/000008_create_merchant_api_keys.up.sql`: API-key columns and status.
- `migrations/000010_create_merchant_webhook_configs.up.sql:6-19`: config columns, `encrypted_secret`, status check, one-config-per-merchant unique constraint.
- `migrations/000011_create_merchant_webhook_deliveries.up.sql:5-29`: delivery columns, `config_id`, status check, event uniqueness.
- `migrations/000014_create_merchant_users.up.sql:42-61`: session columns and indexes.
- `migrations/000018_create_audit_logs.up.sql:9-54`: audit columns, action/actor checks, indexes.
- `migrations/000005_create_webhook_events.up.sql:5-24`: inbound event columns.

### 6.2 Sensitive columns excluded from inventory queries

Never select, export, or print:

- `merchant_webhook_configs.encrypted_secret`
- `dashboard_sessions.refresh_token_hash`
- `merchant_users.password_hash`
- `merchant_api_keys.secret_hash`
- `merchants.api_key`, `merchants.api_secret`
- `merchant_webhook_deliveries.payload`, `endpoint_url`, `last_error`
- `webhook_events.payload`, `signature`
- `email_outbox` bodies
- `audit_logs.metadata` in full, `request_id`, `ip`, `target_id`
- Any value from `.env.stage`, any DSN, any token, cookie, or encryption key

Counts and status distributions are sufficient for this phase.

### 6.3 Webhook delivery dependency map

The merchant webhook subsystem depends on:

1. `merchant_webhook_configs` — one row per merchant, holding the encrypted signing secret and destination.
2. `merchant_webhook_deliveries` — outbox rows with `config_id` referencing the config, `merchant_id`, `transaction_id`, status, attempts, and timestamps.
3. `merchants` — tenant identity and status.
4. `transactions` — referenced by deliveries.
5. `webhook_events` — inbound provider events, separate from outbound merchant delivery.
6. `audit_logs` — `WEBHOOK_CONFIG_CHANGED` history for reissue evidence.

The encryption key affects `merchant_webhook_configs.encrypted_secret`. Reissuing a secret through the application overwrites that column and updates `updated_at`; it does not require decrypting the old value. See `internal/service/merchant_webhook_service.go:137-181` and `internal/repository/merchant_webhook_repository.go:133-179`.

### 6.4 Why the inventory matters

- `dashboard_sessions` count determines the scope of refresh-session revocation.
- `merchant_webhook_configs` count and status determine whether every merchant can be reissued before the webhook encryption key is replaced.
- `merchant_webhook_deliveries` status shows whether disabling delivery left a backlog or dead-letter set that must be understood before rotation.
- `audit_logs` provides the only application-side evidence of webhook config changes and admin authentication failures.
- `merchant_users` and `merchant_api_keys` coverage indicate whether per-merchant reissue authority exists, but they do not provide plaintext credentials.

---

## 7. Aggregate-only query plan (not executed)

### 7.1 Execution rules

The following SQL is a **plan only**. It was not executed in this phase. When eventually run, use all of these controls:

- Connect only to the verified staging instance, using explicit host/port/database/role flags and a `PGPASSFILE` with mode `600`; never put a password on the command line.
- Use `BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;` so all queries see one consistent snapshot.
- Set `statement_timeout` and `lock_timeout`.
- Run as a role with read access; avoid a superuser unless required and approved.
- Do not select secret columns, `SELECT *`, full `metadata`, payloads, URLs, hashes, or environment values.
- End with `ROLLBACK;`, never `COMMIT`.
- Record only aggregate results in the change log or report. Do not paste raw query output containing merchant identifiers, IPs, or request IDs.

### 7.2 Q-1: identity, schema, size, and table presence

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

SELECT
  current_database() AS database_name,
  current_user AS role_name,
  inet_server_addr() AS server_address,
  inet_server_port() AS server_port,
  pg_is_in_recovery() AS in_recovery,
  current_setting('server_version') AS server_version;

SELECT
  d.datname AS database_name,
  pg_get_userbyid(d.datdba) AS database_owner
FROM pg_database d
WHERE d.datname = current_database();

SELECT
  rolname,
  rolsuper,
  rolcreatedb,
  rolcanlogin,
  rolvaliduntil
FROM pg_roles
WHERE rolname = current_user;

SELECT version, dirty FROM schema_migrations;

SELECT
  pg_size_pretty(pg_database_size(current_database())) AS database_size,
  pg_database_size(current_database()) AS database_size_bytes;

SELECT table_name
FROM information_schema.tables
WHERE table_schema = 'public'
  AND table_name IN (
    'merchants',
    'merchant_users',
    'dashboard_sessions',
    'merchant_webhook_configs',
    'merchant_webhook_deliveries',
    'webhook_events',
    'audit_logs',
    'merchant_api_keys',
    'schema_migrations'
  )
ORDER BY table_name;

ROLLBACK;
```

### 7.3 Q-2: exact critical-table counts and public-table estimate

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT 'merchants' AS table_name, count(*) AS rows FROM merchants
UNION ALL SELECT 'merchant_users', count(*) FROM merchant_users
UNION ALL SELECT 'dashboard_sessions', count(*) FROM dashboard_sessions
UNION ALL SELECT 'merchant_webhook_configs', count(*) FROM merchant_webhook_configs
UNION ALL SELECT 'merchant_webhook_deliveries', count(*) FROM merchant_webhook_deliveries
UNION ALL SELECT 'webhook_events', count(*) FROM webhook_events
UNION ALL SELECT 'audit_logs', count(*) FROM audit_logs
UNION ALL SELECT 'merchant_api_keys', count(*) FROM merchant_api_keys
ORDER BY table_name;

SELECT
  relname AS table_name,
  n_live_tup AS estimated_rows
FROM pg_stat_user_tables
WHERE schemaname = 'public'
ORDER BY relname;

ROLLBACK;
```

`pg_stat_user_tables.n_live_tup` is an estimate and may be stale. Use the exact counts above for the critical tables.

### 7.4 Q-3: dashboard session inventory

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT
  count(*) AS total_sessions,
  count(*) FILTER (WHERE expires_at > now()) AS unexpired_sessions,
  count(*) FILTER (WHERE expires_at <= now()) AS expired_sessions,
  count(*) FILTER (WHERE last_used_at IS NULL) AS never_used_sessions,
  count(DISTINCT merchant_user_id) AS distinct_users,
  min(created_at) AS oldest_created_at,
  max(created_at) AS newest_created_at,
  min(expires_at) AS earliest_expires_at,
  max(expires_at) AS latest_expires_at
FROM dashboard_sessions;

ROLLBACK;
```

Do not add `refresh_token_hash` to the select list. Do not join to `merchant_users.email`.

### 7.5 Q-4: merchant webhook configuration inventory

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT status, count(*) AS configs
FROM merchant_webhook_configs
GROUP BY status
ORDER BY status;

SELECT
  count(*) AS total_configs,
  count(DISTINCT merchant_id) AS distinct_merchants,
  count(*) FILTER (WHERE status = 'ACTIVE') AS active_configs,
  count(*) FILTER (WHERE status = 'DISABLED') AS disabled_configs,
  count(*) FILTER (WHERE created_at < now() - interval '7 days') AS older_than_7_days,
  count(*) FILTER (WHERE updated_at >= now() - interval '24 hours') AS updated_last_24_hours,
  min(created_at) AS oldest_created_at,
  max(updated_at) AS newest_updated_at
FROM merchant_webhook_configs;

SELECT count(*) AS duplicate_merchant_groups
FROM (
  SELECT merchant_id
  FROM merchant_webhook_configs
  GROUP BY merchant_id
  HAVING count(*) > 1
) duplicates;

ROLLBACK;
```

Do not select `encrypted_secret`, `url`, or `description`. The unique constraint `uq_merchant_webhook_configs_merchant_id` should make `duplicate_merchant_groups` zero; a non-zero result means the expected schema is not intact.

### 7.6 Q-5: webhook delivery dependency inventory

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT status, count(*) AS deliveries
FROM merchant_webhook_deliveries
GROUP BY status
ORDER BY status;

SELECT
  count(*) AS total_deliveries,
  count(DISTINCT merchant_id) AS distinct_merchants,
  count(DISTINCT config_id) AS distinct_configs,
  count(*) FILTER (WHERE config_id IS NULL) AS deliveries_without_config,
  count(*) FILTER (WHERE status = 'PENDING') AS pending_deliveries,
  count(*) FILTER (WHERE status = 'PROCESSING') AS processing_deliveries,
  count(*) FILTER (WHERE status = 'DELIVERED') AS delivered_deliveries,
  count(*) FILTER (WHERE status = 'FAILED') AS failed_deliveries,
  count(*) FILTER (WHERE status = 'DEAD') AS dead_deliveries,
  count(*) FILTER (WHERE next_attempt_at <= now()) AS due_now,
  min(created_at) AS oldest_created_at,
  max(created_at) AS newest_created_at
FROM merchant_webhook_deliveries;

SELECT
  count(*) FILTER (WHERE d.merchant_id <> c.merchant_id) AS cross_merchant_mismatches,
  count(*) FILTER (WHERE c.status = 'DISABLED' AND d.status IN ('PENDING', 'PROCESSING')) AS active_deliveries_for_disabled_configs
FROM merchant_webhook_deliveries d
JOIN merchant_webhook_configs c ON c.id = d.config_id;

ROLLBACK;
```

Do not select `endpoint_url`, `payload`, `last_error`, `event_id`, or `transaction_id` in the output.

### 7.7 Q-6: audit record inventory

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT
  count(*) AS total_events,
  min(created_at) AS earliest_event_at,
  max(created_at) AS latest_event_at
FROM audit_logs;

SELECT action, actor_type, count(*) AS events
FROM audit_logs
GROUP BY action, actor_type
ORDER BY action, actor_type;

SELECT
  action,
  COALESCE(metadata->>'change', '(none)') AS change_type,
  count(*) AS events
FROM audit_logs
WHERE action = 'WEBHOOK_CONFIG_CHANGED'
GROUP BY 1, 2
ORDER BY 1, 2;

ROLLBACK;
```

Do not select full `metadata`, `ip`, `request_id`, or `target_id`. The only metadata field used is the non-secret `change` category for webhook config events.

### 7.8 Q-7: reissue authority coverage (aggregate only)

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '30s';

SELECT status, count(*) AS merchants
FROM merchants
GROUP BY status
ORDER BY status;

SELECT
  count(*) FILTER (WHERE role IN ('OWNER', 'ADMIN') AND status = 'ACTIVE') AS active_dashboard_admins,
  count(DISTINCT merchant_id) FILTER (WHERE role IN ('OWNER', 'ADMIN') AND status = 'ACTIVE') AS merchants_with_active_dashboard_admin
FROM merchant_users;

SELECT
  count(*) FILTER (WHERE status = 'ACTIVE') AS active_api_keys,
  count(DISTINCT merchant_id) FILTER (WHERE status = 'ACTIVE') AS merchants_with_active_api_key
FROM merchant_api_keys;

ROLLBACK;
```

These counts do **not** prove that a human can log in or that a plaintext merchant API key is available. They only show whether an authorization path may exist. Per-merchant confirmation remains a human task.

### 7.9 Controlled non-aggregate reissue export (separate approval)

The aggregate queries above are sufficient for the report. Merchant-level reissue tracking needs a controlled export of non-secret identifiers. This is **not** an aggregate query and must be separately approved, written only to an access-controlled local file, and never pasted into a report or chat.

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;

SELECT
  c.merchant_id,
  c.status AS config_status,
  m.status AS merchant_status,
  c.created_at,
  c.updated_at
FROM merchant_webhook_configs c
JOIN merchants m ON m.id = c.merchant_id
ORDER BY c.merchant_id;

ROLLBACK;
```

Do not select `url`, `encrypted_secret`, merchant names, user emails, or API-key identifiers. Store the output in the reissue tracking sheet described in §10, with access restricted to the incident owner and rotation operator.

---

## 8. Proposed backup procedure

**This procedure is a plan. It was not executed. Do not execute it until the gates in §13 are closed and a human approves the backup window.**

### 8.1 Preconditions and approval

- Incident owner approves a staging-only backup window.
- Staging application remains stopped.
- Protected backend `pay-gate-app` on `8081` remains untouched.
- Only the staging PostgreSQL service is started; no application, migration, ELK, or unrelated Compose service.
- Backup destination, encryption method, retention, access owner, and restore-drill owner are recorded.
- `WEBHOOK_DELIVERY_ENABLED=false`, `EMAIL_ENABLED=false`, `EMAIL_WORKER_ENABLED=false`, `PAYMENT_EXPIRY_ENABLED=false`, and `PAYMENT_SIMULATOR_ENABLED=false` remain in force.
- No credential is rotated during this procedure.

### 8.2 Step 1: freeze and verify staging identity

1. Record the change window, operator, and incident reference.
2. Confirm `HEAD` and `origin/main` are unchanged for this phase; do not commit.
3. Confirm the staging container is absent and the staging volume is the expected volume.
4. Confirm `pay-gate-app` is running separately on `8081` and is not part of this operation.
5. Confirm no staging backend process is running.
6. Confirm ports `55433` and `18080` are closed before the operation.

### 8.3 Step 2: start only the staging PostgreSQL service

Use the exact staging Compose file, environment file, and project name. Start only the `postgres` service:

```bash
docker compose \
  --env-file .env.stage \
  -f docker-compose.stage.yml \
  --project-name go-saas-m2-staging \
  up -d postgres
```

Do not start the backend, migration job, ELK stack, or any other Compose project. Do not run `docker compose down`, `docker compose down -v`, `docker volume rm`, `docker system prune`, or any command that could delete the staging volume.

Before querying, verify the container metadata without printing environment values:

```bash
docker inspect go-saas-m2-staging-db \
  --format 'Name={{.Name}} Image={{.Config.Image}} Status={{.State.Status}} Health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'

docker inspect go-saas-m2-staging-db \
  --format 'Project={{index .Config.Labels "com.docker.compose.project"}} Service={{index .Config.Labels "com.docker.compose.service"}}'

docker inspect go-saas-m2-staging-db \
  --format 'Mounts={{range .Mounts}}{{.Type}}:{{.Name}}:{{.Destination}} {{end}}'

docker inspect go-saas-m2-staging-db \
  --format 'Ports={{range $p, $b := .NetworkSettings.Ports}}{{$p}}={{range $b}}{{.HostIp}}:{{.HostPort}} {{end}}{{end}}'

docker inspect go-saas-m2-staging-db \
  --format 'Networks={{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}'
```

Required results:

- Image is `postgres:16-alpine`.
- Project label is `go-saas-m2-staging`.
- Service label is `postgres`.
- Mount source is `go-saas-m2-staging-pgdata`.
- Mount destination is `/var/lib/postgresql/data`.
- Host port is `127.0.0.1:55433`.
- Network is `go-saas-m2-staging-net`.
- Health becomes `healthy`.

If the container initializes a new cluster because the volume is missing or empty, **stop immediately** and escalate. Do not write data to a new cluster.

### 8.4 Step 3: preflight the backup destination

Choose an access-controlled local path **outside the repository**, on a filesystem with enough free space. Do not use `/tmp` as the only copy. Do not use a public cloud sync folder, chat attachment, or Git working tree.

Example path policy:

```text
BACKUP_DIR="$HOME/pay-gate-backups"   # operator-selected; create only after approval
umask 077
mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"
```

The current host has:

- `/var/backups` present but not writable by the current user.
- `$HOME` writable.
- Approximately 11 GB free on `/`.
- Staging volume approximately 50.82 MB.
- No confirmed logical database size.

Record the actual `pg_database_size` before choosing the final location. Ensure free space is at least the dump size plus the restore-drill copy plus a safety margin.

### 8.5 Step 4: take the backup

Use a timestamped, non-overwriting filename. Do not put a password on the command line. Prefer a `PGPASSFILE` with mode `600` populated from the approved secret store, or use the container’s local socket if the staging image’s local authentication policy is confirmed.

```bash
set -euo pipefail
set -o noclobber
umask 077

TS="$(date -u +%Y%m%dT%H%M%SZ)"
BASE="$BACKUP_DIR/go_saas_m2_staging_${TS}.dump"

test ! -e "$BASE" || { echo "Refusing to overwrite existing backup"; exit 1; }

PGPASSFILE="$STAGING_PGPASSFILE" pg_dump \
  --host=127.0.0.1 \
  --port=55433 \
  --username=go_saas_m2_staging \
  --dbname=go_saas_m2_staging \
  --format=custom \
  --compress=9 \
  --no-owner \
  --no-privileges \
  --file="$BASE"
```

Notes:

- `pg_dump` 18.6 is available and can dump a PostgreSQL 16 server. Use `pg_restore` 18.6 for the restore drill.
- If policy requires matching client/server versions, use the container’s PostgreSQL 16 `pg_dump` instead. Do not mix a v18 dump with a v16 `pg_restore`.
- Do **not** use `pg_dumpall`; it can include role credential material and is not needed for a database-only backup.
- Do **not** use `--clean` or `--create` for the backup.
- Do **not** pipe the dump through a command that prints it to the terminal or logs.
- Do **not** use `PGPASSWORD=...` inline; it can appear in shell history or process listings.
- Do **not** run `docker compose config` with `.env.stage`; it resolves and prints secrets.

### 8.6 Step 5: encrypt the dump and remove the plaintext

Use an approved encryption method. The host has GnuPG but no public/secret keys in the current keyring, and `age` is not installed. Two acceptable options:

1. Provision an approved GPG recipient key, then encrypt to that recipient.
2. Use GPG symmetric encryption with a strong passphrase held in the approved secret store, supplied through a file descriptor or a `600`-mode passphrase file. Never use `--passphrase` on the command line.

Template for symmetric encryption with an interactive prompt (no passphrase in shell history):

```bash
gpg --symmetric --cipher-algo AES256 --output "$BASE.gpg" "$BASE"
```

If a non-interactive mechanism is required, use the approved secret manager’s file-descriptor injection. Do not place the passphrase in the shell command, environment dump, or report.

After the encrypted artifact is created and verified:

```bash
shred -u "$BASE"
```

On SSDs, `shred` is not a guarantee; rely on full-disk encryption and controlled storage as well. Delete only the plaintext dump, never the encrypted artifact. Do not use wildcard deletion.

### 8.7 Step 6: verify integrity and contents

Before encryption, and again after decryption for restore, verify the dump:

```bash
sha256sum "$BASE" > "$BASE.sha256"
pg_restore --list "$BASE" > "$BASE.toc"

grep -E 'TABLE DATA public (merchants|merchant_users|dashboard_sessions|merchant_webhook_configs|merchant_webhook_deliveries|webhook_events|audit_logs|merchant_api_keys)$' "$BASE.toc"
```

Record in the change log:

- Backup filename.
- Creation timestamp in UTC.
- Source staging resource identifiers.
- Plaintext checksum and encrypted-artifact checksum (checksums are metadata, not secrets; still keep the log access-controlled).
- `pg_restore --list` result.
- Operator and approver.
- Storage location and access owner.

Do not include the checksum values in this report.

After encryption, verify the encrypted artifact can be decrypted and that the decrypted checksum matches:

```bash
gpg --decrypt "$BASE.gpg" | sha256sum
```

Compare with the recorded plaintext checksum. Then remove any temporary decrypted copy under the same controls.

### 8.8 Step 7: restore drill into a disposable database

This is a state-changing database operation and requires separate approval. It must never target `go_saas_m2_staging` itself or any production database.

1. Choose a unique disposable database name:

   ```text
   go_saas_m2_staging_restore_<UTC timestamp>
   ```

2. Confirm the staging role has `CREATEDB`, or obtain an approved administrative role.
3. Create the disposable database only if it does not exist:

   ```bash
   createdb \
     --host=127.0.0.1 \
     --port=55433 \
     --username=go_saas_m2_staging \
     "$RESTORE_DB"
   ```

4. Restore:

   ```bash
   PGPASSFILE="$STAGING_PGPASSFILE" pg_restore \
     --host=127.0.0.1 \
     --port=55433 \
     --username=go_saas_m2_staging \
     --dbname="$RESTORE_DB" \
     --exit-on-error \
     --single-transaction \
     --no-owner \
     --no-privileges \
     "$BASE"
   ```

5. Validate with the queries in §11.
6. Do not start the application against the disposable database.
7. Do not expose the disposable database on any non-loopback interface.
8. Drop it only with explicit approval, after recording results. If retained, isolate it and document the owner and expiry.

### 8.9 Step 8: record evidence and stop

Record:

- Identity query results.
- `schema_migrations.version` and `dirty`.
- Exact critical-table counts.
- Aggregate session/webhook/delivery/audit inventory.
- Backup checksum and TOC verification.
- Restore-drill result and count comparison.
- Any exceptions or anomalies.
- Confirmation that no credential was rotated and no webhook was sent.

Then stop. Do not proceed to credential rotation in this phase.

### 8.10 Commands that must not be used

Do not run any of the following in this phase or during the backup preparation:

- `docker compose down -v`, `docker compose down --volumes`, `docker volume rm`, `docker system prune`, `docker container prune`.
- `docker compose up` without the `postgres` service selector.
- `docker compose --env-file .env.stage config`.
- Unfiltered `docker inspect` on any container.
- `docker exec ... env`, `printenv`, or any environment dump.
- `cat .env.stage`, `git show HEAD:.env.stage`, `git log -p -- .env.stage`, or any command that prints the exposed file.
- `pg_dumpall`.
- `psql` with an inline password, inline DSN, or `PGPASSWORD=...`.
- `ALTER ROLE`, `DROP DATABASE`, `TRUNCATE`, `DELETE`, `UPDATE`, `INSERT`, `COPY ... FROM`, `REINDEX`, `VACUUM FULL`, or `pg_terminate_backend`.
- `make migrate-up`, `make migrate-down`, or any migration.
- Any request to `http://127.0.0.1:8081` or any production endpoint.
- Any webhook send/retry, login, session revocation, or credential test.

---

## 9. Backup protection and integrity requirements

### 9.1 Confidentiality

- Store the backup outside the repository, outside Git, and outside any public sync service.
- Use a directory with mode `700` and files with mode `600`.
- Encrypt at rest with an approved method. The backup contains old webhook ciphertext, refresh-token hashes, password hashes, legacy plaintext merchant credentials where present, invitation tokens in outbox bodies, and audit metadata.
- Keep the encryption key/passphrase in the approved secret store, separate from the backup.
- Restrict access to the incident owner, rotation operator, and approved DBA/security reviewer.
- Do not attach the backup to a ticket, chat, email, or report.
- Do not put the backup inside the Docker volume it is protecting.
- Do not use a world-readable path, `/tmp` as the only copy, or an unencrypted removable drive.

### 9.2 Integrity

- Compute SHA-256 of the plaintext dump immediately after creation.
- Verify `pg_restore --list` and confirm expected table data entries.
- Encrypt the dump and compute a checksum of the encrypted artifact.
- Before any restore, verify the encrypted-artifact checksum, decrypt, and verify the plaintext checksum again.
- Perform a restore drill into a disposable database; accept only `pg_restore` exit code 0 and `--exit-on-error`.
- Compare source and restored `schema_migrations` and critical-table counts.
- Record checksums and verification results in the access-controlled change log, not in this report.

### 9.3 Availability and retention

- Keep at least one verified backup on a different physical/logical location from the live Docker volume if policy permits.
- Do not use the Docker volume as the only backup.
- Define retention, RPO, RTO, and access owner before the backup; these are currently undefined.
- Keep the pre-rotation backup until rotation and restore validation are complete and the incident owner approves destruction.
- Because the backup contains old compromised ciphertext and session hashes, minimize retention once recovery is no longer needed.
- Destroy the backup through the approved secure-destruction process; on SSDs, rely on full-disk encryption and key destruction rather than assuming `shred` is sufficient.
- Record destruction date, operator, and approval.

### 9.4 Chain of custody

Record:

- Backup ID/filename.
- UTC creation time.
- Source staging instance identifiers.
- Operator and approver.
- Tool versions (`pg_dump`/`pg_restore`).
- Plaintext and ciphertext checksums.
- Storage location and access list.
- Restore-drill database name and result.
- Retention/expiry decision.
- Destruction record.

Do not record secret values, passwords, tokens, hashes of secrets, or full connection strings.

### 9.5 Incident-specific cautions

- The backup is **not** a remediation. Six secret categories remain compromised in public Git history.
- The backup contains old `encrypted_secret` ciphertext. It can only be decrypted with the old webhook encryption key, which is also compromised. Do not restore the backup to recover old merchant webhook secrets; reissue them instead.
- A restore can reactivate old `dashboard_sessions` refresh-token hashes. Never start the application against a restored database without deleting sessions first.
- A restore can reintroduce legacy plaintext merchant credentials if `legacy_credential_state` is `LEGACY`. Treat the restored database as sensitive.
- Do not restore the backup into production, the protected backend, or any shared environment.
- If the backup is exposed, treat it as a new incident and escalate immediately.

---

## 10. Merchant webhook reissue tracking requirements

### 10.1 Why tracking is required

The webhook encryption key has no key version, key ID, or decrypt-old/encrypt-new path (`internal/service/webhook_crypto.go:30-99`; `migrations/000010_create_merchant_webhook_configs.up.sql:6-19`). Replacing the key makes every existing `encrypted_secret` unreadable. The supported recovery is to reissue every merchant’s signing secret through the application, which overwrites the ciphertext with a new secret encrypted under the new key (`internal/service/merchant_webhook_service.go:137-181`).

There is no admin bulk reissue endpoint. The available routes are:

- `POST /api/v1/dashboard/webhook-config/rotate` with a dashboard JWT and `OWNER` or `ADMIN` role (`cmd/server/main.go:827-835`).
- `POST /api/v1/merchants/:id/webhook/rotate` with merchant authentication for that merchant (`cmd/server/main.go:560-572`).

Therefore, every merchant must have an authorization path or an explicitly approved exception before the key is replaced.

### 10.2 Tracking fields

Maintain an access-controlled tracking sheet outside the repository with these fields:

| Field | Purpose |
|---|---|
| `merchant_id` | Internal merchant identifier; do not publish |
| `merchant_status` | `ACTIVE`, `INACTIVE`, or `SUSPENDED` |
| `config_status` | `ACTIVE` or `DISABLED` |
| `pre_rotation_updated_at` | Baseline timestamp before key replacement |
| `reissue_required` | Yes/no, with reason |
| `authorization_path` | Dashboard OWNER/ADMIN, merchant API key, approved one-off tool, or unavailable |
| `authorization_owner` | Human responsible for obtaining authorization |
| `reissue_started_at` | UTC timestamp |
| `reissue_completed_at` | UTC timestamp |
| `post_rotation_updated_at` | Must be later than `pre_rotation_updated_at` |
| `result` | Success, failed, or approved exception |
| `audit_event_observed` | `WEBHOOK_CONFIG_CHANGED` / `SECRET_ROTATED` observed |
| `secret_delivery_confirmed` | Yes/no; never store the secret itself |
| `delivery_disabled` | Must remain yes until all rows are accounted for |
| `notes` | Non-secret operational notes |

Do not store plaintext signing secrets, encrypted secret values, dashboard passwords, merchant API keys, JWTs, or cookies in the tracking sheet.

### 10.3 Coverage reconciliation

Before the webhook encryption key is replaced, the operator must be able to state:

```text
total merchant_webhook_configs
  = successfully reissued
  + explicitly disabled with approval
  + approved exceptions
```

No unaccounted row is acceptable. A row is not complete merely because an HTTP rotate call returned success. Confirm all of the following:

- `updated_at` advanced after the rotation start.
- A `WEBHOOK_CONFIG_CHANGED` audit event with `change=SECRET_ROTATED` exists.
- The new plaintext secret was delivered exactly once through the approved channel.
- The row still has a valid status and a destination.
- Delivery remains disabled until every row is accounted for.

### 10.4 Authorization paths

For each merchant, determine one of:

1. **Dashboard OWNER/ADMIN** — a human can authenticate to the dashboard for that merchant and rotate the secret.
2. **Merchant API credential** — a valid merchant API key can call the merchant rotate route for that merchant.
3. **Approved one-off tool** — a reviewed, audited tool can reissue the secret without exposing plaintext; this tool does not exist in the repository and would require separate design/approval.
4. **Approved exception** — the config is disabled and will remain disabled, with explicit incident-owner approval.

If none applies, the webhook-key step remains **BLOCKED**. Do not replace the encryption key and do not enable delivery.

### 10.5 Completion criteria

The reissue phase is complete only when:

- Every `merchant_webhook_configs` row is classified.
- Every required row has a post-rotation `updated_at` and an audit event.
- Every merchant/harness has received its new secret through the approved channel.
- No row remains on old ciphertext.
- `WEBHOOK_DELIVERY_ENABLED=false` remains in force until validation is complete.
- The tracking sheet reconciles exactly to the inventory count.
- The incident owner approves any exceptions.

### 10.6 Exceptions

Exceptions must be explicit and time-bounded:

- Disabled configs may remain disabled with approval.
- A merchant without an authorization path must not be silently skipped.
- Do not delete webhook configs to avoid reissue; deletion is destructive and requires separate approval.
- Do not restore old ciphertext or the old key to avoid reissue.

### 10.7 Audit evidence

For each reissue, capture:

- Merchant ID (in the access-controlled tracking sheet only).
- Timestamp.
- Authorization path and operator.
- `WEBHOOK_CONFIG_CHANGED` event count/observation.
- `updated_at` before/after.
- Delivery-disabled state.
- Secret-delivery confirmation without the secret value.

Do not print or store the new plaintext secret in logs, the report, or the tracking sheet.

---

## 11. Restore-validation plan

### 11.1 Preconditions

- Backup checksum and `pg_restore --list` verified.
- Disposable database name is unique and documented.
- The staging role has `CREATEDB`, or an approved administrative role is available.
- The staging application is stopped.
- `WEBHOOK_DELIVERY_ENABLED=false` and other outbound controls remain off.
- Disk space is sufficient for the restored copy.
- No production or protected-backend connection is used.

### 11.2 Procedure

1. Create the disposable database `go_saas_m2_staging_restore_<UTC timestamp>` only if it does not exist.
2. Restore with:

   ```bash
   pg_restore \
     --host=127.0.0.1 \
     --port=55433 \
     --username=go_saas_m2_staging \
     --dbname="$RESTORE_DB" \
     --exit-on-error \
     --single-transaction \
     --no-owner \
     --no-privileges \
     "$BASE"
   ```

3. Query `schema_migrations` in the restored database:

   ```sql
   SELECT version, dirty FROM schema_migrations;
   ```

4. Run the aggregate counts from §7 against the restored database.
5. Compare the restored counts to the source counts taken in the same frozen window.
6. Verify the expected tables exist and `pg_restore` exited 0.
7. Verify `merchant_webhook_configs` total/status distribution, `dashboard_sessions` total, `merchant_webhook_deliveries` status distribution, and `audit_logs` total/time range match.
8. Do not select secret columns, payloads, URLs, or hashes.
9. Do not start the application against the restored database.
10. Drop the disposable database only with explicit approval; otherwise isolate and document it.

### 11.3 Acceptance criteria

- `pg_restore` exits 0 with `--exit-on-error`.
- `schema_migrations.dirty` is `false`.
- `schema_migrations.version` matches the source and is at least 22; the current migration directory ends at 25.
- Critical-table counts match the source exactly (the application is stopped, so no writes should occur).
- Expected tables and constraints are present.
- `merchant_webhook_configs` count and status distribution match.
- `dashboard_sessions` count matches.
- `merchant_webhook_deliveries` status distribution and config linkage match.
- `audit_logs` count and time range match.
- No secret value, hash, token, ciphertext, or complete environment file appears in output.
- The restored database is not reachable by the application or any external client.

### 11.4 Failure handling

- Do not overwrite or delete the source database.
- Do not restore over `go_saas_m2_staging`.
- Keep the backup and the restore log.
- If `pg_restore` fails, inspect the error without printing row data or secrets.
- Retry into a new disposable database; do not reuse a partially restored database.
- If the backup is corrupt, take a new backup only after investigating the cause and obtaining approval.
- If the source schema is dirty or below the expected version, stop and escalate; do not rotate credentials.

### 11.5 Cleanup

- Drop the disposable database with `dropdb` only after approval and after recording the validation result.
- If retention is required, isolate the disposable database, document its owner and expiry, and ensure no application or external client can reach it.
- Remove any temporary decrypted dump or passphrase file under the approved secure-deletion procedure.
- Keep the encrypted backup until the retention decision is approved.
- Do not use wildcard deletion or delete the live volume.

### 11.6 What restore validation does not prove

- It does not prove that the staging volume is the only copy or that the backup is stored redundantly.
- It does not prove that every merchant can be reissued.
- It does not prove that the old webhook ciphertext is safe; the old secrets remain compromised.
- It does not rotate or revoke any credential.
- It does not validate production readiness or close the incident.

---

## 12. Safe order of inventory, backup, and merchant authorization

### 12.1 Recommended order

| Order | Step | State change | Gate |
|---:|---|---|---|
| 1 | Human approval, change window, incident owner, operator, and restore-drill owner | None | §13 gates |
| 2 | Verify staging identity from Docker/config metadata | None | Read-only |
| 3 | Start only the staging PostgreSQL service | Container start | Separate approval |
| 4 | Verify container labels, volume, network, port, and health | None | Read-only |
| 5 | Run identity/schema/size query (Q-1) | None (read-only transaction) | Staging identity confirmed |
| 6 | Preflight backup destination, space, encryption, and filenames | Directory creation only if approved | §9 |
| 7 | Take the backup | Writes a new backup file | Human-approved backup |
| 8 | Verify checksum and `pg_restore --list` | None | §9.2 |
| 9 | Encrypt and remove the plaintext dump | Writes encrypted artifact; deletes plaintext | §9.1 |
| 10 | Restore into a disposable database | Creates a new database | Separate approval |
| 11 | Run aggregate inventory on the restored copy and confirm on the source in a read-only transaction | None on source | §7 |
| 12 | Build the per-merchant reissue tracking sheet | None on database | §10 |
| 13 | Record evidence and stop | None | §14 |

### 12.2 Why this order

- The backup is taken before any interactive SQL or credential change, so a recovery point exists if a later step fails.
- The restore drill validates the backup before the inventory is trusted.
- Inventory on the restored copy avoids load on the source; a read-only confirmation on the source ensures the restore matches.
- Merchant authorization mapping happens after the inventory so every merchant row is known.
- No write, rotation, reissue, or delivery happens in this phase.

An alternate order of **read-only inventory before backup** is acceptable only if it runs in `BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY` and no write occurs. The backup must still precede any credential rotation or webhook-key change.

### 12.3 Not allowed

- Any write, migration, rotation, session deletion, webhook reissue, or delivery before the backup.
- Any restore over the source database.
- Any backup overwrite.
- Any inventory query that selects secret columns or prints row data.
- Any merchant authorization attempt using credentials in this phase.
- Any production or protected-backend access.
- Any history rewrite, commit, push, or force-push.

---

## 13. Preconditions, blockers, and human decisions

### 13.1 Gate table

| ID | Gate | Current status | Owner |
|---|---|---|---|
| B-01 | Approve starting only the staging PostgreSQL service; keep the backend stopped | **OPEN** | Incident owner |
| B-02 | Provide/pull and pin the `postgres:16-alpine` image digest; image is not present locally | **OPEN** | Operator |
| B-03 | Confirm the exact staging startup command and project name; `docs/mobile-local-staging-execution.md` is missing | **OPEN** | Operator |
| B-04 | Confirm the existing volume `go-saas-m2-staging-pgdata` is the intended cluster and will be mounted, not reinitialized | **OPEN** | DBA/operator |
| B-05 | Define an access-controlled backup destination outside the repository with sufficient space | **OPEN** | Incident owner/DBA |
| B-06 | Define the encryption method and key/passphrase custody; GPG keyring is empty and `age` is absent | **OPEN** | Security/operator |
| B-07 | Define retention, RPO, RTO, access owner, and destruction procedure | **OPEN** | Incident owner/DBA |
| B-08 | Confirm the restore-drill database name, `CREATEDB` privilege, and approval to create/drop it | **OPEN** | DBA |
| B-09 | Verify the volume is healthy and record the actual schema version, dirty state, table counts, and database size | **UNKNOWN** until DB start | DBA |
| B-10 | Confirm per-merchant webhook reissue authority or approve a one-off reissue tool | **OPEN** | Incident owner/merchant ops |
| B-11 | Keep `WEBHOOK_DELIVERY_ENABLED=false`, `EMAIL_ENABLED=false`, `EMAIL_WORKER_ENABLED=false`, `PAYMENT_EXPIRY_ENABLED=false`, and `PAYMENT_SIMULATOR_ENABLED=false` | **VERIFIED** current; must remain | Operator |
| B-12 | Confirm no other process or user uses the staging database during backup | **VERIFIED** no staging backend process; other clients **UNKNOWN** | Operator |
| B-13 | Define whether filesystem product images or other non-database staging artifacts need a separate backup | **UNKNOWN** | Operator |
| B-14 | Fix/document `FRONTEND_PUBLIC_URL` before later dashboard validation; it currently points at the backend port | **OPEN** (not a backup blocker) | Operator |
| B-15 | Record the change in a manual incident/change log; the application does not audit backup or rotation actions | **OPEN** | Incident owner |

### 13.2 Missing information requiring human review

- The actual staging startup procedure: `config.Load()` loads `.env` via `godotenv.Load()` (`internal/config/config.go:308-314`), not `.env.stage`; a plain `make run` or `bin/server` is not a verified staging start. The Compose usage comment is the only command reference, and the referenced `docs/mobile-local-staging-execution.md` is absent.
- Whether the existing volume contains a valid `go_saas_m2_staging` cluster and what its schema version is.
- Whether the configured `STAGING_DB_PASSWORD` matches the role password stored in the existing volume; changing the Compose value alone does not rotate an existing role.
- The logical database size and whether 11 GB free space is sufficient for the dump plus the restore drill.
- The backup destination, encryption recipient/passphrase custody, retention, access owner, and destruction process.
- Whether the staging role has `CREATEDB` for a disposable restore database.
- Whether every merchant has a dashboard OWNER/ADMIN or merchant API credential for reissue.
- Whether a one-off reissue tool is permitted, and who reviews it.
- Whether any staging non-database artifacts (for example, product images) need a separate backup.
- Whether the staging database is used by any other local client, script, or old process during the backup window.

### 13.3 What flips the decision to READY FOR HUMAN-APPROVED BACKUP

Close B-01 through B-08 and B-10, and record the answers to the missing-information items in §13.2. Then the procedure in §8 can be approved for execution. B-09 remains unknown until the database is started, so it is verified during the approved window, not before.

---

## 14. Confirmation of no secret disclosure and no state-changing action

### 14.1 No secret disclosure

- No secret value from `.env.stage` was printed or copied into this report.
- No password hash, refresh-token hash, API-key hash, encrypted secret value, token, cookie, encryption key, or complete environment-file excerpt was printed.
- The audit emitted only key names, populated/empty status, selected non-secret staging identifiers, source references, aggregate query plans, and observed read-only system state.
- This report contains no hashes of secret values and no database connection string with credentials.
- No backup content, merchant webhook ciphertext, or audit metadata was read or printed.

### 14.2 No state-changing action

- No credential was rotated, revoked, or tested.
- No database was connected to and no SQL was executed.
- No backup was created, overwritten, restored, or deleted.
- No container, network, volume, or service was started, stopped, restarted, or reconfigured.
- No Docker volume or database state was modified.
- No migration was run.
- No authentication, session revocation, webhook reissue, or webhook delivery occurred.
- No production system or protected backend was accessed.
- No Git commit, push, force-push, reset, checkout, clean, or history rewrite was performed.
- No environment file was modified.
- The GPG keyring was queried read-only; no key was created, imported, or modified.
- Pre-existing user modifications and untracked Phase 2/3/4A/4B-0/4B-1 documents were preserved.
- The only repository file created by this phase is this report.

### 14.3 Read-only commands used

The inspection used only read-only commands, including:

- `git status`, `git rev-parse`, `git check-ignore`, `git ls-files`.
- `docker ps -a`, `docker volume ls`, `docker volume inspect`, `docker system df -v`, `docker compose ls`, `docker image ls`, `docker inspect` with filtered non-environment fields.
- `ss -ltnp`.
- `pg_dump --version`, `pg_restore --version`, `psql --version`, `gpg --version`, `sha256sum --version`.
- `stat`, `wc`, `df`, `test`.
- Repository file reads and `grep`/`sed` over source, migrations, Compose, CI, Makefile, and documentation.

No `psql` connection, `pg_dump` execution, `pg_restore` execution, `docker compose up`, `docker start`, `docker stop`, `docker rm`, `docker volume rm`, `git add`, `git commit`, or `git push` was run.

### 14.4 Files changed

| Path | Change | Notes |
|---|---|---|
| `docs/phase-4b2-staging-backup-inventory-preparation.md` | Added | This report only. Untracked; not committed. |

No application source, dependency manifest, lockfile, CI/CD workflow, Compose file, migration, environment file, or production configuration was changed. `HEAD` and `origin/main` remain `fd49157`.

---

## 15. Final decision

### **READY AFTER ADDITIONAL PREPARATION**

The staging-only backup and inventory procedure is now defined, and the aggregate-only query plan, protection requirements, restore-validation plan, and merchant reissue tracking requirements are prepared.

It is **not** ready to execute because:

- The staging PostgreSQL container and network are absent, and starting them is a state-changing action requiring approval.
- The `postgres:16-alpine` image is not present locally and must be pulled/pinned under approval.
- The backup destination, encryption method, key custody, retention, access owner, RPO, and RTO are undefined.
- The restore-drill target, `CREATEDB` privilege, and approval are unconfirmed.
- The actual volume health, schema version, and row counts are unknown until the database is started.
- Per-merchant webhook reissue authority is not established.
- The staging startup procedure is undocumented.

Close the gates in §13 and the decision becomes **READY FOR HUMAN-APPROVED BACKUP**.

**Do not perform the backup yet. Do not execute SQL. Do not rotate credentials. Do not reissue webhook secrets. Do not clean Git history. Do not close the incident.**
