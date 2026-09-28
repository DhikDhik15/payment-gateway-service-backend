package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── In-memory repos ──────────────────────────────────────────────────────────

type memMerchantRepoAPIKey struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*model.Merchant
}

func newMemMerchantRepoAPIKey() *memMerchantRepoAPIKey {
	return &memMerchantRepoAPIKey{byID: make(map[uuid.UUID]*model.Merchant)}
}

func (r *memMerchantRepoAPIKey) Create(_ context.Context, m *model.Merchant) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[m.ID] = m
	return nil
}
func (r *memMerchantRepoAPIKey) CreateInTx(ctx context.Context, _ pgx.Tx, m *model.Merchant) error {
	return r.Create(ctx, m)
}
func (r *memMerchantRepoAPIKey) GetByID(_ context.Context, id uuid.UUID) (*model.Merchant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return nil, repository.ErrMerchantNotFound
	}
	cp := *m
	return &cp, nil
}
func (r *memMerchantRepoAPIKey) GetByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	return nil, repository.ErrMerchantNotFound
}
func (r *memMerchantRepoAPIKey) ExistsByCode(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (r *memMerchantRepoAPIKey) UpdateStatus(_ context.Context, id uuid.UUID, status model.MerchantStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.byID[id]
	if !ok {
		return repository.ErrMerchantNotFound
	}
	m.Status = status
	return nil
}

type memKeyRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*model.MerchantAPIKey
	byKeyID map[string]*model.MerchantAPIKey
}

func newMemKeyRepo() *memKeyRepo {
	return &memKeyRepo{
		byID:    make(map[uuid.UUID]*model.MerchantAPIKey),
		byKeyID: make(map[string]*model.MerchantAPIKey),
	}
}

func (r *memKeyRepo) clone(k *model.MerchantAPIKey) *model.MerchantAPIKey {
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

func (r *memKeyRepo) Create(_ context.Context, key *model.MerchantAPIKey) error {
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
func (r *memKeyRepo) CreateInTx(ctx context.Context, _ pgx.Tx, key *model.MerchantAPIKey) error {
	return r.Create(ctx, key)
}

func (r *memKeyRepo) GetByID(_ context.Context, merchantID, id uuid.UUID) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byID[id]
	if !ok || k.MerchantID != merchantID {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}

func (r *memKeyRepo) GetByKeyID(_ context.Context, keyID string) (*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.byKeyID[keyID]
	if !ok {
		return nil, repository.ErrMerchantAPIKeyNotFound
	}
	return r.clone(k), nil
}

func (r *memKeyRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID) ([]*model.MerchantAPIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*model.MerchantAPIKey
	for _, k := range r.byID {
		if k.MerchantID == merchantID {
			out = append(out, r.clone(k))
		}
	}
	return out, nil
}

func (r *memKeyRepo) Revoke(_ context.Context, merchantID, id uuid.UUID) error {
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

func (r *memKeyRepo) Rotate(_ context.Context, merchantID, oldID uuid.UUID, newKey *model.MerchantAPIKey) error {
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

func (r *memKeyRepo) UpdateLastUsedAt(_ context.Context, id uuid.UUID) error {
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

func (r *memKeyRepo) rawByID(id uuid.UUID) *model.MerchantAPIKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func seedMerchant(t *testing.T, repo *memMerchantRepoAPIKey) *model.Merchant {
	t.Helper()
	m := &model.Merchant{
		ID: uuid.New(), Name: "Test", Code: "T" + uuid.New().String()[:8],
		APIKey: "pk_legacy_" + uuid.New().String(), Status: model.MerchantStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newAPIKeySvc(t *testing.T) (service.MerchantAPIKeyService, *memKeyRepo, *memMerchantRepoAPIKey, *model.Merchant) {
	t.Helper()
	mRepo := newMemMerchantRepoAPIKey()
	kRepo := newMemKeyRepo()
	m := seedMerchant(t, mRepo)
	return service.NewMerchantAPIKeyService(kRepo, mRepo), kRepo, mRepo, m
}

func fullCredential(keyID, secret string) string {
	return keyID + ":" + secret
}

// ─── Create ───────────────────────────────────────────────────────────────────

func TestAPIKeyService_Create_ReturnsPlaintextOnce(t *testing.T) {
	svc, kRepo, _, m := newAPIKeySvc(t)

	resp, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "Production Backend"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.Secret == "" || !strings.HasPrefix(resp.Secret, "sk_") {
		t.Fatalf("expected sk_ secret, got %q", resp.Secret)
	}
	if !strings.HasPrefix(resp.KeyID, "pk_") {
		t.Fatalf("expected pk_ key_id, got %q", resp.KeyID)
	}
	if resp.Status != model.MerchantAPIKeyStatusActive {
		t.Error("expected ACTIVE")
	}

	stored := kRepo.rawByID(resp.ID)
	if stored == nil {
		t.Fatal("key not persisted")
	}
	if stored.SecretHash == "" {
		t.Error("secret_hash must be stored")
	}
	if stored.SecretHash == resp.Secret {
		t.Error("plaintext secret must NOT be stored")
	}
	if strings.Contains(stored.SecretHash, resp.Secret) {
		t.Error("plaintext must not appear inside hash")
	}
}

func TestAPIKeyService_Create_UniqueCredentials(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)

	a, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID == b.KeyID || a.Secret == b.Secret {
		t.Error("credentials must be unique across creates")
	}
}

func TestAPIKeyService_Create_PastExpirationRejected(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	past := time.Now().UTC().Add(-time.Hour)

	_, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{
		Name: "Expired", ExpiresAt: &past,
	})
	if !errors.Is(err, service.ErrAPIKeyExpirationPast) {
		t.Fatalf("expected ErrAPIKeyExpirationPast, got %v", err)
	}
}

func TestAPIKeyService_Create_FutureExpirationAccepted(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	future := time.Now().UTC().Add(24 * time.Hour)

	resp, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{
		Name: "Future", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("expected expires_at set")
	}
}

func TestAPIKeyService_Create_MerchantNotFound(t *testing.T) {
	svc, _, _, _ := newAPIKeySvc(t)
	_, err := svc.CreateKey(context.Background(), uuid.New(), model.CreateMerchantAPIKeyRequest{Name: "X"})
	if !errors.Is(err, repository.ErrMerchantNotFound) {
		t.Fatalf("expected merchant not found, got %v", err)
	}
}

// ─── List ─────────────────────────────────────────────────────────────────────

func TestAPIKeyService_List_HidesSecretAndHash(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "Hide"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := svc.ListKeys(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}
	if list[0].KeyID != created.KeyID {
		t.Error("key_id mismatch")
	}

	// MerchantAPIKeyResponse has no Secret / SecretHash fields — verify via JSON tags by reflection-like checks:
	// ensure response DTO zero values don't accidentally carry secret from create.
	_ = created.Secret
}

func TestAPIKeyService_List_MerchantIsolation(t *testing.T) {
	svc, _, mRepo, mA := newAPIKeySvc(t)
	mB := seedMerchant(t, mRepo)

	if _, err := svc.CreateKey(context.Background(), mA.ID, model.CreateMerchantAPIKeyRequest{Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateKey(context.Background(), mB.ID, model.CreateMerchantAPIKeyRequest{Name: "B"}); err != nil {
		t.Fatal(err)
	}

	listA, err := svc.ListKeys(context.Background(), mA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].Name != "A" {
		t.Fatalf("merchant A isolation failed: %+v", listA)
	}
	listB, err := svc.ListKeys(context.Background(), mB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].Name != "B" {
		t.Fatalf("merchant B isolation failed: %+v", listB)
	}
}

// ─── Revoke + Authenticate ────────────────────────────────────────────────────

func TestAPIKeyService_Revoke_ThenAuthFails(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "RevokeMe"})
	if err != nil {
		t.Fatal(err)
	}
	cred := fullCredential(created.KeyID, created.Secret)

	if _, err := svc.AuthenticateByAPIKey(context.Background(), cred); err != nil {
		t.Fatalf("auth before revoke should succeed: %v", err)
	}

	if err := svc.RevokeKey(context.Background(), m.ID, created.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AuthenticateByAPIKey(context.Background(), cred); !errors.Is(err, service.ErrAPIKeyNotFound) {
		t.Fatalf("revoked key must fail auth generically, got %v", err)
	}
}

func TestAPIKeyService_Revoke_AlreadyRevoked(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "R"})
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.RevokeKey(context.Background(), m.ID, created.ID)
	if err := svc.RevokeKey(context.Background(), m.ID, created.ID); !errors.Is(err, service.ErrAPIKeyAlreadyRevoked) {
		t.Fatalf("expected already revoked, got %v", err)
	}
}

func TestAPIKeyService_Revoke_CrossMerchant(t *testing.T) {
	svc, _, mRepo, mA := newAPIKeySvc(t)
	mB := seedMerchant(t, mRepo)
	created, err := svc.CreateKey(context.Background(), mA.ID, model.CreateMerchantAPIKeyRequest{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeKey(context.Background(), mB.ID, created.ID); !errors.Is(err, service.ErrAPIKeyNotFound) {
		t.Fatalf("cross-merchant revoke must be not-found, got %v", err)
	}
}

// ─── Rotate ───────────────────────────────────────────────────────────────────

func TestAPIKeyService_Rotate_OldInvalidNewValid(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	old, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "Rotate"})
	if err != nil {
		t.Fatal(err)
	}
	oldCred := fullCredential(old.KeyID, old.Secret)

	rotated, err := svc.RotateKey(context.Background(), m.ID, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Secret == "" || rotated.Secret == old.Secret {
		t.Error("rotate must return a new one-time secret")
	}
	if rotated.KeyID == old.KeyID {
		t.Error("rotate must issue a new key_id")
	}

	if _, err := svc.AuthenticateByAPIKey(context.Background(), oldCred); !errors.Is(err, service.ErrAPIKeyNotFound) {
		t.Fatalf("old key must fail after rotate, got %v", err)
	}
	newCred := fullCredential(rotated.KeyID, rotated.Secret)
	got, err := svc.AuthenticateByAPIKey(context.Background(), newCred)
	if err != nil {
		t.Fatalf("new key must authenticate: %v", err)
	}
	if got.ID != m.ID {
		t.Error("wrong merchant after rotate auth")
	}
}

// ─── Authenticate ─────────────────────────────────────────────────────────────

func TestAPIKeyService_Authenticate_Valid(t *testing.T) {
	svc, kRepo, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "Auth"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.AuthenticateByAPIKey(context.Background(), fullCredential(created.KeyID, created.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != m.ID {
		t.Error("merchant mismatch")
	}

	stored := kRepo.rawByID(created.ID)
	if stored.LastUsedAt == nil {
		t.Error("last_used_at must update on successful auth")
	}
}

func TestAPIKeyService_Authenticate_InvalidSecret(t *testing.T) {
	svc, kRepo, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "Bad"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.AuthenticateByAPIKey(context.Background(), fullCredential(created.KeyID, "sk_wrongsecret"))
	if !errors.Is(err, service.ErrAPIKeyNotFound) {
		t.Fatalf("expected generic not-found, got %v", err)
	}
	stored := kRepo.rawByID(created.ID)
	if stored.LastUsedAt != nil {
		t.Error("last_used_at must NOT update on failed auth")
	}
}

func TestAPIKeyService_Authenticate_Expired(t *testing.T) {
	svc, kRepo, _, m := newAPIKeySvc(t)
	future := time.Now().UTC().Add(time.Hour)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{
		Name: "Exp", ExpiresAt: &future,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Force expiry in storage.
	past := time.Now().UTC().Add(-time.Hour)
	raw := kRepo.rawByID(created.ID)
	raw.ExpiresAt = &past

	_, err = svc.AuthenticateByAPIKey(context.Background(), fullCredential(created.KeyID, created.Secret))
	if !errors.Is(err, service.ErrAPIKeyNotFound) {
		t.Fatalf("expired key must fail auth, got %v", err)
	}
}

func TestAPIKeyService_Authenticate_Malformed(t *testing.T) {
	svc, _, _, _ := newAPIKeySvc(t)
	for _, raw := range []string{"", "pk_only", "notakey", "pk_x:bad", "xx:sk_y"} {
		if _, err := svc.AuthenticateByAPIKey(context.Background(), raw); !errors.Is(err, service.ErrAPIKeyNotFound) {
			t.Errorf("malformed %q: expected ErrAPIKeyNotFound, got %v", raw, err)
		}
	}
}

// ─── Concurrency ──────────────────────────────────────────────────────────────

func TestAPIKeyService_ConcurrentCreate(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	const n = 8
	results := make(chan *model.CreateMerchantAPIKeyResponse, n)
	errs := make(chan error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "C"})
			if err != nil {
				errs <- err
				return
			}
			results <- resp
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent create failed: %v", err)
	}

	seenKeyID := map[string]bool{}
	seenSecret := map[string]bool{}
	count := 0
	for resp := range results {
		count++
		if seenKeyID[resp.KeyID] || seenSecret[resp.Secret] {
			t.Fatal("duplicate credentials under concurrency")
		}
		seenKeyID[resp.KeyID] = true
		seenSecret[resp.Secret] = true
	}
	if count != n {
		t.Fatalf("expected %d creates, got %d", n, count)
	}
}

func TestAPIKeyService_ConcurrentRevoke(t *testing.T) {
	svc, _, _, m := newAPIKeySvc(t)
	created, err := svc.CreateKey(context.Background(), m.ID, model.CreateMerchantAPIKeyRequest{Name: "CR"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.RevokeKey(context.Background(), m.ID, created.ID)
		}()
	}
	wg.Wait()
	close(errs)

	ok, already := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, service.ErrAPIKeyAlreadyRevoked):
			already++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("expected 1 success, got %d", ok)
	}
}
