package model

import (
	"github.com/google/uuid"
)

// DashboardAnalyticsResponse is the GET /api/v1/dashboard/analytics payload.
// All figures are live DB aggregates scoped to the authenticated merchant and
// the requested half-open UTC window [Period.From, Period.To) — never mocked.
type DashboardAnalyticsResponse struct {
	Period           DashboardPeriod           `json:"period"`
	Summary          DashboardAnalyticsSummary `json:"summary"`
	Interval         string                    `json:"interval"`
	Currency         string                    `json:"currency"`
	RevenueTrend     []DashboardRevenueBucket  `json:"revenue_trend"`
	TopProducts      []DashboardProductSales   `json:"top_products"`
	RevenueByProduct []DashboardProductSales   `json:"revenue_by_product"`
}

// DashboardAnalyticsSummary carries the four headline metrics.
//
// TotalRevenue is GROSS paid revenue (SUM of PAID transaction amounts,
// including order-less legacy payments) — identical to the overview revenue
// definition. Refunds are reported separately and never subtracted.
// SuccessfulPayments counts PAID transaction rows (at most one per order).
// UnitsSold sums immutable order-item quantities of unique paid orders.
// AverageOrderValue is order-attributed paid value divided by unique paid
// orders; nil when no order was paid in the period.
type DashboardAnalyticsSummary struct {
	TotalRevenue       int64  `json:"total_revenue"`
	SuccessfulPayments int64  `json:"successful_payments"`
	UnitsSold          int64  `json:"units_sold"`
	AverageOrderValue  *int64 `json:"average_order_value"`
}

// DashboardRevenueBucket is one trend point. Date is the bucket's UTC
// calendar day (YYYY-MM-DD); for weekly buckets it is the Monday starting
// the week. Revenue is gross paid revenue with paid_at in that bucket.
// Empty buckets are returned with zero revenue — never omitted, never faked.
type DashboardRevenueBucket struct {
	Date    string `json:"date"`
	Revenue int64  `json:"revenue"`
}

// DashboardProductSales aggregates immutable order-item snapshots for one
// product within the period. ProductName is the latest snapshot name, so
// renamed products stay recognizable while historical quantities and prices
// are preserved. ProductID is nil for items whose catalog product was deleted
// (product_id SET NULL) — such rows group by snapshot name. Ranked by the
// caller: units sold for TopProducts, revenue for RevenueByProduct.
type DashboardProductSales struct {
	ProductID   *uuid.UUID `json:"product_id"`
	ProductName string     `json:"product_name"`
	UnitsSold   int64      `json:"units_sold"`
	Revenue     int64      `json:"revenue"`
}

// Paid-order inclusion: an order belongs to the window when the paid_at of
// its single PAID transaction falls in [from, to). The partial unique index
// uq_transactions_order_paid guarantees at most one PAID row per order, so
// joins against paid transactions never duplicate sales. Orders are never
// matched on orders.created_at.
