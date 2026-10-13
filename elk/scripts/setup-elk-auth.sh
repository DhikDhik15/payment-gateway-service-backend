#!/usr/bin/env bash
# Bootstrap the Elasticsearch `elastic` password for the ELK stack (dev only).
#
# - Starts Elasticsearch if it is not running (existing data volume preserved).
# - Reuses the password in .env.elk when it still authenticates (idempotent).
# - Otherwise resets the `elastic` password via elasticsearch-reset-password
#   and stores it in .env.elk (mode 600, gitignored).
# - The password is NEVER printed to stdout/stderr.
#
# Compose services (Kibana, Logstash) consume it via env_file: .env.elk.
# The CA private key is never mounted into any container.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env.elk"
ELK_COMPOSE_FILE="${REPO_ROOT}/docker-compose.elk.yml"
CA_CERT="${REPO_ROOT}/elk/certs/ca/ca.crt"
ES_URL="https://localhost:9200"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-180}"
SLEEP_SECONDS=5

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is not available in PATH" >&2
  exit 1
fi
if [[ ! -f "${CA_CERT}" ]]; then
  echo "ERROR: CA certificate not found at ${CA_CERT}; run 'make elk-certs' first" >&2
  exit 1
fi

echo "[elk-auth] Ensuring Elasticsearch is running (data volume preserved)..."
docker compose -f "${ELK_COMPOSE_FILE}" up -d elasticsearch >/dev/null

echo "[elk-auth] Waiting for Elasticsearch HTTPS (max ${MAX_WAIT_SECONDS}s)..."
deadline=$(( $(date +%s) + MAX_WAIT_SECONDS ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  if curl -fsS --cacert "${CA_CERT}" "${ES_URL}" >/dev/null 2>&1 \
    || curl -fsS --cacert "${CA_CERT}" -o /dev/null -w '%{http_code}' "${ES_URL}" 2>/dev/null | grep -q '401'; then
    break
  fi
  # Treat 401 (auth required) as "up": curl -f fails on 401, so probe the code.
  code="$(curl -s -o /dev/null -w '%{http_code}' --cacert "${CA_CERT}" "${ES_URL}" 2>/dev/null || true)"
  if [ "${code}" = "200" ] || [ "${code}" = "401" ]; then
    break
  fi
  sleep "${SLEEP_SECONDS}"
done

code="$(curl -s -o /dev/null -w '%{http_code}' --cacert "${CA_CERT}" "${ES_URL}" 2>/dev/null || true)"
if [ "${code}" != "200" ] && [ "${code}" != "401" ]; then
  echo "ERROR: Elasticsearch at ${ES_URL} is not reachable (last HTTP code: ${code})" >&2
  exit 1
fi

# Reuse the stored password when it still authenticates (idempotent).
need_reset=1
if [[ -f "${ENV_FILE}" ]]; then
  # shellcheck disable=SC1090
  stored_pass="$(set -a; . "${ENV_FILE}" 2>/dev/null; set +a; printf '%s' "${ELASTIC_PASSWORD:-}")"
  if [[ -n "${stored_pass:-}" ]] && \
     curl -fsS --cacert "${CA_CERT}" -u "elastic:${stored_pass}" "${ES_URL}" >/dev/null 2>&1; then
    echo "[elk-auth] Stored credentials in .env.elk are still valid."
    need_reset=0
  else
    echo "[elk-auth] Stored credentials missing or stale; resetting..."
  fi
  unset stored_pass
fi

if [[ "${need_reset}" == "1" ]]; then
  echo "[elk-auth] Resetting 'elastic' password (new value goes only into .env.elk)..."
  new_pass="$(docker exec pay-gate-elasticsearch bin/elasticsearch-reset-password \
    -u elastic -b -s --url https://localhost:9200 2>/dev/null | tr -d '\r\n')"
  if [[ -z "${new_pass}" ]]; then
    echo "ERROR: password reset produced no output" >&2
    exit 1
  fi

  umask 077
  printf 'ELASTIC_PASSWORD=%s\n' "${new_pass}" > "${ENV_FILE}"
  chmod 600 "${ENV_FILE}"
  unset new_pass

  # Verify without ever printing the secret.
  verify_pass="$(set -a; . "${ENV_FILE}"; set +a; printf '%s' "${ELASTIC_PASSWORD}")"
  if curl -fsS --cacert "${CA_CERT}" -u "elastic:${verify_pass}" "${ES_URL}" >/dev/null 2>&1; then
    echo "[elk-auth] Credentials verified against ${ES_URL}. Stored in .env.elk (mode 600)."
  else
    echo "ERROR: freshly stored credentials failed to authenticate" >&2
    unset verify_pass
    exit 1
  fi
  unset verify_pass
fi

# ── Kibana service account token ──────────────────────────────────────────
# Kibana forbids the `elastic` superuser; it requires a service account token
# for elastic/kibana. Reuse the stored token when present, otherwise create one.
# Old unused tokens accumulate in the .security index (dev-only tradeoff).
existing_token="$(set -a; . "${ENV_FILE}" 2>/dev/null; set +a; printf '%s' "${ELASTICSEARCH_SERVICEACCOUNTTOKEN:-}")"
if [[ -n "${existing_token}" ]]; then
  echo "[elk-auth] Kibana service token already present in .env.elk. Nothing to do."
  unset existing_token
  exit 0
fi
unset existing_token

token_name="kibana-$(date +%Y%m%d-%H%M%S)"
echo "[elk-auth] Creating Kibana service token '${token_name}' (value goes only into .env.elk)..."
token_out="$(docker exec pay-gate-elasticsearch bin/elasticsearch-service-tokens \
  create elastic/kibana "${token_name}" 2>/dev/null)"
new_token="${token_out##*= }"
if [[ -z "${new_token}" || "${new_token}" == "${token_out}" ]]; then
  echo "ERROR: failed to create Kibana service token" >&2
  exit 1
fi
unset token_out

elastic_pass="$(set -a; . "${ENV_FILE}"; set +a; printf '%s' "${ELASTIC_PASSWORD}")"
umask 077
printf 'ELASTIC_PASSWORD=%s\nELASTICSEARCH_SERVICEACCOUNTTOKEN=%s\n' \
  "${elastic_pass}" "${new_token}" > "${ENV_FILE}"
chmod 600 "${ENV_FILE}"
unset elastic_pass new_token
echo "[elk-auth] Kibana service token stored in .env.elk (mode 600)."
