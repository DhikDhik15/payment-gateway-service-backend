# Kibana Dashboard Provisioning (Phase Observability 2)

`GO-SAAS Payment Gateway Overview` is a Kibana dashboard built from Lens
visualizations over the existing `payment-gateway-logs-*` index.

## Dashboard

| Setting | Value |
| --- | --- |
| Dashboard name | `GO-SAAS Payment Gateway Overview` |
| Dashboard Saved Object ID | `go-saas-payment-gateway-overview` |
| Data View ID | `go-saas-payment-gateway-logs` |
| Data View | `GO-SAAS Payment Gateway Logs` (`payment-gateway-logs-*`, `@timestamp`) |
| Default time range | last 24 hours (`now-24h` → `now`), overridable via Kibana time picker |
| Kibana URL | http://localhost:5601/app/dashboards |

## Visualizations and fields

| Panel | Type | Fields used | Query/filter |
| --- | --- | --- | --- |
| Total HTTP Requests | Metric | count | `app_log.msg.keyword : "http request"` |
| Successful Requests (2xx) | Metric | count | msg http request, `app_log.status >= 200 and < 300` |
| Client Errors (4xx) | Metric | count | msg http request, `app_log.status >= 400 and < 500` |
| Server Errors (5xx) | Metric | count | msg http request, `app_log.status >= 500 and < 600` |
| HTTP Requests Over Time | XY area | `@timestamp`, count | msg http request, auto date histogram |
| HTTP Status Distribution | XY bar | `app_log.status`, count | msg http request |
| Top API Endpoints | XY horizontal bar | `app_log.path.keyword`, count | msg http request, excludes `/health`, `/health/ready`, `/health/live`, `unmatched`, top 10 |
| Request Latency p50/p95/p99 (ms) | XY line | `app_log.latency`, `@timestamp` | msg http request; `percentile(app_log.latency, percent=N) / 1000000` |
| Log Level Distribution | XY bar | `app_log.level.keyword`, count | all events |
| Transactions Created | Metric | count | `app_log.msg.keyword : "transaction created"` |
| Total Transaction Amount | Metric | `sum(app_log.amount)` | tx created |
| Transactions by Currency | XY bar | `app_log.currency.keyword`, count | tx created |
| Webhook Batches | Metric | count | `app_log.msg.keyword : "merchant webhook worker: deliveries processed"` |
| Webhook Deliveries | Metric | `sum(app_log.count)` | same |
| Webhook Deliveries Over Time | XY area | `@timestamp`, `sum(app_log.count)` | same |

### Latency unit note

`app_log.latency` is written by the application via
`slog.Duration("latency", time.Since(start))`, which Go encodes as
**nanoseconds** (integer). The dashboard divides by 1,000,000 to display
milliseconds (p50 ≈ 0.5 ms in current data — consistent with nanoseconds).

## Provision

```bash
make elk-up          # start ELK (if needed)
make elk-provision   # idempotent: Data View + Dashboard
# or individually:
./elk/scripts/provision-kibana-dataview.sh
./elk/scripts/provision-kibana-dashboard.sh
# Makefile shortcuts:
make elk-provision-dashboard   # dashboard only
make elk-dashboard             # alias
```

Environment overrides: `KIBANA_URL`, `DATA_VIEW_ID`, `DASHBOARD_ID`,
`MAX_WAIT_SECONDS`, `SLEEP_SECONDS`.

## Idempotency

- Every visualization and the dashboard use stable Saved Object IDs
  (`go-saas-*`).
- Each run creates missing objects (POST) and updates existing ones (PUT) to
  the same deterministic configuration.
- Re-running produces no duplicate Data Views, visualizations, or dashboards
  (verified: exactly one `GO-SAAS Payment Gateway Overview` dashboard and one
  `go-saas-payment-gateway-logs` Data View after multiple runs).
- If Kibana is unavailable or the Data View is missing, the script exits with a
  clear error message.

## Business/webhook metrics

Available (driven by actual indexed fields):

- Transactions created, total transaction amount, transactions by currency.
- Webhook delivery batches and total deliveries (`app_log.count`).

Not available yet (intentionally omitted — not present in current data):

- Per-payment status/last_status, payment provider breakdown, payment failure
  rates — no reliable indexed fields yet.
- Webhook delivery success/failure split and per-attempt latency — current
  webhook events only carry `app_log.count`/`app_log.attempt`, not explicit
  outcome fields.

## Verify

```bash
# Index has documents
curl -s http://localhost:9200/_cat/indices | grep payment-gateway-logs

# Kibana healthy
curl -s http://localhost:5601/api/status | grep -o '"level":"available"'

# Data View exists
curl -s -H 'kbn-xsrf: true' \
  http://localhost:5601/api/saved_objects/index-pattern/go-saas-payment-gateway-logs

# Dashboard exists
curl -s -H 'kbn-xsrf: true' \
  http://localhost:5601/api/saved_objects/dashboard/go-saas-payment-gateway-overview

# Dashboard listed in API
curl -s "http://localhost:5601/api/saved_objects/_find?type=dashboard&search=GO-SAAS" \
  | grep -o '"title":"GO-SAAS Payment Gateway Overview"'
```

Open http://localhost:5601/app/dashboards and select
`GO-SAAS Payment Gateway Overview`. Panels should render data for the selected
time range (default last 24h).
