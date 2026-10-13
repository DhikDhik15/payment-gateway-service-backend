package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── stub image service ──────────────────────────────────────────────────────

type stubProductImageService struct {
	uploadResult *service.ProductImageUpload
	uploadErr    error
	uploadCalls  int
	lastMerchant uuid.UUID
	lastName     string
	lastData     []byte

	clearResult *model.Product
	clearErr    error

	serveData        []byte
	serveContentType string
	serveErr         error
}

func (s *stubProductImageService) Upload(_ context.Context, merchantID uuid.UUID, data []byte, claimedName string) (*service.ProductImageUpload, error) {
	s.uploadCalls++
	s.lastMerchant, s.lastData, s.lastName = merchantID, data, claimedName
	if s.uploadErr != nil {
		return nil, s.uploadErr
	}
	return s.uploadResult, nil
}

func (s *stubProductImageService) ClearProductImage(_ context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	s.lastMerchant = merchantID
	_ = productID
	if s.clearErr != nil {
		return nil, s.clearErr
	}
	return s.clearResult, nil
}

type seekReader struct {
	*bytes.Reader
}

func (s seekReader) Close() error { return nil }

func (s *stubProductImageService) ReadProductImageForServe(_ uuid.UUID, _ string) (io.ReadSeekCloser, int64, string, error) {
	if s.serveErr != nil {
		return nil, 0, "", s.serveErr
	}
	return seekReader{bytes.NewReader(s.serveData)}, int64(len(s.serveData)), s.serveContentType, nil
}

// ─── router + request helpers ────────────────────────────────────────────────

func testImageUser(merchant uuid.UUID, role model.DashboardUserRole) *model.MerchantUser {
	return &model.MerchantUser{ID: uuid.New(), MerchantID: merchant, Email: "owner@example.com", Role: role, Status: model.DashboardUserStatusActive}
}

func newProductImageRouter(stub *stubProductImageService, user *model.MerchantUser, cfg service.ProductImageConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardProductImageHandler(stub, cfg)
	auth := func(c *gin.Context) {
		if user != nil {
			c.Set(model.ContextKeyDashboardUser, user)
		}
		c.Next()
	}
	d := r.Group("/api/v1/dashboard", auth)
	d.POST("/product-images",
		middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
		h.UploadProductImage,
	)
	d.DELETE("/products/:product_id/image",
		middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
		h.DeleteProductImage,
	)
	r.GET("/api/v1/product-images/:merchant_id/:filename", h.ServeProductImage)
	return r
}

func testImageCfg(t *testing.T) service.ProductImageConfig {
	t.Helper()
	return service.ProductImageConfig{Dir: t.TempDir(), MaxBytes: 5 << 20, MaxDimension: 4096, MaxPixels: 4096 * 4096}
}

func multipartImageRequest(t *testing.T, path, field, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func readImageFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "service", "testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return data
}

func decodeImageEnvelope(t *testing.T, w *httptest.ResponseRecorder) productEnvelope {
	t.Helper()
	return decodeProductEnvelope(t, w)
}

// ─── upload ──────────────────────────────────────────────────────────────────

func TestUploadProductImageSuccess(t *testing.T) {
	merchant := uuid.New()
	data := readImageFixture(t, "valid.png")
	stub := &stubProductImageService{
		uploadResult: &service.ProductImageUpload{
			ImageURL:    model.ProductImageURLPrefix + merchant.String() + "/" + uuid.NewString() + ".png",
			Format:      "png",
			Width:       16,
			Height:      12,
			SizeBytes:   int64(len(data)),
			ContentType: "image/png",
		},
	}
	r := newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleOwner), testImageCfg(t))

	req := multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "shop photo.png", data)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %q", w.Code, w.Body.String())
	}
	env := decodeImageEnvelope(t, w)
	if !env.Success {
		t.Fatalf("success = false, body %q", w.Body.String())
	}
	var up service.ProductImageUpload
	if err := json.Unmarshal(env.Data, &up); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if !model.IsLocalProductImageRef(up.ImageURL) {
		t.Errorf("image_url %q is not a local reference", up.ImageURL)
	}
	// Merchant context comes from the authenticated session, never the body.
	if stub.lastMerchant != merchant {
		t.Errorf("merchant = %v, want %v", stub.lastMerchant, merchant)
	}
	if !bytes.Equal(stub.lastData, data) {
		t.Errorf("service received %d bytes, want %d", len(stub.lastData), len(data))
	}
}

func TestUploadProductImageAuth(t *testing.T) {
	data := readImageFixture(t, "valid.png")
	stub := &stubProductImageService{uploadResult: &service.ProductImageUpload{}}
	newReq := func() *http.Request {
		return multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "a.png", data)
	}

	// No authenticated user → 401, service never runs.
	r := newProductImageRouter(stub, nil, testImageCfg(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, newReq())
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", w.Code)
	}

	// VIEWER role → 403, service never runs.
	merchant := uuid.New()
	r = newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleViewer), testImageCfg(t))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newReq())
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", w.Code)
	}
	if stub.uploadCalls != 0 {
		t.Errorf("service must not run for rejected callers, calls = %d", stub.uploadCalls)
	}

	// ADMIN is allowed.
	r = newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleAdmin), testImageCfg(t))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newReq())
	if w.Code != http.StatusCreated {
		t.Errorf("admin: status = %d, want 201 (body %q)", w.Code, w.Body.String())
	}
}

func TestUploadProductImageValidation(t *testing.T) {
	merchant := uuid.New()
	newRouter := func(stub *stubProductImageService, cfg service.ProductImageConfig) *gin.Engine {
		return newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleOwner), cfg)
	}

	// Missing file → 400 with the field name in details.
	r := newRouter(&stubProductImageService{}, testImageCfg(t))
	var empty bytes.Buffer
	w := multipart.NewWriter(&empty)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/product-images", &empty)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing file: status = %d, want 400", rec.Code)
	}
	if env := decodeImageEnvelope(t, rec); env.Error.Details["image"] == "" {
		t.Errorf("missing file: expected field error for \"image\", body %q", rec.Body.String())
	}

	// Wrong field name → 400.
	r = newRouter(&stubProductImageService{}, testImageCfg(t))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, multipartImageRequest(t, "/api/v1/dashboard/product-images", "file", "a.png", readImageFixture(t, "valid.png")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("wrong field: status = %d, want 400", rec.Code)
	}

	// Oversized body → 413 (tiny limit exercises the LimitReader cap, not
	// just the advisory multipart size).
	tiny := testImageCfg(t)
	tiny.MaxBytes = 10
	r = newRouter(&stubProductImageService{}, tiny)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "a.png", readImageFixture(t, "valid.png")))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: status = %d, want 413", rec.Code)
	}

	// Service-level rejections map to stable client codes.
	mapped := []struct {
		name   string
		err    error
		status int
	}{
		{"unsupported", service.ErrProductImageUnsupportedType, http.StatusBadRequest},
		{"invalid", service.ErrProductImageInvalid, http.StatusBadRequest},
		{"dimensions", service.ErrProductImageDimensions, http.StatusBadRequest},
		{"too large", service.ErrProductImageTooLarge, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range mapped {
		r = newRouter(&stubProductImageService{uploadErr: tc.err}, testImageCfg(t))
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "a.png", readImageFixture(t, "valid.png")))
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d (body %q)", tc.name, rec.Code, tc.status, rec.Body.String())
		}
	}
}

func TestUploadProductImageStorageFailureHidesInternals(t *testing.T) {
	merchant := uuid.New()
	// The wrapped error carries a filesystem path; the response must not.
	stub := &stubProductImageService{uploadErr: service.ErrProductImageStore}
	r := newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleOwner), testImageCfg(t))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "a.png", readImageFixture(t, "valid.png")))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/app/storage") || strings.Contains(rec.Body.String(), "product-images") {
		t.Errorf("internal paths leaked: %q", rec.Body.String())
	}
}

// ─── clear ───────────────────────────────────────────────────────────────────

func TestDeleteProductImage(t *testing.T) {
	merchant := uuid.New()
	productID := uuid.New()
	cleared := &model.Product{ID: productID, MerchantID: merchant, Name: "Widget", Price: 100, Currency: "IDR"}
	stub := &stubProductImageService{clearResult: cleared}
	r := newProductImageRouter(stub, testImageUser(merchant, model.DashboardUserRoleAdmin), testImageCfg(t))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/dashboard/products/"+productID.String()+"/image", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if stub.lastMerchant != merchant {
		t.Errorf("merchant = %v, want %v (must come from the session)", stub.lastMerchant, merchant)
	}

	// Unknown product / imageless product → 404.
	r = newProductImageRouter(&stubProductImageService{clearErr: service.ErrProductImageNotFound}, testImageUser(merchant, model.DashboardUserRoleOwner), testImageCfg(t))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/dashboard/products/"+uuid.NewString()+"/image", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing image: status = %d, want 404", rec.Code)
	}

	// Bad UUID → 400, service never runs.
	spy := &stubProductImageService{clearResult: cleared}
	r = newProductImageRouter(spy, testImageUser(merchant, model.DashboardUserRoleOwner), testImageCfg(t))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/dashboard/products/not-a-uuid/image", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad uuid: status = %d, want 400", rec.Code)
	}

	// VIEWER cannot clear.
	r = newProductImageRouter(spy, testImageUser(merchant, model.DashboardUserRoleViewer), testImageCfg(t))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/dashboard/products/"+productID.String()+"/image", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", rec.Code)
	}
}

// ─── serve ───────────────────────────────────────────────────────────────────

func TestServeProductImage(t *testing.T) {
	merchant := uuid.New()
	filename := uuid.NewString() + ".png"
	data := readImageFixture(t, "valid.png")
	stub := &stubProductImageService{serveData: data, serveContentType: "image/png"}

	// Wire a router whose stub validates the merchant/filename binding the
	// way the real service does (strict reference grammar).
	validating := &validatingServeStub{stub: stub, merchant: merchant, filename: filename}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := handler.NewDashboardProductImageHandler(validating, testImageCfg(t))
	r.GET("/api/v1/product-images/:merchant_id/:filename", h.ServeProductImage)

	// Public: no dashboard user, still served.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/product-images/"+merchant.String()+"/"+filename, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing X-Content-Type-Options: nosniff")
	}
	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Errorf("served %d bytes, want %d", rec.Body.Len(), len(data))
	}

	// Traversal, unknown files, and bad UUIDs → bare 404 with no detail.
	for name, path := range map[string]string{
		"traversal":  "/api/v1/product-images/" + merchant.String() + "/..%2F..%2Fetc%2Fpasswd",
		"unknown":    "/api/v1/product-images/" + merchant.String() + "/" + uuid.NewString() + ".png",
		"bad uuid":   "/api/v1/product-images/not-a-uuid/" + filename,
		"bad suffix": "/api/v1/product-images/" + merchant.String() + "/" + filename + ".exe",
	} {
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, rec.Code)
		}
	}
}

// validatingServeStub enforces the same merchant/filename binding as the real
// store so the handler test proves the URL cannot address foreign files.
type validatingServeStub struct {
	stub     *stubProductImageService
	merchant uuid.UUID
	filename string
}

func (s *validatingServeStub) Upload(ctx context.Context, merchantID uuid.UUID, data []byte, name string) (*service.ProductImageUpload, error) {
	return s.stub.Upload(ctx, merchantID, data, name)
}

func (s *validatingServeStub) ClearProductImage(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	return s.stub.ClearProductImage(ctx, merchantID, productID)
}

func (s *validatingServeStub) ReadProductImageForServe(merchantID uuid.UUID, filename string) (io.ReadSeekCloser, int64, string, error) {
	if merchantID != s.merchant || filename != s.filename {
		return nil, 0, "", service.ErrProductImageNotFound
	}
	return s.stub.ReadProductImageForServe(merchantID, filename)
}
