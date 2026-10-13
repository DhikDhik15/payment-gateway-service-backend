#!/usr/bin/env bash
set -euo pipefail

# Determine repository root (script is in elk/scripts, repo root is two levels up)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CERTS_DIR="${REPO_ROOT}/elk/certs"

CA_DIR="${CERTS_DIR}/ca"
ES_DIR="${CERTS_DIR}/elasticsearch"

CA_KEY="${CA_DIR}/ca.key"
CA_CERT="${CA_DIR}/ca.crt"
ES_KEY="${ES_DIR}/elasticsearch.key"
ES_CSR="${ES_DIR}/elasticsearch.csr"
ES_CERT="${ES_DIR}/elasticsearch.crt"

FORCE=false

usage() {
  cat >&2 <<EOF
Usage: $(basename "$0") [--force]
  --force   Regenerate certificates even if they already exist
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --force)
      FORCE=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

# Fail clearly if openssl is unavailable
if ! command -v openssl >/dev/null 2>&1; then
  echo "ERROR: openssl is not available in PATH" >&2
  exit 1
fi

# Create required directories
mkdir -p "${CA_DIR}" "${ES_DIR}"

# Idempotency check - do not overwrite existing certificate material by default
if [[ "${FORCE}" != true ]]; then
  if [[ -f "${CA_CERT}" && -f "${CA_KEY}" && -f "${ES_CERT}" && -f "${ES_KEY}" ]]; then
    echo "Certificate material already exists."
    echo "Use --force to regenerate."
    echo ""
    echo "Summary:"
    echo "  CA cert: ${CA_CERT}"
    echo "  ES cert: ${ES_CERT}"
    exit 0
  fi
fi

# If forcing, remove existing files
if [[ "${FORCE}" == true ]]; then
  echo "Regenerating certificates (--force)..."
  rm -f "${CA_KEY}" "${CA_CERT}" "${ES_KEY}" "${ES_CSR}" "${ES_CERT}"
fi

# Temporary openssl config for SAN generation
OPENSSL_CFG="$(mktemp)"
trap 'rm -f "${OPENSSL_CFG}"' EXIT

cat >"${OPENSSL_CFG}" <<EOF
[ req ]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[ req_distinguished_name ]
C = XX
ST = Local
L = Local
O = Dev
OU = PayGate-Dev
CN = elasticsearch

[ v3_req ]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[ alt_names ]
DNS.1 = elasticsearch
DNS.2 = localhost
IP.1 = 127.0.0.1

[ v3_ca ]
basicConstraints = CA:TRUE
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always,issuer:always
EOF

# Generate CA private key
openssl genrsa -out "${CA_KEY}" 4096

# Generate CA certificate (self-signed, valid for 1 year for dev)
openssl req -x509 -new -nodes \
  -key "${CA_KEY}" \
  -sha256 \
  -days 365 \
  -out "${CA_CERT}" \
  -subj "/C=XX/ST=Local/L=Local/O=DevCA/OU=PayGate-Dev/CN=DevCA" \
  -extensions v3_ca \
  -config "${OPENSSL_CFG}"

# Generate Elasticsearch private key
openssl genrsa -out "${ES_KEY}" 4096

# Generate Elasticsearch CSR
openssl req -new -key "${ES_KEY}" -out "${ES_CSR}" -config "${OPENSSL_CFG}"

# Sign CSR using development CA with SAN extensions
openssl x509 -req \
  -in "${ES_CSR}" \
  -CA "${CA_CERT}" \
  -CAkey "${CA_KEY}" \
  -CAcreateserial \
  -out "${ES_CERT}" \
  -days 365 \
  -sha256 \
  -extensions v3_req \
  -extfile "${OPENSSL_CFG}"

# Set restrictive permissions on private keys
chmod 600 "${CA_KEY}" "${ES_KEY}"

# Make generated certificate files readable by Docker containers
chmod 644 "${CA_CERT}" "${ES_CERT}"

# Verify generated certificate
echo ""
echo "Verifying certificate..."
openssl x509 -in "${ES_CERT}" -noout -subject -issuer -dates -ext subjectAltName

# Verify certificate chain validates against CA
echo ""
echo "Verifying certificate chain..."
openssl verify -CAfile "${CA_CERT}" "${ES_CERT}"

# Success summary
echo ""
echo "✓ Certificate generation complete"
echo ""
echo "Paths:"
echo "  CA cert: ${CA_CERT}"
echo "  CA key:  ${CA_KEY}"
echo "  ES cert: ${ES_CERT}"
echo "  ES key:  ${ES_KEY}"
echo ""
echo "Note: Private keys are chmod 600 and not printed."
