# Database Documentation

## Overview

Database: `payment_gateway` (PostgreSQL 16)

All tables use `UUID` primary keys generated server-side (`gen_random_uuid()`).
All timestamps are `TIMESTAMP WITH TIME ZONE` stored in UTC.
Monetary amounts are `BIGINT` (smallest currency unit — no floating point).

Phase 8D.5 adds the append-only `audit_logs` table (migration `000018`) for
security events. It stores tenant/actor/request/IP attribution and bounded,
secret-safe JSONB metadata. Audit rows have no cascading business-row foreign
keys, and the application repository has no update/delete operation. Retention
is currently indefinite; see `docs/phase-8d5-security-audit.md`.

---

## Entity Relationship Diagram

```
merchants
    │
    ├── 1:N ──► transactions ── 1:N ──► payment_attempts
    │
    ├── 1:1 ──► merchant_webhook_configs   (outbound endpoint, Phase 6)
    │
    └── 1:N ──► merchant_webhook_deliveries (transactional outbox, Phase 6)
                      │
                      └── N:1 ──► transactions
```

**Note:** `webhook_events` (Phase 3) is inbound **provider → gateway** and is
not shown above. Phase 6 outbound tables are separate.

```
merchants
─────────────────────────────────────
id          UUID PK
name        VARCHAR(150) NOT NULL
code        VARCHAR(50)  UNIQUE NOT NULL
api_key     VARCHAR(255) UNIQUE NULL      ← legacy key; NULL = none exists (8D.3)
api_secret  VARCHAR(255) NULL             ← SHA-256 hash, never plain text
status      VARCHAR(20)  NOT NULL        ← ACTIVE | INACTIVE | SUSPENDED
legacy_credential_state
            VARCHAR(20)  NOT NULL DEFAULT 'LEGACY'
                                          ← LEGACY | MIGRATED | LEGACY_DISABLED (8D.3)
legacy_credential_disabled_at
            TIMESTAMPTZ NULL              ← set once, at first disable (8D.3)
created_at  TIMESTAMPTZ NOT NULL
updated_at  TIMESTAMPTZ NOT NULL

transactions
─────────────────────────────────────
id                      UUID PK
merchant_id             UUID FK → merchants.id
merchant_order_id       VARCHAR(100) NOT NULL
amount                  BIGINT NOT NULL        ← smallest currency unit (e.g. IDR in rupiah)
currency                VARCHAR(3) NOT NULL    ← ISO 4217 (IDR in Phase 2)
payment_method          VARCHAR(50) NOT NULL   ← QRIS | VA | EWALLET (Phase 3+)
provider                VARCHAR(50)            ← MOCK (Phase 2) | future providers
provider_transaction_id VARCHAR(100)           ← provider's own reference (set after CREATED→PENDING)
payment_url             TEXT                   ← URL for customer payment (set after CREATED→PENDING)
status                  VARCHAR(30) NOT NULL   ← see state machine
expired_at              TIMESTAMPTZ
paid_at                 TIMESTAMPTZ
created_at              TIMESTAMPTZ NOT NULL
updated_at              TIMESTAMPTZ NOT NULL

payment_attempts
─────────────────────────────────────
id                      UUID PK
transaction_id          UUID FK → transactions.id
provider                VARCHAR(50) NOT NULL
provider_transaction_id VARCHAR(100)       ← provider's own reference
request_payload         JSONB              ← full request sent to provider
response_payload        JSONB              ← full response received
status                  VARCHAR(30) NOT NULL ← SUCCESS | FAILED
attempt_number          INT NOT NULL        ← starts at 1
created_at              TIMESTAMPTZ NOT NULL
updated_at              TIMESTAMPTZ NOT NULL
```

---

## Table Details

### merchants

| Column | Type | Nullable | Notes |
|--------|------|----------|-------|
| id | UUID | NOT NULL | PK, server-generated |
| name | VARCHAR(150) | NOT NULL | Display name |
| code | VARCHAR(50) | NOT NULL | Short unique identifier |
| api_key | VARCHAR(255) | **NULL allowed** (since 000017) | Legacy public key `pk_<hex>`. **NULL = no legacy credential exists.** Creation frozen in Phase 8D.3 |
| api_secret | VARCHAR(255) | **NULL allowed** (since 000017) | SHA-256 of `sk_<hex>`, never returned via API. NULL whenever `api_key` is NULL |
| status | VARCHAR(20) | NOT NULL | `ACTIVE` \| `INACTIVE` \| `SUSPENDED` |
| legacy_credential_state | VARCHAR(20) | NOT NULL, default `'LEGACY'` | Phase 8D.3: `LEGACY` \| `MIGRATED` \| `LEGACY_DISABLED` (CHECK). Authoritative gate for legacy-key auth |
| legacy_credential_disabled_at | TIMESTAMPTZ | NULL | Set **once**, when `LEGACY_DISABLED` is first reached |
| created_at | TIMESTAMPTZ | NOT NULL | |
| updated_at | TIMESTAMPTZ | NOT NULL | |

**Indexes:**
- `merchants_pkey` — PRIMARY KEY on `id`
- `uq_merchants_code` — UNIQUE on `code`
- `uq_merchants_api_key` — UNIQUE on `api_key` (PostgreSQL permits many `NULL`s)
- `idx_merchants_api_key` — btree on `api_key` (fast auth lookup)

**Check constraints:**
- `status IN ('ACTIVE', 'INACTIVE', 'SUSPENDED')`
- `chk_merchants_legacy_credential_state` — `legacy_credential_state IN ('LEGACY', 'MIGRATED', 'LEGACY_DISABLED')`

> `NOT NULL` on `api_key`/`api_secret` was dropped by migration `000017` so
> tenants created after the Phase 8D.3 freeze can exist with **no** legacy
> material (`api_key IS NULL`, state `MIGRATED`). Because a `NULL` key can
> never match `WHERE api_key = $1`, such merchants are unreachable through
> legacy auth regardless of state. See
> [legacy-credentials.md](./legacy-credentials.md).

---

### transactions

| Column | Type | Nullable | Notes |
|--------|------|----------|-------|
| id | UUID | NOT NULL | PK, server-generated |
| merchant_id | UUID | NOT NULL | FK → merchants.id |
| merchant_order_id | VARCHAR(100) | NOT NULL | Merchant's own order reference |
| amount | BIGINT | NOT NULL | > 0, smallest currency unit |
| currency | VARCHAR(3) | NOT NULL | ISO 4217 (IDR in Phase 2) |
| payment_method | VARCHAR(50) | NOT NULL | QRIS in Phase 2 |
| provider | VARCHAR(50) | NULL | Set after provider call (e.g. MOCK) |
| provider_transaction_id | VARCHAR(100) | NULL | Provider's own reference, set when CREATED→PENDING |

For the Midtrans Snap adapter the gateway transaction UUID is used as its unique
Midtrans order ID and is persisted as `provider_transaction_id`; no credentials
or full sensitive provider responses are stored.
| payment_url | TEXT | NULL | Customer payment URL, set when CREATED→PENDING |
| status | VARCHAR(30) | NOT NULL | State machine value |
| expired_at | TIMESTAMPTZ | NULL | Set by provider |
| paid_at | TIMESTAMPTZ | NULL | Set when PAID |
| created_at | TIMESTAMPTZ | NOT NULL | |
| updated_at | TIMESTAMPTZ | NOT NULL | Updated on every state change |

**Indexes:**
- `transactions_pkey` — PRIMARY KEY on `id`
- `uq_transactions_merchant_order` — UNIQUE on `(merchant_id, merchant_order_id)`
- `idx_transactions_merchant_id` — btree on `merchant_id`
- `idx_transactions_status` — btree on `status`
- `idx_transactions_created_at` — btree on `created_at DESC`
- `idx_transactions_provider_tx_id` — btree on `provider_transaction_id` (WHERE NOT NULL)
- `idx_transactions_merchant_created_at` — btree on `(merchant_id, created_at DESC, id DESC)` — supports `GET /api/v1/payments` unfiltered listing
- `idx_transactions_merchant_status_created_at` — btree on `(merchant_id, status, created_at DESC, id DESC)` — supports listing with `status` filter

**Foreign key:** `merchant_id REFERENCES merchants(id)`

**Check constraints:**
- `status IN ('CREATED','PENDING','PAID','FAILED','EXPIRED','CANCELLED')`
- `amount > 0`

**Key design note:** The `(merchant_id, merchant_order_id)` unique index enforces
that a merchant cannot create two transactions with the same order ID.
Two different merchants **can** use the same order ID.

**Phase 2 note:** `provider_transaction_id` and `payment_url` are stored directly
on the transaction row (not just in `payment_attempts`) so that `GET /payments/:id`
can return them with a single indexed query — no join required.

---

### payment_attempts

| Column | Type | Nullable | Notes |
|--------|------|----------|-------|
| id | UUID | NOT NULL | PK, server-generated |
| transaction_id | UUID | NOT NULL | FK → transactions.id |
| provider | VARCHAR(50) | NOT NULL | e.g. `MOCK` |
| provider_transaction_id | VARCHAR(100) | NULL | Provider's own reference |
| request_payload | JSONB | NULL | Full request sent to provider |
| response_payload | JSONB | NULL | Full response received |
| status | VARCHAR(30) | NOT NULL | `SUCCESS` or `FAILED` |
| attempt_number | INT | NOT NULL | Monotonically increasing per transaction |
| created_at | TIMESTAMPTZ | NOT NULL | |
| updated_at | TIMESTAMPTZ | NOT NULL | |

**Indexes:**
- `payment_attempts_pkey` — PRIMARY KEY on `id`
- `idx_payment_attempts_transaction_id` — btree on `transaction_id`
- `idx_payment_attempts_transaction_attempt` — btree on `(transaction_id, attempt_number DESC)`

**Foreign key:** `transaction_id REFERENCES transactions(id)`

**Check constraint:** `attempt_number > 0`

---

### merchant_webhook_configs (Phase 6 — outbound)

One row per merchant. Signing secret stored as AES-256-GCM ciphertext (`encrypted_secret`).

| Column | Type | Notes |
|--------|------|-------|
| id | UUID | PK |
| merchant_id | UUID | FK → merchants, UNIQUE (1:1) |
| url | TEXT | Merchant HTTPS endpoint |
| encrypted_secret | TEXT | Never exposed via API |
| status | VARCHAR(20) | `ACTIVE` \| `DISABLED` |
| description | VARCHAR(100) | Optional |
| created_at / updated_at | TIMESTAMPTZ | |

---

### merchant_webhook_deliveries (Phase 6 — transactional outbox)

Separate from inbound `webhook_events`. Worker claims rows with `FOR UPDATE SKIP LOCKED`.

| Column | Type | Notes |
|--------|------|-------|
| id | UUID | PK |
| merchant_id | UUID | FK → merchants |
| config_id | UUID | FK → merchant_webhook_configs (nullable) |
| event_id | VARCHAR(64) | Stable `evt_…` — UNIQUE per `(merchant_id, event_id)` |
| event_type | VARCHAR(64) | e.g. `payment.paid` |
| transaction_id | UUID | FK → transactions |
| endpoint_url | TEXT | Snapshot at enqueue time |
| payload | JSONB | Versioned event envelope |
| attempt_count | INT | Delivery attempts |
| status | VARCHAR(20) | `PENDING` \| `PROCESSING` \| `DELIVERED` \| `FAILED` \| `DEAD` |
| next_attempt_at | TIMESTAMPTZ | Worker schedule |
| processing_at | TIMESTAMPTZ | Claim timestamp (stale recovery) |
| delivered_at | TIMESTAMPTZ | Set on 2xx |
| last_http_status / last_error | INT / TEXT | Safe diagnostics |

---

## Transaction Status Values

| Status | Description | Terminal? |
|--------|-------------|-----------|
| `CREATED` | Transaction persisted, provider not yet called | No |
| `PENDING` | Provider accepted, awaiting customer payment | No |
| `PAID` | Payment received (Phase 3+) | Yes |
| `FAILED` | Provider rejected or internal error | Yes |
| `EXPIRED` | Payment link expired without payment (Phase 3+) | Yes |
| `CANCELLED` | Cancelled by merchant | Yes |

---

## Allowed State Transitions

```
CREATED  → PENDING    (provider accepted)
CREATED  → FAILED     (provider rejected at creation)
CREATED  → CANCELLED  (merchant cancels before provider call)

PENDING  → PAID       (webhook/polling — Phase 3+)
PENDING  → FAILED     (provider reports failure)
PENDING  → EXPIRED    (expiry job — Phase 3+)
PENDING  → CANCELLED  (merchant cancels + provider confirms)
```

Invalid transitions (enforced in code and never written to DB):
```
PAID      → any
FAILED    → any
EXPIRED   → any
CANCELLED → any
```

---

## Migration Files

| File | Description |
|------|-------------|
| `000001_create_merchants.up.sql` | Create `merchants` table, indexes, constraints |
| `000001_create_merchants.down.sql` | Drop everything from migration 1 |
| `000002_create_transactions.up.sql` | Create `transactions` table |
| `000002_create_transactions.down.sql` | Drop everything from migration 2 |
| `000003_create_payment_attempts.up.sql` | Create `payment_attempts` table |
| `000003_create_payment_attempts.down.sql` | Drop everything from migration 3 |
| `000004_add_payment_url_provider_tx_id.up.sql` | Add `payment_url` and `provider_transaction_id` columns to transactions |
| `000004_add_payment_url_provider_tx_id.down.sql` | Drop those two columns |
| `000005_create_webhook_events.up.sql` | Create `webhook_events` table |
| `000005_create_webhook_events.down.sql` | Drop webhook_events |
| `000006_add_expiry_index.up.sql` | Add partial index for expiry worker queries |
| `000006_add_expiry_index.down.sql` | Drop expiry index |
| `000007_create_idempotency_keys.up.sql` | Create `idempotency_keys` table |
| `000007_create_idempotency_keys.down.sql` | Drop idempotency_keys |
| `000008_create_merchant_api_keys.up.sql` | Create `merchant_api_keys` table |
| `000008_create_merchant_api_keys.down.sql` | Drop merchant_api_keys |
| `000009_add_transaction_listing_indexes.up.sql` | Add composite indexes for `GET /api/v1/payments` listing |
| `000009_add_transaction_listing_indexes.down.sql` | Drop listing indexes |
| `000010_create_merchant_webhook_configs.up.sql` | Create `merchant_webhook_configs` (outbound endpoint + encrypted secret) |
| `000010_create_merchant_webhook_configs.down.sql` | Drop merchant_webhook_configs |
| `000011_create_merchant_webhook_deliveries.up.sql` | Create `merchant_webhook_deliveries` transactional outbox |
| `000011_create_merchant_webhook_deliveries.down.sql` | Drop merchant_webhook_deliveries |
| `000012_refund_core.up.sql` | Add `refunded_amount` / `reserved_refund_amount` to transactions; create `refunds`, `refund_attempts`; extend `idempotency_keys` with `refund_id` |
| `000012_refund_core.down.sql` | Reverse migration 12 |
| `000013_create_settlements.up.sql` | Create `settlements` table |
| `000013_create_settlements.down.sql` | Drop `settlements` |
| `000014_create_merchant_users.up.sql` | Create `merchant_users` dashboard accounts |
| `000014_create_merchant_users.down.sql` | Drop `merchant_users` |
| `000015_create_merchant_user_invitations.up.sql` | Create `merchant_user_invitations` |
| `000015_create_merchant_user_invitations.down.sql` | Drop `merchant_user_invitations` |
| `000016_create_email_outbox.up.sql` | Create `email_outbox` |
| `000016_create_email_outbox.down.sql` | Drop `email_outbox` |
| `000017_legacy_credential_state.up.sql` | Phase 8D.3: drop `NOT NULL` from `merchants.api_key`/`api_secret`; add `legacy_credential_state` (CHECK, default `LEGACY`) and `legacy_credential_disabled_at` |
| `000017_legacy_credential_state.down.sql` | Drop the two state columns and the CHECK (**`NOT NULL` is deliberately NOT restored** — credential-less merchants exist) |
| `000018_create_audit_logs.up.sql` | Phase 8D.5: create append-only, tenant-aware `audit_logs` with bounded JSONB metadata and investigation indexes |
| `000018_create_audit_logs.down.sql` | Drop `audit_logs` (retention is otherwise indefinite) |

Migrations are tracked by `golang-migrate` in `schema_migrations`. The down
scripts are not a substitute for an application rollback plan: several are
destructive, and migration 17 intentionally does not restore the earlier
`NOT NULL` constraints. Do not run a down migration automatically; require
explicit approval and use a backup when schema restoration is necessary.

---

## Common Queries

```sql
-- Auth lookup (indexed, fast) — legacy path; gated by legacy_credential_state
-- and LEGACY_API_CREDENTIALS_ENABLED before it is ever used (Phase 8D.3).
-- A NULL api_key can never match, so credential-less merchants are unreachable.
SELECT * FROM merchants WHERE api_key = $1;

-- Phase 8D.3 migration progress (runbook: docs/legacy-credentials.md)
SELECT legacy_credential_state, COUNT(*)
FROM   merchants GROUP BY 1 ORDER BY 1;

-- Merchant-scoped transaction fetch (isolation enforced here)
SELECT * FROM transactions WHERE id = $1 AND merchant_id = $2;

-- Duplicate order check
SELECT COUNT(*) FROM transactions
WHERE merchant_id = $1 AND merchant_order_id = $2;

-- Conditional status update (optimistic lock)
UPDATE transactions
SET status = $1, updated_at = NOW()
WHERE id = $2 AND status = $3;

-- Update status + provider info atomically (CREATED → PENDING)
UPDATE transactions
SET status                   = $1,
    provider                 = $2,
    provider_transaction_id  = $3,
    payment_url              = $4,
    updated_at               = NOW()
WHERE id = $5 AND status = $6;

-- Payment attempts for a transaction (audit trail)
SELECT * FROM payment_attempts
WHERE transaction_id = $1
ORDER BY attempt_number ASC;

-- Paginated listing with merchant isolation (GET /api/v1/payments)
-- Merchant_id is always $1. Additional optional filters use $2…$N.
-- ORDER BY is fixed — not client-controlled.
SELECT id, merchant_id, merchant_order_id, amount, currency,
       payment_method, provider, provider_transaction_id, payment_url,
       status, expired_at, paid_at, created_at, updated_at
FROM   transactions
WHERE  merchant_id = $1
  AND  status      = $2   -- optional; may be omitted
  AND  created_at >= $3   -- optional created_from (inclusive)
  AND  created_at <  $4   -- optional created_to (exclusive)
ORDER  BY created_at DESC, id DESC
LIMIT  $5 OFFSET $6;

-- Matching count query for accurate total/total_pages
SELECT COUNT(*)
FROM   transactions
WHERE  merchant_id = $1
  AND  status      = $2   -- same optional filters as above
  AND  created_at >= $3
  AND  created_at <  $4;
```

---

## Phase 7B — Refund tables (IMPLEMENTED in migration 000012)

### transactions — added columns

| Column | Type | Notes |
|--------|------|-------|
| `refunded_amount` | BIGINT | Sum of SUCCEEDED refund amounts. Default 0. |
| `reserved_refund_amount` | BIGINT | Sum of PENDING + PROCESSING refund amounts. Default 0. |

**Check constraints added:**
```sql
CHECK (refunded_amount >= 0)
CHECK (reserved_refund_amount >= 0)
CHECK (refunded_amount + reserved_refund_amount <= amount)
```

### refunds

| Column | Type | Notes |
|--------|------|-------|
| id | UUID PK | |
| merchant_id | UUID FK → merchants | Tenant isolation |
| transaction_id | UUID FK → transactions | |
| amount | BIGINT | CHECK > 0 |
| currency | VARCHAR(3) | Must match transaction |
| status | VARCHAR(30) | CHECK enum (PENDING / PROCESSING / SUCCEEDED / FAILED) |
| provider | VARCHAR(50) | From transaction |
| provider_refund_id | VARCHAR(150) NULL | UNIQUE (provider, provider_refund_id) WHERE NOT NULL |
| reason | TEXT NULL | |
| failure_code | VARCHAR(50) NULL | Set on FAILED |
| failure_message | TEXT NULL | Set on FAILED |
| idempotency_key_id | UUID FK NULL | Links to idempotency_keys.id |
| requested_at | TIMESTAMPTZ | |
| succeeded_at | TIMESTAMPTZ NULL | |
| failed_at | TIMESTAMPTZ NULL | |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

**Indexes:**
- `idx_refunds_merchant_id_created` — `(merchant_id, created_at DESC, id DESC)` — list queries
- `idx_refunds_transaction_id` — `(transaction_id, created_at DESC, id DESC)` — payment refund list
- `idx_refunds_status` — `status` — filter queries
- `uq_refunds_provider_refund_id` — `UNIQUE (provider, provider_refund_id) WHERE provider_refund_id IS NOT NULL`

### refund_attempts

Mirrors `payment_attempts` for refund provider calls.

| Column | Type | Notes |
|--------|------|-------|
| id | UUID PK | |
| refund_id | UUID FK → refunds | |
| provider | VARCHAR(50) | |
| provider_refund_id | VARCHAR(150) NULL | |
| request_payload | JSONB NULL | Request sent to provider |
| response_payload | JSONB NULL | Response from provider |
| status | VARCHAR(30) | SUCCESS / FAILED / TIMEOUT |
| attempt_number | INT | CHECK > 0 |
| created_at | TIMESTAMPTZ | |
| updated_at | TIMESTAMPTZ | |

**Index:** `idx_refund_attempts_refund_id` — `(refund_id, attempt_number ASC)`

### idempotency_keys — added column

| Column | Type | Notes |
|--------|------|-------|
| refund_id | UUID NULL FK → refunds | Set when idempotency key was used for a refund |

---

## Phase 7C — Settlement and reconciliation tables

Migration `000013_create_settlements` creates:

| Table | Purpose |
|-------|---------|
| `settlements` | Provider settlement batch imports |
| `settlement_items` | Lines within a settlement batch |
| `reconciliation_runs` | Auditable execution summary for one reconciliation run |
| `reconciliation_results` | Current audit result per settlement item |

Foreign keys, status/type checks, pagination indexes, and uniqueness for `(provider, settlement_ref)`, `(settlement_id, item_ref)`, and provider payment/refund identities across settlement batches are included. Settlement rows are external evidence and do not mutate payment/refund truth.

---

## Phase 8 — Dashboard users and sessions

Migration `000014_create_merchant_users` creates:

| Table | Purpose |
|-------|---------|
| `merchant_users` | Dashboard accounts (email + Argon2id password hash, role, status) |
| `dashboard_sessions` | Refresh sessions; stores SHA-256 of opaque refresh token only |

Email is **globally unique**. Roles: `OWNER`, `ADMIN`, `VIEWER`. Statuses: `ACTIVE`, `DISABLED`. See [phase-8-auth-design.md](./phase-8-auth-design.md).

---

## Phase 8B — Team invitations

Migration `000015_create_merchant_user_invitations` creates:

| Column | Type | Notes |
|--------|------|-------|
| id | UUID | PK |
| merchant_id | UUID | FK → merchants (ON DELETE CASCADE) |
| email | VARCHAR(254) | Normalised (lowercase, trimmed) |
| role | VARCHAR(20) | `OWNER` \| `ADMIN` \| `VIEWER` (CHECK) |
| token_hash | VARCHAR(64) | **SHA-256 hex of the opaque invitation token — the ONLY token store; plaintext is returned once in the create response and never persisted** |
| status | VARCHAR(20) | `PENDING` → `ACCEPTED` \| `EXPIRED` \| `REVOKED` (CHECK; `EXPIRED` transitions lazily at read/acceptance time) |
| expires_at | TIMESTAMPTZ | Token validity (`INVITATION_TOKEN_TTL`, default 48h) |
| accepted_at | TIMESTAMPTZ | NULL until accepted |
| created_at / updated_at | TIMESTAMPTZ | |

Indexes: `uq_merchant_user_invitations_token_hash` (O(1) single-use token lookup),
`uq_merchant_user_invitations_pending` partial UNIQUE `(merchant_id, email) WHERE status = 'PENDING'`
(at most one active invitation per email), `idx_merchant_user_invitations_merchant_created`.

**This table is the sole authority for invitation validity.** No background
worker ever mutates it — including the Phase 8C.3C email retention cleanup.

---

## Phase 8C — Email outbox (transactional email)

Migration `000016_create_email_outbox` creates:

| Column | Type | Notes |
|--------|------|-------|
| id | UUID | PK |
| merchant_id | UUID | FK → merchants (ON DELETE CASCADE) |
| reference_id | UUID | **Conceptual reference to the invitation id — deliberately NOT a foreign key** (type-generic correlation column; see below) |
| type | VARCHAR(30) | `INVITATION` (CHECK) |
| recipient | VARCHAR(254) | **PII — delivery target** |
| subject | VARCHAR(998) | |
| text_body / html_body | TEXT | **SENSITIVE: rendered at enqueue time and contains the plaintext invitation bearer token by design; `token_hash` is NEVER stored here** |
| status | VARCHAR(20) | `PENDING` \| `PROCESSING` \| `SENT` \| `FAILED` \| `DEAD` (CHECK) |
| attempt_count | INT | Incremented at claim time (the claim IS the attempt) |
| next_attempt_at | TIMESTAMPTZ | Worker schedule |
| processing_at | TIMESTAMPTZ | Claim timestamp (stale recovery) |
| last_attempt_at / sent_at | TIMESTAMPTZ | Attempt / delivery timestamps |
| last_error | TEXT | Sanitized + length-bounded diagnostics (recipient redacted) |
| created_at / updated_at | TIMESTAMPTZ | |

Indexes: `idx_email_outbox_claim` (partial, `PENDING` due rows),
`idx_email_outbox_stale` (partial, `PROCESSING` recovery),
`idx_email_outbox_merchant_id`, and `idx_email_outbox_status` — the latter was
intentionally provisioned for **operational/retention sweeps** (Phase 8C.3C).

State machine — owned by two workers with **disjoint** state sets:

| Worker | States | Behavior |
|--------|--------|----------|
| `EmailOutboxWorker` (8C.3B) | `PENDING`, `PROCESSING` | `FOR UPDATE SKIP LOCKED` claim → send → `SENT` / retry `PENDING` (shared webhook backoff+jitter, max attempts) / `DEAD`; stale `PROCESSING` recovery. **At-least-once.** |
| `EmailOutboxCleanupWorker` (8C.3C) | `SENT`, `DEAD` only | Retention `DELETE`, full-row, bounded batches |

Retention policy (defaults, `EMAIL_OUTBOX_SENT_RETENTION` / `EMAIL_OUTBOX_DEAD_RETENTION`):

- `SENT` → deleted after **7 days** (strict `sent_at < now() - 168h`)
- `DEAD` → deleted after **30 days** (strict `updated_at < now() - 720h`)
- `PENDING` / `PROCESSING` → **never** deleted by retention, regardless of age
  (stale recovery stays exclusively with the delivery worker)
- Worker is opt-in (`EMAIL_CLEANUP_ENABLED`, default `false`); zero/negative
  retention, interval, or batch size is rejected at startup

**`email_outbox.reference_id` vs `merchant_user_invitations`:** the
invitation-scoped email stores the invitation id in `reference_id` purely as a
correlation value — there is intentionally **no database foreign key** (the
column is type-generic and the outbox must never constrain or cascade into the
invitation table). Consequently, deleting an `email_outbox` row during retention
cleanup removes delivery history only: invitation rows, `token_hash`, status,
and token expiration are untouched, and invitation validity is unaffected.
`merchant_user_invitations` rows are removed only by their own lifecycle
(`merchant_users`-driven flows / merchant cascade) — never by email cleanup.

**No API exposes `email_outbox`** (rows contain the plaintext bearer token);
access is worker/repository only, and cleanup logs carry counts/duration only —
never recipient, subject, body, or token material.
