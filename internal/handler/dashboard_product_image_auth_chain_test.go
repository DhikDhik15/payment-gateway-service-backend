package handler_test

// Full-chain regression test: an upload request rejected with HTTP 401 by the
// REAL dashboard authentication middleware must not persist an image file.
//
// Chain exercised (all production code, no stubs in the request path):
//   - middleware.RequireDashboardAuth with a REAL service.AuthService
//     (test-only signing key; never touches repositories on these paths, so
//     the user/merchant repositories are nil and would panic if reached)
//   - middleware.RequireRole (OWNER/ADMIN)
//   - handler.DashboardProductImageHandler.UploadProductImage
//   - service.productImageService + service.LocalProductImageStore over a
//     temporary directory
//
// The expired JWT below is minted with the standard library only, mirroring
// the documented wire format in internal/service/jwt_util.go
// (constant header {"alg":"HS256","typ":"JWT"}, base64url payload, HMAC-SHA256).
// The signing key is test-only and random per run.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/dhikaarta/pay-gate-backend/internal/handler"
	"github.com/dhikaarta/pay-gate-backend/internal/middleware"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"io"
)

// countingProductImageService delegates to a real ProductImageService while
// observing whether Upload was invoked. ClearProductImage/ReadProductImageForServe
// are pure pass-throughs so the wrapper stays faithful to production behavior.
type countingProductImageService struct {
	inner       service.ProductImageService
	uploadCalls int
}

func (s *countingProductImageService) Upload(ctx context.Context, merchantID uuid.UUID, data []byte, claimedName string) (*service.ProductImageUpload, error) {
	s.uploadCalls++
	return s.inner.Upload(ctx, merchantID, data, claimedName)
}

func (s *countingProductImageService) ClearProductImage(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	return s.inner.ClearProductImage(ctx, merchantID, productID)
}

func (s *countingProductImageService) ReadProductImageForServe(merchantID uuid.UUID, filename string) (io.ReadSeekCloser, int64, string, error) {
	return s.inner.ReadProductImageForServe(merchantID, filename)
}

// mintExpiredDashboardToken builds a correctly-signed but already-expired
// dashboard access token for secret. Repository-independent: only the JWT
// signature and exp claim are evaluated before any database access.
func mintExpiredDashboardToken(t *testing.T, secret []byte) string {
	t.Helper()
	const headerB64 = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" // {"alg":"HS256","typ":"JWT"}
	payload, err := json.Marshal(map[string]any{
		"sub":  uuid.NewString(),
		"mid":  uuid.NewString(),
		"role": string(model.DashboardUserRoleOwner),
		"iat":  time.Now().UTC().Add(-2 * time.Hour).Unix(),
		"exp":  time.Now().UTC().Add(-time.Hour).Unix(),
		"jti":  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := headerB64 + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput)) //nolint:errcheck // hash.Write never fails
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

type authChainFixture struct {
	router *gin.Engine
	store  string // temporary storage root; must stay file-free on 401
	secret []byte // test-only JWT signing key (never a production secret)
	images *countingProductImageService
}

func newAuthChainFixture(t *testing.T) *authChainFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	secret := []byte("test-only-auth-chain-secret-32bytes!!")
	if len(secret) < 32 {
		t.Fatalf("test signing key must be at least 32 bytes")
	}
	authSvc := service.NewAuthService(nil, nil, service.AuthConfig{JWTSecret: secret})

	dir := t.TempDir()
	cfg := service.ProductImageConfig{Dir: dir, MaxBytes: 5 << 20, MaxDimension: 4096, MaxPixels: 4096 * 4096}
	store := service.NewLocalProductImageStore(cfg, nil)
	images := &countingProductImageService{inner: service.NewProductImageService(nil, store)}
	h := handler.NewDashboardProductImageHandler(images, cfg)

	r := gin.New()
	d := r.Group("/api/v1/dashboard",
		middleware.RequireDashboardAuth(authSvc, nil, nil),
	)
	d.POST("/product-images",
		middleware.RequireRole(model.DashboardUserRoleOwner, model.DashboardUserRoleAdmin),
		h.UploadProductImage,
	)
	return &authChainFixture{router: r, store: dir, secret: secret, images: images}
}

func assertNoStoredFiles(t *testing.T, dir string) {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk store dir: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("store dir must be file-free after 401, found %d file(s): %q", len(files), files)
	}
}

func TestUploadProductImageChain_MissingTokenPersistsNothing(t *testing.T) {
	f := newAuthChainFixture(t)
	data := readImageFixture(t, "valid.png")

	req := multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "shop photo.png", data)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %q)", w.Code, w.Body.String())
	}
	if f.images.uploadCalls != 0 {
		t.Errorf("upload service invoked %d time(s) for a 401 request, want 0", f.images.uploadCalls)
	}
	assertNoStoredFiles(t, f.store)
}

func TestUploadProductImageChain_ExpiredTokenPersistsNothing(t *testing.T) {
	f := newAuthChainFixture(t)
	data := readImageFixture(t, "valid.png")

	req := multipartImageRequest(t, "/api/v1/dashboard/product-images", "image", "shop photo.png", data)
	req.Header.Set("Authorization", "Bearer "+mintExpiredDashboardToken(t, f.secret))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Access token has expired") {
		t.Errorf("expected expiry-specific message, got body %q", w.Body.String())
	}
	if f.images.uploadCalls != 0 {
		t.Errorf("upload service invoked %d time(s) for a 401 request, want 0", f.images.uploadCalls)
	}
	assertNoStoredFiles(t, f.store)
}
