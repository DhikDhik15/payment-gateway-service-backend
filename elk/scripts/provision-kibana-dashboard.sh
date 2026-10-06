#!/usr/bin/env bash
# Provision the GO-SAAS Payment Gateway Kibana dashboard (idempotent).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "${SCRIPT_DIR}/provision_kibana_dashboard.py" "$@"
