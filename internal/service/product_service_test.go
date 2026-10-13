package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
)

// ─── In-memory product/category doubles ──────────────────────────────────────

type memProductRepo struct {
	mu       sync.Mutex
	products map[uuid.UUID]*model.Product
}

func newMemProductRepo() *memProductRepo {
	return &memProductRepo{products: map[uuid.UUID]*model.Product{}}
}

func (r *memProductRepo) Create(_ context.Context, product *model.Product) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.products {
		if existing.MerchantID == product.MerchantID && product.SKU != "" && existing.SKU == product.SKU {
			return repository.ErrProductSKUExists
		}
	}
	cp := *product
	r.products[product.ID] = &cp
	return nil
}

func (r *memProductRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID, filter model.ListProductsFilter, page, limit int) (model.ListProductsResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.Product, 0)
	for _, p := range r.products {
		if p.MerchantID != merchantID {
			continue
		}
		out = append(out, *p)
	}
	return model.ListProductsResult{Products: out, Page: page, Limit: limit, Total: int64(len(out)), TotalPages: 1}, nil
}

func (r *memProductRepo) GetByID(_ context.Context, merchantID, productID uuid.UUID) (*model.Product, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.products[productID]
	if !ok || p.MerchantID != merchantID {
		return nil, repository.ErrProductNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *memProductRepo) Update(_ context.Context, product *model.Product) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.products[product.ID]
	if !ok || existing.MerchantID != product.MerchantID {
		return repository.ErrProductNotFound
	}
	for id, other := range r.products {
		if id != product.ID && other.MerchantID == product.MerchantID && product.SKU != "" && other.SKU == product.SKU {
			return repository.ErrProductSKUExists
		}
	}
	cp := *product
	r.products[product.ID] = &cp
	return nil
}

func (r *memProductRepo) Delete(_ context.Context, merchantID, productID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.products[productID]
	if !ok || p.MerchantID != merchantID {
		return repository.ErrProductNotFound
	}
	delete(r.products, productID)
	return nil
}

type memCategoryRepo struct {
	mu         sync.Mutex
	categories map[uuid.UUID]*model.Category
}

func newMemCategoryRepo() *memCategoryRepo {
	return &memCategoryRepo{categories: map[uuid.UUID]*model.Category{}}
}

func (r *memCategoryRepo) Create(_ context.Context, category *model.Category) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.categories {
		if existing.MerchantID == category.MerchantID && strings.EqualFold(existing.Name, category.Name) {
			return repository.ErrCategoryNameExists
		}
	}
	cp := *category
	r.categories[category.ID] = &cp
	return nil
}

func (r *memCategoryRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]model.Category, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.Category, 0)
	for _, c := range r.categories {
		if c.MerchantID == merchantID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (r *memCategoryRepo) GetByID(_ context.Context, merchantID, categoryID uuid.UUID) (*model.Category, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.categories[categoryID]
	if !ok || c.MerchantID != merchantID {
		return nil, repository.ErrCategoryNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *memCategoryRepo) Update(_ context.Context, category *model.Category) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.categories[category.ID]
	if !ok || existing.MerchantID != category.MerchantID {
		return repository.ErrCategoryNotFound
	}
	for id, other := range r.categories {
		if id != category.ID && other.MerchantID == category.MerchantID && strings.EqualFold(other.Name, category.Name) {
			return repository.ErrCategoryNameExists
		}
	}
	cp := *category
	r.categories[category.ID] = &cp
	return nil
}

func (r *memCategoryRepo) Delete(_ context.Context, merchantID, categoryID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.categories[categoryID]
	if !ok || c.MerchantID != merchantID {
		return repository.ErrCategoryNotFound
	}
	delete(r.categories, categoryID)
	return nil
}

func productSvc() (service.ProductService, *memProductRepo, *memCategoryRepo, uuid.UUID) {
	prepo, crepo := newMemProductRepo(), newMemCategoryRepo()
	return service.NewProductService(prepo, crepo), prepo, crepo, uuid.New()
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }
func int64ptr(i int64) *int64 { return &i }
func validProductReq() model.CreateProductRequest {
	return model.CreateProductRequest{Name: "Coffee", SKU: "KF-1", Price: 25000, Currency: "IDR"}
}

// ─── Create validation ───────────────────────────────────────────────────────

func TestProductCreateValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*model.CreateProductRequest)
		want error
	}{
		{"empty name", func(r *model.CreateProductRequest) { r.Name = "" }, service.ErrProductInvalidName},
		{"blank name", func(r *model.CreateProductRequest) { r.Name = "   " }, service.ErrProductInvalidName},
		{"negative price", func(r *model.CreateProductRequest) { r.Price = -1 }, service.ErrProductInvalidPrice},
		{"usd currency", func(r *model.CreateProductRequest) { r.Currency = "USD" }, service.ErrProductInvalidCurrency},
		{"lowercase xxx currency", func(r *model.CreateProductRequest) { r.Currency = "xxx" }, service.ErrProductInvalidCurrency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, merchant := productSvc()
			req := validProductReq()
			tc.mut(&req)
			if _, err := svc.Create(context.Background(), merchant, req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestProductCreateCurrencyNormalized(t *testing.T) {
	svc, _, _, merchant := productSvc()
	req := validProductReq()
	req.Currency = "idr"
	p, err := svc.Create(context.Background(), merchant, req)
	if err != nil {
		t.Fatalf("lowercase idr must normalize: %v", err)
	}
	if p.Currency != "IDR" {
		t.Fatalf("currency = %q, want IDR", p.Currency)
	}
}

func TestProductCreateImageURL(t *testing.T) {
	valid := "https://cdn.example.com/p/coffee.png?size=large"
	for _, tc := range []struct {
		name    string
		image   *string
		wantErr error
	}{
		{"nil absent", nil, nil},
		{"valid https", strptr(valid), nil},
		{"https with port and path", strptr("https://cdn.example.com:8443/a/b?q=1#frag"), nil},
		{"http rejected", strptr("http://cdn.example.com/a.png"), service.ErrProductInvalidImage},
		{"uppercase scheme rejected for DB consistency", strptr("HTTPS://cdn.example.com/a.png"), service.ErrProductInvalidImage},
		{"ftp rejected", strptr("ftp://cdn.example.com/a.png"), service.ErrProductInvalidImage},
		{"javascript rejected", strptr("javascript:alert(1)"), service.ErrProductInvalidImage},
		{"file rejected", strptr("file:///etc/passwd"), service.ErrProductInvalidImage},
		{"relative rejected", strptr("images/coffee.png"), service.ErrProductInvalidImage},
		{"absolute path rejected", strptr("/var/www/coffee.png"), service.ErrProductInvalidImage},
		{"malformed rejected", strptr("https://"), service.ErrProductInvalidImage},
		{"host only rejected", strptr("https:///path"), service.ErrProductInvalidImage},
		{"credentials rejected", strptr("https://user:pass@cdn.example.com/a.png"), service.ErrProductInvalidImage},
		{"user only rejected", strptr("https://user@cdn.example.com/a.png"), service.ErrProductInvalidImage},
		{"blank rejected", strptr("   "), service.ErrProductInvalidImage},
		{"empty rejected", strptr(""), service.ErrProductInvalidImage},
		{"too long rejected", strptr("https://cdn.example.com/" + strings.Repeat("a", 2048)), service.ErrProductInvalidImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, merchant := productSvc()
			req := validProductReq()
			req.ImageURL = tc.image
			p, err := svc.Create(context.Background(), merchant, req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.image == nil {
				if p.ImageURL != nil {
					t.Fatalf("nil input must stay nil, got %q", *p.ImageURL)
				}
				return
			}
			if p.ImageURL == nil || *p.ImageURL != strings.TrimSpace(*tc.image) {
				t.Fatalf("image not preserved: %+v", p.ImageURL)
			}
		})
	}
}

func TestProductCreatePreservesSKUConflictAndCategory(t *testing.T) {
	svc, _, crepo, merchant := productSvc()
	other := uuid.New()
	if _, err := svc.Create(context.Background(), merchant, validProductReq()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	dup := validProductReq()
	dup.Name = "Other name"
	if _, err := svc.Create(context.Background(), merchant, dup); !errors.Is(err, service.ErrProductSKUUsed) {
		t.Fatalf("duplicate SKU: got %v", err)
	}
	foreignCat := uuid.New()
	crepo.categories[foreignCat] = &model.Category{ID: foreignCat, MerchantID: other, Name: "Beans"}
	catReq := validProductReq()
	catReq.SKU = "KF-2"
	catReq.CategoryID = &foreignCat
	if _, err := svc.Create(context.Background(), merchant, catReq); !errors.Is(err, service.ErrCategoryNotFound) {
		t.Fatalf("foreign category: got %v", err)
	}
}

// ─── Update validation ───────────────────────────────────────────────────────

func TestProductUpdateValidation(t *testing.T) {
	svc, _, _, merchant := productSvc()
	seed, err := svc.Create(context.Background(), merchant, validProductReq())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, tc := range []struct {
		name string
		req  model.UpdateProductRequest
		want error
	}{
		{"blank name", model.UpdateProductRequest{Name: strptr("  ")}, service.ErrProductInvalidName},
		{"negative price", model.UpdateProductRequest{Price: int64ptr(-5)}, service.ErrProductInvalidPrice},
		{"usd currency", model.UpdateProductRequest{Currency: strptr("USD")}, service.ErrProductInvalidCurrency},
		{"http image", model.UpdateProductRequest{ImageURL: strptr("http://x.test/a.png")}, service.ErrProductInvalidImage},
		{"blank image", model.UpdateProductRequest{ImageURL: strptr("")}, service.ErrProductInvalidImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Update(context.Background(), merchant, seed.ID, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	// Valid patch preserves the rest and normalizes the image.
	img := "  https://cdn.example.com/new.png  "
	price := int64(30000)
	updated, err := svc.Update(context.Background(), merchant, seed.ID, model.UpdateProductRequest{Price: &price, ImageURL: &img})
	if err != nil {
		t.Fatalf("valid patch: %v", err)
	}
	if updated.Price != 30000 || updated.Name != "Coffee" {
		t.Fatalf("patch applied wrong: %+v", updated)
	}
	if updated.ImageURL == nil || *updated.ImageURL != "https://cdn.example.com/new.png" {
		t.Fatalf("image not trimmed/preserved: %+v", updated.ImageURL)
	}
	if updated.Currency != "IDR" {
		t.Fatalf("currency changed unexpectedly: %q", updated.Currency)
	}
}

// ─── List pagination validation ──────────────────────────────────────────────

func TestProductListPaginationValidation(t *testing.T) {
	svc, _, _, merchant := productSvc()
	for _, tc := range [][2]int{{0, 20}, {1, 0}, {1, 101}, {-1, 20}, {1, -5}} {
		if _, err := svc.List(context.Background(), merchant, model.ListProductsFilter{}, tc[0], tc[1]); !errors.Is(err, service.ErrProductInvalidPage) {
			t.Fatalf("page=%d limit=%d: got %v", tc[0], tc[1], err)
		}
	}
	if _, err := svc.List(context.Background(), merchant, model.ListProductsFilter{}, 1, 20); err != nil {
		t.Fatalf("valid list: %v", err)
	}
}

// ─── Merchant isolation ──────────────────────────────────────────────────────

func TestProductMerchantIsolation(t *testing.T) {
	svc, _, _, merchantA := productSvc()
	merchantB := uuid.New()
	p, err := svc.Create(context.Background(), merchantA, validProductReq())
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Get(context.Background(), merchantB, p.ID); !errors.Is(err, service.ErrProductNotFound) {
		t.Fatalf("foreign get: got %v", err)
	}
	if err := svc.Delete(context.Background(), merchantB, p.ID); !errors.Is(err, service.ErrProductNotFound) {
		t.Fatalf("foreign delete: got %v", err)
	}
	if _, err := svc.Update(context.Background(), merchantB, p.ID, model.UpdateProductRequest{Name: strptr("Hijack")}); !errors.Is(err, service.ErrProductNotFound) {
		t.Fatalf("foreign update: got %v", err)
	}
	got, err := svc.Get(context.Background(), merchantA, p.ID)
	if err != nil || got.Name != "Coffee" {
		t.Fatalf("owner read after attacks: %+v %v", got, err)
	}
}
