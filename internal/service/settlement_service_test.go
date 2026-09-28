package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

type concurrentSettlementRepository struct {
	mu          sync.Mutex
	settlements map[string]*model.Settlement
	items       map[uuid.UUID][]*model.SettlementItem
}

func newConcurrentSettlementRepository() *concurrentSettlementRepository {
	return &concurrentSettlementRepository{
		settlements: make(map[string]*model.Settlement),
		items:       make(map[uuid.UUID][]*model.SettlementItem),
	}
}

func (r *concurrentSettlementRepository) CreateWithItems(_ context.Context, settlement *model.Settlement, items []*model.SettlementItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := settlement.Provider + "\x00" + settlement.SettlementRef
	if _, exists := r.settlements[key]; exists {
		return repository.ErrSettlementAlreadyExists
	}
	copySettlement := *settlement
	r.settlements[key] = &copySettlement
	r.items[settlement.ID] = items
	return nil
}

func (r *concurrentSettlementRepository) GetByID(_ context.Context, id uuid.UUID) (*model.Settlement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, settlement := range r.settlements {
		if settlement.ID == id {
			copySettlement := *settlement
			return &copySettlement, nil
		}
	}
	return nil, repository.ErrSettlementNotFound
}

func (r *concurrentSettlementRepository) GetByProviderAndRef(_ context.Context, provider, ref string) (*model.Settlement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	settlement, ok := r.settlements[provider+"\x00"+ref]
	if !ok {
		return nil, repository.ErrSettlementNotFound
	}
	copySettlement := *settlement
	return &copySettlement, nil
}

func (r *concurrentSettlementRepository) List(context.Context, model.SettlementListFilter) ([]*model.Settlement, error) {
	return nil, nil
}

func (r *concurrentSettlementRepository) Count(context.Context, model.SettlementListFilter) (int64, error) {
	return 0, nil
}

func (r *concurrentSettlementRepository) ListItems(_ context.Context, id uuid.UUID) ([]*model.SettlementItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items[id], nil
}

func (r *concurrentSettlementRepository) GetItemByID(context.Context, uuid.UUID) (*model.SettlementItem, error) {
	return nil, repository.ErrSettlementNotFound
}

func (r *concurrentSettlementRepository) ClaimForReconciliation(context.Context, uuid.UUID, time.Duration) (*model.Settlement, error) {
	return nil, errors.New("not implemented in import test")
}

func (r *concurrentSettlementRepository) UpdateAfterReconciliation(context.Context, uuid.UUID, model.SettlementStatus, int, int, int, *time.Time) error {
	return errors.New("not implemented in import test")
}

func (r *concurrentSettlementRepository) MarkFailed(context.Context, uuid.UUID) error {
	return errors.New("not implemented in import test")
}

func testSettlementPayload(fee int64) model.ImportSettlementRequest {
	return model.ImportSettlementRequest{
		Provider:      "mock",
		SettlementRef: "settlement-1",
		Payload:       []byte(fmt.Sprintf(`{"settlement_date":"2026-09-15","currency":"IDR","gross_amount":1000,"fee_amount":%d,"net_amount":1000,"items":[{"item_type":"ADJUSTMENT","currency":"IDR","gross_amount":1000,"net_amount":1000}]}`, fee)),
	}
}

func TestSettlementService_AllowsMockProviderInDevelopmentWiring(t *testing.T) {
	repo := newConcurrentSettlementRepository()
	svc := NewSettlementServiceForProvider(repo, "mock", NewMockSettlementImporter())
	if _, _, err := svc.Import(context.Background(), testSettlementPayload(0)); err != nil {
		t.Fatalf("mock settlement import failed: %v", err)
	}
}

func TestSettlementService_RejectsUnconfiguredNonMockProvider(t *testing.T) {
	repo := newConcurrentSettlementRepository()
	svc := NewSettlementServiceForProvider(repo, "midtrans", NewMockSettlementImporter())
	_, _, err := svc.Import(context.Background(), testSettlementPayload(0))
	if !errors.Is(err, ErrSettlementProviderUnsupported) {
		t.Fatalf("error = %v, want ErrSettlementProviderUnsupported", err)
	}
	if len(repo.settlements) != 0 {
		t.Fatal("unsupported settlement import wrote a settlement row")
	}
}

func TestSettlementImportConcurrentReplay(t *testing.T) {
	repo := newConcurrentSettlementRepository()
	svc := NewSettlementService(repo, NewMockSettlementImporter())
	req := testSettlementPayload(0)

	const workers = 10
	results := make(chan *model.Settlement, workers)
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			settlement, _, err := svc.Import(context.Background(), req)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- settlement
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)

	for err := range errorsCh {
		t.Fatalf("concurrent import failed: %v", err)
	}
	count := 0
	var firstID uuid.UUID
	for settlement := range results {
		count++
		if firstID == uuid.Nil {
			firstID = settlement.ID
		}
		if settlement.ID != firstID {
			t.Fatalf("concurrent import returned multiple settlement IDs: %s and %s", firstID, settlement.ID)
		}
	}
	if count != workers {
		t.Fatalf("got %d successful imports, want %d", count, workers)
	}
	if len(repo.settlements) != 1 {
		t.Fatalf("got %d stored settlements, want 1", len(repo.settlements))
	}
}

func TestSettlementImportConflictingReplay(t *testing.T) {
	repo := newConcurrentSettlementRepository()
	svc := NewSettlementService(repo, NewMockSettlementImporter())
	first := testSettlementPayload(0)
	if _, _, err := svc.Import(context.Background(), first); err != nil {
		t.Fatalf("first import: %v", err)
	}

	conflict := testSettlementPayload(1)
	if _, _, err := svc.Import(context.Background(), conflict); !errors.Is(err, ErrSettlementImportConflict) {
		t.Fatalf("got error %v, want settlement import conflict", err)
	}
}
