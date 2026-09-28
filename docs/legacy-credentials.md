# Legacy Plaintext Credential Lifecycle (Phase 8D.3)

> **Status:** implemented. Migration `000017_legacy_credential_state`.
> **Scope:** freeze creation of new legacy credentials, gate existing ones
> behind a migration window, migrate tenants to the canonical Phase 5C key
> system, then disable the legacy credential permanently.

---

## 1. Why

Since Phase 1 the gateway has authenticated merchant traffic with a
**row-level plaintext API key** stored in `merchants.api_key`:

```
X-API-Key: pk_<hex>
```

Phase 5C replaced this with named, revocable, Argon2id-hashed compound keys:

```
X-API-Key: pk_<hex>:sk_<hex>
```

Phase 5C keys are strictly better — revocable per key, expiry-aware,
`last_used_at` tracked, plaintext never stored — but the legacy format cannot
simply be deleted: existing merchants depend on it. Phase 8D.3 therefore runs a
**controlled migration window** rather than a breaking change:

| Goal | Mechanism |
|------|-----------|
| No *new* plaintext credentials | `POST /api/v1/merchants` frozen (409) |
| Existing merchants keep working | `LEGACY_API_CREDENTIALS_ENABLED` window flag |
| Deterministic, auditable progress | per-merchant `legacy_credential_state` |
| One-way, atomic migration | `SELECT … FOR UPDATE` state transition |
| Guaranteed end state | explicit, idempotent `disable` |

**Out of scope (deliberately):** Redis, Kafka, MFA, password reset, admin key
rotation, email-token redesign, a new auth architecture, SSRF redesign, full
audit logging, and any framework-level refactor.

---

## 2. State machine

Stored in `merchants.legacy_credential_state` (CHECK-constrained to exactly
these three values):

```
                    ┌──────────────────────────────────────────┐
                    │  creation of new legacy credentials is  │
                    │  FROZEN — LEGACY is never entered again │
                    └──────────────────────────────────────────┘

   LEGACY ────────── migrate ──────────► MIGRATED ────── disable ──────► LEGACY_DISABLED
 (plaintext                                   │                              │
  key live)                                   │                              │
                                              └──── (never reachable back) ───┘
```

| State | Legacy key authenticates? | Notes |
|-------|---------------------------|-------|
| `LEGACY` | Yes, while the window flag is on | Pre-existing tenants. Cannot be created any more. |
| `MIGRATED` | **Yes, until explicitly disabled** | Phase 5C key exists. Migration ≠ revocation, so a tenant is never cut off mid-rollout. |
| `LEGACY_DISABLED` | **Never** | Terminal. Only reachable from `MIGRATED`. |

Additional invariants:

- **`api_key IS NULL` independently means "no legacy credential exists"** —
  `WHERE api_key = $1` can never match `NULL`, so the row is unreachable
  through legacy auth regardless of state.
- An **unknown or empty state fails closed** (rejected).
- The state check runs **before** the `ACTIVE` lifecycle check, so a disabled
  credential is indistinguishable from an unknown one (§5).

---

## 3. Migration window flag

`LEGACY_API_CREDENTIALS_ENABLED` (parsed strictly at startup):

| Variable | `APP_ENV` | Result |
|----------|-----------|--------|
| unset | `development` / `test` | `true` — existing merchants keep working |
| unset | `production` or anything else | **`false` — fail-safe closed default** |
| `true` | any | `true` — explicit operator override (allowed in production) |
| `false` | any | `false` |
| anything non-boolean | any | **startup fails** — a typo can never choose a default |

Compared with `getEnvBool` (which silently falls back on a parse error), this
value uses `strconv.ParseBool` and returns an error, because silently defaulting
a security window in either direction is unacceptable.

The flag is logged once at startup (`legacy api credentials enabled/disabled`)
so an operator can see the window position immediately. The flag value is
configuration, never a secret.

Compose plumbing: `docker-compose.yml` passes
`LEGACY_API_CREDENTIALS_ENABLED: ${LEGACY_API_CREDENTIALS_ENABLED:-}` so an
unset host variable lets `config.Load` apply the environment default.

---

## 4. Schema (migration `000017`)

```sql
ALTER TABLE merchants ALTER COLUMN api_key    DROP NOT NULL;
ALTER TABLE merchants ALTER COLUMN api_secret DROP NOT NULL;

ALTER TABLE merchants
    ADD COLUMN legacy_credential_state VARCHAR(20) NOT NULL DEFAULT 'LEGACY',
    ADD CONSTRAINT chk_merchants_legacy_credential_state
        CHECK (legacy_credential_state IN ('LEGACY','MIGRATED','LEGACY_DISABLED')),
    ADD COLUMN legacy_credential_disabled_at TIMESTAMPTZ;
```

- `DEFAULT 'LEGACY'` gives every pre-existing row the correct starting state.
- Dropping `NOT NULL` is what lets freeze-created tenants (§6) exist with
  **no** legacy material at all. The unique index on `api_key` still works —
  PostgreSQL allows any number of `NULL`s.
- The **down** migration drops the two columns and the constraint but
  deliberately **does not restore `NOT NULL`**: credential-less merchants would
  make that fail. Pre-8D.3 code always supplied both columns, so it tolerates
  the nullable form.
- No migration framework was added; this is a single `golang-migrate` pair like
  every other migration.

---

## 5. Authentication gate

`middleware.Auth(merchantSvc, apiKeySvc, legacyCredentialsEnabled)` keeps its
two-stage shape:

1. **Compound Phase 5C** (`pk_…:sk_…`) — unchanged, and **completely
   unaffected by the flag** in either position.
2. **Legacy** (`pk_…` with no `:sk_` segment) — gated twice:

   1. **Flag first, before any database lookup.** Window closed →
      `401 LEGACY_CREDENTIALS_NOT_ENABLED`. Because no lookup happens, the
      response cannot confirm whether the credential exists.
   2. **State second.** `AllowsLegacyAuth()` must be true (`LEGACY` or
      `MIGRATED`). `LEGACY_DISABLED` — and any unknown value — is rejected as a
      plain `401 INVALID_API_KEY`, **before** the `ACTIVE` check, so the
      disabled state cannot be probed through `MERCHANT_INACTIVE`.

3. Both fail → `401 INVALID_API_KEY`.

### No validity oracle

| Situation | Status | Code |
|-----------|--------|------|
| Window closed + would-be-valid key | 401 | `LEGACY_CREDENTIALS_NOT_ENABLED` |
| Window closed + never-existed key | 401 | `LEGACY_CREDENTIALS_NOT_ENABLED` |
| Window open + `LEGACY_DISABLED` key | 401 | `INVALID_API_KEY` |
| Window open + never-existed key | 401 | `INVALID_API_KEY` |
| Window open + `LEGACY`/`MIGRATED` + `SUSPENDED` | 401 | `MERCHANT_INACTIVE` (Phase 7 behaviour preserved) |

The first four pairs are byte-identical apart from `meta.request_id`.

---

## 6. Freeze — no new legacy credentials

`MerchantService.CreateMerchant` now returns
`ErrLegacyCredentialCreationDisabled` **as its first statement** — before the
duplicate-code check and before any repository call — so:

- `POST /api/v1/merchants` always returns
  **`409 LEGACY_CREDENTIAL_CREATION_DISABLED`** (the route itself is retained so
  existing clients get a stable error rather than a 404);
- an existing `code` still yields the *freeze* error, not
  `DUPLICATE_MERCHANT_CODE`;
- a repository failure can never surface here, because the repository is never
  reached.

`generateAPIKey` / `generateAPISecret` (and their `apiKeyPrefix`,
`apiSecretPrefix`, `credentialBytes` constants) were **deleted**. The only
credential generator left in the codebase is `generateAPIKeyPair`
(Argon2id) — **one key format system-wide**.

`POST /api/v1/admin/onboarding/merchants` no longer generates legacy material:
the merchant row is written with `api_key = api_secret = NULL` and
`legacy_credential_state = 'MIGRATED'` directly, and still returns exactly one
Phase 5C secret.

---

## 7. Endpoints

Both live under `/api/v1/dashboard`, require a Bearer JWT, and are gated by
`RequireRole(OWNER, ADMIN)` — `VIEWER` receives `403 INSUFFICIENT_ROLE`.

**The merchant is always taken from the JWT (`caller.MerchantID`).** No
`merchant_id` is read from the path, query, or body, so a caller can only ever
act on its own tenant. A dedicated `/legacy-credential` prefix (rather than
`/merchants/:id/…`) avoids a Gin wildcard conflict with the existing `:id`
siblings.

### `POST /api/v1/dashboard/legacy-credential/migrate` → `201`

Atomic `LEGACY → MIGRATED`. Creates a canonical Phase 5C credential and
returns its plaintext secret **exactly once**:

```json
{
  "id": "…", "merchant_id": "…", "name": "Migrated API Key",
  "key_id": "pk_…", "secret": "sk_…", "status": "ACTIVE",
  "created_at": "…",
  "legacy_credential_state": "MIGRATED",
  "legacy_disabled": false
}
```

- Field conventions (`key_id` + `secret`) reuse Phase 5C — no second format.
- The secret is Argon2id-hashed before it is persisted; the plaintext exists
  only in the response frame and is never logged.
- Re-migrating (including the losing side of a race) →
  `409 LEGACY_CREDENTIAL_ALREADY_MIGRATED`. The secret can never be re-read.

### `POST /api/v1/dashboard/legacy-credential/disable` → `200`

Atomic `MIGRATED → LEGACY_DISABLED`. Returns state only — no credentials:

```json
{
  "legacy_credential_state": "LEGACY_DISABLED",
  "legacy_credential_disabled_at": "2026-09-23T16:00:00Z",
  "already_disabled": false
}
```

- **Idempotent:** repeats return `200` with `already_disabled: true` and never
  rewrite `legacy_credential_disabled_at`.
- Still-`LEGACY` merchant → `409 LEGACY_CREDENTIAL_MIGRATION_REQUIRED` (the
  tenant is never left with no working credential).
- Missing merchant → `404 MERCHANT_NOT_FOUND`.

### Stability report surface

`GET /api/v1/merchants/:id` and `GET /api/v1/dashboard/settings` now include
`legacy_credential_state` (+ `legacy_credential_disabled_at`) so the dashboard
can prompt for migration. These are state labels, never credentials.
`model.Merchant.APIKey` now carries `json:"-"` alongside `APISecret`.

### New error codes

| Code | HTTP | Meaning |
|------|------|---------|
| `LEGACY_CREDENTIAL_CREATION_DISABLED` | 409 | Creation frozen (always) |
| `LEGACY_CREDENTIALS_NOT_ENABLED` | 401 | Window closed; no lookup performed |
| `LEGACY_CREDENTIAL_MIGRATION_REQUIRED` | 409 | Disable before migrate |
| `LEGACY_CREDENTIAL_ALREADY_MIGRATED` | 409 | Migrate outside `LEGACY` |

---

## 8. Atomicity & concurrency

`repository.LegacyCredentialStore` (`NewPGLegacyCredentialStore(pool, keyRepo)`)
runs every transition in **one transaction** that takes a row lock before it
classifies anything:

```sql
SELECT legacy_credential_state FROM merchants WHERE id = $1 FOR UPDATE;
```

then, for migrate: `keyRepo.CreateInTx` (Phase 5C insert) → `UPDATE` the state
→ commit. Key insert and state flip **commit or roll back together**, mirroring
`OnboardingProvisioner`.

Consequences:

- Two concurrent `migrate` calls → exactly one wins; the loser observes a
  non-`LEGACY` state under its lock and gets `409`.
- Concurrent `disable` calls → the first writes, the rest are no-ops with
  `already_disabled: true` and the original timestamp.
- `disabled_at` is written with `COALESCE(legacy_credential_disabled_at, NOW())`
  so it can only ever be set once.

> **Test limitation (documented, not hidden):** the repository has no
> PostgreSQL integration harness (no `pgxpool` in any `_test.go`), so the
> concurrency tests drive the real `LegacyCredentialService` against an
> in-memory store whose mutex reproduces the `FOR UPDATE` critical section
> (classify + write under one lock). They verify delegation, single-winner
> semantics and stable error mapping under real goroutines; the SQL itself is
> additionally covered by the manual checklist in the phase report.

---

## 9. Rate limiting

No new limiter was added. Dashboard routes have never been rate-limited in this
codebase, and the Phase 8D.1 limiters on the API / login / admin routes are
untouched — so `RATE_LIMIT_*` behaviour is unchanged.

---

## 10. Security invariants

- **No plaintext at rest:** legacy `api_secret` remains a SHA-256 hash; the
  migrated Phase 5C secret is Argon2id-hashed; neither plaintext is ever
  written to the database.
- **One-time disclosure:** the migrate secret is returned in the `201` body
  only, never logged, never re-issuable (`409` on every later attempt).
- **No leakage:** no `sk_` secret, no bare legacy `pk_` credential, no
  `secret_hash` and no Argon2 parameters ever appear in responses, logs or
  Swagger. The one credential-adjacent value that *is* logged on migrate is the
  new compound credential's **`key_id`** (`pk_<key_id>`) — the public half of a
  Phase 5C pair, whose secret half is a separate `sk_` stored only as an
  Argon2id hash. That matches the existing convention: `key_id` is already
  returned by the Phase 5C list/create endpoints and is not a credential on its
  own. Verified against the container logs: the only match in 20 minutes of
  traffic was that single `key_id` field; the 67-character `sk_` never appears.
- Handlers return static message strings; internal errors are logged
  server-side and surfaced as `500 INTERNAL_ERROR`.
- **Tenant isolation:** merchant identity comes from the JWT only.
- **Authorization:** OWNER/ADMIN only; VIEWER → 403; anonymous → 401.
- **Merchant lifecycle:** non-ACTIVE tenants are rejected by
  `RequireDashboardAuth` before either handler runs.
- **Fail-safe:** production defaults to the closed window; invalid config
  fails startup; unknown state fails closed.

---

## 11. Operational runbook

```bash
# 1. Open the migration window in production (deliberate operator action).
LEGACY_API_CREDENTIALS_ENABLED=true

# 2. Each tenant migrates from its dashboard (OWNER/ADMIN):
curl -sX POST $BASE/api/v1/dashboard/legacy-credential/migrate \
     -H "Authorization: Bearer $TOKEN"        # 201 → key_id + secret (ONCE)

# 3. Roll the tenant over to the Phase 5C key, verify traffic, then:
curl -sX POST $BASE/api/v1/dashboard/legacy-credential/disable \
     -H "Authorization: Bearer $TOKEN"        # 200, idempotent

# 4. Once every tenant is LEGACY_DISABLED, close the window permanently:
LEGACY_API_CREDENTIALS_ENABLED=false          # (production default when unset)
```

Check progress at any time:

```sql
SELECT legacy_credential_state, COUNT(*)
FROM   merchants GROUP BY 1 ORDER BY 1;
```

---

## 12. Test coverage

| Area | Where |
|------|-------|
| Flag defaults / fail-safe / strict parse | `internal/config/config_legacy_credentials_test.go` |
| Flag gate, state gate, fail-closed, no oracle | `internal/middleware/auth_legacy_credential_test.go` |
| Freeze (always 409, pre-duplicate, pre-repo) | `internal/handler/merchant_handler_test.go` |
| Migrate/disable service logic, sentinels, idempotency, isolation, concurrency | `internal/service/legacy_credential_service_test.go` |
| Handler roles, tenant, lifecycle, 409/404/500 mapping, leakage, HTTP concurrency | `internal/handler/dashboard_legacy_credential_handler_test.go` |
| Full HTTP flow + window on/off (F4 regression) | `internal/handler/legacy_credential_e2e_test.go` |
| Onboarding creates no legacy material | `internal/service/onboarding_service_test.go` |

The e2e test walks the canonical path end-to-end over HTTP:
**legacy works → migrate (one-time secret) → legacy still works → disable →
legacy rejected and indistinguishable from an unknown key → disable idempotent
→ re-migrate 409.**
