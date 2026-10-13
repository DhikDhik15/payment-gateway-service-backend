# ELK TLS - Phase 3A.3 & 3A.4 (Development Certificates)

## Why TLS is being introduced

To secure traffic between Elasticsearch, Kibana, and Logstash, this phase
enables TLS with authentication between ELK components.

## Architecture selected: B — TLS with authentication

Phase 3A.4 uses **TLS with authentication** (not TLS without auth).

Why: on Elasticsearch 9.1.4, `xpack.security.enabled=false` causes the
`xpack.security.http.ssl.*` settings to be **ignored** — the node serves
plaintext HTTP. This was verified at runtime: with security disabled, an
HTTPS probe fails with `SSL routines::wrong version number` while plaintext
`http://localhost:9200` returns 200. TLS without authentication (option A) is
therefore not supported by the actual Elasticsearch configuration, so this
phase enables security, HTTP TLS, **and** transport TLS together with
credentials for Kibana/Logstash. The two models are not mixed.

## Phase 3A.3 - What was generated

Reproducible local-development certificates using OpenSSL:

- A local development CA (Certificate Authority)
- An Elasticsearch server certificate signed by the development CA
  (reused for both HTTP TLS and transport TLS in this single-node dev setup)

### Certificate hierarchy

```
Development CA
   │
   └── Elasticsearch server certificate (HTTP + transport, single node)
```

### SANs included in the Elasticsearch certificate

Valid for connections from inside the Docker network and from localhost:

- `DNS:elasticsearch` (Docker service/container hostname)
- `DNS:localhost`
- `IP:127.0.0.1`

## Phase 3A.4 - What was enabled

### Elasticsearch

Elasticsearch exposes **HTTPS only** on port 9200
(`docker-compose.elk.yml` + `elk/elasticsearch/elasticsearch.yml`):

- `xpack.security.enabled=true`
- `xpack.security.http.ssl.enabled=true` (key/cert/CA as below)
- `xpack.security.transport.ssl.enabled=true` — **required**: with security
  enabled, Elasticsearch logs
  `Transport SSL must be enabled if security is enabled` and the combination
  `security.enabled=true` + `transport.ssl.enabled=false` is invalid.
- `verification_mode=certificate` on both layers (CA-signed, dev-only;
  hostname checking is not enforced)
- `client_authentication=none` on the HTTP layer
- Certificates mounted read-only: CA cert, ES cert, ES key
- CA private key (`ca.key`) is **not** mounted into any container

Unauthenticated HTTPS requests correctly return `401`
(`missing authentication credentials`); authenticated requests return `200`
with full CA verification (`ssl_verify_result=0`).

### Kibana -> Elasticsearch

Kibana connects over HTTPS with CA verification:

- `ELASTICSEARCH_HOSTS=https://elasticsearch:9200`
- `ELASTICSEARCH_SSL_VERIFICATIONMODE=full`
- `ELASTICSEARCH_SSL_CERTIFICATEAUTHORITIES=/usr/share/kibana/config/certs/ca.crt`
- Only the CA certificate is mounted into Kibana (read-only)

Authentication uses a **service account token** (`elastic/kibana`) supplied as
`ELASTICSEARCH_SERVICEACCOUNTTOKEN` via `env_file: .env.elk`. Kibana
**forbids** the `elastic` superuser as its server user (startup `FATAL`
otherwise), so a token is mandatory — verified at runtime.

Kibana browser endpoint remains `http://localhost:5601` (browser TLS not
enabled in this phase). Because Elasticsearch security is enabled, Kibana's
Saved Objects APIs now require Basic auth; the provision scripts accept
optional `KIBANA_USER`/`KIBANA_PASSWORD`.

### Logstash -> Elasticsearch

Logstash connects over HTTPS with CA verification:

- Output hosts: `https://elasticsearch:9200`
- `user => "elastic"` / `password => "${ELASTIC_PASSWORD}"` (interpolated
  from the container environment provided via `env_file: .env.elk`; the
  secret is **not** hardcoded in the tracked pipeline file)
- `ssl_certificate_authorities => "/usr/share/logstash/config/certs/ca.crt"`
- `ssl_verification_mode => "full"`
- Only the CA certificate is mounted into Logstash (read-only)
- Existing parsing behavior and index naming preserved:
  `payment-gateway-logs-%{+YYYY.MM.dd}`

Two dev-only Logstash specifics (verified necessary at runtime):

- The container runs as root (`user: "0:0"`) so it can read Docker container
  log files, with `allow_superuser: true` in
  `elk/logstash/config/logstash.yml` (Logstash 9.x refuses root otherwise).
- The file input uses a wildcard (`/var/lib/docker/containers/*/*-json.log`)
  because Docker log paths embed rotating container IDs (the previous config
  pinned a dead container ID and ingested nothing). To keep the index
  semantics unchanged, events without the application's `app_log.time` field
  (other containers' logs) are dropped, and the debug `stdout { rubydebug }`
  output was removed — with a wildcard input it re-ingested its own output.
  Internal log level is `error` so routine parse rejections cannot re-enter
  the input as new log lines.

## Credentials (development only)

Secrets live in `.env.elk` at the repository root (gitignored, mode `600`):

```
ELASTIC_PASSWORD=...
ELASTICSEARCH_SERVICEACCOUNTTOKEN=...
```

- Created/rotated by `./elk/scripts/setup-elk-auth.sh` (idempotent: reuses
  stored credentials while they authenticate). The script never prints
  secrets.
- `ELASTIC_PASSWORD` authenticates Logstash output, `make elk-tls-check`,
  and Kibana API access (`KIBANA_USER=elastic`); the Kibana **server**
  connection uses the service account token.
- Using the `elastic` superuser for Logstash is development-only; production
  must use least-privilege roles/users and a real secret manager.

## How to generate certificates (3A.3)

From the repository root:

```bash
./elk/scripts/generate-dev-certs.sh
```

Idempotent — exits without overwriting when material exists. Force:

```bash
./elk/scripts/generate-dev-certs.sh --force
```

## Usage (3A.4)

First-time (or after credential rotation):

```bash
make elk-certs        # one-time certificate generation
docker compose -f docker-compose.elk.yml up -d elasticsearch
make elk-auth-setup   # bootstrap elastic password + Kibana token into .env.elk
make elk-up           # start the full stack (reads .env.elk via env_file)
make elk-provision    # Data View + dashboard (idempotent, uses .env.elk auth)
```

Day-to-day:

```bash
make elk-up
make elk-tls-check
```

Verify TLS connectivity manually (never use `curl -k`):

```bash
set -a; . ./.env.elk; set +a
curl --fail --cacert ./elk/certs/ca/ca.crt \
  -u "elastic:${ELASTIC_PASSWORD}" https://localhost:9200
curl --fail --cacert ./elk/certs/ca/ca.crt \
  -u "elastic:${ELASTIC_PASSWORD}" 'https://localhost:9200/_cluster/health?pretty'
curl --fail http://localhost:5601/api/status
unset ELASTIC_PASSWORD ELASTICSEARCH_SERVICEACCOUNTTOKEN
```

Provision (Kibana API requires auth now):

```bash
make elk-provision
```

## Which generated/secret files must remain untracked

- `elk/certs/ca/ca.key` (CA private key)
- `elk/certs/ca/ca.crt` (CA certificate)
- `elk/certs/elasticsearch/elasticsearch.key` (Elasticsearch private key)
- `elk/certs/elasticsearch/elasticsearch.crt` (server certificate)
- `.env.elk` (elastic password + Kibana service token)

`elk/certs/.gitignore` ignores `ca/`, `elasticsearch/`, `*.key`, `*.crt`;
the root `.gitignore` ignores `.env.elk`.

## Security & Important notes

- **Development only**: dev CA, shared HTTP/transport cert, superuser for
  Logstash, root Logstash, browser HTTP — **NOT** a production design.
- **Authentication is enabled**: `xpack.security.enabled=true` with transport
  TLS and credentials (see "Architecture selected" for why auth-less TLS was
  rejected on 9.1.4).
- **CA private key never mounted**: `ca.key` is not mounted into
  Elasticsearch, Kibana, or Logstash containers (verified via
  `docker inspect`).
- **TLS verification enforced**: `full`/`certificate` modes only; no
  `ssl_verification_mode => "none"`, no `curl -k`, no bypass.
- **Kibana browser remains HTTP**: `http://localhost:5601` unchanged.
- **Data preserved**: existing Elasticsearch volume (`elasticsearch-data`),
  indices, Kibana saved objects, Data View, and dashboard are not deleted.
  Do not run `docker compose down -v`.
- **Docker compatibility**: certificate files mounted read-only; private keys
  on host remain 600; `.env.elk` remains 600.
- **Secrets hygiene**: passwords/tokens never appear in tracked files,
  terminal output, or logs. `setup-elk-auth.sh` writes them straight to
  `.env.elk`.

## CI/CD considerations

- Production/CI must inject trusted certificate material via secure secret
  management or proper PKI. Dev-style certs must not be used in production.
- Private keys and `.env.elk` must never be committed.
- Production needs per-service least-privilege credentials, Kibana browser
  TLS, and transport certificates with hostname verification — not the
  shortcuts documented above.

## Verification

After startup, verify HTTPS from host:

```bash
# Check SANs and validity (optional)
openssl x509 -in elk/certs/elasticsearch/elasticsearch.crt -noout -subject -issuer -dates -ext subjectAltName

# Verify certificate chain
openssl verify -CAfile elk/certs/ca/ca.crt elk/certs/elasticsearch/elasticsearch.crt

# Elasticsearch HTTPS with CA verification + auth
set -a; . ./.env.elk; set +a
curl --fail --cacert elk/certs/ca/ca.crt \
  -u "elastic:${ELASTIC_PASSWORD}" https://localhost:9200
curl --fail --cacert elk/certs/ca/ca.crt \
  -u "elastic:${ELASTIC_PASSWORD}" 'https://localhost:9200/_cluster/health?pretty'
```

Verify Logstash ingestion:

```bash
curl --fail --cacert elk/certs/ca/ca.crt -u "elastic:${ELASTIC_PASSWORD}" \
  'https://localhost:9200/payment-gateway-logs-*/_count'
curl --fail --cacert elk/certs/ca/ca.crt -u "elastic:${ELASTIC_PASSWORD}" \
  'https://localhost:9200/payment-gateway-logs-*/_search?size=1&sort=@timestamp:desc'
```

Verify no CA private key mounted in containers:

```bash
docker inspect pay-gate-elasticsearch --format '{{range .Mounts}}{{.Source}}:{{.Destination}}:{{.Mode}}{{"\n"}}{{end}}' | grep ca.key
docker inspect pay-gate-kibana --format '{{range .Mounts}}{{.Source}}:{{.Destination}}:{{.Mode}}{{"\n"}}{{end}}' | grep ca.key
docker inspect pay-gate-logstash --format '{{range .Mounts}}{{.Source}}:{{.Destination}}:{{.Mode}}{{"\n"}}{{end}}' | grep ca.key
```
(Expected: no output)

Check host private key permissions:

```bash
stat -c '%a %n' elk/certs/ca/ca.key elk/certs/elasticsearch/elasticsearch.key .env.elk
```
(Expected: 600 600 600)
