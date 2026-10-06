#!/usr/bin/env bash
# Provision the Kibana Data View for GO-SAAS application logs.
#
# - Waits for Kibana to become ready (no arbitrary sleeps).
# - Creates the Data View only if it does not already exist (idempotent).
# - Fails clearly if Kibana never becomes ready.
set -euo pipefail

KIBANA_URL="${KIBANA_URL:-http://localhost:5601}"
DATA_VIEW_ID="${DATA_VIEW_ID:-go-saas-payment-gateway-logs}"
DATA_VIEW_NAME="${DATA_VIEW_NAME:-GO-SAAS Payment Gateway Logs}"
DATA_VIEW_TITLE="${DATA_VIEW_TITLE:-payment-gateway-logs-*}"
TIME_FIELD="${TIME_FIELD:-@timestamp}"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-180}"
SLEEP_SECONDS="${SLEEP_SECONDS:-5}"

echo "[provision] Target Kibana: ${KIBANA_URL}"

# ── Wait for Kibana readiness ───────────────────────────────────────────────
echo "[provision] Waiting for Kibana status 'available' (max ${MAX_WAIT_SECONDS}s)..."
deadline=$(( $(date +%s) + MAX_WAIT_SECONDS ))
ready=0
while [ "$(date +%s)" -lt "${deadline}" ]; do
  status="$(curl -fsS "${KIBANA_URL}/api/status" 2>/dev/null || true)"
  if echo "${status}" | grep -q '"level":"available"'; then
    ready=1
    break
  fi
  sleep "${SLEEP_SECONDS}"
done

if [ "${ready}" -ne 1 ]; then
  echo "[provision] ERROR: Kibana at ${KIBANA_URL} is not available after ${MAX_WAIT_SECONDS}s" >&2
  exit 1
fi
echo "[provision] Kibana is available."

# ── Check whether the Data View already exists ──────────────────────────────
http_code="$(curl -s -o /tmp/kibana-dv-get.json -w '%{http_code}' \
  -H 'kbn-xsrf: true' \
  "${KIBANA_URL}/api/saved_objects/index-pattern/${DATA_VIEW_ID}")"

if [ "${http_code}" = "200" ]; then
  existing_title="$(grep -o '"title":"[^"]*"' /tmp/kibana-dv-get.json | head -1 | cut -d'"' -f4)"
  existing_ts="$(grep -o '"timeFieldName":"[^"]*"' /tmp/kibana-dv-get.json | head -1 | cut -d'"' -f4)"
  if [ "${existing_title}" = "${DATA_VIEW_TITLE}" ] && [ "${existing_ts}" = "${TIME_FIELD}" ]; then
    echo "[provision] Data View '${DATA_VIEW_NAME}' already exists with title '${DATA_VIEW_TITLE}' and timestamp '${TIME_FIELD}'. Nothing to do."
    exit 0
  fi
  echo "[provision] Data View exists but differs (title='${existing_title}', timeFieldName='${existing_ts}'). Updating..."
  update_code="$(curl -s -o /tmp/kibana-dv-update.json -w '%{http_code}' -X PUT \
    -H 'kbn-xsrf: true' -H 'Content-Type: application/json' \
    "${KIBANA_URL}/api/saved_objects/index-pattern/${DATA_VIEW_ID}" \
    -d "{\"attributes\":{\"title\":\"${DATA_VIEW_TITLE}\",\"timeFieldName\":\"${TIME_FIELD}\",\"name\":\"${DATA_VIEW_NAME}\"}}")"
  if [ "${update_code}" -ge 200 ] && [ "${update_code}" -lt 300 ]; then
    echo "[provision] Data View updated."
    exit 0
  fi
  echo "[provision] ERROR: failed to update Data View (HTTP ${update_code})" >&2
  cat /tmp/kibana-dv-update.json >&2
  exit 1
fi

# ── Create the Data View ────────────────────────────────────────────────────
echo "[provision] Creating Data View '${DATA_VIEW_NAME}'..."
create_code="$(curl -s -o /tmp/kibana-dv-create.json -w '%{http_code}' -X POST \
  -H 'kbn-xsrf: true' -H 'Content-Type: application/json' \
  "${KIBANA_URL}/api/saved_objects/index-pattern/${DATA_VIEW_ID}" \
  -d "{\"attributes\":{\"title\":\"${DATA_VIEW_TITLE}\",\"timeFieldName\":\"${TIME_FIELD}\",\"name\":\"${DATA_VIEW_NAME}\"}}")"

if [ "${create_code}" -ge 200 ] && [ "${create_code}" -lt 300 ]; then
  echo "[provision] Data View created successfully."
  exit 0
fi

echo "[provision] ERROR: failed to create Data View (HTTP ${create_code})" >&2
cat /tmp/kibana-dv-create.json >&2
exit 1
