# GO-SAAS Phase 4B-0 — Public Secret Exposure Remediation

**Date:** 2026-10-20  
**Repository:** `DhikDhik15/payment-gateway-service-backend` (public)  
**Branch / HEAD:** `main` / `fd49157`  
**Incident status:** **OPEN — local containment applied; credential rotation and Git history cleanup are still required.**

**Handling note:** This report records only key names, metadata, and remediation steps. It contains no secret values, no hashes of secret values, and no excerpts from environment files. No remote history was rewritten, no credentials were rotated, and nothing was committed or pushed in this phase.

---

## 1. Executive summary

The backend repository is public, and the tracked file `.env.stage` contains populated values for six secret categories: database credentials, a staging database password, the dashboard JWT signing secret, the admin API key, the webhook encryption key, and the mock webhook secret.

Because the repository is public, these values must be treated as compromised. Removing the file from the current tree does not remove the values from Git history, GitHub caches, existing clones, or any external copies. **Credential rotation is the primary containment action and has not been performed.**

Safe local containment was completed in this phase:

- `.env.stage` was removed from the Git index with `git rm --cached`. The local file was preserved and its permissions were restricted to `600`.
- `.gitignore` now ignores `.env.*`, with explicit exceptions for `.env.example` and the new `.env.stage.example`.
- `.env.stage.example` was added as a safe template containing placeholders only.
- No application source, dependency manifest, lockfile, CI/CD configuration, staging configuration, or production configuration was changed.
- No commit, push, force-push, history rewrite, credential rotation, or deployment was performed.

The incident remains open until all six categories are rotated, dependent data is migrated where required, and the exposed path is purged from reachable history under a separately approved history-cleanup operation.

---

## 2. Incident scope

### 2.1 Repository facts

| Item | Finding |
|---|---|
| Repository | `DhikDhik15/payment-gateway-service-backend` |
| Visibility | Public |
| Default / only remote branch | `main` |
| Local HEAD | `fd49157` |
| `origin/main` at time of audit | `fd49157` |
| Remote tags | None |
| Forks visible at time of audit | None |
| Affected path | `.env.stage` |
| Commits touching the path | `cd703bf` (introduced), `fd49157` (modified) |
| File tracked at HEAD | Yes, before local index removal |
| Local file after containment | Present, ignored, mode `600`, content not edited |

### 2.2 Exposure window

The file was introduced on 2026-10-16 and modified on 2026-10-19. It remained tracked in the public repository until local index removal on 2026-10-20. Because the repository is public, it is not possible to determine who cloned, fetched, indexed, or cached the file during that window.

### 2.3 Potential blast radius

The exposed values could affect:

- The PostgreSQL database used by the backend and migration job.
- The staging PostgreSQL container configured by `docker-compose.stage.yml`.
- Dashboard authentication and access-token issuance.
- Ops-only admin endpoints protected by `X-Admin-Key`.
- Encryption at rest for merchant webhook signing secrets.
- Mock payment/webhook flows used by staging and the local E2E harness.

No evidence of actual misuse was established in this phase. Absence of evidence is not evidence of safety; the values were public and must be assumed compromised.

### 2.4 Out of scope / not asserted

- No production system was accessed.
- No production credentials were confirmed to be the same as the exposed values.
- No third-party provider was contacted.
- No secret values were printed or copied into this report.
- No claim is made that the values were or were not used by an attacker.
- This phase does not implement CI/CD, secret-scanning infrastructure, or a key-versioning code change.

---

## 3. Affected secret categories (no values)

| Category | Configuration key | Populated? | Likely consumers | Priority | Main dependency / impact |
|---|---|---:|---|---|---|
| Database password | `DB_PASSWORD` | Yes | Backend DB pool, migration job, `docker-compose.yml`, local DB clients, any external client using the same role | **P0** | Changing it requires coordinating the database role, backend replicas, and migration job. Active connections fail until updated. |
| Staging database password | `STAGING_DB_PASSWORD` | Yes | PostgreSQL container in `docker-compose.stage.yml` | **P0/P1** | Rotate if the staging database is reachable outside the local machine. Container recreation/restart is required. Local volume data remains. |
| Dashboard JWT signing secret | `AUTH_JWT_SECRET` | Yes | `internal/config/config.go`, `internal/service/auth_service.go`; dashboard access-token verification | **P0** | Rotation invalidates existing access tokens. Opaque refresh tokens remain valid unless dashboard sessions are revoked separately. |
| Admin API key | `ADMIN_API_KEY` | Yes | `internal/middleware/admin_auth.go`, merchant onboarding/lifecycle/settlement handlers, ops clients using `X-Admin-Key` | **P0** | Ops clients receive `401` until updated. The key can create or modify merchant and settlement state; review audit logs. |
| Webhook encryption key | `WEBHOOK_SECRET_ENCRYPTION_KEY` | Yes | `internal/config/config.go`, `internal/service/webhook_crypto.go`, merchant webhook create/rotate service, webhook dispatcher; `merchant_webhook_configs.encrypted_secret` | **P0** | Existing encrypted webhook secrets become unreadable if the key is replaced without re-encryption. A data migration is required. |
| Mock webhook secret | `MOCK_WEBHOOK_SECRET` | Yes | Mock webhook parser, simulator handler, dashboard payment handler, local/staging mock flows | **P1** | Old mock signatures stop verifying. No persistent encrypted data. Rotate despite lower production impact. |

Other secret-like keys inspected:

- `MIDTRANS_SERVER_KEY` — present but empty.
- `MIDTRANS_CLIENT_KEY` — present but empty.
- `SMTP_PASSWORD` — present but empty.
- `DEV_RECOVERY_KEY` — present but empty.
- `AUTH_ACCESS_TOKEN_TTL`, `AUTH_REFRESH_TOKEN_TTL`, `INVITATION_TOKEN_TTL`, `PASSWORD_RESET_TOKEN_TTL` — present but are durations, not secret material.
- `LEGACY_API_CREDENTIALS_ENABLED` — present but is a feature flag, not secret material.
- `DB_USER` / `STAGING_DB_USER` — identity fields, not passwords.

Priority meaning used here: **P0** means rotate immediately and treat as compromised; **P1** means rotate promptly but with lower direct data impact. A staging value is not assumed harmless.

---

## 4. Rotation checklist

Do not rotate automatically. Perform these actions through the approved database, secret-manager, and deployment procedures. Do not place new values in Git, chat, tickets, or this report.

### 4.1 Universal preconditions

- [ ] Incident owner and change window approved.
- [ ] Current database and webhook-config backups/snapshots available.
- [ ] New values generated with an approved cryptographic generator.
- [ ] New values stored in the approved secret manager or deployment secret store.
- [ ] All backend replicas, migration jobs, staging containers, and ops clients identified.
- [ ] Logging and command output reviewed so new values are not printed.
- [ ] Rollback owner and rollback window agreed.
- [ ] Old values retained only in a secure escrow location until validation completes.
- [ ] No real payment provider or real money flow used for validation.

### 4.2 Database credentials (`DB_PASSWORD`, `STAGING_DB_PASSWORD`)

**Required administrative action**

- [ ] Determine whether the two keys refer to the same role/database or different ones.
- [ ] Create a new password for each affected database role using the database administrative procedure.
- [ ] Update the secret manager and deployment environment.
- [ ] Update the local `.env.stage` with the new value; never commit it.
- [ ] Restart the backend, migration job, and staging PostgreSQL container as applicable.
- [ ] Revoke the old password after the new connection path is validated.
- [ ] Review database roles, grants, active sessions, and recent administrative activity for signs of misuse.

**Expected impact**

- Existing database connections are terminated or fail authentication.
- Migrations and backend startup fail until the new value is available.
- The staging Compose container may need recreation; its data volume is not inherently deleted.

**Dependent services / clients**

- Backend DB pool and readiness checks.
- Migration job in `docker-compose.yml`.
- PostgreSQL service in `docker-compose.stage.yml`.
- Any local scripts, BI tools, or external clients using the exposed role.

**Safe validation**

- [ ] Backend starts and reports ready.
- [ ] A non-destructive `SELECT 1` succeeds through the application path.
- [ ] Migration status is current.
- [ ] Dashboard login and a read-only API request succeed.
- [ ] Old password no longer authenticates.
- [ ] No new authentication failures beyond the expected restart window.

**Rollback / recovery**

- Prefer forward repair. If rollback is unavoidable, temporarily re-enable the old password only within a tightly controlled window, then rotate again.
- If unauthorized database changes are suspected, restore from a known-good backup and investigate before resuming.
- Keep the old password in escrow, not in the repository, until the change window closes.

### 4.3 Dashboard JWT signing secret (`AUTH_JWT_SECRET`)

**Required administrative action**

- [ ] Generate a new random signing secret of at least 32 bytes.
- [ ] Update the secret manager and all backend replicas.
- [ ] Restart or roll all backend instances so no instance verifies with the old secret.
- [ ] Revoke existing dashboard sessions by deleting/expiring `dashboard_sessions` rows, or introduce an approved session-epoch/version mechanism.
- [ ] Remove the old secret from active configuration after validation.

**Expected impact**

- All existing access tokens become invalid immediately.
- Dashboard users must sign in again.
- **Important:** refresh tokens are opaque random values stored as SHA-256 hashes in `dashboard_sessions`; they are not signed with `AUTH_JWT_SECRET`. Rotating the JWT secret alone does not revoke them. If the secret was exposed, revoke sessions explicitly so an attacker cannot use a stolen refresh token to mint new access tokens.
- Merchant API keys are a separate credential type and are not rotated by this action.

**Dependent services / clients**

- Dashboard frontend and any API client using dashboard JWTs.
- Session repository and authentication tests.
- Operational tooling that assumes long-lived access tokens.

**Safe validation**

- [ ] A token issued before rotation is rejected.
- [ ] A new login succeeds and issues a working access token.
- [ ] Old refresh tokens fail after session revocation.
- [ ] New refresh tokens work for new sessions.
- [ ] No unexpected authentication failures across replicas.

**Rollback / recovery**

- Do not restore the exposed secret as a permanent fix; that would re-enable forged tokens.
- If a rolling deployment requires temporary dual verification, implement an approved old-plus-new verification window, then remove the old key.
- If users are locked out, issue new sessions rather than reverting to the compromised secret.

### 4.4 Admin API key (`ADMIN_API_KEY`)

**Required administrative action**

- [ ] Generate a new high-entropy admin key of at least 32 bytes.
- [ ] Update the secret manager and backend deployment.
- [ ] Update every authorized ops client, script, and runbook that sends `X-Admin-Key`.
- [ ] Restart the backend.
- [ ] Verify the old key is rejected, then retire it.
- [ ] Review admin audit logs for merchant creation, lifecycle changes, webhook URL changes, and settlement mutations.

**Expected impact**

- Admin, onboarding, lifecycle, and settlement endpoints return `401` until clients are updated.
- Setting the key empty disables admin routes; this is not a recommended substitute for rotation.
- No database schema change is required.

**Dependent services / clients**

- Ops scripts and administrative tooling.
- Merchant onboarding/lifecycle and settlement operations.
- Any documentation that references the old key location, but not the value.

**Safe validation**

- [ ] A non-destructive authorized admin request succeeds with the new key.
- [ ] The same request fails with the old key.
- [ ] Unauthorized requests still fail.
- [ ] Audit logs show only expected operators and actions.

**Rollback / recovery**

- If the new deployment is broken, fix the client or deployment rather than restoring a publicly exposed key.
- If the old key must be temporarily re-enabled, treat it as an emergency exception and rotate again immediately after recovery.

### 4.5 Webhook encryption key (`WEBHOOK_SECRET_ENCRYPTION_KEY`)

**Required administrative action**

- [ ] Generate a new 32-byte key in the required hex or base64 representation.
- [ ] Determine whether `merchant_webhook_configs` contains encrypted rows that must remain readable.
- [ ] Take a database backup/snapshot before any re-encryption.
- [ ] Keep the old key in secure escrow for the migration window.
- [ ] Use an approved one-time migration/re-encryption procedure, or deploy key-version support, to decrypt each `encrypted_secret` with the old key and re-encrypt it with the new key.
- [ ] Update the backend configuration to the new key only after the migration is ready.
- [ ] Verify all rows decrypt with the new key and that webhook dispatch still signs correctly.
- [ ] Remove the old key after successful validation and backup retention.

**Expected impact**

- Direct replacement of the key without re-encryption makes existing encrypted webhook secrets unreadable (`ErrDecryptFailed`); webhook dispatch will fail to sign payloads.
- Newly created or rotated webhook secrets use the new key; old ciphertext remains tied to the old key.
- The ciphertext is not exposed through JSON APIs, but it is present in the database.

**Dependent services / clients**

- `merchant_webhook_service.go` (create/rotate webhook secret).
- `merchant_webhook_dispatcher.go` (decrypt before signing deliveries).
- `merchant_webhook_configs.encrypted_secret` and its repository.
- Dashboard webhook configuration flows.
- Any test or local fixture that exercises encrypted webhook secrets.

**Key migration requirement**

The current implementation has no key identifier or version prefix in the ciphertext. `EncryptWebhookSecret` uses AES-256-GCM with a random nonce and returns base64(nonce || ciphertext || tag). `DecryptWebhookSecret` accepts a single key. Therefore:

- Existing encrypted data **must remain readable** until it is re-encrypted.
- A safe rotation is either:
  1. A one-time, backed-up, batched re-encryption using old and new keys in a controlled tool; or
  2. A code change adding key versioning and support for a previous key during a migration window.
- Do not simply replace the environment value and assume old rows will continue to work.

**Combined database + encryption-key exposure**

`DB_PASSWORD` and `WEBHOOK_SECRET_ENCRYPTION_KEY` were both populated in the same public file. An attacker who used the database credential to read `merchant_webhook_configs` and obtained the encryption key could decrypt stored merchant webhook signing secrets. Re-encrypting those rows with a new key protects against future reads, but it does not make the already-exposed plaintext secret again. After the key migration, rotate or reissue affected merchant webhook signing secrets through the existing webhook rotation flow and review webhook configuration changes for the exposure window. This is a data-confidentiality issue, not only a key-storage issue.

**Safe validation**

- [ ] Count `merchant_webhook_configs` rows before and after; counts match.
- [ ] Every row decrypts with the new key, or a documented exception is resolved.
- [ ] A newly created/rotated webhook secret round-trips encrypt/decrypt.
- [ ] A controlled mock webhook delivery reaches a local/mock endpoint and verifies its signature.
- [ ] No dispatcher decryption errors appear.
- [ ] The old key is no longer required for normal operation.

**Rollback / recovery**

- Keep the pre-migration database backup and old key in escrow until validation is complete.
- If re-encryption is partial, restore the backup or rerun an idempotent migration; do not delete the old key.
- If a row cannot be decrypted, stop and investigate before discarding either key.
- After successful conversion and retention expiry, destroy the old key through the approved secret-destruction process.

### 4.6 Mock webhook secret (`MOCK_WEBHOOK_SECRET`)

**Required administrative action**

- [ ] Generate a new random secret of at least 32 bytes.
- [ ] Update the local/staging secret store and the test harness.
- [ ] Restart the backend and mock/simulator flows.
- [ ] Update any mock client that signs webhook payloads.
- [ ] Retire the old value.

**Expected impact**

- Old mock webhook signatures fail verification.
- Mock/simulator tests and local E2E flows may fail until updated.
- No persistent encrypted data and no real payment provider impact.

**Dependent services / clients**

- `internal/handler/simulator_handler.go`.
- `internal/handler/dashboard_payment_handler.go`.
- `service.NewMockWebhookParser` wiring in `cmd/server/main.go`.
- Local/staging mock webhook clients and tests.

**Safe validation**

- [ ] A payload signed with the new secret verifies.
- [ ] A payload signed with the old secret is rejected.
- [ ] The local mock-webhook smoke test passes without contacting a real provider.
- [ ] No mock delivery is sent to a real external endpoint.

**Rollback / recovery**

- Restore the old value only for a short recovery window, then rotate again. The old value was public and should not remain active.

### 4.7 Recommended rotation order

1. Database credentials — restores control of the data store needed for later migration.
2. JWT secret plus dashboard-session revocation.
3. Admin API key.
4. Webhook encryption key plus re-encryption of existing ciphertext.
5. Mock webhook secret.

The exact order may be adjusted for operational dependencies, but do not rotate the webhook encryption key before database access and a backup are available.

---

## 5. Dependency and recovery considerations

### 5.1 Dependency map

| Change | Direct dependencies | Data at risk | Recovery concern |
|---|---|---|---|
| `DB_PASSWORD` | Backend, migration job, DB role, external clients | Database availability, not data deletion | Old password must remain available only as a controlled fallback |
| `STAGING_DB_PASSWORD` | Staging Compose PostgreSQL container | Staging data volume | Container recreation must not delete the volume |
| `AUTH_JWT_SECRET` | Dashboard clients, auth service, all replicas | Session continuity | Secret rotation alone does not revoke opaque refresh tokens |
| `ADMIN_API_KEY` | Ops clients, admin endpoints | Merchant/settlement state if misused | Old key must be retired; audit logs matter more than rollback |
| `WEBHOOK_SECRET_ENCRYPTION_KEY` | Webhook service, dispatcher, `encrypted_secret` rows | Webhook signing secrets | Existing ciphertext must be re-encrypted or preserved with old key |
| `MOCK_WEBHOOK_SECRET` | Simulator, mock parser, E2E harness | None persistent | Old signatures stop working; update harness |

### 5.2 Cross-cutting recovery rules

- Keep old values only in an approved secret escrow, never in Git or this report.
- Take a database backup before webhook-key migration.
- Validate with mock/local endpoints; do not use real payment providers or real money.
- Monitor for authentication failures, database authentication errors, webhook decryption/dispatch failures, and unexpected admin activity.
- Review access and audit logs for the exposed period:
  - Database logins and role changes.
  - Admin endpoint calls using `X-Admin-Key`.
  - Merchant onboarding, lifecycle changes, webhook configuration changes, and settlement mutations.
  - Dashboard session creation and refresh anomalies.
- If unauthorized activity is found, escalate to the incident owner and consider restoring from a known-good backup. This phase did not access production logs.

### 5.3 Specific caution: JWT and refresh sessions

The dashboard refresh token is an opaque random value whose SHA-256 hash is stored in `dashboard_sessions`. It is not a JWT signed by `AUTH_JWT_SECRET`. Rotating the JWT secret therefore:

- Invalidates existing access tokens.
- Does **not** by itself invalidate existing refresh tokens.

To fully contain a leaked JWT signing secret, revoke or expire dashboard sessions, or add an approved session-epoch check. Until then, a stolen refresh token could still mint a new access token under the new signing secret. This is a material remaining risk.

### 5.4 Specific caution: webhook ciphertext

`merchant_webhook_configs.encrypted_secret` is encrypted with the single configured key. The code has no key version field. Replacing the key without re-encryption will make existing webhook secrets unreadable and break deliveries. The migration is a data operation, not just a configuration change; it requires backup, validation, and a retention plan for the old key.

Because the database password and the webhook encryption key were exposed together, assume the confidentiality of existing merchant webhook signing secrets is compromised. The key migration protects future ciphertext, but the old plaintext secrets should be treated as leaked and rotated or reissued through the application's webhook rotation flow. Review webhook URL and configuration changes for the exposure window as well.

### 5.5 Specific caution: admin key blast radius

The admin key protects merchant onboarding/lifecycle and settlement operations. Exposure is not limited to read access. Review whether any unexpected merchants, webhook URLs, lifecycle transitions, or settlement actions occurred during the exposure window. Do not assume the key was only used by the intended operator.

---

## 6. Local repository remediation plan (performed)

### 6.1 Ignore rules

`.gitignore` was inspected before editing. The environment section now contains:

```gitignore
# ── Environment & secrets ─────────────────────────────────────────────────────
.env
.env.*
!.env.example
!.env.stage.example
.env.elk
*.local
```

Effect:

- `.env.stage` is ignored and can no longer be re-added by a normal `git add`.
- `.env.example` remains available as the existing tracked template.
- `.env.stage.example` is explicitly allowed as the new safe template.
- The existing `.env.elk` and `*.local` protections remain in place.
- A forced add (`git add -f`) can still bypass ignore rules; review and secret scanning remain necessary.

### 6.2 Stop tracking `.env.stage` without deleting local configuration

Executed locally:

```text
git rm --cached -- .env.stage
```

Result:

- The file is staged for deletion from the index.
- The local `.env.stage` file remains on disk and was not edited.
- The local file is now ignored by the new rule.
- The local file mode was restricted from `664` to `600` (`dhika:dhika`), reducing local read exposure.
- No commit was created; the deletion is staged only.

This removes the file from the next commit's tree, but **the file still exists in existing commits and on the public remote**. It must not be treated as purged until history cleanup is completed.

### 6.3 Safe template

Created: `.env.stage.example`.

- Contains comments explaining that it must be copied to `.env.stage`, populated from an approved secret source, and never committed.
- Secret-bearing fields use `<set-me>` placeholders, including:
  - `DB_USER`
  - `DB_PASSWORD`
  - `STAGING_DB_USER`
  - `STAGING_DB_PASSWORD`
  - `AUTH_JWT_SECRET`
  - `ADMIN_API_KEY`
  - `MOCK_WEBHOOK_SECRET`
  - `WEBHOOK_SECRET_ENCRYPTION_KEY`
  - `TRUSTED_PROXIES`
- Non-secret values are safe local examples only; they are not production values.
- Security controls are shown in the enabled/safe direction: `RATE_LIMIT_ENABLED=true`, `WEBHOOK_REQUIRE_HTTPS=true`, and payment expiry enabled.
- Provider and SMTP secrets are empty by default.
- The template was checked for accidental inclusion of the six `.env.stage` secret values; none were found.
- The template parses as valid key/value lines with no malformed non-comment entries.

The existing `.env.example` was inspected and left unchanged. It was not overwritten.

### 6.4 Preserve existing user work

The pre-existing modified source/test files and untracked Phase 2/3/4A documents were left in place. No application source file was modified. The only local changes are the staged index removal, `.gitignore`, the new template, and this report.

### 6.5 Future prevention

Recommended follow-up, not implemented in this phase:

- Enable GitHub secret scanning and push protection for the repository.
- Add a pre-commit secret scanner such as gitleaks.
- Add a CI secret-scanning job that scans both the working tree and history.
- Require review for changes to `.gitignore` exceptions and environment templates.
- Document that `git add -f .env*` is prohibited without an incident-owner exception.
- Keep populated environment files outside the repository when practical, or at minimum with `600` permissions and an explicit ignore rule.

### 6.6 Operational caution

Do not run any of the following until the exposed values have been rotated and the incident owner approves safe handling:

```text
git diff --cached -- .env.stage
git show HEAD:.env.stage
git log -p -- .env.stage
```

Those commands can print the old secret values. Use path-level status/stat output instead. This phase deliberately avoided them.

---

## 7. Git history cleanup plan

### 7.1 Affected history

- Path: `.env.stage`
- Introducing commit: `cd703bf`
- Modifying commit: `fd49157`
- Reachable branches at audit time: `main` only.
- Remote tags: none.
- Visible forks at audit time: none.
- `origin/main`: `fd49157`.

Because the file existed in two commits on the only branch, a complete purge requires rewriting those commits and all descendants. This changes commit SHAs.

### 7.2 Preconditions — do not start cleanup until all are true

- [ ] All six secret categories have been rotated or retired.
- [ ] Webhook ciphertext migration is complete or has a tested rollback.
- [ ] Incident owner has approved history rewriting.
- [ ] Collaborators have been notified to stop pushing and to re-clone afterward.
- [ ] A full mirror backup exists outside the working repository.
- [ ] The force-push has a separate, explicit human approval.
- [ ] The team accepts that forks, external clones, caches, and search indexes may retain copies.

### 7.3 Procedure (plan only — not executed)

1. **Back up the repository**
   - Create a mirror clone and store it in an access-controlled location.
   - Record the current `main` SHA and the list of branches/tags.
   - Do not store the backup in a public location.

2. **Rewrite only the affected path**
   - Work on a fresh mirror or disposable clone.
   - Use `git filter-repo --path .env.stage --invert-paths` (preferred) or an equivalent reviewed tool.
   - Do not use an interactive rebase that risks unrelated history.
   - Preserve unrelated commits, authorship, branches, and tags where possible.

3. **Verify locally before any push**
   - `git log --all --oneline -- .env.stage` returns no commits.
   - `git rev-list --objects --all | grep -F '.env.stage'` returns no path.
   - `git cat-file -e <ref>:.env.stage` fails for every branch/tag/ref.
   - The working tree contains no tracked `.env.stage`.
   - The local ignored `.env.stage` is kept outside the rewritten repository or is clearly excluded.
   - A fresh clone of the rewritten mirror also lacks the path.

4. **Force-push under separate approval**
   - This is the destructive remote operation and is **not authorized by this phase**.
   - Use `git push --force-with-lease` rather than an unconditional force-push.
   - Notify collaborators before and after; old clones still contain the secrets and must be replaced.
   - If branch protection or required status checks block the push, resolve through the repository owner rather than disabling protections silently.

5. **Post-push verification**
   - Confirm the public branch history no longer lists `.env.stage`.
   - Confirm raw URLs for the old path return not found where testable.
   - Confirm CI runs against the new commit SHAs and required checks still work.
   - Re-scan a fresh clone with a secret scanner.
   - Ask GitHub Support about cached views or stale references if the repository remains publicly accessible and the path is still retrievable.

6. **Handle collaborators and copies**
   - Existing clones contain the old commits and therefore the secrets.
   - Rotate first; then require collaborators to re-clone or run an approved history-cleaning procedure on their clones.
   - Forks and external copies may persist even after a successful rewrite. No history rewrite can guarantee deletion from third-party copies.

### 7.4 Why this is not being executed now

- The exposed credentials have not been rotated.
- History rewriting changes commit SHAs and requires coordinated force-push.
- Force-push is a destructive remote operation and requires separate human approval.
- The user explicitly prohibited remote history rewriting in this phase.

---

## 8. Verification checklist

### 8.1 Completed local verification

- [x] `git status` inspected before any changes.
- [x] Existing modified and untracked user files were preserved.
- [x] `.env.stage` removed from the Git index with `git rm --cached`.
- [x] Local `.env.stage` still exists after index removal.
- [x] Local `.env.stage` content was not edited; byte and line counts match the previous HEAD version.
- [x] Local `.env.stage` permissions restricted to `600`.
- [x] `.env.stage` matches the new ignore rule.
- [x] `.env.example` remains tracked and is not ignored.
- [x] `.env.stage.example` exists, is not ignored, and parses as valid key/value lines.
- [x] The template contains placeholders and no `.env.stage` secret value was found in it.
- [x] No application source, dependency manifest, lockfile, CI/CD configuration, or deployment configuration was changed.
- [x] HEAD remains `fd49157`; no commit was created.
- [x] `origin/main` remains `fd49157`; no push or force-push occurred.
- [x] No remote history was rewritten.
- [x] No credential was rotated, revoked, or deployed in this phase.

### 8.2 Pending human verification

- [ ] All six secret categories rotated through their authoritative systems.
- [ ] Old credentials revoked or retired after validation.
- [ ] Dashboard sessions revoked or session epoch deployed.
- [ ] Webhook ciphertext re-encrypted and validated.
- [ ] Access/audit logs reviewed for unauthorized use.
- [ ] New values stored only in the approved secret store and local ignored file.
- [ ] Local remediation reviewed and committed by a human.
- [ ] History rewritten on a backup/mirror and verified locally.
- [ ] Force-push explicitly approved and performed.
- [ ] Collaborators re-cloned; old clones handled.
- [ ] GitHub secret scanning and push protection enabled.
- [ ] Fresh-clone secret scan passes.

---

## 9. Human actions required

1. **Declare and own the incident.** Assign an incident owner, approve the rotation window, and record that all six values are treated as compromised.
2. **Rotate database credentials.** Coordinate the DB role, backend, migration job, staging Compose service, and any external clients. Validate and retire the old password.
3. **Rotate the JWT signing secret and revoke sessions.** Update all replicas, invalidate access tokens, and explicitly revoke or expire `dashboard_sessions` so opaque refresh tokens cannot mint new tokens under the new secret.
4. **Rotate the admin API key.** Update all authorized ops clients and review admin audit logs for unauthorized merchant, lifecycle, webhook, or settlement changes.
5. **Rotate the webhook encryption key with a migration.** Back up the database, keep the old key in escrow, re-encrypt `merchant_webhook_configs.encrypted_secret`, validate all rows, then retire the old key. Do not replace the key without re-encryption. Because the DB password and encryption key were both exposed, also rotate or reissue affected merchant webhook signing secrets through the application's webhook rotation flow.
6. **Rotate the mock webhook secret.** Update local/staging harnesses and verify old signatures are rejected. Do not contact real providers.
7. **Update local configuration.** Put new values into the local ignored `.env.stage`; never commit it. Keep the file at mode `600`.
8. **Review the local remediation.** Inspect `git status` and the staged changes, then have a human commit the removal of `.env.stage` and the safe template when ready. This phase did not commit.
9. **Plan history cleanup.** After rotation, follow the mirror/backup/rewrite/verify procedure in section 7. The force-push must be a separate, explicit human approval.
10. **Notify collaborators and re-clone.** Existing clones retain the old secrets even after a remote rewrite.
11. **Enable prevention controls.** Turn on GitHub secret scanning and push protection; add a pre-commit scanner and a CI history scan.
12. **Monitor and investigate.** Review database, admin, dashboard, and webhook logs for the exposure window. Escalate any evidence of unauthorized activity.

**Exact action needed to proceed with history cleanup:** obtain incident-owner approval for a force-push after all credentials are rotated, then perform the mirror backup, `git filter-repo` rewrite, local verification, and `--force-with-lease` push as a separate operation. Do not proceed if any rotation or webhook re-encryption is incomplete.

---

## 10. Remaining risks

1. **The exposed values remain public in existing commits.** Local index removal does not remove them from GitHub history. Rotation is the only effective containment.
2. **The exposure window is uncontrolled.** The repository is public; the values may have been cloned, indexed, cached, or copied. No history rewrite can guarantee deletion from external copies.
3. **No forks were visible at audit time, but external clones may exist.** Forks can appear later; cached views and third-party mirrors may persist.
4. **The local `.env.stage` still contains old values until the human rotates them.** Mode `600` reduces local read exposure but does not address public exposure.
5. **JWT rotation alone does not revoke opaque refresh tokens.** Without session revocation or an epoch/version mechanism, a stolen refresh token may still mint new access tokens.
6. **Webhook key rotation without re-encryption breaks existing merchant webhook secrets.** The current format has no key version, so a one-time migration or code change is required.
7. **Existing merchant webhook signing secrets may already be decrypted.** The database password and webhook encryption key were exposed in the same file. Re-encrypting with a new key does not retroactively protect the old plaintext; affected webhook secrets should be rotated or reissued.
8. **Unauthorized admin or database activity may already have occurred.** No production access or log review was performed in this phase. Merchant, settlement, and webhook changes must be audited.
9. **History rewriting changes commit SHAs.** CI, open work, collaborator clones, and any external references may need coordination.
10. **Secret scanning is not yet enforced.** A future `git add -f` or a new environment file could reintroduce exposure.
11. **CI/CD and deployment work remains blocked.** This incident remediation does not resolve the separate Phase 4A blockers (public `.env.stage`, clean-checkout backend failure, and Compose DB credential failure).

---

## 11. Files changed

| Path | Change | Notes |
|---|---|---|
| `.env.stage` | Removed from Git index (staged deletion) | Local file preserved, content not edited, mode `600`. Not committed. |
| `.gitignore` | Updated | Added `.env.*`, with exceptions for `.env.example` and `.env.stage.example`. |
| `.env.stage.example` | Added | Safe template with placeholders only; no secret values. |
| `docs/phase-4b0-secret-incident-remediation.md` | Added | This report. |

No application source, dependency manifest, lockfile, CI/CD workflow, staging configuration, or production configuration was changed. Pre-existing user modifications and untracked Phase 2/3/4A documents were left intact.

Current local staged state (paths only):

```text
D  .env.stage
A  .env.stage.example
M  .gitignore
```

No commit was created. `HEAD` and `origin/main` remain at `fd49157`.

---

## 12. Confirmation

- **No secret values, hashes of secret values, or environment-file excerpts are included in this report.**
- **No remote history was rewritten.**
- **No force-push was performed.**
- **No commit or push was performed.**
- **No credential was rotated, revoked, or deployed.**
- **No production system was accessed.**
- **No real payment provider, real money, or real external webhook was used.**
- **No security control was disabled.**
- **Local repository containment was completed only as described above.**

The incident is not closed. Rotation, session revocation, webhook re-encryption, log review, and approved history cleanup remain human actions.
