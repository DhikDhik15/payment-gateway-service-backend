package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── stubs for Phase 9 dashboard APIs ─────────────────────────────────────────

type stubOverviewService struct {
	resp           *model.DashboardOverviewResponse
	err            error
	lastMerchantID uuid.UUID
}

func (s *stubOverviewService) GetOverview(_ context.Context, merchantID uuid.UUID, _, _ *time.Time) (*model.DashboardOverviewResponse, error) {
	s.lastMerchantID = merchantID
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &model.DashboardOverviewResponse{
		Period:         model.DashboardPeriod{From: time.Now().UTC(), To: time.Now().UTC()},
		Payments:       model.DashboardPaymentStats{},
		Revenue:        model.DashboardRevenueStats{Amount: 0, Currency: "IDR"},
		Refunds:        model.DashboardRefundStats{Count: 0, Amount: 0, Currency: "IDR"},
		RecentPayments: []model.PaymentResponse{},
	}, nil
}

type stubPaymentService struct {
	listResult   *model.ListPaymentsResult
	listErr      error
	getResp      *model.PaymentResponse
	getErr       error
	createResp   *model.CreatePaymentResponse
	createErr    error
	lastMerchant uuid.UUID
	lastFilter   model.TransactionListFilter
	lastTxID     uuid.UUID
	createCalled bool
}

func (s *stubPaymentService) CreatePayment(context.Context, uuid.UUID, model.CreatePaymentRequest) (*model.CreatePaymentResponse, error) {
	return nil, nil
}
func (s *stubPaymentService) CreatePaymentWithIdempotency(_ context.Context, merchantID uuid.UUID, _ model.CreatePaymentRequest, _ string) (*model.CreatePaymentResponse, error) {
	s.lastMerchant = merchantID
	s.createCalled = true
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResp, nil
}
func (s *stubPaymentService) CancelPayment(context.Context, uuid.UUID, uuid.UUID) (*model.PaymentResponse, error) {
	return nil, nil
}
func (s *stubPaymentService) GetPayment(_ context.Context, merchantID, txID uuid.UUID) (*model.PaymentResponse, error) {
	s.lastMerchant = merchantID
	s.lastTxID = txID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResp, nil
}
func (s *stubPaymentService) ListPayments(_ context.Context, merchantID uuid.UUID, filter model.TransactionListFilter) (*model.ListPaymentsResult, error) {
	s.lastMerchant = merchantID
	s.lastFilter = filter
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listResult != nil {
		return s.listResult, nil
	}
	page, limit := filter.Page, filter.Limit
	if page == 0 {
		page = model.DefaultPage
	}
	if limit == 0 {
		limit = model.DefaultLimit
	}
	return &model.ListPaymentsResult{
		Transactions: []model.PaymentResponse{},
		Total:        0,
		TotalPages:   0,
		Page:         page,
		Limit:        limit,
	}, nil
}

type stubDashRefundService struct {
	createResp   *model.RefundResponse
	createErr    error
	getResp      *model.RefundResponse
	getErr       error
	listResult   *service.ListRefundsResult
	listErr      error
	lastMerchant uuid.UUID
	createCalled bool
}

func (s *stubDashRefundService) CreateRefundWithIdempotency(_ context.Context, merchantID, _ uuid.UUID, _ model.CreateRefundRequest, _ string) (*model.RefundResponse, error) {
	s.lastMerchant = merchantID
	s.createCalled = true
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResp, nil
}
func (s *stubDashRefundService) GetRefund(_ context.Context, merchantID, _ uuid.UUID) (*model.RefundResponse, error) {
	s.lastMerchant = merchantID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResp, nil
}
func (s *stubDashRefundService) ListRefunds(context.Context, uuid.UUID, uuid.UUID, model.RefundListFilter) (*service.ListRefundsResult, error) {
	return nil, nil
}
func (s *stubDashRefundService) ListRefundsByMerchant(_ context.Context, merchantID uuid.UUID, filter model.RefundListFilter) (*service.ListRefundsResult, error) {
	s.lastMerchant = merchantID
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.listResult != nil {
		return s.listResult, nil
	}
	return &service.ListRefundsResult{Refunds: []model.RefundResponse{}, Page: filter.Page, Limit: filter.Limit}, nil
}

type stubAPIKeyService struct {
	list         []model.MerchantAPIKeyResponse
	createResp   *model.CreateMerchantAPIKeyResponse
	createErr    error
	revokeErr    error
	lastMerchant uuid.UUID
	createCalled bool
	revokeCalled bool
}

func (s *stubAPIKeyService) CreateKey(_ context.Context, merchantID uuid.UUID, _ model.CreateMerchantAPIKeyRequest) (*model.CreateMerchantAPIKeyResponse, error) {
	s.lastMerchant = merchantID
	s.createCalled = true
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createResp, nil
}
func (s *stubAPIKeyService) ListKeys(_ context.Context, merchantID uuid.UUID) ([]model.MerchantAPIKeyResponse, error) {
	s.lastMerchant = merchantID
	return s.list, nil
}
func (s *stubAPIKeyService) RevokeKey(_ context.Context, merchantID, _ uuid.UUID) error {
	s.lastMerchant = merchantID
	s.revokeCalled = true
	return s.revokeErr
}
func (s *stubAPIKeyService) RotateKey(context.Context, uuid.UUID, uuid.UUID) (*model.RotateMerchantAPIKeyResponse, error) {
	return nil, nil
}
func (s *stubAPIKeyService) AuthenticateByAPIKey(context.Context, string) (*model.Merchant, error) {
	return nil, nil
}

type stubMerchantService struct {
	resp   *model.GetMerchantResponse
	err    error
	lastID uuid.UUID
}

func (s *stubMerchantService) CreateMerchant(context.Context, model.CreateMerchantRequest) (*model.CreateMerchantResponse, error) {
	return nil, nil
}
func (s *stubMerchantService) GetMerchant(_ context.Context, id uuid.UUID) (*model.GetMerchantResponse, error) {
	s.lastID = id
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}
func (s *stubMerchantService) GetMerchantByAPIKey(context.Context, string) (*model.Merchant, error) {
	return nil, nil
}

func (s *stubMerchantService) UpdateMerchantStatus(_ context.Context, _ uuid.UUID, _ model.MerchantStatus) (*model.GetMerchantResponse, error) {
	return s.resp, nil
}

type stubReconService struct {
	listResult *service.ListReconResultsResult
	getResult  *model.ReconciliationResult
	getErr     error
	lastFilter model.ReconciliationListFilter
}

func (s *stubReconService) ReconcileSettlement(context.Context, uuid.UUID) (*model.ReconciliationSummary, error) {
	return nil, nil
}
func (s *stubReconService) ListResults(_ context.Context, filter model.ReconciliationListFilter) (*service.ListReconResultsResult, error) {
	s.lastFilter = filter
	if s.listResult != nil {
		return s.listResult, nil
	}
	return &service.ListReconResultsResult{Results: []model.ReconciliationResult{}, Page: filter.Page, Limit: filter.Limit}, nil
}
func (s *stubReconService) GetResult(_ context.Context, _ uuid.UUID) (*model.ReconciliationResult, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getResult, nil
}

// ─── router ───────────────────────────────────────────────────────────────────

type dashPhase9Deps struct {
	router      *gin.Engine
	user        *model.MerchantUser
	overviewSvc *stubOverviewService
	paymentSvc  *stubPaymentService
	refundSvc   *stubDashRefundService
	apiKeySvc   *stubAPIKeyService
	merchantSvc *stubMerchantService
	reconSvc    *stubReconService
}

func newDashPhase9Router(role model.DashboardUserRole) *dashPhase9Deps {
	gin.SetMode(gin.TestMode)

	merchantID := uuid.New()
	userID := uuid.New()
	user := &model.MerchantUser{
		ID:         userID,
		MerchantID: merchantID,
		Email:      "owner@test.com",
		Role:       role,
		Status:     model.DashboardUserStatusActive,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}

	authSvc := &mockAuthService{
		verifyClaims: &service.JWTClaims{
			MerchantID: merchantID.String(),
			Role:       string(role),
		},
	}
	// Subject must be set for middleware uuid.Parse
	authSvc.verifyClaims.Subject = userID.String()

	userRepo := &mockUserRepoForMiddleware{user: user}

	overviewSvc := &stubOverviewService{}
	paymentSvc := &stubPaymentService{}
	refundSvc := &stubDashRefundService{}
	apiKeySvc := &stubAPIKeyService{list: []model.MerchantAPIKeyResponse{}}
	merchantSvc := &stubMerchantService{
		resp: &model.GetMerchantResponse{
			ID: merchantID, Name: "Test Merchant", Code: "test", Status: model.MerchantStatusActive,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		},
	}
	reconSvc := &stubReconService{}

	r := gin.New()
	r.Use(middleware.RequestID())

	overviewH := handler.NewDashboardOverviewHandler(overviewSvc)
	paymentH := handler.NewDashboardPaymentHandler(paymentSvc, refundSvc)
	refundH := handler.NewDashboardRefundHandler(refundSvc)
	apiKeyH := handler.NewDashboardAPIKeyHandler(apiKeySvc)
	settingsH := handler.NewDashboardSettingsHandler(merchantSvc)
	reconH := handler.NewDashboardReconciliationHandler(reconSvc)

	dash := r.Group("/api/v1/dashboard")
	dash.Use(middleware.RequireDashboardAuth(authSvc, userRepo, newActiveMerchantRepo(merchantID)))
	{
		dash.GET("/overview", overviewH.GetOverview)
		dash.GET("/payments", paymentH.ListPayments)
		dash.POST("/payments",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			paymentH.CreatePayment,
		)
		dash.GET("/payments/:payment_id", paymentH.GetPayment)
		dash.POST("/payments/:payment_id/refunds",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			paymentH.CreateRefund,
		)
		dash.GET("/refunds", refundH.ListRefunds)
		dash.GET("/refunds/:refund_id", refundH.GetRefund)
		dash.GET("/api-keys", apiKeyH.ListAPIKeys)
		dash.POST("/api-keys",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			apiKeyH.CreateAPIKey,
		)
		dash.POST("/api-keys/:id/revoke",
			middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
			apiKeyH.RevokeAPIKey,
		)
		dash.GET("/settings", settingsH.GetSettings)
		dash.GET("/reconciliation/mismatches", reconH.ListMismatches)
		dash.GET("/reconciliation/mismatches/:id", reconH.GetMismatch)
	}

	return &dashPhase9Deps{
		router: r, user: user,
		overviewSvc: overviewSvc, paymentSvc: paymentSvc, refundSvc: refundSvc,
		apiKeySvc: apiKeySvc, merchantSvc: merchantSvc, reconSvc: reconSvc,
	}
}

func dashDo(r *gin.Engine, method, path string, body []byte, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── auth ─────────────────────────────────────────────────────────────────────

func TestDashboardPhase9_MissingBearer_401(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardPhase9_Overview_UsesAuthenticatedMerchant(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview", nil, "valid-token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if deps.overviewSvc.lastMerchantID != deps.user.MerchantID {
		t.Fatalf("overview used merchant %s, want %s", deps.overviewSvc.lastMerchantID, deps.user.MerchantID)
	}

	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	data := env["data"].(map[string]any)
	revenue := data["revenue"].(map[string]any)
	if revenue["amount"].(float64) != 0 {
		t.Fatalf("empty overview must return revenue amount 0, got %v", revenue["amount"])
	}
}

func TestDashboardPhase9_PaymentsList_MerchantScoped(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	txID := uuid.New()
	deps.paymentSvc.listResult = &model.ListPaymentsResult{
		Transactions: []model.PaymentResponse{{
			TransactionID:   txID,
			MerchantOrderID: "ord-1",
			Amount:          1000,
			Currency:        "IDR",
			Status:          model.TransactionStatusPaid,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		}},
		Total: 1, TotalPages: 1, Page: 1, Limit: 20,
	}

	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if deps.paymentSvc.lastMerchant != deps.user.MerchantID {
		t.Fatalf("list used wrong merchant")
	}
}

// TestDashboardPhase9_PaymentsList_EnvelopeWithPayment locks the contract the
// dashboard SPA reads: success + data[] + flat meta.page/limit/total/total_pages.
func TestDashboardPhase9_PaymentsList_EnvelopeWithPayment(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	txID := uuid.New()
	deps.paymentSvc.listResult = &model.ListPaymentsResult{
		Transactions: []model.PaymentResponse{{
			TransactionID:   txID,
			MerchantOrderID: "ORDER-001",
			Amount:          50000,
			Currency:        "IDR",
			Status:          model.TransactionStatusPending,
			CreatedAt:       time.Now().UTC(),
			UpdatedAt:       time.Now().UTC(),
		}},
		Total: 1, TotalPages: 1, Page: 1, Limit: 10,
	}

	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments?page=1&limit=10", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var env struct {
		Success bool `json:"success"`
		Data    []struct {
			TransactionID   string `json:"transaction_id"`
			MerchantOrderID string `json:"merchant_order_id"`
			Amount          int64  `json:"amount"`
			Currency        string `json:"currency"`
			Status          string `json:"status"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !env.Success {
		t.Fatal("expected success=true")
	}
	if len(env.Data) != 1 {
		t.Fatalf("expected data.length==1, got %d", len(env.Data))
	}
	if env.Data[0].TransactionID != txID.String() || env.Data[0].MerchantOrderID != "ORDER-001" {
		t.Fatalf("unexpected payment row: %+v", env.Data[0])
	}
	if _, ok := env.Meta["pagination"]; ok {
		t.Fatal("API contract uses flat meta.total_pages, not meta.pagination")
	}
	for _, key := range []string{"page", "limit", "total", "total_pages"} {
		if _, ok := env.Meta[key]; !ok {
			t.Fatalf("meta missing %q: %+v", key, env.Meta)
		}
	}
	if int(env.Meta["total"].(float64)) != 1 || int(env.Meta["total_pages"].(float64)) != 1 {
		t.Fatalf("unexpected pagination meta: %+v", env.Meta)
	}
	if deps.paymentSvc.lastFilter.Page != 1 || deps.paymentSvc.lastFilter.Limit != 10 {
		t.Fatalf("filter page/limit: got page=%d limit=%d", deps.paymentSvc.lastFilter.Page, deps.paymentSvc.lastFilter.Limit)
	}
}

func TestDashboardPhase9_PaymentsList_EmptyMerchant(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	deps.paymentSvc.listResult = &model.ListPaymentsResult{
		Transactions: []model.PaymentResponse{},
		Total:        0, TotalPages: 0, Page: 1, Limit: 10,
	}

	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments?page=1&limit=10", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Meta    map[string]any  `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Success {
		t.Fatal("expected success=true")
	}
	if string(env.Data) != "[]" {
		t.Fatalf("expected data=[], got %s", env.Data)
	}
	if int(env.Meta["total"].(float64)) != 0 || int(env.Meta["total_pages"].(float64)) != 0 {
		t.Fatalf("empty list meta: %+v", env.Meta)
	}
}

func TestDashboardPhase9_PaymentsList_SearchAndStatusFilters(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleAdmin)
	deps.paymentSvc.listResult = &model.ListPaymentsResult{
		Transactions: []model.PaymentResponse{},
		Total:        0, TotalPages: 0, Page: 1, Limit: 10,
	}

	w := dashDo(deps.router, http.MethodGet,
		"/api/v1/dashboard/payments?page=1&limit=10&status=PENDING&search=ORDER-001", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	f := deps.paymentSvc.lastFilter
	if f.Status == nil || *f.Status != model.TransactionStatusPending {
		t.Fatalf("expected status=PENDING, got %#v", f.Status)
	}
	if f.Search == nil || *f.Search != "ORDER-001" {
		t.Fatalf("expected search=ORDER-001, got %#v", f.Search)
	}
}

func TestDashboardPhase9_PaymentsList_RejectsLiteralAllStatus(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments?status=all", nil, "token")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for status=all, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestDashboardPhase9_PaymentDetail_NotFoundCrossMerchant(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleAdmin)
	deps.paymentSvc.getErr = repository.ErrTransactionNotFound

	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments/"+uuid.New().String(), nil, "token")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestDashboardPhase9_PaymentDetail_Timeline(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	paidAt := time.Now().UTC().Add(-time.Hour)
	created := paidAt.Add(-time.Hour)
	txID := uuid.New()
	deps.paymentSvc.getResp = &model.PaymentResponse{
		TransactionID:   txID,
		MerchantOrderID: "ord-2",
		Amount:          5000,
		Currency:        "IDR",
		Status:          model.TransactionStatusPaid,
		PaidAt:          &paidAt,
		CreatedAt:       created,
		UpdatedAt:       paidAt,
	}

	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/payments/"+txID.String(), nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Timeline []struct {
				Type string `json:"type"`
			} `json:"timeline"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Timeline) != 2 {
		t.Fatalf("expected CREATED+PAID timeline, got %+v", env.Data.Timeline)
	}
	if env.Data.Timeline[0].Type != "CREATED" || env.Data.Timeline[1].Type != "PAID" {
		t.Fatalf("unexpected timeline: %+v", env.Data.Timeline)
	}
}

func TestDashboardPhase9_CreateRefund_ViewerForbidden(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	body, _ := json.Marshal(model.CreateRefundRequest{Amount: 100, Currency: "IDR"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/payments/"+uuid.New().String()+"/refunds", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	deps.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer expected 403, got %d", w.Code)
	}
	if deps.refundSvc.createCalled {
		t.Fatal("viewer must not call refund service")
	}
}

func TestDashboardPhase9_CreateRefund_OwnerAllowed(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	deps.refundSvc.createResp = &model.RefundResponse{
		RefundID: uuid.New(), Amount: 100, Currency: "IDR", Status: model.RefundStatusSucceeded,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), RequestedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(model.CreateRefundRequest{Amount: 100, Currency: "IDR"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/payments/"+uuid.New().String()+"/refunds", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	deps.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if deps.refundSvc.lastMerchant != deps.user.MerchantID {
		t.Fatal("refund create must use authenticated merchant")
	}
}

func TestDashboardPhase9_APIKeyCreate_ViewerForbidden(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	body, _ := json.Marshal(model.CreateMerchantAPIKeyRequest{Name: "prod"})
	w := dashDo(deps.router, http.MethodPost, "/api/v1/dashboard/api-keys", body, "token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestDashboardPhase9_APIKeyCreate_AdminSecretOnce(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleAdmin)
	deps.apiKeySvc.createResp = &model.CreateMerchantAPIKeyResponse{
		ID: uuid.New(), MerchantID: deps.user.MerchantID, Name: "prod",
		KeyID: "pk_abc", Secret: "sk_secret_once", Status: model.MerchantAPIKeyStatusActive,
		CreatedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(model.CreateMerchantAPIKeyRequest{Name: "prod"})
	w := dashDo(deps.router, http.MethodPost, "/api/v1/dashboard/api-keys", body, "token")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("sk_secret_once")) {
		t.Fatal("create response must include one-time secret")
	}

	deps.apiKeySvc.list = []model.MerchantAPIKeyResponse{{
		ID: deps.apiKeySvc.createResp.ID, MerchantID: deps.user.MerchantID,
		Name: "prod", KeyID: "pk_abc", Status: model.MerchantAPIKeyStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
	w2 := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/api-keys", nil, "token")
	if w2.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d", w2.Code)
	}
	if bytes.Contains(w2.Body.Bytes(), []byte("sk_secret_once")) || bytes.Contains(w2.Body.Bytes(), []byte(`"secret"`)) {
		t.Fatal("list must never include secret")
	}
}

func TestDashboardPhase9_Settings_NoSecrets(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/settings", nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if deps.merchantSvc.lastID != deps.user.MerchantID {
		t.Fatal("settings must use authenticated merchant")
	}
	body := w.Body.String()
	for _, leak := range []string{"api_secret", "api_key", "password", "secret_hash"} {
		if bytes.Contains([]byte(body), []byte(leak)) {
			t.Fatalf("settings leaked %q", leak)
		}
	}
}

func TestDashboardPhase9_Reconciliation_ForcesMerchantID(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/reconciliation/mismatches?merchant_id="+uuid.New().String(), nil, "token")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if deps.reconSvc.lastFilter.MerchantID == nil || *deps.reconSvc.lastFilter.MerchantID != deps.user.MerchantID {
		t.Fatal("mismatches must force authenticated merchant_id")
	}
}

func TestDashboardPhase9_Reconciliation_GetMismatch_CrossMerchant404(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	other := uuid.New()
	deps.reconSvc.getResult = &model.ReconciliationResult{
		ID: uuid.New(), MerchantID: &other, Status: model.ReconciliationResultMismatch,
	}
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/reconciliation/mismatches/"+deps.reconSvc.getResult.ID.String(), nil, "token")
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-merchant mismatch expected 404, got %d", w.Code)
	}
}

func TestDashboardPhase9_Overview_InvalidPeriod_400(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	w := dashDo(deps.router, http.MethodGet, "/api/v1/dashboard/overview?from=2026-09-30&to=2026-09-01", nil, "token")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─── dashboard payments create ───────────────────────────────────────────────

func TestDashboardPhase9_CreatePayment_Success(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleAdmin)
	txID := uuid.New()
	deps.paymentSvc.createResp = &model.CreatePaymentResponse{
		TransactionID:   txID,
		MerchantOrderID: "ord-dash-1",
		Amount:          150000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
		Status:          model.TransactionStatusPending,
		CreatedAt:       time.Now().UTC(),
	}

	body, _ := json.Marshal(model.CreatePaymentRequest{
		MerchantOrderID: "ord-dash-1",
		Amount:          150000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/payments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Idempotency-Key", "dash-key-1")

	w := httptest.NewRecorder()
	deps.router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if !deps.paymentSvc.createCalled {
		t.Fatal("payment service not called")
	}
	if deps.paymentSvc.lastMerchant != deps.user.MerchantID {
		t.Fatal("payment create used wrong merchant")
	}
}

func TestDashboardPhase9_CreatePayment_MissingIdempotency_400(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleOwner)
	body, _ := json.Marshal(model.CreatePaymentRequest{
		MerchantOrderID: "ord-1",
		Amount:          1000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
	})

	w := dashDo(deps.router, http.MethodPost, "/api/v1/dashboard/payments", body, "token")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Idempotency-Key") {
		t.Fatal("error must mention Idempotency-Key")
	}
}

func TestDashboardPhase9_CreatePayment_ViewerForbidden(t *testing.T) {
	deps := newDashPhase9Router(model.DashboardUserRoleViewer)
	body, _ := json.Marshal(model.CreatePaymentRequest{
		MerchantOrderID: "ord-1",
		Amount:          1000,
		Currency:        "IDR",
		PaymentMethod:   "QRIS",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/payments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Idempotency-Key", "viewer-key")

	w := httptest.NewRecorder()
	deps.router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer expected 403, got %d", w.Code)
	}
}
