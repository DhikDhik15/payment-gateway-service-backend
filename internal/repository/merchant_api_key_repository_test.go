package repository_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── In-memory MerchantAPIKeyRepository ───────────────────────────────────────
//
// Mirrors the PostgreSQL contract (merchant scoping, soft revoke, atomic rotate,
// unique key_id) without requiring a live database. Used by repository and
// service/handler tests throughout Phase 5C.

type memAPIKeyRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*model.MerchantAPIKey
	byKeyID map[string]*model.MerchantAPIKey
}

func newMemAPIKeyRepo() *memAPIKeyRepo {
	return &memAPIKeyRepo{
		byID:    make(map[uuid.UUID]*model.MerchantAPIKey),
		byKeyID: make(map[string]*model.MerchantAPIKey),
	}
}

func (r *memAPIKeyRepo) clone(k *model.MerchantAPIKey) *model.MerchantAPIKey {
	cp := *k
	if k.ExpiresAt != nil {
		t := *k.ExpiresAt
		cp.ExpiresAt = &t
	}
	if k.RevokedAt != nil {
		t := *k.RevokedAt
		cp.RevokedAt = &t
	}
	if k.LastUsedAt != nil {
		t := *k.LastUsedAt
		cp.LastUsedAt = &t
	}
	return &cp
}

func (r *memAPIKeyRepo) Create(_ context.Context, key *model.MerchantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byKeyID[key.KeyID]; exists {
		return errors.New("duplicate key_id")
	}
	stored := r.clone(key)
	r.byID[key.ID] = stored
	r.byKeyID[key.KeyID] = stored
	return nil
}

func (r *memAPIKeyRepo) GetByID(_ context.Context, merchantID, id uuid.UUID) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.MerchantID != merchantID {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}

func (r *memAPIKeyRepo) GetByKeyID(_ context.Context, keyID string) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byKeyID[keyID]
	if !ok {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}

func (r *memAPIKeyRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.MerchantAPIKey
	for _, k := range r.byID {
		if k.MerchantID == merchantID {
			out = append(out, r.clone(k))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (r *memAPIKeyRepo) Revoke(_ context.Context, merchantID, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.MerchantID != merchantID {
		return repository.ErrMerchantAPIKeyNotFound
	}
	if k.Status == model.MerchantAPIKeyStatusRevoked {
		return repository.ErrMerchantAPIKeyAlreadyRevoked
	}
	now := time.Now().UTC()
	k.Status = model.MerchantAPIKeyStatusRevoked
	k.RevokedAt = &now
	k.UpdatedAt = now
	return nil
}

func (r *memAPIKeyRepo) Rotate(_ context.Context, merchantID, oldID uuid.UUID, newKey *model.MerchantAPIKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	old, ok := r.byID[oldID]
	if !ok || old.MerchantID != merchantID || old.Status != model.MerchantAPIKeyStatusActive {
		return repository.ErrMerchantAPIKeyNotFound
	}
	if _, exists := r.byKeyID[newKey.KeyID]; exists {
		return errors.New("duplicate key_id")
	}

	now := time.Now().UTC()
	old.Status = model.MerchantAPIKeyStatusRevoked
	old.RevokedAt = &now
	old.UpdatedAt = now

	stored := r.clone(newKey)
	r.byID[newKey.ID] = stored
	r.byKeyID[newKey.KeyID] = stored
	return nil
}

func (r *memAPIKeyRepo) UpdateLastUsedAt(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok {
		return repository.ErrMerchantAPIKeyNotFound
	}
	now := time.Now().UTC()
	k.LastUsedAt = &now
	k.UpdatedAt = now
	return nil
}

var _ repository.MerchantAPIKeyRepository = (*memAPIKeyRepo)(nil)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func seedKey(t *testing.T, repo *memAPIKeyRepo, merchantID uuid.UUID, keyID, secretHash, name string, status model.MerchantAPIKeyStatus, expiresAt *time.Time) *model.MerchantAPIKey {
	t.Helper()
	now := time.Now().UTC()
	k := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: merchantID,
		KeyID:      keyID,
		SecretHash: secretHash,
		Name:       name,
		Status:     status,
		ExpiresAt:  expiresAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := repo.Create(context.Background(), k); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return k
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestAPIKeyRepo_CreateAndGetByKeyID(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	k := seedKey(t, repo, mid, "pk_abc", "hash_only", "Prod", model.MerchantAPIKeyStatusActive, nil)

	got, err := repo.GetByKeyID(context.Background(), "pk_abc")
	if err != nil {
		t.Fatalf("GetByKeyID: %v", err)
	}
	if got.ID != k.ID {
		t.Error("ID mismatch")
	}
	if got.SecretHash != "hash_only" {
		t.Error("expected stored hash")
	}
	if got.SecretHash == "sk_plaintext" {
		t.Error("plaintext must never be stored")
	}
}

func TestAPIKeyRepo_GetByID_MerchantIsolation(t *testing.T) {
	repo := newMemAPIKeyRepo()
	merchantA := uuid.New()
	merchantB := uuid.New()
	k := seedKey(t, repo, merchantA, "pk_a1", "hash", "A", model.MerchantAPIKeyStatusActive, nil)

	if _, err := repo.GetByID(context.Background(), merchantA, k.ID); err != nil {
		t.Fatalf("owner should retrieve: %v", err)
	}
	if _, err := repo.GetByID(context.Background(), merchantB, k.ID); !errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
		t.Fatalf("cross-merchant GetByID must be not-found, got %v", err)
	}
}

func TestAPIKeyRepo_ListByMerchant_Isolation(t *testing.T) {
	repo := newMemAPIKeyRepo()
	merchantA := uuid.New()
	merchantB := uuid.New()
	seedKey(t, repo, merchantA, "pk_a1", "h1", "A1", model.MerchantAPIKeyStatusActive, nil)
	seedKey(t, repo, merchantA, "pk_a2", "h2", "A2", model.MerchantAPIKeyStatusActive, nil)
	seedKey(t, repo, merchantB, "pk_b1", "h3", "B1", model.MerchantAPIKeyStatusActive, nil)

	listA, err := repo.ListByMerchant(context.Background(), merchantA)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 2 {
		t.Fatalf("merchant A expected 2 keys, got %d", len(listA))
	}
	listB, err := repo.ListByMerchant(context.Background(), merchantB)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 {
		t.Fatalf("merchant B expected 1 key, got %d", len(listB))
	}
}

func TestAPIKeyRepo_Revoke(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	k := seedKey(t, repo, mid, "pk_rev", "hash", "Rev", model.MerchantAPIKeyStatusActive, nil)

	if err := repo.Revoke(context.Background(), mid, k.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, err := repo.GetByID(context.Background(), mid, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.MerchantAPIKeyStatusRevoked {
		t.Error("expected REVOKED")
	}
	if got.RevokedAt == nil {
		t.Error("expected revoked_at set")
	}

	if err := repo.Revoke(context.Background(), mid, k.ID); !errors.Is(err, repository.ErrMerchantAPIKeyAlreadyRevoked) {
		t.Fatalf("expected already-revoked, got %v", err)
	}
}

func TestAPIKeyRepo_Revoke_CrossMerchant(t *testing.T) {
	repo := newMemAPIKeyRepo()
	merchantA := uuid.New()
	merchantB := uuid.New()
	k := seedKey(t, repo, merchantA, "pk_x", "hash", "X", model.MerchantAPIKeyStatusActive, nil)

	if err := repo.Revoke(context.Background(), merchantB, k.ID); !errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
		t.Fatalf("expected not-found for cross-merchant revoke, got %v", err)
	}
}

func TestAPIKeyRepo_ExpirationData(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	exp := time.Now().UTC().Add(24 * time.Hour)
	k := seedKey(t, repo, mid, "pk_exp", "hash", "Exp", model.MerchantAPIKeyStatusActive, &exp)

	got, err := repo.GetByID(context.Background(), mid, k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("expires_at mismatch: got %v want %v", got.ExpiresAt, exp)
	}
}

func TestAPIKeyRepo_UpdateLastUsedAt(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	k := seedKey(t, repo, mid, "pk_lu", "hash", "LU", model.MerchantAPIKeyStatusActive, nil)

	if k.LastUsedAt != nil {
		t.Fatal("last_used_at should start nil")
	}
	if err := repo.UpdateLastUsedAt(context.Background(), k.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByID(context.Background(), mid, k.ID)
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at updated")
	}
}

func TestAPIKeyRepo_UniqueKeyID(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	seedKey(t, repo, mid, "pk_dup", "h1", "One", model.MerchantAPIKeyStatusActive, nil)

	dup := &model.MerchantAPIKey{
		ID: uuid.New(), MerchantID: mid, KeyID: "pk_dup", SecretHash: "h2",
		Name: "Two", Status: model.MerchantAPIKeyStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), dup); err == nil {
		t.Fatal("expected unique key_id conflict")
	}
}

func TestAPIKeyRepo_Rotate_Atomic(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	old := seedKey(t, repo, mid, "pk_old", "hold", "Prod", model.MerchantAPIKeyStatusActive, nil)

	now := time.Now().UTC()
	newKey := &model.MerchantAPIKey{
		ID: uuid.New(), MerchantID: mid, KeyID: "pk_new", SecretHash: "hnew",
		Name: "Prod", Status: model.MerchantAPIKeyStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.Rotate(context.Background(), mid, old.ID, newKey); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	oldGot, err := repo.GetByID(context.Background(), mid, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldGot.Status != model.MerchantAPIKeyStatusRevoked {
		t.Error("old key must be REVOKED")
	}

	newGot, err := repo.GetByKeyID(context.Background(), "pk_new")
	if err != nil {
		t.Fatal(err)
	}
	if newGot.Status != model.MerchantAPIKeyStatusActive {
		t.Error("new key must be ACTIVE")
	}
}

func TestAPIKeyRepo_Rotate_AlreadyRevoked(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	old := seedKey(t, repo, mid, "pk_old2", "h", "Prod", model.MerchantAPIKeyStatusActive, nil)
	_ = repo.Revoke(context.Background(), mid, old.ID)

	newKey := &model.MerchantAPIKey{
		ID: uuid.New(), MerchantID: mid, KeyID: "pk_n2", SecretHash: "hn",
		Name: "Prod", Status: model.MerchantAPIKeyStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Rotate(context.Background(), mid, old.ID, newKey); !errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
		t.Fatalf("expected not-found for revoked rotate, got %v", err)
	}
}

func TestAPIKeyRepo_ConcurrentRevoke(t *testing.T) {
	repo := newMemAPIKeyRepo()
	mid := uuid.New()
	k := seedKey(t, repo, mid, "pk_cr", "h", "C", model.MerchantAPIKeyStatusActive, nil)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.Revoke(context.Background(), mid, k.ID)
		}()
	}
	wg.Wait()
	close(errs)

	success, already := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, repository.ErrMerchantAPIKeyAlreadyRevoked):
			already++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("expected exactly 1 successful revoke, got %d", success)
	}
	if already != 19 {
		t.Fatalf("expected 19 already-revoked, got %d", already)
	}
}
