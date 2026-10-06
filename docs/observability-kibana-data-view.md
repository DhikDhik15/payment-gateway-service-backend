# Kibana Data View Provisioning (Phase Observability 1)

The GO-SAAS payment gateway ingests application logs through Logstash into
Elasticsearch indices matching `payment-gateway-logs-*`. To explore these
logs in Kibana Discover, Kibana needs a Data View.

This repository provisions that Data View reproducibly, so it does not need
to be recreated manually in the Kibana UI.

## Expected configuration

| Setting        | Value                              |
| -------------- | ---------------------------------- |
| Data View name | `GO-SAAS Payment Gateway Logs`     |
| Index pattern  | `payment-gateway-logs-*`           |
| Timestamp field| `@timestamp`                       |
| Kibana URL     | http://localhost:5601              |

## How to run

Start the ELK stack (if not already running):

```bash
make elk-up
# or: docker compose -f docker-compose.elk.yml up -d
```

Provision the Data View:

```bash
make elk-provision
# or directly:
./elk/scripts/provision-kibana-dataview.sh
```

The provisioning script:

- waits for Kibana to report status `available` via `/api/status`
  (no arbitrary sleeps; fails clearly after `MAX_WAIT_SECONDS`, default 180s),
- checks whether the Data View already exists,
- creates it if missing, updates it if the title/timestamp differ,
- exits without changes if it already matches (idempotent).

Environment overrides:

```bash
KIBANA_URL=http://localhost:5601 \
DATA_VIEW_ID=go-saas-payment-gateway-logs \
DATA_VIEW_NAME="GO-SAAS Payment Gateway Logs" \
DATA_VIEW_TITLE="payment-gateway-logs-*" \
TIME_FIELD="@timestamp" \
./elk/scripts/provision-kibana-dataview.sh
```

## Verify

1. Elasticsearch has the log indices:

   ```bash
   curl -s http://localhost:9200/_cat/indices?v | grep payment-gateway-logs
   ```

2. Kibana is healthy:

   ```bash
   curl -s http://localhost:5601/api/status | grep -o '"level":"available"'
   ```

3. The Data View exists with the right settings:

   ```bash
   curl -s -H 'kbn-xsrf: true' \
     http://localhost:5601/api/saved_objects/index-pattern/go-saas-payment-gateway-logs
   ```

   Expect `"title":"payment-gateway-logs-*"` and
   `"timeFieldName":"@timestamp"`.

4. In Kibana Discover (http://localhost:5601/app/discover), select the
   `GO-SAAS Payment Gateway Logs` Data View from the Data View picker.
   Log documents with fields like `app_log.level`, `app_log.msg`,
   `app_log.request_id`, `app_log.method`, `app_log.path`, `app_log.status`,
   `app_log.latency`, `app_log.client_ip` should be visible.

## Idempotency

Running `make elk-provision` multiple times is safe: it will report
`Data View ... already exists ... Nothing to do.` and never create
duplicates.
