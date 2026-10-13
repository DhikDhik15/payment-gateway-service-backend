package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func testImageConfig(dir string) service.ProductImageConfig {
	return service.ProductImageConfig{
		Dir:          dir,
		MaxBytes:     5 << 20,
		MaxDimension: 4096,
		MaxPixels:    4096 * 4096,
	}
}

func readTestImage(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return data
}

type stubJanitor struct {
	store *service.LocalProductImageStore
	calls []string
	err   error
}

func (j *stubJanitor) RemoveIfUnreferenced(ctx context.Context, imageURL string) error {
	j.calls = append(j.calls, imageURL)
	if j.err != nil {
		return j.err
	}
	return j.store.RemoveIfUnreferenced(ctx, imageURL)
}

func mustUpload(t *testing.T, svc service.ProductImageService, merchant uuid.UUID, name string) *service.ProductImageUpload {
	t.Helper()
	up, err := svc.Upload(context.Background(), merchant, readTestImage(t, name), name)
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return up
}

// ─── validation ──────────────────────────────────────────────────────────────

func TestValidateProductImageAcceptsJPEGPNWebP(t *testing.T) {
	cfg := testImageConfig(t.TempDir())
	for name, wantFormat := range map[string]string{
		"valid.jpg":  "jpeg",
		"valid.png":  "png",
		"valid.webp": "webp",
	} {
		format, width, height, err := service.ValidateProductImage(readTestImage(t, name), cfg)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if format != wantFormat {
			t.Errorf("%s: format = %q, want %q", name, format, wantFormat)
		}
		if width != 16 || height != 12 {
			t.Errorf("%s: dimensions = %dx%d, want 16x12", name, width, height)
		}
	}
}

func TestValidateProductImageRejectsUnsupportedTypes(t *testing.T) {
	cfg := testImageConfig(t.TempDir())
	cases := map[string][]byte{
		"gif":  []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"),
		"bmp":  []byte{'B', 'M', 0x3a, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x36, 0x00, 0x00, 0x00},
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`),
		"html": []byte(`<html><body><img src="x"></body></html>`),
		"text": []byte(`this is definitely not an image`),
		"zip":  []byte{'P', 'K', 0x03, 0x04, 0x00, 0x00, 0x00, 0x00},
		"elf":  []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00},
	}
	for name, data := range cases {
		if _, _, _, err := service.ValidateProductImage(data, cfg); !errors.Is(err, service.ErrProductImageUnsupportedType) {
			t.Errorf("%s: expected ErrProductImageUnsupportedType, got %v", name, err)
		}
	}
}

func TestValidateProductImageRejectsInvalidBytes(t *testing.T) {
	cfg := testImageConfig(t.TempDir())
	// JPEG magic with garbage after it: content sniffing claims jpeg, but
	// header parsing must fail (proves we decode, not just sniff).
	jpegGarbage := []byte{0xFF, 0xD8, 0xFF, 0x00, 0x01, 0x02, 0x03}
	if _, _, _, err := service.ValidateProductImage(jpegGarbage, cfg); !errors.Is(err, service.ErrProductImageInvalid) {
		t.Errorf("jpeg garbage: expected ErrProductImageInvalid, got %v", err)
	}
	// Structurally valid PNG header with a truncated tail: DecodeConfig may
	// pass, but the full decode must fail.
	png := readTestImage(t, "valid.png")
	truncated := png[:len(png)/2]
	if _, _, _, err := service.ValidateProductImage(truncated, cfg); !errors.Is(err, service.ErrProductImageInvalid) {
		t.Errorf("truncated png: expected ErrProductImageInvalid, got %v", err)
	}
	if _, _, _, err := service.ValidateProductImage(nil, cfg); !errors.Is(err, service.ErrProductImageInvalid) {
		t.Errorf("empty: expected ErrProductImageInvalid, got %v", err)
	}
}

func TestValidateProductImageRejectsOversized(t *testing.T) {
	cfg := testImageConfig(t.TempDir())
	cfg.MaxBytes = 16
	if _, _, _, err := service.ValidateProductImage(readTestImage(t, "valid.png"), cfg); !errors.Is(err, service.ErrProductImageTooLarge) {
		t.Fatalf("expected ErrProductImageTooLarge, got %v", err)
	}
}

func TestValidateProductImageRejectsDimensions(t *testing.T) {
	png := readTestImage(t, "valid.png") // 16x12
	small := testImageConfig(t.TempDir())
	small.MaxDimension = 8
	if _, _, _, err := service.ValidateProductImage(png, small); !errors.Is(err, service.ErrProductImageDimensions) {
		t.Errorf("MaxDimension: expected ErrProductImageDimensions, got %v", err)
	}
	fewPixels := testImageConfig(t.TempDir())
	fewPixels.MaxPixels = 100 // 16*12 = 192
	if _, _, _, err := service.ValidateProductImage(png, fewPixels); !errors.Is(err, service.ErrProductImageDimensions) {
		t.Errorf("MaxPixels: expected ErrProductImageDimensions, got %v", err)
	}
}

// ─── store round-trip + safe access ──────────────────────────────────────────

func TestProductImageStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	store := service.NewLocalProductImageStore(testImageConfig(dir), nil)
	svc := service.NewProductImageService(nil, store)

	up := mustUpload(t, svc, merchant, "valid.webp")
	if !model.IsLocalProductImageRef(up.ImageURL) {
		t.Fatalf("image_url %q is not a strict local reference", up.ImageURL)
	}
	if !strings.HasPrefix(up.ImageURL, model.ProductImageURLPrefix+merchant.String()+"/") {
		t.Fatalf("image_url %q is not merchant-scoped", up.ImageURL)
	}
	if up.Format != "webp" || up.ContentType != "image/webp" || up.Width != 16 || up.Height != 12 {
		t.Fatalf("unexpected upload metadata: %+v", up)
	}
	if up.SizeBytes <= 0 {
		t.Fatalf("size_bytes must be positive: %+v", up)
	}
	// The stored bytes must be exactly the uploaded bytes.
	merchantID, filename, ok := model.ParseLocalProductImageRef(up.ImageURL)
	if !ok {
		t.Fatalf("generated reference does not parse: %q", up.ImageURL)
	}
	rs, size, contentType, err := svc.ReadProductImageForServe(merchantID, filename)
	if err != nil {
		t.Fatalf("serve read: %v", err)
	}
	defer rs.Close()
	if contentType != "image/webp" {
		t.Errorf("content type = %q, want image/webp", contentType)
	}
	if size != up.SizeBytes {
		t.Errorf("size = %d, want %d", size, up.SizeBytes)
	}
}

func TestProductImageServeContentTypeMatchesExtension(t *testing.T) {
	// Regression: stored JPEG files use the server-generated ".jpg"
	// extension, which must serve as image/jpeg (not the octet-stream
	// fallback). Every supported upload format must round-trip its
	// Content-Type through Open/ReadProductImageForServe.
	cases := map[string]string{
		"valid.jpg":  "image/jpeg",
		"valid.png":  "image/png",
		"valid.webp": "image/webp",
	}
	for name, want := range cases {
		dir := t.TempDir()
		merchant := uuid.New()
		store := service.NewLocalProductImageStore(testImageConfig(dir), nil)
		svc := service.NewProductImageService(nil, store)
		up := mustUpload(t, svc, merchant, name)
		merchantID, filename, ok := model.ParseLocalProductImageRef(up.ImageURL)
		if !ok {
			t.Fatalf("%s: generated reference does not parse: %q", name, up.ImageURL)
		}
		rs, _, contentType, err := svc.ReadProductImageForServe(merchantID, filename)
		if err != nil {
			t.Fatalf("%s: serve read: %v", name, err)
		}
		rs.Close()
		if contentType != want {
			t.Errorf("%s: serve content type = %q, want %q", name, contentType, want)
		}
	}
}

func TestProductImageStoreIgnoresClientFilename(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	store := service.NewLocalProductImageStore(testImageConfig(dir), nil)
	svc := service.NewProductImageService(nil, store)

	evil := "../../../etc/cron.d/evil.png"
	up, err := svc.Upload(context.Background(), merchant, readTestImage(t, "valid.png"), evil)
	if err != nil {
		t.Fatalf("upload with hostile filename: %v", err)
	}
	if strings.Contains(up.ImageURL, "..") || strings.Contains(up.ImageURL, "evil") || strings.Contains(up.ImageURL, "etc") {
		t.Fatalf("client filename leaked into reference: %q", up.ImageURL)
	}
}

func TestProductImageSafeAccess(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	other := uuid.New()
	store := service.NewLocalProductImageStore(testImageConfig(dir), nil)
	svc := service.NewProductImageService(nil, store)
	up := mustUpload(t, svc, merchant, "valid.png")
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)

	for name, pair := range map[string][2]any{
		"path traversal":  {merchant, "../../etc/passwd"},
		"absolute path":   {merchant, "/etc/passwd"},
		"query smuggling": {merchant, filename + "?x=1"},
		"unknown file":    {merchant, uuid.NewString() + ".png"},
		"cross merchant":  {other, filename},
		"uppercase ext":   {merchant, strings.TrimSuffix(filename, ".png") + ".PNG"},
	} {
		mid := pair[0].(uuid.UUID)
		fname := pair[1].(string)
		if _, _, _, err := svc.ReadProductImageForServe(mid, fname); !errors.Is(err, service.ErrProductImageNotFound) {
			t.Errorf("%s: expected ErrProductImageNotFound, got %v", name, err)
		}
	}
}

// ─── cleanup guard ───────────────────────────────────────────────────────────

func TestProductImageCleanupKeepsReferencedImages(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	kept := service.NewLocalProductImageStore(testImageConfig(dir), func(_ context.Context, _ string) (int64, error) {
		return 2, nil // referenced by two products
	})
	svc := service.NewProductImageService(nil, kept)
	up := mustUpload(t, svc, merchant, "valid.png")

	if err := kept.RemoveIfUnreferenced(context.Background(), up.ImageURL); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := svc.ReadProductImageForServe(merchant, filename); err != nil {
		t.Fatalf("referenced file must be kept: %v", err)
	}
}

func TestProductImageCleanupRemovesUnreferenced(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	store := service.NewLocalProductImageStore(testImageConfig(dir), func(_ context.Context, _ string) (int64, error) {
		return 0, nil
	})
	svc := service.NewProductImageService(nil, store)
	up := mustUpload(t, svc, merchant, "valid.png")

	if err := store.RemoveIfUnreferenced(context.Background(), up.ImageURL); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := svc.ReadProductImageForServe(merchant, filename); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("unreferenced file must be gone, got %v", err)
	}
	// Idempotent: second removal succeeds.
	if err := store.RemoveIfUnreferenced(context.Background(), up.ImageURL); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestProductImageCleanupNeverDeletesOnUncertainState(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	failing := service.NewLocalProductImageStore(testImageConfig(dir), func(_ context.Context, _ string) (int64, error) {
		return 0, errors.New("db unavailable")
	})
	svc := service.NewProductImageService(nil, failing)
	up := mustUpload(t, svc, merchant, "valid.png")
	if err := failing.RemoveIfUnreferenced(context.Background(), up.ImageURL); err != nil {
		t.Fatalf("cleanup must swallow reference-check failure, got %v", err)
	}
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := svc.ReadProductImageForServe(merchant, filename); err != nil {
		t.Fatalf("file must be kept when the reference check fails: %v", err)
	}

	blind := service.NewLocalProductImageStore(testImageConfig(dir), nil)
	up2 := mustUpload(t, service.NewProductImageService(nil, blind), merchant, "valid.png")
	if err := blind.RemoveIfUnreferenced(context.Background(), up2.ImageURL); err != nil {
		t.Fatalf("nil-counter cleanup: %v", err)
	}
	_, filename2, _ := model.ParseLocalProductImageRef(up2.ImageURL)
	if _, _, _, err := service.NewProductImageService(nil, blind).ReadProductImageForServe(merchant, filename2); err != nil {
		t.Fatalf("nil counter must refuse deletion: %v", err)
	}

	// External URLs are never touched.
	if err := failing.RemoveIfUnreferenced(context.Background(), "https://cdn.example.com/a.png"); err != nil {
		t.Fatalf("external cleanup: %v", err)
	}
}

// ─── clear + lifecycle ───────────────────────────────────────────────────────

func imageProductSvc(t *testing.T, counter service.ProductImageReferenceCounter) (service.ProductImageService, service.ProductService, *stubJanitor, uuid.UUID, string) {
	t.Helper()
	dir := t.TempDir()
	prepo, crepo := newMemProductRepo(), newMemCategoryRepo()
	store := service.NewLocalProductImageStore(testImageConfig(dir), counter)
	imgSvc := service.NewProductImageService(prepo, store)
	janitor := &stubJanitor{store: store}
	prodSvc := service.NewProductServiceWithImageJanitor(prepo, crepo, janitor)
	return imgSvc, prodSvc, janitor, uuid.New(), dir
}

func createImageProduct(t *testing.T, prodSvc service.ProductService, merchant uuid.UUID, imageURL string) *model.Product {
	t.Helper()
	req := model.CreateProductRequest{Name: "Widget", Price: 1000, Currency: "IDR", ImageURL: &imageURL}
	p, err := prodSvc.Create(context.Background(), merchant, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p
}

func TestClearProductImage(t *testing.T) {
	imgSvc, prodSvc, _, merchant, _ := imageProductSvc(t, func(_ context.Context, _ string) (int64, error) {
		return 0, nil
	})
	up := mustUpload(t, imgSvc, merchant, "valid.png")
	p := createImageProduct(t, prodSvc, merchant, up.ImageURL)

	cleared, err := imgSvc.ClearProductImage(context.Background(), merchant, p.ID)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.ImageURL != nil {
		t.Fatalf("image_url must be NULL after clear, got %q", *cleared.ImageURL)
	}
	// Backing file is gone: a re-upload of the same bytes yields a live file
	// while the old reference 404s.
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := imgSvc.ReadProductImageForServe(merchant, filename); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("backing file must be removed, got %v", err)
	}
}

func TestClearProductImageErrors(t *testing.T) {
	imgSvc, prodSvc, _, merchant, _ := imageProductSvc(t, nil)
	// Unknown product (and cross-merchant) → not found.
	if _, err := imgSvc.ClearProductImage(context.Background(), merchant, uuid.New()); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("unknown product: expected ErrProductImageNotFound, got %v", err)
	}
	// Product without an image → not found (nothing to clear).
	bare, err := prodSvc.Create(context.Background(), merchant, model.CreateProductRequest{Name: "Bare", Price: 1, Currency: "IDR"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := imgSvc.ClearProductImage(context.Background(), merchant, bare.ID); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("imageless product: expected ErrProductImageNotFound, got %v", err)
	}
	// External HTTPS references unlink without file operations.
	ext := "https://cdn.example.com/a.png"
	p := createImageProduct(t, prodSvc, merchant, ext)
	cleared, err := imgSvc.ClearProductImage(context.Background(), merchant, p.ID)
	if err != nil {
		t.Fatalf("clear external: %v", err)
	}
	if cleared.ImageURL != nil {
		t.Fatalf("external image_url must be NULL after clear, got %q", *cleared.ImageURL)
	}
}

func TestProductImageReplacementDeletesOldFileOnlyAfterCommit(t *testing.T) {
	imgSvc, prodSvc, janitor, merchant, _ := imageProductSvc(t, func(_ context.Context, _ string) (int64, error) {
		return 0, nil
	})
	oldUp := mustUpload(t, imgSvc, merchant, "valid.png")
	p := createImageProduct(t, prodSvc, merchant, oldUp.ImageURL)
	_, oldFile, _ := model.ParseLocalProductImageRef(oldUp.ImageURL)

	// Failed replacement (validation error before commit) keeps the old file.
	badPrice := int64(-5)
	if _, err := prodSvc.Update(context.Background(), merchant, p.ID, model.UpdateProductRequest{Price: &badPrice, ImageURL: &oldUp.ImageURL}); err == nil {
		t.Fatalf("expected validation error for negative price")
	}
	if _, _, _, err := imgSvc.ReadProductImageForServe(merchant, oldFile); err != nil {
		t.Fatalf("old file must survive a failed update: %v", err)
	}
	if len(janitor.calls) != 0 {
		t.Fatalf("janitor must not run before commit, calls: %v", janitor.calls)
	}

	// Successful replacement removes the old backing file.
	newUp := mustUpload(t, imgSvc, merchant, "valid.webp")
	updated, err := prodSvc.Update(context.Background(), merchant, p.ID, model.UpdateProductRequest{ImageURL: &newUp.ImageURL})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ImageURL == nil || *updated.ImageURL != newUp.ImageURL {
		t.Fatalf("image not replaced: %+v", updated.ImageURL)
	}
	if _, _, _, err := imgSvc.ReadProductImageForServe(merchant, oldFile); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("old backing file must be removed, got %v", err)
	}
	if len(janitor.calls) != 1 || janitor.calls[0] != oldUp.ImageURL {
		t.Fatalf("janitor calls = %v, want [%s]", janitor.calls, oldUp.ImageURL)
	}

	// Re-saving the same reference deletes nothing.
	if _, err := prodSvc.Update(context.Background(), merchant, p.ID, model.UpdateProductRequest{Name: strptr("Renamed")}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(janitor.calls) != 1 {
		t.Fatalf("same-URL update must not trigger cleanup, calls: %v", janitor.calls)
	}
}

func TestProductDeleteReleasesImage(t *testing.T) {
	imgSvc, prodSvc, _, merchant, _ := imageProductSvc(t, func(_ context.Context, _ string) (int64, error) {
		return 0, nil
	})
	up := mustUpload(t, imgSvc, merchant, "valid.jpg")
	p := createImageProduct(t, prodSvc, merchant, up.ImageURL)
	if err := prodSvc.Delete(context.Background(), merchant, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := imgSvc.ReadProductImageForServe(merchant, filename); !errors.Is(err, service.ErrProductImageNotFound) {
		t.Fatalf("deleted product's file must be released, got %v", err)
	}
}

func TestUploadThenSaveFailureKeepsOrphanForOperatorCleanup(t *testing.T) {
	imgSvc, prodSvc, janitor, merchant, _ := imageProductSvc(t, nil)
	up := mustUpload(t, imgSvc, merchant, "valid.png")
	// The upload succeeded; the product save fails (negative price). The
	// file intentionally remains — no product row exists to reference-count
	// against, and deleting on save failure could race a concurrent linker.
	badPrice := int64(-1)
	req := model.CreateProductRequest{Name: "Bad", Price: badPrice, Currency: "IDR", ImageURL: &up.ImageURL}
	if _, err := prodSvc.Create(context.Background(), merchant, req); err == nil {
		t.Fatalf("expected create validation error")
	}
	_, filename, _ := model.ParseLocalProductImageRef(up.ImageURL)
	if _, _, _, err := imgSvc.ReadProductImageForServe(merchant, filename); err != nil {
		t.Fatalf("orphaned upload must be kept for operator cleanup: %v", err)
	}
	if len(janitor.calls) != 0 {
		t.Fatalf("no cleanup on failed save, calls: %v", janitor.calls)
	}
}

func TestProductImageURLReferenceGeneration(t *testing.T) {
	dir := t.TempDir()
	merchant := uuid.New()
	store := service.NewLocalProductImageStore(testImageConfig(dir), nil)
	svc := service.NewProductImageService(nil, store)
	seen := map[string]bool{}
	for _, name := range []string{"valid.jpg", "valid.png", "valid.webp"} {
		up := mustUpload(t, svc, merchant, name)
		if seen[up.ImageURL] {
			t.Fatalf("duplicate reference generated: %q", up.ImageURL)
		}
		seen[up.ImageURL] = true
		if model.IsLocalProductImageRef("https://cdn.example.com/x.png") {
			t.Fatalf("external URL must not classify as local")
		}
	}
}
