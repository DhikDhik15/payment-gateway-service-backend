# Phase 11.2A disposable verification

This is a PostgreSQL-only Compose environment. It never references the normal
application Compose project, its database, or its credentials.

Generate temporary values in the current shell (do not commit them), then use
the same shell for the commands below:

```sh
export PHASE11_DB_USER=phase11_verify
export PHASE11_DB_PASSWORD="$(openssl rand -hex 24)"
export PHASE11_DB_NAME=phase11_verify
export PHASE11_DB_PORT=55433
export PHASE11_ADMIN_KEY="$(openssl rand -hex 24)"
docker compose -f docker-compose.phase-11-verify.yml up -d
```

After Compose reports healthy, apply the complete migration chain with the
same `migrate/migrate:v4.18.1` image used by the repository Compose runner:

```sh
docker run --rm --network paygate_phase11_verify_default \
  -v "$PWD/migrations:/migrations:ro" migrate/migrate:v4.18.1 \
  -path=/migrations \
  -database "postgres://${PHASE11_DB_USER}:${PHASE11_DB_PASSWORD}@postgres:5432/${PHASE11_DB_NAME}?sslmode=disable" up
```

Start a separate server on port 18080. The mock provider's PaymentURL points
at the separately served React frontend, while this process remains the API
origin used by `VITE_API_BASE_URL`:

```sh
APP_ENV=test APP_PORT=18080 DB_HOST=127.0.0.1 DB_PORT="$PHASE11_DB_PORT" \
DB_USER="$PHASE11_DB_USER" DB_PASSWORD="$PHASE11_DB_PASSWORD" DB_NAME="$PHASE11_DB_NAME" \
DB_SSLMODE=disable PAYMENT_PROVIDER=mock FRONTEND_PUBLIC_URL=http://localhost:5173 \
ADMIN_API_KEY="$PHASE11_ADMIN_KEY" PAYMENT_SIMULATOR_ENABLED=false PAYMENT_EXPIRY_INTERVAL=1s \
WEBHOOK_DELIVERY_ENABLED=false EMAIL_ENABLED=false go run ./cmd/server
```

Provision an isolated merchant with `POST /api/v1/admin/onboarding/merchants`
and use its one-time `key_id:secret` response as `X-API-Key`. The normal
onboarding endpoint preserves the production authentication model.

Destroy all verification state after the run:

```sh
docker compose -f docker-compose.phase-11-verify.yml down -v
unset TEST_DATABASE_URL PHASE11_DB_USER PHASE11_DB_PASSWORD PHASE11_DB_NAME PHASE11_DB_PORT PHASE11_ADMIN_KEY
```
