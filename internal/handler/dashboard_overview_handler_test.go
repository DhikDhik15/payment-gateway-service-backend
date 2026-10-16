package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

// Phase 10J: the overview date-range filter contract.
//
//   - Inclusive start / inclusive end from the user's perspective; the API
//     carries a half-open window via from (inclusive) / to (exclusive).
//   - Date-only values are UTC midnight; never 23:59:59.
//   - from/to must be paired, valid, and satisfy from < to.
//   - Requests without params keep the legacy default (current UTC month).

func overviewData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data in %s", body)
	}
	return data
}

func TestDashboardOverview_ValidRangePassesThrough(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2026-10-01&to=2026-10-08", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if deps.overviewSvc.lastFrom == nil || deps.overviewSvc.lastTo == nil {
		t.Fatalf("service never received the period")
	}
	if got := deps.overviewSvc.lastFrom.UTC().Format("2006-01-02"); got != "2026-10-01" {
		t.Fatalf("from = %v, want 2026-10-01 UTC midnight", deps.overviewSvc.lastFrom)
	}
	if got := deps.overviewSvc.lastTo.UTC().Format("2006-01-02"); got != "2026-10-08" {
		t.Fatalf("to = %v, want 2026-10-08 UTC midnight", deps.overviewSvc.lastTo)
	}
}

func TestDashboardOverview_SingleDayRange(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2024-02-29&to=2024-03-01", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("single leap-day range must be 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardOverview_RFC3339Range(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleAdmin)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2026-10-01T00:00:00Z&to=2026-10-08T00:00:00Z", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardOverview_HalfSuppliedRange400(t *testing.T) {
	for _, path := range []string{
		"/api/v1/dashboard/overview?from=2026-10-01",
		"/api/v1/dashboard/overview?to=2026-10-08",
	} {
		deps := newDashPhase9Router(model.DashboardUserRoleOwner)
		w := dashDo(deps.router, http.MethodGet, path, nil, "token")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestDashboardOverview_MalformedDate400(t *testing.T) {
	for _, path := range []string{
		"/api/v1/dashboard/overview?from=not-a-date&to=2026-10-08",
		"/api/v1/dashboard/overview?from=2026-10-01&to=2026-13-45",
		"/api/v1/dashboard/overview?from=2026-02-30&to=2026-03-01",
	} {
		deps := newDashPhase9Router(model.DashboardUserRoleOwner)
		w := dashDo(deps.router, http.MethodGet, path, nil, "token")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestDashboardOverview_StartEqualsEnd400(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2026-10-01&to=2026-10-01", nil, "token")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardOverview_NoParamsKeepsLegacyDefault(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if deps.overviewSvc.lastFrom != nil || deps.overviewSvc.lastTo != nil {
		t.Fatalf("no-param request must reach the service with nil period (service default), got %v..%v",
			deps.overviewSvc.lastFrom, deps.overviewSvc.lastTo)
	}
}

func TestDashboardOverview_ResponseCarriesUnsuccessfulField(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	now := time.Now().UTC()
	deps.overviewSvc.resp = &model.DashboardOverviewResponse{
		Period:   model.DashboardPeriod{From: now.AddDate(0, 0, -7), To: now},
		Payments: model.DashboardPaymentStats{Total: 5, Paid: 2, Failed: 1, Expired: 1, Cancelled: 1, UnsuccessfulTransactions: 3},
		Revenue:  model.DashboardRevenueStats{Amount: 0, Currency: "IDR"},
		Refunds:  model.DashboardRefundStats{Count: 0, Amount: 0, Currency: "IDR"},
	}
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2026-10-01&to=2026-10-08", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	payments := overviewData(t, w.Body.Bytes())["payments"].(map[string]any)
	if payments["unsuccessful_transactions"].(float64) != 3 {
		t.Fatalf("unsuccessful_transactions = %v, want 3", payments["unsuccessful_transactions"])
	}
	for _, key := range []string{"total", "paid", "pending", "failed", "expired", "cancelled"} {
		if _, ok := payments[key]; !ok {
			t.Fatalf("existing payments field %q missing (backward compat)", key)
		}
	}
}
