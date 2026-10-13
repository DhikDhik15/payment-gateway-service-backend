package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	// ErrProductImageTooLarge is returned when the uploaded bytes exceed the
	// configured PRODUCT_IMAGE_MAX_BYTES limit.
	ErrProductImageTooLarge = errors.New("product image exceeds the maximum upload size")
	// ErrProductImageUnsupportedType is returned for bytes that are not a
	// supported image format (JPEG, PNG, and WebP are decodable; GIF, BMP,
	// and SVG/HTML are explicitly rejected).
	ErrProductImageUnsupportedType = errors.New("product image format is not supported")
	// ErrProductImageInvalid is returned when the bytes claim a supported
	// format but fail header parsing or full decoding (truncated/corrupt).
	ErrProductImageInvalid = errors.New("product image content is invalid or corrupt")
	// ErrProductImageDimensions is returned when the decoded dimensions
	// exceed the configured limits (decompression-bomb guard).
	ErrProductImageDimensions = errors.New("product image dimensions exceed the allowed limits")
	// ErrProductImageStore is returned when validated bytes cannot be
	// persisted to (or read back from) the image store.
	ErrProductImageStore = errors.New("product image storage failed")
	// ErrProductImageNotFound is returned when clearing a product image that
	// does not exist (unknown product, or the product has no image).
	ErrProductImageNotFound = errors.New("product image not found")
)

// ProductImageConfig controls product image upload validation and local
// persistent storage. Every value must be strictly positive (validated in
// config.Load); the storage directory must be a Docker-mounted persistent
// volume in container deployments so files survive recreation.
type ProductImageConfig struct {
	// Dir is the on-disk root for stored images (files live at
	// <Dir>/<merchant_id>/<file_uuid>.<ext>).
	Dir string
	// MaxBytes bounds the uploaded file size (default 5 MiB).
	MaxBytes int64
	// MaxDimension bounds decoded width AND height in pixels (default 4096).
	MaxDimension int
	// MaxPixels bounds decoded width*height (default 4096*4096).
	MaxPixels int64
}

// DefaultProductImageConfig returns the documented development defaults.
func DefaultProductImageConfig() ProductImageConfig {
	return ProductImageConfig{
		Dir:          "./storage/product-images",
		MaxBytes:     5 << 20,
		MaxDimension: 4096,
		MaxPixels:    4096 * 4096,
	}
}

// ProductImageReferenceCounter counts products referencing an image URL
// across ALL merchants (a URL pasted into another merchant's product still
// protects the file from deletion).
type ProductImageReferenceCounter func(ctx context.Context, imageURL string) (int64, error)

// ProductImageUpload is the stable result of a successful upload, suitable
// for storing in products.image_url via the existing product PATCH endpoint.
type ProductImageUpload struct {
	ImageURL    string `json:"image_url"`
	Format      string `json:"format"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

// ProductImageService uploads stand-alone product images and clears
// product-linked images. Linking an uploaded reference to a product reuses
// the existing product PATCH contract (products.image_url).
type ProductImageService interface {
	Upload(ctx context.Context, merchantID uuid.UUID, data []byte, claimedName string) (*ProductImageUpload, error)
	ClearProductImage(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error)
	ReadProductImageForServe(merchantID uuid.UUID, filename string) (io.ReadSeekCloser, int64, string, error)
}

// ProductImageJanitor removes replaced/orphaned local image files. It is
// called best-effort by the product service after a successful product
// update/delete and never fails the product operation.
type ProductImageJanitor interface {
	RemoveIfUnreferenced(ctx context.Context, imageURL string) error
}

// ValidateProductImage enforces the upload validation contract on raw bytes:
//
//  1. non-empty and within MaxBytes,
//  2. magic-byte allowlist (JPEG, PNG, WebP),
//  3. header dimensions within MaxDimension/MaxPixels (DecodeConfig — cheap,
//     before the full pixel allocation),
//  4. successful full decode (rejects truncated/corrupt files, not just the
//     filename or Content-Type).
//
// SVG/HTML, GIF, and BMP are rejected as unsupported: SVG/HTML must never be
// served as an image (script execution). WebP decoding is provided by the
// pure-Go golang.org/x/image/webp package (no cgo, safe for the static
// CGO_ENABLED=0 build).
func ValidateProductImage(data []byte, cfg ProductImageConfig) (format string, width, height int, err error) {
	if len(data) == 0 {
		return "", 0, 0, ErrProductImageInvalid
	}
	if int64(len(data)) > cfg.MaxBytes {
		return "", 0, 0, ErrProductImageTooLarge
	}
	switch detectImageFormat(data) {
	case "jpeg", "png", "webp":
		format = detectImageFormat(data)
	case "gif", "bmp", "svg", "html":
		return "", 0, 0, fmt.Errorf("%w: only JPEG and PNG are accepted", ErrProductImageUnsupportedType)
	default:
		return "", 0, 0, fmt.Errorf("%w: unrecognized image content", ErrProductImageUnsupportedType)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %v", ErrProductImageInvalid, err)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > cfg.MaxDimension || config.Height > cfg.MaxDimension ||
		int64(config.Width)*int64(config.Height) > cfg.MaxPixels {
		return "", 0, 0, fmt.Errorf(
			"%w: decoded %dx%d exceeds %dx%d/%d pixels",
			ErrProductImageDimensions, config.Width, config.Height,
			cfg.MaxDimension, cfg.MaxDimension, cfg.MaxPixels,
		)
	}
	// Full decode proves the file is intact (truncated IDAT/scan data passes
	// DecodeConfig but fails here). The pixel buffer is transient.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", 0, 0, fmt.Errorf("%w: %v", ErrProductImageInvalid, err)
	}
	return format, config.Width, config.Height, nil
}

// detectImageFormat sniffs magic bytes only (never the filename).
func detectImageFormat(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E &&
		data[3] == 0x47 && data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A:
		return "png"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	case len(data) >= 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a"):
		return "gif"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "bmp"
	case len(trimmed) > 0 && trimmed[0] == '<':
		lower := bytes.ToLower(trimmed[:min(len(trimmed), 256)])
		if bytes.Contains(lower, []byte("<svg")) || bytes.Contains(lower, []byte("<html")) ||
			bytes.HasPrefix(lower, []byte("<?xml")) || bytes.HasPrefix(lower, []byte("<!doctype")) {
			if bytes.Contains(lower, []byte("<svg")) {
				return "svg"
			}
			return "html"
		}
		return "html"
	default:
		return ""
	}
}

// productImageContentType maps a validated format to its served Content-Type.
// Stored JPEG files use the server-generated ".jpg" extension, so "jpg" must
// map alongside "jpeg" — otherwise served JPEGs fall through to
// application/octet-stream.
func productImageContentType(format string) string {
	switch format {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// productImageExtension maps a validated format to its server-generated
// extension (never derived from the client filename).
func productImageExtension(format string) string {
	if format == "jpeg" {
		return "jpg"
	}
	return format
}

// LocalProductImageStore persists validated images on a mounted persistent
// volume at <Dir>/<merchant_id>/<file_uuid>.<ext> and serves them back by
// strict reference. Filenames are always server-generated (uuid); the
// supplied original name is used only for logging context.
type LocalProductImageStore struct {
	cfg     ProductImageConfig
	counter ProductImageReferenceCounter
}

// NewLocalProductImageStore builds a store. counter may be nil, in which
// case RemoveIfUnreferenced deletes unconditionally-guarded files only when
// they are provably absent (nil counter refuses deletion to avoid breaking
// references it cannot see).
func NewLocalProductImageStore(cfg ProductImageConfig, counter ProductImageReferenceCounter) *LocalProductImageStore {
	return &LocalProductImageStore{cfg: cfg, counter: counter}
}

// Save validates data and persists it, returning the stable public image_url
// reference (never a filesystem path).
func (s *LocalProductImageStore) Save(ctx context.Context, merchantID uuid.UUID, data []byte) (*ProductImageUpload, error) {
	format, width, height, err := ValidateProductImage(data, s.cfg)
	if err != nil {
		return nil, err
	}
	filename := uuid.NewString() + "." + productImageExtension(format)
	merchantDir := filepath.Join(s.cfg.Dir, merchantID.String())
	if err := os.MkdirAll(merchantDir, 0o750); err != nil {
		return nil, fmt.Errorf("%w: create merchant directory: %v", ErrProductImageStore, err)
	}
	// O_EXCL guarantees a server-generated UUID collision never silently
	// overwrites an existing file (retry with a fresh UUID instead).
	fullPath := filepath.Join(merchantDir, filename)
	f, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: filename collision, retry the upload", ErrProductImageStore)
		}
		return nil, fmt.Errorf("%w: create file: %v", ErrProductImageStore, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("%w: write file: %v", ErrProductImageStore, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("%w: close file: %v", ErrProductImageStore, err)
	}
	imageURL := model.ProductImageURLPrefix + merchantID.String() + "/" + filename
	return &ProductImageUpload{
		ImageURL:    imageURL,
		Format:      format,
		Width:       width,
		Height:      height,
		SizeBytes:   int64(len(data)),
		ContentType: productImageContentType(format),
	}, nil
}

// Open returns a read handle for a strictly-validated public reference.
// Callers must close the file. A missing file is reported as
// ErrProductImageNotFound (the public handler maps it to 404 without detail).
func (s *LocalProductImageStore) Open(merchantID uuid.UUID, filename string) (*os.File, int64, string, error) {
	refMerchant, refName, ok := model.ParseLocalProductImageRef(
		model.ProductImageURLPrefix + merchantID.String() + "/" + filename,
	)
	if !ok || refMerchant != merchantID || refName != filename {
		return nil, 0, "", ErrProductImageNotFound
	}
	fullPath := filepath.Join(s.cfg.Dir, merchantID.String(), filename)
	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, "", ErrProductImageNotFound
		}
		return nil, 0, "", fmt.Errorf("%w: open file: %v", ErrProductImageStore, err)
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		_ = f.Close()
		return nil, 0, "", ErrProductImageNotFound
	}
	dot := strings.LastIndex(filename, ".")
	contentType := productImageContentType(strings.ToLower(filename[dot+1:]))
	return f, info.Size(), contentType, nil
}

// RemoveIfUnreferenced deletes the file behind imageURL only when it is a
// local reference AND no product references it anymore. External HTTPS URLs
// are never touched. A nil counter refuses deletion (fail-safe against
// invisible references). Missing files are success (idempotent).
func (s *LocalProductImageStore) RemoveIfUnreferenced(ctx context.Context, imageURL string) error {
	merchantID, filename, ok := model.ParseLocalProductImageRef(imageURL)
	if !ok {
		return nil // external URL — not ours to delete
	}
	if s.counter == nil {
		slog.Warn("product image cleanup skipped: no reference counter configured")
		return nil
	}
	count, err := s.counter(ctx, imageURL)
	if err != nil {
		// Never delete when the reference check itself failed.
		slog.Error("product image reference check failed; keeping file", slog.String("error", err.Error()))
		return nil
	}
	if count > 0 {
		return nil
	}
	fullPath := filepath.Join(s.cfg.Dir, merchantID.String(), filename)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: remove file: %v", ErrProductImageStore, err)
	}
	// Best-effort removal of the now-possibly-empty merchant directory.
	_ = os.Remove(filepath.Join(s.cfg.Dir, merchantID.String()))
	return nil
}

// productImageService is the default ProductImageService implementation.
type productImageService struct {
	repo  repository.ProductRepository
	store *LocalProductImageStore
}

// NewProductImageService wires the image service over the product repository
// (for clear-and-cleanup) and the local file store.
func NewProductImageService(repo repository.ProductRepository, store *LocalProductImageStore) ProductImageService {
	return &productImageService{repo: repo, store: store}
}

func (s *productImageService) Upload(ctx context.Context, merchantID uuid.UUID, data []byte, _ string) (*ProductImageUpload, error) {
	return s.store.Save(ctx, merchantID, data)
}

// ClearProductImage removes the image reference from a merchant-scoped
// product (sets image_url NULL) and deletes the backing file when it is a
// local reference no longer used by any product. External HTTPS references
// are simply unlinked. A product with no image reports
// ErrProductImageNotFound.
func (s *productImageService) ClearProductImage(ctx context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	product, err := s.repo.GetByID(ctx, merchantID, productID)
	if err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductImageNotFound
		}
		return nil, err
	}
	if product.ImageURL == nil {
		return nil, ErrProductImageNotFound
	}
	oldURL := *product.ImageURL
	product.ImageURL = nil
	if err := s.repo.Update(ctx, product); err != nil {
		if errors.Is(err, repository.ErrProductNotFound) {
			return nil, ErrProductImageNotFound
		}
		return nil, err
	}
	// Best-effort file cleanup: the DB update already committed, so a
	// storage failure must not fail the request — the file becomes an
	// unreferenced orphan for operator cleanup instead.
	if model.IsLocalProductImageRef(oldURL) {
		if err := s.store.RemoveIfUnreferenced(ctx, oldURL); err != nil {
			slog.Error("product image orphan cleanup failed",
				slog.String("merchant_id", merchantID.String()),
				slog.String("error", err.Error()),
			)
		}
	}
	return product, nil
}

// ReadProductImageForServe streams a stored file for the public GET handler.
// It is a thin method on the service so handlers stay free of storage paths.
func (s *productImageService) ReadProductImageForServe(merchantID uuid.UUID, filename string) (io.ReadSeekCloser, int64, string, error) {
	return s.store.Open(merchantID, filename)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
