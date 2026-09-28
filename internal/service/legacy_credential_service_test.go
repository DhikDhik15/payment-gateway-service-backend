package service

// legacy_credential_service_test.go — Phase 8D.3 tests for the legacy
// plaintext credential migration lifecycle: migrate (one-time secret), disable
// (idempotent), the stable sentinels each maps to, tenant isolation, and
// concurrency behaviour.
//
// LIMITATION (documented deliberately): there is no PostgreSQL integration-test
// harness in this repository (no pgxpool in any _test.go), so the store is an
// in-memory fake whose mutex reproduces the SELECT ... FOR UPDATE row lock of
// pgLegacyCredentialStore: classify + write happen while the lock is held and
// concurrent callers serialise exactly as they do against PostgreSQL. These
// tests therefore verify the service's delegation and error mapping under real
// concurrency; the SQL itself is additionally exercised by the manual
// checklist in the phase report.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── in-memory LegacyCredentialStore (simulates FOR UPDATE) ──────────────────

type memLegacyRow struct {
	state      model.LegacyCredentialState
	disabledAt *time.Time
}

type memLegacyCredentialStore struct {
	mu        sync.Mutex
	merchants map[uuid.UUID]*memLegacyRow
	// keys is keyed by the public key_id; values are the persisted rows.
	keys map[string]*model.MerchantAPIKey
}

func newMemLegacyCredentialStore() *memLegacyCredentialStore {
	return &memLegacyCredentialStore{
		merchants: make(map[uuid.UUID]*memLegacyRow),
		keys:      make(map[string]*model.MerchantAPIKey),
	}
}

// seed adds a merchant in the given state. apiKeyName is irrelevant here; the
// presence of a row models api_key IS NOT NULL.
func (s *memLegacyCredentialStore) seed(id uuid.UUID, state model.LegacyCredentialState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merchants[id] = &memLegacyRow{state: state}
}

func (s *memLegacyCredentialStore) stateOf(id uuid.UUID) (model.LegacyCredentialState, *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.merchants[id]
	if !ok {
		return "", nil
	}
	return row.state, row.disabledAt
}

func (s *memLegacyCredentialStore) keyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

// Migrate holds the "row lock" for the whole classify→insert→update critical
// section, exactly like pgLegacyCredentialStore's transaction.
func (s *memLegacyCredentialStore) Migrate(_ context.Context, merchantID uuid.UUID, key *model.MerchantAPIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.merchants[merchantID]
	if !ok {
		return repository.ErrLegacyCredentialMerchantNotFound
	}
	if row.state != model.LegacyCredentialStateLegacy {
		return repository.ErrLegacyCredentialAlreadyMigrated
	}
	if _, exists := s.keys[key.KeyID]; exists {
		return errors.New("duplicate key id")
	}
	s.keys[key.KeyID] = key
	row.state = model.LegacyCredentialStateMigrated
	row.disabledAt = nil
	return nil
}

func (s *memLegacyCredentialStore) Disable(_ context.Context, merchantID uuid.UUID) (*repository.LegacyCredentialDisableResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, ok := s.merchants[merchantID]
	if !ok {
		return nil, repository.ErrLegacyCredentialMerchantNotFound
	}
	switch row.state {
	case model.LegacyCredentialStateLegacy:
		return nil, repository.ErrLegacyCredentialMigrationRequired
	case model.LegacyCredentialStateLegacyDisabled:
		return &repository.LegacyCredentialDisableResult{
			State:           row.state,
			DisabledAt:      row.disabledAt,
			AlreadyDisabled: true,
		}, nil
	case model.LegacyCredentialStateMigrated:
		now := time.Now().UTC()
		row.state = model.LegacyCredentialStateLegacyDisabled
		row.disabledAt = &now
		return &repository.LegacyCredentialDisableResult{
			State:           row.state,
			DisabledAt:      row.disabledAt,
			AlreadyDisabled: false,
		}, nil
	default:
		return nil, errors.New("unexpected state")
	}
}

var _ repository.LegacyCredentialStore = (*memLegacyCredentialStore)(nil)

// ─── migrate ─────────────────────────────────────────────────────────────────

func TestLegacyCredentialService_Migrate_Success(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	resp, err := svc.Migrate(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if resp.Secret == "" {
		t.Fatal("one-time plaintext secret must be returned")
	}
	if resp.KeyID == "" {
		t.Fatal("key_id must be returned")
	}
	if !strings.HasPrefix(resp.KeyID, "pk_") {
		t.Errorf("key_id must use the Phase 5C pk_ format, got %q", resp.KeyID)
	}
	if resp.LegacyCredentialState != model.LegacyCredentialStateMigrated {
		t.Errorf("state = %q, want MIGRATED", resp.LegacyCredentialState)
	}
	if resp.LegacyDisabled {
		t.Error("legacy_disabled must be false right after migration (legacy key still works until disable)")
	}
	if resp.MerchantID != merchantID {
		t.Errorf("tenant mismatch: got %s want %s", resp.MerchantID, merchantID)
	}
	if resp.Name != migratedAPIKeyName {
		t.Errorf("name = %q, want %q", resp.Name, migratedAPIKeyName)
	}
	if resp.Status != model.MerchantAPIKeyStatusActive {
		t.Errorf("status = %q, want ACTIVE", resp.Status)
	}

	state, disabledAt := store.stateOf(merchantID)
	if state != model.LegacyCredentialStateMigrated {
		t.Errorf("persisted state = %q, want MIGRATED", state)
	}
	if disabledAt != nil {
		t.Error("disabled_at must stay nil after migrate")
	}
}

// TestLegacyCredentialService_Migrate_SecretNeverPersisted is the core
// disclosure invariant: only an Argon2id hash reaches storage, and it is not
// the plaintext that was handed back.
func TestLegacyCredentialService_Migrate_SecretNeverPersisted(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	resp, err := svc.Migrate(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	store.mu.Lock()
	persisted := store.keys[resp.KeyID]
	store.mu.Unlock()
	if persisted == nil {
		t.Fatal("expected the key row to be persisted")
	}
	if persisted.SecretHash == resp.Secret {
		t.Fatal("plaintext secret must never be persisted")
	}
	if !strings.HasPrefix(persisted.SecretHash, "$argon2id$") {
		t.Errorf("expected argon2id hash, got %q", persisted.SecretHash[:min(20, len(persisted.SecretHash))])
	}
	if strings.HasPrefix(persisted.SecretHash, "sk_") {
		t.Fatal("stored hash must not be a plaintext sk_ value")
	}
	// The public key_id IS stored (it is not a secret).
	if persisted.KeyID != resp.KeyID {
		t.Error("key_id mismatch between response and storage")
	}
}

func TestLegacyCredentialService_Migrate_AlreadyMigrated_ReturnsStableSentinel(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateMigrated)
	svc := NewLegacyCredentialService(store)

	_, err := svc.Migrate(context.Background(), merchantID)
	if !errors.Is(err, ErrLegacyCredentialAlreadyMigrated) {
		t.Fatalf("want ErrLegacyCredentialAlreadyMigrated, got %v", err)
	}
	if store.keyCount() != 0 {
		t.Errorf("no key may be minted for a non-LEGACY merchant (keys=%d)", store.keyCount())
	}
}

func TestLegacyCredentialService_Migrate_FromLegacyDisabled_ReturnsStableSentinel(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacyDisabled)
	svc := NewLegacyCredentialService(store)

	_, err := svc.Migrate(context.Background(), merchantID)
	if !errors.Is(err, ErrLegacyCredentialAlreadyMigrated) {
		t.Fatalf("want ErrLegacyCredentialAlreadyMigrated, got %v", err)
	}
	if store.keyCount() != 0 {
		t.Error("a disabled credential must never be resurrected by migrate")
	}
}

func TestLegacyCredentialService_Migrate_NotFound(t *testing.T) {
	svc := NewLegacyCredentialService(newMemLegacyCredentialStore())

	_, err := svc.Migrate(context.Background(), uuid.New())
	if !errors.Is(err, repository.ErrMerchantNotFound) {
		t.Fatalf("want ErrMerchantNotFound, got %v", err)
	}
}

// ─── disable ─────────────────────────────────────────────────────────────────

func TestLegacyCredentialService_Disable_Success(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateMigrated)
	svc := NewLegacyCredentialService(store)

	resp, err := svc.Disable(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if resp.LegacyCredentialState != model.LegacyCredentialStateLegacyDisabled {
		t.Errorf("state = %q, want LEGACY_DISABLED", resp.LegacyCredentialState)
	}
	if resp.AlreadyDisabled {
		t.Error("first disable must report already_disabled=false")
	}
	if resp.LegacyCredentialDisabledAt == nil {
		t.Fatal("legacy_credential_disabled_at must be set")
	}
}

// TestLegacyCredentialService_Disable_Idempotent is the idempotency contract:
// repeats are no-ops that never rewrite the original timestamp.
func TestLegacyCredentialService_Disable_Idempotent(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateMigrated)
	svc := NewLegacyCredentialService(store)

	first, err := svc.Disable(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("first Disable: %v", err)
	}
	if first.AlreadyDisabled {
		t.Fatal("first call must not be already_disabled")
	}

	for i := 0; i < 3; i++ {
		again, err := svc.Disable(context.Background(), merchantID)
		if err != nil {
			t.Fatalf("repeat %d Disable: %v", i, err)
		}
		if !again.AlreadyDisabled {
			t.Errorf("repeat %d: want already_disabled=true", i)
		}
		if again.LegacyCredentialState != model.LegacyCredentialStateLegacyDisabled {
			t.Errorf("repeat %d: state = %q", i, again.LegacyCredentialState)
		}
		if again.LegacyCredentialDisabledAt == nil {
			t.Fatalf("repeat %d: disabled_at must be preserved", i)
		}
		if !again.LegacyCredentialDisabledAt.Equal(*first.LegacyCredentialDisabledAt) {
			t.Errorf("repeat %d: disabled_at rewritten: %v != %v",
				i, again.LegacyCredentialDisabledAt, first.LegacyCredentialDisabledAt)
		}
	}
}

func TestLegacyCredentialService_Disable_FromLegacy_MigrationRequired(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	_, err := svc.Disable(context.Background(), merchantID)
	if !errors.Is(err, ErrLegacyCredentialMigrationRequired) {
		t.Fatalf("want ErrLegacyCredentialMigrationRequired, got %v", err)
	}
	// The tenant must not have been stranded: still LEGACY (still working).
	state, _ := store.stateOf(merchantID)
	if state != model.LegacyCredentialStateLegacy {
		t.Errorf("state = %q, want unchanged LEGACY", state)
	}
}

func TestLegacyCredentialService_Disable_NotFound(t *testing.T) {
	svc := NewLegacyCredentialService(newMemLegacyCredentialStore())

	_, err := svc.Disable(context.Background(), uuid.New())
	if !errors.Is(err, repository.ErrMerchantNotFound) {
		t.Fatalf("want ErrMerchantNotFound, got %v", err)
	}
}

// ─── tenant isolation ────────────────────────────────────────────────────────

func TestLegacyCredentialService_Migrate_TenantIsolation(t *testing.T) {
	store := newMemLegacyCredentialStore()
	tenantA, tenantB := uuid.New(), uuid.New()
	store.seed(tenantA, model.LegacyCredentialStateLegacy)
	store.seed(tenantB, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	resp, err := svc.Migrate(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("Migrate A: %v", err)
	}
	if resp.MerchantID != tenantA {
		t.Fatalf("migrate issued a credential for the wrong tenant: %s", resp.MerchantID)
	}

	if state, _ := store.stateOf(tenantB); state != model.LegacyCredentialStateLegacy {
		t.Errorf("tenant B state changed to %q — cross-tenant write", state)
	}
	if store.keyCount() != 1 {
		t.Errorf("expected exactly 1 key, got %d", store.keyCount())
	}
}

// ─── concurrency ─────────────────────────────────────────────────────────────

// TestLegacyCredentialService_ConcurrentMigrate_SingleWinner runs many migrate
// calls at once: exactly one may mint a credential, every loser must receive
// the stable 409 sentinel (never a partial success or a duplicate key).
func TestLegacyCredentialService_ConcurrentMigrate_SingleWinner(t *testing.T) {
	const callers = 16

	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		secrets []string
		losers  int
		other   []error
	)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := svc.Migrate(context.Background(), merchantID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				secrets = append(secrets, resp.Secret)
			case errors.Is(err, ErrLegacyCredentialAlreadyMigrated):
				losers++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range other {
		t.Errorf("unexpected error: %v", err)
	}
	if len(secrets) != 1 {
		t.Fatalf("exactly one caller must win, got %d winners", len(secrets))
	}
	if losers != callers-1 {
		t.Errorf("losers = %d, want %d", losers, callers-1)
	}
	if store.keyCount() != 1 {
		t.Errorf("keys minted = %d, want exactly 1", store.keyCount())
	}
	if state, _ := store.stateOf(merchantID); state != model.LegacyCredentialStateMigrated {
		t.Errorf("state = %q, want MIGRATED", state)
	}
}

// TestLegacyCredentialService_ConcurrentDisable_Idempotent runs disable
// concurrently: every caller succeeds (200), all observe the SAME timestamp,
// and the state settles on LEGACY_DISABLED.
func TestLegacyCredentialService_ConcurrentDisable_Idempotent(t *testing.T) {
	const callers = 16

	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateMigrated)
	svc := NewLegacyCredentialService(store)

	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		firsts  int
		stamps  []time.Time
		missing int
		other   []error
	)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := svc.Disable(context.Background(), merchantID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errors.Is(err, ErrLegacyCredentialMigrationRequired) ||
					errors.Is(err, repository.ErrMerchantNotFound) {
					other = append(other, err)
				} else {
					other = append(other, err)
				}
				return
			}
			if !resp.AlreadyDisabled {
				firsts++
			}
			if resp.LegacyCredentialDisabledAt == nil {
				missing++
				return
			}
			stamps = append(stamps, *resp.LegacyCredentialDisabledAt)
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range other {
		t.Errorf("unexpected error: %v", err)
	}
	if firsts != 1 {
		t.Errorf("exactly one caller must perform the write, got %d", firsts)
	}
	if missing != 0 {
		t.Errorf("%d callers saw a nil disabled_at", missing)
	}
	for i, ts := range stamps {
		if !ts.Equal(stamps[0]) {
			t.Fatalf("disabled_at diverged: stamps[0]=%v stamps[%d]=%v", stamps[0], i, ts)
		}
	}
	if state, _ := store.stateOf(merchantID); state != model.LegacyCredentialStateLegacyDisabled {
		t.Errorf("state = %q, want LEGACY_DISABLED", state)
	}
}

// TestLegacyCredentialService_ConcurrentMixedOperations exercises migrate and
// disable racing on the same merchant: the outcome must be one of the two
// legal terminal states with no invariant violation.
func TestLegacyCredentialService_ConcurrentMixedOperations(t *testing.T) {
	for iter := 0; iter < 8; iter++ {
		store := newMemLegacyCredentialStore()
		merchantID := uuid.New()
		store.seed(merchantID, model.LegacyCredentialStateLegacy)
		svc := NewLegacyCredentialService(store)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if i%2 == 0 {
					_, _ = svc.Migrate(context.Background(), merchantID)
				} else {
					_, _ = svc.Disable(context.Background(), merchantID)
				}
			}(i)
		}
		close(start)
		wg.Wait()

		state, disabledAt := store.stateOf(merchantID)
		switch state {
		case model.LegacyCredentialStateLegacy:
			if store.keyCount() != 0 {
				t.Fatalf("iter %d: LEGACY but %d keys minted", iter, store.keyCount())
			}
		case model.LegacyCredentialStateMigrated:
			if store.keyCount() != 1 {
				t.Fatalf("iter %d: MIGRATED with %d keys, want 1", iter, store.keyCount())
			}
		case model.LegacyCredentialStateLegacyDisabled:
			if store.keyCount() != 1 {
				t.Fatalf("iter %d: LEGACY_DISABLED with %d keys, want 1", iter, store.keyCount())
			}
			if disabledAt == nil {
				t.Fatalf("iter %d: LEGACY_DISABLED without disabled_at", iter)
			}
		default:
			t.Fatalf("iter %d: illegal state %q", iter, state)
		}
	}
}

// ─── full lifecycle (F4 regression: legacy → migrate → disable) ─────────────

// TestLegacyCredentialService_FullLifecycle walks the entire state machine and
// asserts every transition's contract in order.
func TestLegacyCredentialService_FullLifecycle(t *testing.T) {
	store := newMemLegacyCredentialStore()
	merchantID := uuid.New()
	store.seed(merchantID, model.LegacyCredentialStateLegacy)
	svc := NewLegacyCredentialService(store)

	// 1. Disable before migrating is refused.
	if _, err := svc.Disable(context.Background(), merchantID); !errors.Is(err, ErrLegacyCredentialMigrationRequired) {
		t.Fatalf("step 1: want migration-required, got %v", err)
	}

	// 2. Migrate issues the one-time secret.
	mig, err := svc.Migrate(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("step 2: %v", err)
	}
	if mig.Secret == "" {
		t.Fatal("step 2: missing one-time secret")
	}

	// 3. Migrating again is refused and issues no second secret.
	if _, err := svc.Migrate(context.Background(), merchantID); !errors.Is(err, ErrLegacyCredentialAlreadyMigrated) {
		t.Fatalf("step 3: want already-migrated, got %v", err)
	}
	if store.keyCount() != 1 {
		t.Fatalf("step 3: keys = %d, want 1", store.keyCount())
	}

	// 4. Disable succeeds once.
	dis, err := svc.Disable(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("step 4: %v", err)
	}
	if dis.AlreadyDisabled {
		t.Fatal("step 4: first disable must not be already_disabled")
	}

	// 5. Repeat is a no-op.
	dis2, err := svc.Disable(context.Background(), merchantID)
	if err != nil {
		t.Fatalf("step 5: %v", err)
	}
	if !dis2.AlreadyDisabled {
		t.Fatal("step 5: repeat must be already_disabled")
	}

	// 6. Terminal: migrate can never run again.
	if _, err := svc.Migrate(context.Background(), merchantID); !errors.Is(err, ErrLegacyCredentialAlreadyMigrated) {
		t.Fatalf("step 6: want already-migrated, got %v", err)
	}
	if store.keyCount() != 1 {
		t.Fatalf("step 6: keys = %d, want 1", store.keyCount())
	}
}
