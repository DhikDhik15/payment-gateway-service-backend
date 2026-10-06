#!/usr/bin/env python3
"""Provision the GO-SAAS Payment Gateway Kibana dashboard (idempotent)."""
import json
import os
import sys
import time
import urllib.request
import urllib.error

KIBANA_URL = os.environ.get("KIBANA_URL", "http://localhost:5601").rstrip("/")
DATA_VIEW_ID = os.environ.get("DATA_VIEW_ID", "go-saas-payment-gateway-logs")
DASHBOARD_ID = os.environ.get("DASHBOARD_ID", "go-saas-payment-gateway-overview")
MAX_WAIT_SECONDS = int(os.environ.get("MAX_WAIT_SECONDS", "180"))
SLEEP_SECONDS = int(os.environ.get("SLEEP_SECONDS", "5"))

HTTP_REQUEST = 'app_log.msg.keyword : "http request"'
WEBHOOK_BATCH = 'app_log.msg.keyword : "merchant webhook worker: deliveries processed"'
TX_CREATED = 'app_log.msg.keyword : "transaction created"'


def req(method, path, body=None):
    url = f"{KIBANA_URL}{path}"
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, method=method)
    r.add_header("kbn-xsrf", "true")
    r.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(r) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def wait_for_kibana():
    print(f"[provision-dashboard] Waiting for Kibana status 'available' "
          f"(max {MAX_WAIT_SECONDS}s)...")
    deadline = time.time() + MAX_WAIT_SECONDS
    while time.time() < deadline:
        try:
            status, body = req("GET", "/api/status")
            if status == 200 and '"level":"available"' in body:
                print("[provision-dashboard] Kibana is available.")
                return
        except Exception:
            pass
        time.sleep(SLEEP_SECONDS)
    sys.exit(f"[provision-dashboard] ERROR: Kibana at {KIBANA_URL} is not "
             f"available after {MAX_WAIT_SECONDS}s")


def check_data_view():
    status, body = req("GET", f"/api/saved_objects/index-pattern/{DATA_VIEW_ID}")
    if status != 200:
        sys.exit(f"[provision-dashboard] ERROR: Data View '{DATA_VIEW_ID}' not "
                 f"found (HTTP {status}). Run the Data View provisioning first.")
    print(f"[provision-dashboard] Data View '{DATA_VIEW_ID}' exists.")


# ── Lens column helpers ──────────────────────────────────────────────────────

def col_count(label="Count"):
    return {"operationType": "count", "label": label, "dataType": "number",
            "isBucketed": False, "scale": "ratio"}


def col_date_histogram():
    return {"operationType": "date_histogram", "dataType": "date",
            "isBucketed": True, "scale": "interval",
            "sourceField": "@timestamp", "label": "@timestamp",
            "params": {"interval": "auto"}}


def col_terms(field, size, order_col, excludes=None):
    params = {"size": size,
              "orderBy": {"type": "column", "columnId": order_col},
              "orderDirection": "desc"}
    if excludes:
        params["exclude"] = excludes
    return {"operationType": "terms",
            "dataType": "number" if field == "app_log.status" else "string",
            "isBucketed": True,
            "scale": "ordinal", "sourceField": field, "label": field,
            "params": params}


def col_sum(field, label):
    return {"operationType": "sum", "dataType": "number", "isBucketed": False,
            "scale": "ratio", "sourceField": field, "label": label,
            "customLabel": True}


def col_percentile_formula(pct, label):
    return {"operationType": "formula", "dataType": "number",
            "isBucketed": False, "scale": "ratio", "label": label,
            "customLabel": True,
            "params": {"formula":
                       f"percentile(app_log.latency, percent={pct}) / 1000000"},
            "references": []}


def layer(columns, order):
    return {"columnOrder": order, "columns": columns}


def lens_state(query, datasource_layer, visualization):
    return {"adHocDataViews": {},
            "filters": [],
            "query": {"language": "kuery", "query": query},
            "datasourceStates": {"formBased": {"layers": {"layer1": datasource_layer}}},
            "visualization": visualization}


VIZ_REFS = lambda: [
    {"type": "index-pattern", "id": DATA_VIEW_ID,
     "name": "indexpattern-datasource-current-indexpattern"},
    {"type": "index-pattern", "id": DATA_VIEW_ID,
     "name": "indexpattern-datasource-layer-layer1"},
]


def metric_lens(viz_id, title, query, value_col_id="col1"):
    return {
        "id": viz_id, "type": "lens",
        "attributes": {
            "title": title, "visualizationType": "lnsMetric",
            "state": lens_state(query, layer({value_col_id: col_count()}, [value_col_id]),
                                {"layerId": "layer1", "layerType": "data",
                                 "metricAccessor": value_col_id}),
        },
        "references": VIZ_REFS(),
    }


def sum_metric_lens(viz_id, title, query, field, label):
    return {
        "id": viz_id, "type": "lens",
        "attributes": {
            "title": title, "visualizationType": "lnsMetric",
            "state": lens_state(query, layer({"col1": col_sum(field, label)}, ["col1"]),
                                {"layerId": "layer1", "layerType": "data",
                                 "metricAccessor": "col1"}),
        },
        "references": VIZ_REFS(),
    }


def xy_lens(viz_id, title, query, columns, order, xy_state_extra,
            preferred, accessors, x_accessor, series_type, y_config_labels=None):
    layers = [{"accessors": accessors, "layerId": "layer1", "layerType": "data",
               "seriesType": series_type, "xAccessor": x_accessor,
               "yConfig": [{"forAccessor": a} for a in accessors]}]
    vis_state = {"legend": {"isVisible": True, "position": "right"},
                 "preferredSeriesType": preferred, "valueLabels": "hide",
                 "fittingFunction": "None",
                 "axisTitlesVisibilitySettings": {"x": True, "yLeft": True, "yRight": True},
                 "tickLabelsVisibilitySettings": {"x": True, "yLeft": True, "yRight": True},
                 "gridlinesVisibilitySettings": {"x": True, "yLeft": True, "yRight": True},
                 "layers": layers}
    vis_state.update(xy_state_extra)
    return {
        "id": viz_id, "type": "lens",
        "attributes": {"title": title, "visualizationType": "lnsXY",
                       "state": lens_state(query, layer(columns, order), vis_state)},
        "references": VIZ_REFS(),
    }


# ── Build all visualizations ─────────────────────────────────────────────────

objs = []

objs.append(metric_lens("go-saas-total-requests", "Total HTTP Requests", HTTP_REQUEST))
objs.append(metric_lens("go-saas-success-requests", "Successful Requests (2xx)",
                        HTTP_REQUEST + ' and app_log.status >= 200 and app_log.status < 300'))
objs.append(metric_lens("go-saas-client-errors", "Client Errors (4xx)",
                        HTTP_REQUEST + ' and app_log.status >= 400 and app_log.status < 500'))
objs.append(metric_lens("go-saas-server-errors", "Server Errors (5xx)",
                        HTTP_REQUEST + ' and app_log.status >= 500 and app_log.status < 600'))

objs.append(xy_lens(
    "go-saas-requests-over-time", "HTTP Requests Over Time", HTTP_REQUEST,
    {"col1": col_date_histogram(), "col2": col_count("Requests")}, ["col1", "col2"],
    {}, "area", ["col2"], "col1", "area"))

objs.append(xy_lens(
    "go-saas-status-distribution", "HTTP Status Distribution", HTTP_REQUEST,
    {"col1": col_terms("app_log.status", 10, "col2"), "col2": col_count()},
    ["col1", "col2"], {}, "bar", ["col2"], "col1", "bar"))

objs.append(xy_lens(
    "go-saas-top-endpoints", "Top API Endpoints",
    HTTP_REQUEST + ' and not app_log.path.keyword : "/health"'
                 + ' and not app_log.path.keyword : "/health/ready"'
                 + ' and not app_log.path.keyword : "/health/live"'
                 + ' and not app_log.path.keyword : "unmatched"',
    {"col1": col_terms("app_log.path.keyword", 10, "col2"), "col2": col_count()},
    ["col1", "col2"], {}, "bar_horizontal", ["col2"], "col1", "bar_horizontal"))

objs.append(xy_lens(
    "go-saas-latency", "Request Latency (ms) — p50 / p95 / p99", HTTP_REQUEST,
    {"col1": col_date_histogram(),
     "col2": col_percentile_formula(50, "p50 (ms)"),
     "col3": col_percentile_formula(95, "p95 (ms)"),
     "col4": col_percentile_formula(99, "p99 (ms)")},
    ["col1", "col2", "col3", "col4"], {}, "line", ["col2", "col3", "col4"],
    "col1", "line"))

objs.append(xy_lens(
    "go-saas-log-levels", "Log Level Distribution", "",
    {"col1": col_terms("app_log.level.keyword", 10, "col2"),
     "col2": col_count()}, ["col1", "col2"], {}, "bar", ["col2"], "col1", "bar"))

objs.append(metric_lens("go-saas-tx-created", "Transactions Created", TX_CREATED))
objs.append(sum_metric_lens("go-saas-tx-volume", "Total Transaction Amount",
                            TX_CREATED, "app_log.amount", "Sum of amount"))
objs.append(xy_lens(
    "go-saas-tx-currency", "Transactions by Currency", TX_CREATED,
    {"col1": col_terms("app_log.currency.keyword", 10, "col2"), "col2": col_count()},
    ["col1", "col2"], {}, "bar", ["col2"], "col1", "bar"))

objs.append(metric_lens("go-saas-webhook-batches", "Webhook Batches", WEBHOOK_BATCH))
objs.append(sum_metric_lens("go-saas-webhook-deliveries", "Webhook Deliveries",
                            WEBHOOK_BATCH, "app_log.count", "Deliveries"))
objs.append(xy_lens(
    "go-saas-webhook-deliveries-over-time", "Webhook Deliveries Over Time",
    WEBHOOK_BATCH,
    {"col1": col_date_histogram(), "col2": col_sum("app_log.count", "Deliveries")},
    ["col1", "col2"], {}, "area", ["col2"], "col1", "area"))

# ── Dashboard ────────────────────────────────────────────────────────────────

def panel(i, x, y, w, h, viz_id):
    pid = f"panel_{i}"
    return ({"version": "9.1.4", "type": "lens",
             "gridData": {"i": pid, "x": x, "y": y, "w": w, "h": h, "sectionId": None},
             "panelIndex": pid, "embeddableConfig": {},
             "panelRefName": f"panel_ref_{i}"},
            {"name": f"panel_ref_{i}", "type": "lens", "id": viz_id})

panels, references = [], []
layout = [
    # (viz_id, x, y, w, h)
    ("go-saas-total-requests", 0, 0, 12, 12),
    ("go-saas-success-requests", 12, 0, 12, 12),
    ("go-saas-client-errors", 24, 0, 12, 12),
    ("go-saas-server-errors", 36, 0, 12, 12),
    ("go-saas-requests-over-time", 0, 12, 48, 15),
    ("go-saas-status-distribution", 0, 27, 24, 15),
    ("go-saas-top-endpoints", 24, 27, 24, 15),
    ("go-saas-latency", 0, 42, 24, 15),
    ("go-saas-log-levels", 24, 42, 24, 15),
    ("go-saas-tx-created", 0, 57, 12, 12),
    ("go-saas-tx-volume", 12, 57, 12, 12),
    ("go-saas-tx-currency", 24, 57, 24, 12),
    ("go-saas-webhook-batches", 0, 69, 12, 12),
    ("go-saas-webhook-deliveries", 12, 69, 12, 12),
    ("go-saas-webhook-deliveries-over-time", 24, 69, 24, 12),
]
for i, (viz_id, x, y, w, h) in enumerate(layout):
    p, r = panel(i, x, y, w, h, viz_id)
    panels.append(p)
    references.append(r)

dashboard = {
    "id": DASHBOARD_ID, "type": "dashboard",
    "attributes": {
        "title": "GO-SAAS Payment Gateway Overview",
        "description": "Operational overview of HTTP traffic, errors, latency, "
                       "payment transactions and webhook deliveries.",
        "hits": 0,
        "timeRestore": True,
        "timeFrom": "now-24h",
        "timeTo": "now",
        "refreshInterval": {"pause": True, "value": 0},
        "optionsJSON": json.dumps({"useMargins": True, "syncColors": True,
                                   "syncTooltips": False, "syncCursor": True,
                                   "hidePanelTitles": False}),
        "panelsJSON": json.dumps(panels),
        "version": 1,
    },
    "references": references,
}

# ── Create / update ─────────────────────────────────────────────────────────

def create_or_update(obj):
    oid, otype = obj["id"], obj["type"]
    body = {"attributes": obj["attributes"], "references": obj["references"]}
    status, resp = req("POST", f"/api/saved_objects/{otype}/{oid}", body)
    if status in (200, 201):
        print(f"[provision-dashboard] created {otype}/{oid}")
        return
    if status == 409:
        status, resp = req("PUT", f"/api/saved_objects/{otype}/{oid}", body)
        if status in (200, 201):
            print(f"[provision-dashboard] updated {otype}/{oid}")
            return
        sys.exit(f"[provision-dashboard] ERROR updating {otype}/{oid}: HTTP {status}\n{resp}")
    sys.exit(f"[provision-dashboard] ERROR creating {otype}/{oid}: HTTP {status}\n{resp}")


def main():
    wait_for_kibana()
    check_data_view()
    for o in objs:
        create_or_update(o)
    create_or_update(dashboard)
    print(f"[provision-dashboard] Done. Dashboard ID: {DASHBOARD_ID}")


if __name__ == "__main__":
    main()
