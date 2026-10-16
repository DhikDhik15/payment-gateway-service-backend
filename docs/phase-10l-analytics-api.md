# Phase 10L dashboard analytics API

`GET /api/v1/dashboard/analytics` — merchant-scoped revenue and product-sales
analytics. Companion to the Phase 9 overview endpoint; the overview contract
is unchanged.

**Auth:** Bearer JWT (dashboard). **Roles:** OWNER, ADMIN, VIEWER.
Merchant identity comes from the authenticated user — never from client input.

**Query:** same period contract as overview.

| Param | Required | Description |
|---|---|---|
| `from` | no | Period start (RFC3339 or `YYYY-MM-DD` UTC). Must be paired with `to`. |
| `to` | no | Period end (exclusive). Half-open `[from, to)`. |

**Default period:** current UTC calendar month `[month_start, next_month)`.

**Response `data`:**

```json
{
  "period": { "from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z" },
  "interval": "day",
  "summary": {
    "total_revenue": 0,
    "successful_payments": 0,
    "units_sold": 0,
    "average_order_value": null
  },
  "revenue_trend": [{ "date": "2026-10-01", "revenue": 0 }],
  "top_products": [],
  "revenue_by_product": []
}
```

**Semantics (all predicates additionally scoped by merchant):**

- `summary.total_revenue`: gross `SUM(transactions.amount)` where
  `status = PAID` and `paid_at ∈ [from, to)`. Identical to overview revenue.
  Refunds are never subtracted (reported separately by overview).
- `summary.successful_payments`: `COUNT(*)` of the same PAID rows.
- Units / AOV: an order belongs to the window when the `paid_at` of its
  single PAID transaction falls in `[from, to)`. The partial unique index
  `uq_transactions_order_paid` guarantees at most one PAID row per order, so
  retried failures never duplicate sales. `units_sold` sums immutable
  `order_items.quantity`; `average_order_value` is order-attributed paid
  value divided by unique paid orders, `null` when zero orders paid.
  Order-less legacy payments count toward revenue but not units/AOV.
- `interval`: `day` for windows up to 62 days, otherwise `week`
  (Monday-start buckets). `revenue_trend` zero-fills every bucket in
  `[from, to)`; bucket `date` is the UTC calendar day (`YYYY-MM-DD`).
- `top_products`: at most 5 rows ranked by units sold; `revenue_by_product`:
  bounded to 50 rows ranked by revenue desc. Both aggregate immutable
  order-item snapshots of paid orders only — quantities, prices, and names at
  purchase time; live catalog prices are never used. Display name is the
  latest snapshot, so renamed products stay recognizable. Deleted products
  (whose `product_id` was `SET NULL`) group by snapshot name with a null
  `product_id`.
- Empty merchant/period → zeros, `null` AOV, zero-filled trend, empty arrays.

**Errors:** `400` invalid period, `401`, `500`.

**Notes on `docs/api/openapi.yaml`:** that hand-written file does not cover
the dashboard reporting surface (overview, payments, refunds, and now
analytics are absent there by the same scoping). The generated
`swagger/swagger.{yaml,json}` from handler annotations is the contract source
for this endpoint.
