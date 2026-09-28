package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Mem atomic provisioner ───────────────────────────────────────────────────
//
// Simulates PostgreSQL transaction semantics: writes land in a staging buffer and
// are only committed to durable maps when Provision returns nil. On any error the
// buffer is discarded so partial tenants never appear in committed state.

type memOnboardingProvisioner struct {
	mu sync.Mutex

	merchants map[uuid.UUID]*model.Merchant
	users     map[uuid.UUID]*model.MerchantUser
	keys      map[uuid.UUID]*model.MerchantAPIKey
	codes     map[string]bool
	emails    map[string]bool

	failOnOwner error
	failOnKey   error
}

func newMemOnboardingProvisioner() *memOnboardingProvisioner {
	return &memOnboardingProvisioner{
		merchants: make(map[uuid.UUID]*model.Merchant),
		users:     make(map[uuid.UUID]*model.MerchantUser),
		keys:      make(map[uuid.UUID]*model.MerchantAPIKey),
		codes:     make(map[string]bool),
		emails:    make(map[string]bool),
	}
}

func (p *memOnboardingProvisioner) ExistsByCode(_ context.Context, code string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.codes[code], nil
}

func (p *memOnboardingProvisioner) Provision(
	_ context.Context,
	merchant *model.Merchant,
	owner *model.MerchantUser,
	key *model.MerchantAPIKey,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Staging buffer — discarded on failure (rollback).
	stagedMerchant := *merchant
	stagedOwner := *owner
	stagedKey := *key

	if p.codes[merchant.Code] {
		return repository.ErrMerchantCodeExists
	}
	if p.emails[owner.Email] {
		return repository.ErrMerchantUserEmailExists
	}

	if p.failOnOwner != nil {
		return p.failOnOwner
	}
	if p.failOnKey != nil {
		return p.failOnKey
	}

	// Commit.
	p.merchants[stagedMerchant.ID] = &stagedMerchant
	p.codes[stagedMerchant.Code] = true
	p.users[stagedOwner.ID] = &stagedOwner
	p.emails[stagedOwner.Email] = true
	p.keys[stagedKey.ID] = &stagedKey
	return nil
}

func (p *memOnboardingProvisioner) committedMerchant(id uuid.UUID) *model.Merchant {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.merchants[id]
}

func (p *memOnboardingProvisioner) committedUser(id uuid.UUID) *model.MerchantUser {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.users[id]
}

func (p *memOnboardingProvisioner) committedKey(id uuid.UUID) *model.MerchantAPIKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keys[id]
}

func (p *memOnboardingProvisioner) merchantCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.merchants)
}

func (p *memOnboardingProvisioner) userCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.users)
}

func (p *memOnboardingProvisioner) keyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

func validOnboardReq() model.OnboardMerchantRequest {
	return model.OnboardMerchantRequest{
		Name:          "Toko Baru",
		Code:          "TOKO_NEW_01",
		OwnerEmail:    "owner@example.com",
		OwnerPassword: "securepass1",
	}
}

// ─── Success ──────────────────────────────────────────────────────────────────

func TestOnboardMerchant_Success_ProvisionsAllRecords(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	resp, err := svc.OnboardMerchant(context.Background(), validOnboardReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Merchant.ID == uuid.Nil {
		t.Fatal("expected merchant id")
	}
	if resp.Merchant.Status != model.MerchantStatusActive {
		t.Fatalf("expected ACTIVE, got %s", resp.Merchant.Status)
	}
	if resp.Owner.Role != model.DashboardUserRoleOwner {
		t.Fatalf("expected OWNER, got %s", resp.Owner.Role)
	}
	if resp.Owner.Email != "owner@example.com" {
		t.Fatalf("expected normalised email, got %s", resp.Owner.Email)
	}
	if !strings.HasPrefix(resp.APICredential.KeyID, "pk_") {
		t.Fatalf("expected pk_ key_id, got %s", resp.APICredential.KeyID)
	}
	if !strings.HasPrefix(resp.APICredential.Secret, "sk_") {
		t.Fatalf("expected sk_ secret, got %s", resp.APICredential.Secret)
	}

	// DB state: all three belong to the same merchant.
	m := prov.committedMerchant(resp.Merchant.ID)
	u := prov.committedUser(resp.Owner.ID)
	k := prov.committedKey(resp.APICredential.ID)
	if m == nil || u == nil || k == nil {
		t.Fatalf("missing committed records m=%v u=%v k=%v", m != nil, u != nil, k != nil)
	}
	if u.MerchantID != m.ID || k.MerchantID != m.ID {
		t.Fatalf("tenant isolation violation: merchant=%s owner.mid=%s key.mid=%s",
			m.ID, u.MerchantID, k.MerchantID)
	}

	// Credential security: password hash stored, plaintext secret not stored.
	if u.PasswordHash == "" || u.PasswordHash == "securepass1" {
		t.Fatal("password must be stored as argon2id hash, not plaintext")
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("expected argon2id password hash, got %s", u.PasswordHash[:20])
	}
	if k.SecretHash == "" || k.SecretHash == resp.APICredential.Secret {
		t.Fatal("API secret must be stored hashed, not plaintext")
	}
	if !strings.HasPrefix(k.SecretHash, "$argon2id$") {
		t.Fatal("expected argon2id secret hash")
	}
	// Phase 8D.3: NO legacy credential is generated at all. Both columns are
	// stored as NULL (empty in the model) and the merchant starts MIGRATED,
	// so the Auth middleware's legacy stage can never match it.
	if m.APIKey != "" {
		t.Fatalf("onboarding must not create a legacy api_key, got %q", m.APIKey)
	}
	if m.APISecret != "" {
		t.Fatalf("onboarding must not create a legacy api_secret, got %q", m.APISecret)
	}
	if m.LegacyCredentialState != model.LegacyCredentialStateMigrated {
		t.Fatalf("expected legacy state MIGRATED for a new tenant, got %q", m.LegacyCredentialState)
	}
}

func TestOnboardMerchant_NormalisesOwnerEmail(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	req := validOnboardReq()
	req.OwnerEmail = "  Owner@Example.COM "
	resp, err := svc.OnboardMerchant(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Owner.Email != "owner@example.com" {
		t.Fatalf("expected lowercased trimmed email, got %q", resp.Owner.Email)
	}
}

func TestOnboardMerchant_ResponseNeverExposesPasswordOrHashes(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	resp, err := svc.OnboardMerchant(context.Background(), validOnboardReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Structural check: response DTOs have no password / hash fields by design.
	// Also ensure secret is present (one-time) and key_id is not the hash.
	if resp.APICredential.Secret == "" {
		t.Fatal("expected one-time secret in response")
	}
	if strings.Contains(resp.APICredential.Secret, "$argon2id$") {
		t.Fatal("response must expose plaintext secret, not hash")
	}
	if resp.Owner.Email == "" {
		t.Fatal("expected owner email")
	}
}

// ─── Atomic rollback ──────────────────────────────────────────────────────────

func TestOnboardMerchant_Rollback_WhenOwnerFails(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	prov.failOnOwner = errors.New("forced owner failure")
	svc := NewOnboardingService(prov)

	_, err := svc.OnboardMerchant(context.Background(), validOnboardReq())
	if err == nil {
		t.Fatal("expected error")
	}
	if prov.merchantCount() != 0 || prov.userCount() != 0 || prov.keyCount() != 0 {
		t.Fatalf("expected full rollback; got merchants=%d users=%d keys=%d",
			prov.merchantCount(), prov.userCount(), prov.keyCount())
	}
}

func TestOnboardMerchant_Rollback_WhenAPIKeyFails(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	prov.failOnKey = errors.New("forced api key failure")
	svc := NewOnboardingService(prov)

	_, err := svc.OnboardMerchant(context.Background(), validOnboardReq())
	if err == nil {
		t.Fatal("expected error")
	}
	if prov.merchantCount() != 0 || prov.userCount() != 0 || prov.keyCount() != 0 {
		t.Fatalf("expected full rollback; got merchants=%d users=%d keys=%d",
			prov.merchantCount(), prov.userCount(), prov.keyCount())
	}
}

// ─── Duplicates ───────────────────────────────────────────────────────────────

func TestOnboardMerchant_DuplicateMerchantCode(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	if _, err := svc.OnboardMerchant(context.Background(), validOnboardReq()); err != nil {
		t.Fatalf("first onboard: %v", err)
	}

	req2 := validOnboardReq()
	req2.OwnerEmail = "other@example.com"
	_, err := svc.OnboardMerchant(context.Background(), req2)
	if !errors.Is(err, ErrDuplicateMerchantCode) {
		t.Fatalf("expected ErrDuplicateMerchantCode, got %v", err)
	}
	if prov.merchantCount() != 1 || prov.userCount() != 1 || prov.keyCount() != 1 {
		t.Fatalf("duplicate must not leave partial records; got m=%d u=%d k=%d",
			prov.merchantCount(), prov.userCount(), prov.keyCount())
	}
}

func TestOnboardMerchant_DuplicateOwnerEmail(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	if _, err := svc.OnboardMerchant(context.Background(), validOnboardReq()); err != nil {
		t.Fatalf("first onboard: %v", err)
	}

	req2 := validOnboardReq()
	req2.Code = "OTHER_CODE"
	// Same email — unique globally.
	_, err := svc.OnboardMerchant(context.Background(), req2)
	if !errors.Is(err, ErrEmailAlreadyExists) {
		t.Fatalf("expected ErrEmailAlreadyExists, got %v", err)
	}
	if prov.merchantCount() != 1 || prov.userCount() != 1 || prov.keyCount() != 1 {
		t.Fatalf("duplicate email must not leave partial records; got m=%d u=%d k=%d",
			prov.merchantCount(), prov.userCount(), prov.keyCount())
	}
}

func TestOnboardMerchant_InvalidEmail(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	req := validOnboardReq()
	req.OwnerEmail = "not-an-email"
	_, err := svc.OnboardMerchant(context.Background(), req)
	if !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
	if prov.merchantCount() != 0 {
		t.Fatal("invalid email must not create merchant")
	}
}

func TestOnboardMerchant_OwnerMerchantIDNotClientControlled(t *testing.T) {
	prov := newMemOnboardingProvisioner()
	svc := NewOnboardingService(prov)

	resp, err := svc.OnboardMerchant(context.Background(), validOnboardReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := prov.committedUser(resp.Owner.ID)
	if u.MerchantID != resp.Merchant.ID {
		t.Fatalf("owner.merchant_id must equal created merchant id")
	}
}
