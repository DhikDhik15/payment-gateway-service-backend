package repository_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Product history requires migration 000023 (image_url column). The regular
// unit suite does not require PostgreSQL; when TEST_DATABASE_URL points at a
// schema-22 database these tests skip with the schema requirement reported.
func productTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	var version int
	var dirty bool
	if err := pool.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil || dirty || version < 23 {
		pool.Close()
		t.Skipf("product image_url history requires a clean migration 23 database: version=%d dirty=%v err=%v", version, dirty, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func productFixtureMerchant(t *testing.T, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	merchant := uuid.New()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO merchants(id,name,code,status) VALUES ($1,'Product test merchant',$2,'ACTIVE')`, merchant, code); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM merchants WHERE id=$1`, merchant) })
	return merchant
}

func TestProductRepo_PaginationFilterIsolation(t *testing.T) {
	pool := productTestPool(t)
	ctx := context.Background()
	repo := repository.NewProductRepository(pool)
	merchantA := productFixtureMerchant(t, pool, "prod-pg-a-"+uuid.NewString())
	merchantB := productFixtureMerchant(t, pool, "prod-pg-b-"+uuid.NewString())

	seed := func(merchant uuid.UUID, name string, active bool) {
		t.Helper()
		p := &model.Product{ID: uuid.New(), MerchantID: merchant, Name: name, Price: 1000, Currency: "IDR", Active: active}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		seed(merchantA, fmt.Sprintf("apple-%d", i), true)
	}
	seed(merchantA, "durian", false)
	seed(merchantB, "apple-foreign", true)

	// Page 1 of 2 per page: total counts all of merchant A's rows.
	page1, err := repo.ListByMerchant(ctx, merchantA, model.ListProductsFilter{}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 6 || page1.TotalPages != 3 || page1.Page != 1 || len(page1.Products) != 2 {
		t.Fatalf("page1 wrong: %+v", page1)
	}
	page3, err := repo.ListByMerchant(ctx, merchantA, model.ListProductsFilter{}, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Products) != 2 || page3.Page != 3 {
		t.Fatalf("page3 wrong: %+v", len(page3.Products))
	}
	// No overlap between pages.
	seen := map[uuid.UUID]bool{}
	for _, p := range append(page1.Products, page3.Products...) {
		if seen[p.ID] {
			t.Fatalf("duplicate row across pages: %s", p.ID)
		}
		seen[p.ID] = true
		if p.MerchantID != merchantA {
			t.Fatalf("cross-merchant row leaked: %+v", p.ID)
		}
	}

	// Active filter uses identical predicates for count and rows.
	activeOnly, err := repo.ListByMerchant(ctx, merchantA, model.ListProductsFilter{Active: &[]bool{true}[0]}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if activeOnly.Total != 5 || len(activeOnly.Products) != 5 {
		t.Fatalf("active filter wrong: %+v", activeOnly.Total)
	}
	for _, p := range activeOnly.Products {
		if !p.Active {
			t.Fatalf("inactive row leaked: %+v", p.ID)
		}
	}

	// Search filter parity.
	searched, err := repo.ListByMerchant(ctx, merchantA, model.ListProductsFilter{Search: "DURIAN"}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if searched.Total != 1 || searched.Products[0].Name != "durian" {
		t.Fatalf("search wrong: %+v", searched)
	}
}

func TestProductRepo_ImageURLRoundTrip(t *testing.T) {
	pool := productTestPool(t)
	ctx := context.Background()
	repo := repository.NewProductRepository(pool)
	merchant := productFixtureMerchant(t, pool, "prod-pg-img-"+uuid.NewString())

	img := "https://cdn.example.com/p/coffee.png"
	p := &model.Product{ID: uuid.New(), MerchantID: merchant, Name: "Coffee", Price: 1000, Currency: "IDR", ImageURL: &img, Active: true}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, merchant, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageURL == nil || *got.ImageURL != img {
		t.Fatalf("image not round-tripped: %+v", got.ImageURL)
	}

	// NULL image stays NULL for legacy-style rows.
	plain := &model.Product{ID: uuid.New(), MerchantID: merchant, Name: "Plain", Price: 1000, Currency: "IDR", Active: true}
	if err := repo.Create(ctx, plain); err != nil {
		t.Fatal(err)
	}
	gotPlain, err := repo.GetByID(ctx, merchant, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPlain.ImageURL != nil {
		t.Fatalf("nil image must stay nil: %+v", gotPlain.ImageURL)
	}

	// Database CHECK rejects non-HTTPS at the boundary too.
	bad := "http://cdn.example.com/a.png"
	badRow := &model.Product{ID: uuid.New(), MerchantID: merchant, Name: "Bad", Price: 1000, Currency: "IDR", ImageURL: &bad, Active: true}
	if err := repo.Create(ctx, badRow); err == nil {
		t.Fatal("expected CHECK rejection for http image_url")
	}
}
