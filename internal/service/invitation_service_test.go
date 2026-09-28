package service

// invitation_service_test.go — Phase 8B unit tests for self-service team
// invitations: creation authorization, token security (hash-only storage,
// one-time disclosure, never logged), duplicate/expiry rules, merchant
// lifecycle gating, and atomic single-use acceptance.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// ─── Mock invitation repository ───────────────────────────────────────────────

// mockInvitationRepo is an in-memory MerchantInvitationRepository.
//
// It mirrors BOTH production transaction semantics:
//   - CreateWithEmailOutbox: the invitation INSERT and email_outbox INSERT
//     commit or roll back together (Phase 8C.3A) — every failure hook returns
//     BEFORE any state is written, so a failed call leaves no partial row,
//     exactly like the real ROLLBACK.
//   - Accept: the single-use claim is rolled back when the user insert fails
//     (e.g. unique-email race), so tests can assert the invitation stays
//     PENDING after a failed accept.
type mockInvitationRepo struct {
	byTokenHash map[string]*model.MerchantInvitation
	byID        map[uuid.UUID]*model.MerchantInvitation
	pendingKey  map[string]*model.MerchantInvitation // merchantID|email → PENDING inv
	outbox      []*model.EmailOutbox                 // committed outbox rows

	users *mockMerchantUserRepo // shared store — represents the same transaction

	// Failure hooks.
	createErr       error
	outboxErr       error // simulate email_outbox INSERT failure → whole tx rolls back
	expireErr       error
	getPendingErr   error
	markExpiredErr  error
	acceptClaimErr  error // forced claim classification error
	acceptEmailRace bool  // simulate unique-email violation inside Accept
	createRace      bool  // simulate concurrent pending unique violation on Create
}

func newMockInvitationRepo(users *mockMerchantUserRepo) *mockInvitationRepo {
	return &mockInvitationRepo{
		byTokenHash: make(map[string]*model.MerchantInvitation),
		byID:        make(map[uuid.UUID]*model.MerchantInvitation),
		pendingKey:  make(map[string]*model.MerchantInvitation),
		users:       users,
	}
}

func invPendingKey(merchantID uuid.UUID, email string) string {
	return merchantID.String() + "|" + email
}

func (m *mockInvitationRepo) CreateWithEmailOutbox(_ context.Context, inv *model.MerchantInvitation, entry *model.EmailOutbox) error {
	if m.createErr != nil {
		return m.createErr // invitation INSERT failed → ROLLBACK
	}
	if m.createRace {
		return repository.ErrInvitationAlreadyPending // concurrent insert won
	}
	if _, exists := m.pendingKey[invPendingKey(inv.MerchantID, inv.Email)]; exists {
		return repository.ErrInvitationAlreadyPending
	}
	if m.outboxErr != nil {
		return m.outboxErr // email_outbox INSERT failed → ROLLBACK both rows
	}
	// COMMIT: both rows become visible together.
	m.byTokenHash[inv.TokenHash] = inv
	m.byID[inv.ID] = inv
	m.pendingKey[invPendingKey(inv.MerchantID, inv.Email)] = inv
	m.outbox = append(m.outbox, entry)
	return nil
}

func (m *mockInvitationRepo) GetByTokenHash(_ context.Context, tokenHash string) (*model.MerchantInvitation, error) {
	inv, ok := m.byTokenHash[tokenHash]
	if !ok {
		return nil, repository.ErrInvitationNotFound
	}
	return inv, nil
}

func (m *mockInvitationRepo) GetPendingByMerchantEmail(_ context.Context, merchantID uuid.UUID, email string) (*model.MerchantInvitation, error) {
	if m.getPendingErr != nil {
		return nil, m.getPendingErr
	}
	inv, ok := m.pendingKey[invPendingKey(merchantID, email)]
	if !ok {
		return nil, repository.ErrInvitationNotFound
	}
	return inv, nil
}

func (m *mockInvitationRepo) ExpireStalePending(_ context.Context, merchantID uuid.UUID, email string) (int64, error) {
	if m.expireErr != nil {
		return 0, m.expireErr
	}
	inv, ok := m.pendingKey[invPendingKey(merchantID, email)]
	if !ok {
		return 0, nil
	}
	if inv.IsExpired(time.Now().UTC()) {
		inv.Status = model.InvitationStatusExpired
		inv.UpdatedAt = time.Now().UTC()
		delete(m.pendingKey, invPendingKey(merchantID, email))
		return 1, nil
	}
	return 0, nil
}

func (m *mockInvitationRepo) MarkExpired(_ context.Context, id uuid.UUID) error {
	if m.markExpiredErr != nil {
		return m.markExpiredErr
	}
	if inv, ok := m.byID[id]; ok && inv.Status == model.InvitationStatusPending {
		inv.Status = model.InvitationStatusExpired
		inv.UpdatedAt = time.Now().UTC()
		delete(m.pendingKey, invPendingKey(inv.MerchantID, inv.Email))
	}
	return nil
}

func (m *mockInvitationRepo) Accept(_ context.Context, invitationID uuid.UUID, user *model.MerchantUser) error {
	if m.acceptClaimErr != nil {
		return m.acceptClaimErr
	}
	inv, ok := m.byID[invitationID]
	if !ok {
		return repository.ErrInvitationNotFound
	}
	if inv.Status != model.InvitationStatusPending {
		if inv.Status == model.InvitationStatusAccepted {
			return repository.ErrInvitationAlreadyAccepted
		}
		return repository.ErrInvitationNotFound
	}
	if inv.IsExpired(time.Now().UTC()) {
		return repository.ErrInvitationNotFound
	}

	// Claim (rolled back if the user insert fails — mirrors the tx).
	now := time.Now().UTC()
	prevStatus, prevAccepted := inv.Status, inv.AcceptedAt
	inv.Status = model.InvitationStatusAccepted
	inv.AcceptedAt = &now
	inv.UpdatedAt = now
	delete(m.pendingKey, invPendingKey(inv.MerchantID, inv.Email))

	rollback := func() {
		inv.Status = prevStatus
		inv.AcceptedAt = prevAccepted
		m.pendingKey[invPendingKey(inv.MerchantID, inv.Email)] = inv
	}

	if m.acceptEmailRace {
		rollback()
		return repository.ErrMerchantUserEmailExists
	}
	if err := m.users.Create(context.Background(), user); err != nil {
		rollback()
		return err
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

const testInvitationTTL = 48 * time.Hour

// testDashboardBaseURL is the fixed SPA origin used by invitation-link tests.
const testDashboardBaseURL = "https://dashboard.example.test"

// newTestInvitationService wires the service exactly as production does after
// Phase 8C.3A — repository + TTL + dashboard base URL only. There is
// deliberately NO EmailSender parameter: the service has no sender dependency
// (email content commits into email_outbox instead).
func newTestInvitationService(
	invRepo *mockInvitationRepo,
	userRepo *mockMerchantUserRepo,
	merchantRepo *mockMerchantRepo,
) InvitationService {
	return NewInvitationService(invRepo, userRepo, merchantRepo, testInvitationTTL, testDashboardBaseURL)
}

// seedInviteeMerchant seeds an ACTIVE merchant and returns it.
// (seedMerchant lives in dashboard_user_service_test.go — same package.)
func seedInviteeMerchant(t *testing.T, merchantRepo *mockMerchantRepo) *model.Merchant {
	t.Helper()
	return seedMerchant(merchantRepo)
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ─── Create: authorization ───────────────────────────────────────────────────

func TestCreateInvitation_OwnerSuccess(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)

	merchant := seedInviteeMerchant(t, merchantRepo)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "  New.User@EXAMPLE.com ", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	// Token: one-time disclosure, correct shape (32 random bytes hex = 64 chars).
	if !hex64.MatchString(resp.Token) {
		t.Errorf("token: got %q, want 64 lowercase hex characters", resp.Token)
	}

	// Response fields.
	if resp.Email != "new.user@example.com" {
		t.Errorf("email: got %q, want normalised %q", resp.Email, "new.user@example.com")
	}
	if resp.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", resp.Role)
	}
	if resp.Status != model.InvitationStatusPending {
		t.Errorf("status: got %v, want PENDING", resp.Status)
	}
	if resp.MerchantID != merchant.ID {
		t.Error("merchant ID must come from the authenticated caller")
	}
	if resp.ID == uuid.Nil {
		t.Error("expected a generated invitation ID")
	}

	// Stored state: only the SHA-256 hash is persisted — never the plaintext.
	stored, ok := invRepo.byID[resp.ID]
	if !ok {
		t.Fatal("invitation not persisted in mock repo")
	}
	if stored.TokenHash == resp.Token {
		t.Error("plaintext token must never be stored")
	}
	if stored.TokenHash != HashRefreshTokenPublic(resp.Token) {
		t.Error("stored token_hash must be the SHA-256 hex of the plaintext token")
	}
	if len(stored.TokenHash) != 64 {
		t.Errorf("token hash length: got %d, want 64", len(stored.TokenHash))
	}

	// TTL: default48h (audit recommendation window48–72h).
	if got := stored.ExpiresAt.Sub(stored.CreatedAt); got != testInvitationTTL {
		t.Errorf("token TTL: got %v, want %v", got, testInvitationTTL)
	}
}

func TestCreateInvitation_AdminForbidden(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleAdmin, merchant.ID,
		model.CreateInvitationRequest{Email: "x@example.com", Role: model.DashboardUserRoleViewer})
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole for ADMIN, got %v", err)
	}
	if len(invRepo.byID) != 0 {
		t.Error("no invitation must be created for a forbidden caller")
	}
}

func TestCreateInvitation_ViewerForbidden(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleViewer, merchant.ID,
		model.CreateInvitationRequest{Email: "x@example.com", Role: model.DashboardUserRoleViewer})
	if !errors.Is(err, ErrInsufficientRole) {
		t.Errorf("expected ErrInsufficientRole for VIEWER, got %v", err)
	}
}

// ─── Create: validation + duplicates ─────────────────────────────────────────

func TestCreateInvitation_InvalidRoleRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	// Service-level defense in depth — the handler binding already rejects
	// non-enumerated roles, but the service must not silently default either.
	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "x@example.com", Role: "SUPERADMIN"})
	if !errors.Is(err, ErrInvalidRole) {
		t.Errorf("expected ErrInvalidRole, got %v", err)
	}
	if len(invRepo.byID) != 0 {
		t.Error("no invitation must be created for an invalid role")
	}
}

func TestCreateInvitation_InvalidEmailRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "not-an-email", Role: model.DashboardUserRoleViewer})
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestCreateInvitation_EmailAlreadyRegistered(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	// Global email uniqueness: an existing user blocks the invite — including
	// DISABLED users (they must be re-enabled, not re-invited).
	for _, status := range []model.DashboardUserStatus{
		model.DashboardUserStatusActive,
		model.DashboardUserStatusDisabled,
	} {
		// Note: the mock map is keyed by the normalised (lowercase) email —
		// matching the service's GetByEmail lookup.
		email := "taken." + strings.ToLower(string(status)) + "@example.com"
		userRepo.users[email] = &model.MerchantUser{
			ID: uuid.New(), MerchantID: uuid.New(), Email: email,
			Role: model.DashboardUserRoleViewer, Status: status,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}

		_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
			model.CreateInvitationRequest{Email: email, Role: model.DashboardUserRoleViewer})
		if !errors.Is(err, ErrEmailAlreadyExists) {
			t.Errorf("status %s: expected ErrEmailAlreadyExists, got %v", status, err)
		}
	}
	if len(invRepo.byID) != 0 {
		t.Error("no invitation must be created for a registered email")
	}
}

func TestCreateInvitation_DuplicatePendingRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	req := model.CreateInvitationRequest{Email: "dup@example.com", Role: model.DashboardUserRoleViewer}
	if _, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID, req); err != nil {
		t.Fatalf("first invitation: %v", err)
	}

	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID, req)
	if !errors.Is(err, ErrInvitationAlreadyPending) {
		t.Errorf("expected ErrInvitationAlreadyPending, got %v", err)
	}
	if len(invRepo.byID) != 1 {
		t.Errorf("expected exactly1 invitation, got %d", len(invRepo.byID))
	}
}

func TestCreateInvitation_StaleExpiredPendingReplaced(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	req := model.CreateInvitationRequest{Email: "stale@example.com", Role: model.DashboardUserRoleViewer}
	first, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID, req)
	if err != nil {
		t.Fatalf("first invitation: %v", err)
	}

	// Simulate the first invitation going stale (past expiry, still PENDING).
	old := invRepo.byID[first.ID]
	old.ExpiresAt = time.Now().UTC().Add(-time.Minute)

	// Re-inviting must succeed: the stale row is lazily EXPIRED and replaced.
	second, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID, req)
	if err != nil {
		t.Fatalf("re-invite after expiry must succeed, got %v", err)
	}
	if second.Token == first.Token {
		t.Error("re-invite must issue a fresh token")
	}
	if old.Status != model.InvitationStatusExpired {
		t.Errorf("stale invitation status: got %v, want EXPIRED", old.Status)
	}
	if len(invRepo.pendingKey) != 1 {
		t.Errorf("expected exactly1 pending invitation after re-invite, got %d", len(invRepo.pendingKey))
	}
}

func TestCreateInvitation_ConcurrentPendingRace(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	invRepo.createRace = true // DB partial unique index fires on insert

	_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "race@example.com", Role: model.DashboardUserRoleViewer})
	if !errors.Is(err, ErrInvitationAlreadyPending) {
		t.Errorf("expected ErrInvitationAlreadyPending for insert race, got %v", err)
	}
}

// ─── Create: merchant lifecycle ──────────────────────────────────────────────

func TestCreateInvitation_MerchantNotActive(t *testing.T) {
	for _, status := range []model.MerchantStatus{
		model.MerchantStatusSuspended,
		model.MerchantStatusInactive,
	} {
		userRepo := newMockMerchantUserRepo()
		merchantRepo := newMockMerchantRepo()
		invRepo := newMockInvitationRepo(userRepo)
		svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
		merchant := seedInviteeMerchant(t, merchantRepo)
		merchant.Status = status

		_, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
			model.CreateInvitationRequest{Email: "x@example.com", Role: model.DashboardUserRoleViewer})
		if !errors.Is(err, ErrMerchantNotActive) {
			t.Errorf("merchant %s: expected ErrMerchantNotActive, got %v", status, err)
		}
		if len(invRepo.byID) != 0 {
			t.Errorf("merchant %s: no invitation may be created", status)
		}
	}
}

// ─── Create: token security ──────────────────────────────────────────────────

func TestCreateInvitation_TokenAndEmailNeverLogged(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "secret.person@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, resp.Token) {
		t.Error("plaintext invitation token must never be logged")
	}
	if stored := invRepo.byID[resp.ID]; stored != nil && strings.Contains(logs, stored.TokenHash) {
		t.Error("token hash must never be logged either")
	}
	if strings.Contains(logs, "secret.person@example.com") {
		t.Error("invitee email must never be logged")
	}
}

// ─── Get metadata ────────────────────────────────────────────────────────────

func TestGetInvitation_ValidToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo) // Name = "Test Merchant"

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "preview@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	meta, err := svc.GetInvitationByToken(context.Background(), created.Token)
	if err != nil {
		t.Fatalf("GetInvitationByToken: %v", err)
	}
	if meta.Email != "preview@example.com" {
		t.Errorf("email: got %q", meta.Email)
	}
	if meta.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", meta.Role)
	}
	if meta.MerchantName != merchant.Name {
		t.Errorf("merchant name: got %q, want %q", meta.MerchantName, merchant.Name)
	}
	if meta.Status != model.InvitationStatusPending {
		t.Errorf("status: got %v, want PENDING", meta.Status)
	}
	if !meta.ExpiresAt.Equal(created.ExpiresAt) {
		t.Errorf("expires_at: got %v, want %v", meta.ExpiresAt, created.ExpiresAt)
	}
	// The metadata response structurally has no token-hash field.
}

func TestGetInvitation_UnknownToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	seedInviteeMerchant(t, merchantRepo)

	_, err := svc.GetInvitationByToken(context.Background(), strings.Repeat("f", 64))
	if !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("expected ErrInvitationNotFound, got %v", err)
	}
}

func TestGetInvitation_ExpiredToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "expired@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	invRepo.byID[created.ID].ExpiresAt = time.Now().UTC().Add(-time.Minute)

	_, err = svc.GetInvitationByToken(context.Background(), created.Token)
	if !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("expired token: expected ErrInvitationNotFound, got %v", err)
	}
	// Lazy expiry must be persisted.
	if invRepo.byID[created.ID].Status != model.InvitationStatusExpired {
		t.Errorf("expected lazy EXPIRED transition, got %v", invRepo.byID[created.ID].Status)
	}
}

func TestGetInvitation_AcceptedTokenLooksInvalid(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "consume@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if _, err := svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "acceptpassword"}); err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}

	// Already-accepted tokens are indistinguishable from unknown ones on the
	// metadata endpoint (no state disclosure to token probes).
	if _, err := svc.GetInvitationByToken(context.Background(), created.Token); !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("accepted token: expected ErrInvitationNotFound, got %v", err)
	}
}

func TestGetInvitation_SuspendedMerchantStillPreviewable(t *testing.T) {
	// Reading metadata is not a mutation — only acceptance enforces ACTIVE.
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "later@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	merchant.Status = model.MerchantStatusSuspended

	meta, err := svc.GetInvitationByToken(context.Background(), created.Token)
	if err != nil {
		t.Fatalf("metadata preview must not be blocked by merchant suspension: %v", err)
	}
	if meta.Email != "later@example.com" {
		t.Errorf("email: got %q", meta.Email)
	}
}

// ─── Accept ──────────────────────────────────────────────────────────────────

func TestAcceptInvitation_Success(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "joiner@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	const password = "acceptedpassword123"
	user, err := svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: password})
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}

	// New user shape: ACTIVE, invited role, invitation's merchant.
	if user.Email != "joiner@example.com" {
		t.Errorf("email: got %q", user.Email)
	}
	if user.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role: got %v, want ADMIN", user.Role)
	}
	if user.Status != model.DashboardUserStatusActive {
		t.Errorf("status: got %v, want ACTIVE", user.Status)
	}
	if user.MerchantID != merchant.ID {
		t.Error("user must belong to the invitation's merchant — never client-supplied")
	}

	// Password: stored only as an Argon2id hash that verifies.
	stored := userRepo.usersByID[user.ID]
	if stored == nil {
		t.Fatal("user not persisted")
	}
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$") {
		t.Errorf("password hash format: got %q, want Argon2id PHC", stored.PasswordHash)
	}
	if !verifyPassword(password, stored.PasswordHash) {
		t.Error("stored hash must verify the accepted password")
	}

	// Invitation consumed atomically: single-use.
	inv := invRepo.byID[created.ID]
	if inv.Status != model.InvitationStatusAccepted {
		t.Errorf("invitation status: got %v, want ACCEPTED", inv.Status)
	}
	if inv.AcceptedAt == nil {
		t.Error("accepted_at must be set")
	}
	if _, ok := invRepo.pendingKey[invPendingKey(merchant.ID, "joiner@example.com")]; ok {
		t.Error("accepted invitation must no longer be pending")
	}
}

func TestAcceptInvitation_InvitedAsOwnerAllowed(t *testing.T) {
	// Phase 8A parity: an OWNER caller may promote a member to OWNER via
	// UpdateUserRole — so OWNER invitations are allowed for OWNER callers and
	// do not bypass the role-management rules.
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "coowner@example.com", Role: model.DashboardUserRoleOwner})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	user, err := svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "coownerpassword1"})
	if err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}
	if user.Role != model.DashboardUserRoleOwner {
		t.Errorf("role: got %v, want OWNER", user.Role)
	}
}

func TestAcceptInvitation_ReplayRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "once@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if _, err := svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "firstpassword11"}); err != nil {
		t.Fatalf("first accept: %v", err)
	}

	// Single-use: replay → conflict (audit R5), and no second user is created.
	usersBefore := len(userRepo.usersByID)
	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "secondpassword1"})
	if !errors.Is(err, ErrInvitationAlreadyAccepted) {
		t.Errorf("replay: expected ErrInvitationAlreadyAccepted, got %v", err)
	}
	if len(userRepo.usersByID) != usersBefore {
		t.Error("replay must not create a second user")
	}
}

func TestAcceptInvitation_UnknownToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	seedInviteeMerchant(t, merchantRepo)

	_, err := svc.AcceptInvitation(context.Background(), strings.Repeat("0", 64),
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("expected ErrInvitationNotFound, got %v", err)
	}
}

func TestAcceptInvitation_ExpiredToken(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "toolate@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	invRepo.byID[created.ID].ExpiresAt = time.Now().UTC().Add(-time.Second)

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrInvitationNotFound) {
		t.Errorf("expired token: expected ErrInvitationNotFound, got %v", err)
	}
	if len(userRepo.usersByID) != 0 {
		t.Error("expired token must not create a user")
	}
	if invRepo.byID[created.ID].Status != model.InvitationStatusExpired {
		t.Error("expected lazy EXPIRED transition")
	}
}

func TestAcceptInvitation_MerchantSuspendedAfterInvite(t *testing.T) {
	// The invitation was created while ACTIVE; the merchant is suspended
	// before acceptance — acceptance must be rejected.
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "blocked@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	merchant.Status = model.MerchantStatusSuspended

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrMerchantNotActive) {
		t.Errorf("suspended merchant: expected ErrMerchantNotActive, got %v", err)
	}
	if len(userRepo.usersByID) != 0 {
		t.Error("no user may be created for a suspended merchant")
	}
	if invRepo.byID[created.ID].Status != model.InvitationStatusPending {
		t.Error("invitation must remain PENDING when acceptance is blocked")
	}
}

func TestAcceptInvitation_EmailRegisteredSinceInvite(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "taken.later@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	// Someone registers the email after the invitation was created.
	userRepo.users["taken.later@example.com"] = &model.MerchantUser{
		ID: uuid.New(), MerchantID: uuid.New(), Email: "taken.later@example.com",
		Role: model.DashboardUserRoleViewer, Status: model.DashboardUserStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrEmailAlreadyExists) {
		t.Errorf("expected ErrEmailAlreadyExists, got %v", err)
	}
}

func TestAcceptInvitation_EmailRaceInsideTransaction(t *testing.T) {
	// The pre-check passed, but the unique-email constraint fires inside the
	// acceptance transaction (concurrent registration) — the transaction must
	// roll back, leaving the invitation PENDING.
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "raced@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	invRepo.acceptEmailRace = true

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrEmailAlreadyExists) {
		t.Errorf("expected ErrEmailAlreadyExists, got %v", err)
	}
	if invRepo.byID[created.ID].Status != model.InvitationStatusPending {
		t.Errorf("rollback: invitation must stay PENDING, got %v", invRepo.byID[created.ID].Status)
	}
	if len(userRepo.usersByID) != 0 {
		t.Error("no user may be created on a failed acceptance")
	}
}

func TestAcceptInvitation_ConcurrentClaimRace(t *testing.T) {
	// Another request consumed the token between our read and the claim —
	// classified as already-accepted (audit R5 → conflict).
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "claimrace@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	invRepo.acceptClaimErr = repository.ErrInvitationAlreadyAccepted

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "validpassword1"})
	if !errors.Is(err, ErrInvitationAlreadyAccepted) {
		t.Errorf("expected ErrInvitationAlreadyAccepted, got %v", err)
	}
}

func TestAcceptInvitation_ShortPasswordRejected(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "weak@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	_, err = svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: "short"})
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}
	if len(userRepo.usersByID) != 0 {
		t.Error("no user may be created with a weak password")
	}
	if invRepo.byID[created.ID].Status != model.InvitationStatusPending {
		t.Error("invitation must remain PENDING after a rejected password")
	}
}

func TestAcceptInvitation_PasswordAndTokenNeverLogged(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	created, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "quiet@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	const password = "supersecretpass99"
	if _, err := svc.AcceptInvitation(context.Background(), created.Token,
		model.AcceptInvitationRequest{Password: password}); err != nil {
		t.Fatalf("AcceptInvitation: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, password) {
		t.Error("plaintext password must never be logged")
	}
	if strings.Contains(logs, created.Token) {
		t.Error("plaintext invitation token must never be logged")
	}
	if strings.Contains(logs, "quiet@example.com") {
		t.Error("invitee email must never be logged")
	}
}

// ─── Phase 8C.3A: atomic email outbox ────────────────────────────────────────

// invitationURLFromText extracts the first absolute URL from a plain-text
// body and parses it with the standard URL API. In 8C.3A it operates on the
// COMMITTED email_outbox text body — the actual queued email payload.
func invitationURLFromText(t *testing.T, body string) *url.URL {
	t.Helper()
	m := urlInText.FindString(body)
	if m == "" {
		t.Fatalf("no URL found in body:\n%s", body)
	}
	u, err := url.Parse(m)
	if err != nil {
		t.Fatalf("parse URL %q: %v", m, err)
	}
	return u
}

var urlInText = regexp.MustCompile(`https?://[^\s]+`)

func invitationURLFromHTML(t *testing.T, body string) *url.URL {
	t.Helper()
	m := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no href found in HTML:\n%s", body)
	}
	u, err := url.Parse(m[1]) // URL-escaped by html/template if needed
	if err != nil {
		t.Fatalf("parse href %q: %v", m[1], err)
	}
	return u
}

// onlyOutbox returns the single committed outbox row or fails the test.
func onlyOutbox(t *testing.T, invRepo *mockInvitationRepo) *model.EmailOutbox {
	t.Helper()
	if len(invRepo.outbox) != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1", len(invRepo.outbox))
	}
	return invRepo.outbox[0]
}

// ─── §21.A — atomicity: success ──────────────────────────────────────────────

func TestCreateInvitation_QueuesAtomicEmailOutbox(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo) // Name = "Test Merchant"

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "invitee@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}

	// Both rows committed together: the invitation…
	stored, ok := invRepo.byID[resp.ID]
	if !ok || stored.Status != model.InvitationStatusPending {
		t.Fatalf("invitation not persisted as PENDING: %+v", stored)
	}
	// …and EXACTLY ONE outbox row with the §15 delivery defaults.
	entry := onlyOutbox(t, invRepo)
	if entry.Status != model.EmailOutboxStatusPending {
		t.Errorf("outbox status = %v, want PENDING (no worker exists in 8C.3A)", entry.Status)
	}
	if entry.AttemptCount != 0 {
		t.Errorf("attempt_count = %d, want 0", entry.AttemptCount)
	}
	if entry.NextAttemptAt.IsZero() {
		t.Error("next_attempt_at must be populated at enqueue time")
	}
	if entry.ProcessingAt != nil {
		t.Errorf("processing_at = %v, want NULL", entry.ProcessingAt)
	}
	if entry.LastAttemptAt != nil {
		t.Errorf("last_attempt_at = %v, want NULL", entry.LastAttemptAt)
	}
	if entry.SentAt != nil {
		t.Errorf("sent_at = %v, want NULL — nothing has been sent in 8C.3A", entry.SentAt)
	}
	if entry.LastError != nil {
		t.Errorf("last_error = %v, want NULL", entry.LastError)
	}
	if entry.Type != model.EmailOutboxTypeInvitation {
		t.Errorf("type = %q, want INVITATION", entry.Type)
	}

	// Rendered content: recipient, subject, bodies with role/link/expiry.
	if entry.Recipient != "invitee@example.com" {
		t.Errorf("recipient = %q, want invitee@example.com", entry.Recipient)
	}
	if !strings.Contains(entry.Subject, merchant.Name) {
		t.Errorf("Subject = %q, want merchant name %q", entry.Subject, merchant.Name)
	}
	if strings.TrimSpace(entry.TextBody) == "" || strings.TrimSpace(entry.HTMLBody) == "" {
		t.Error("both plain-text and HTML bodies are required")
	}
	textURL := invitationURLFromText(t, entry.TextBody)
	for _, want := range []string{"ADMIN", textURL.String()} {
		if !strings.Contains(entry.TextBody, want) {
			t.Errorf("text_body missing %q", want)
		}
	}
	if !strings.Contains(entry.TextBody, "expires at") {
		t.Errorf("text_body missing expiry information:\n%s", entry.TextBody)
	}
	if !strings.Contains(entry.HTMLBody, "ADMIN") {
		t.Error("html_body missing the invited role")
	}

	// §19: response stays the Phase 8B contract — no outbox internals leak.
	if resp.Token == "" || resp.Status != model.InvitationStatusPending {
		t.Errorf("response changed shape: %+v", resp)
	}
	respJSON, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, forbidden := range []string{"outbox", "delivery_status", "attempt", "recipient", "subject", "body"} {
		if strings.Contains(strings.ToLower(string(respJSON)), forbidden) {
			t.Errorf("internal field %q leaked into the response: %s", forbidden, respJSON)
		}
	}
}

// ─── §12/§21.B — exactly one token generation ────────────────────────────────

func TestCreateInvitation_OutboxBodiesUseSameTokenAsResponse(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "same.token@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation failed: %v", err)
	}
	entry := onlyOutbox(t, invRepo)

	// The token embedded in the COMMITTED email_outbox URL equals the API
	// response token — never generated, hashed, or re-fetched separately.
	textURL := invitationURLFromText(t, entry.TextBody)
	if textURL.Scheme != "https" || textURL.Host != "dashboard.example.test" {
		t.Errorf("text URL origin = %s://%s, want https://dashboard.example.test", textURL.Scheme, textURL.Host)
	}
	if textURL.Path != "/accept-invitation" {
		t.Errorf("text URL path = %q, want /accept-invitation", textURL.Path)
	}
	if tok := textURL.Query().Get("token"); tok != resp.Token {
		t.Errorf("outbox token = %q, want response token %q — a second token must never be generated", tok, resp.Token)
	}
	if len(textURL.Query()) != 1 {
		t.Errorf("query = %v, want exactly the token parameter", textURL.Query())
	}

	// The HTML link in html_body must carry the identical token.
	htmlURL := invitationURLFromHTML(t, entry.HTMLBody)
	if tok := htmlURL.Query().Get("token"); tok != resp.Token {
		t.Errorf("HTML link token = %q, want response token %q", tok, resp.Token)
	}

	// The stored hash is the SHA-256 of that SAME token (single generation).
	stored := invRepo.byID[resp.ID]
	if stored == nil || stored.TokenHash != HashRefreshTokenPublic(resp.Token) {
		t.Error("invitation token_hash must be the hash of the response token")
	}
}

// ─── §21.C + §23 — token scoping and token_hash absence ──────────────────────

func TestCreateInvitation_TokenScopedToOutboxBodiesAndHashNeverStored(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "hash.check@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	stored := invRepo.byID[resp.ID]
	if stored == nil || stored.TokenHash == "" {
		t.Fatal("expected a stored token_hash")
	}
	entry := onlyOutbox(t, invRepo)

	// §21.C: the token_hash appears in NONE of the outbox fields.
	for field, value := range map[string]string{
		"recipient": entry.Recipient,
		"subject":   entry.Subject,
		"text_body": entry.TextBody,
		"html_body": entry.HTMLBody,
	} {
		if strings.Contains(value, stored.TokenHash) {
			t.Errorf("token_hash leaked into outbox field %s", field)
		}
	}

	// §23: the plaintext token exists ONLY in the intended email bodies —
	// never in recipient, subject, status/type fields, or the response body
	// beyond the documented one-time token field.
	if strings.Contains(entry.Recipient, resp.Token) || strings.Contains(entry.Subject, resp.Token) {
		t.Error("plaintext token must appear only in email bodies, not recipient/subject")
	}
	if !strings.Contains(entry.TextBody, resp.Token) || !strings.Contains(entry.HTMLBody, resp.Token) {
		t.Error("the queued email bodies must contain the invitation token (the payload itself)")
	}

	respJSON, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(respJSON), stored.TokenHash) {
		t.Error("token_hash must never appear in the API response")
	}
	// Backward compatibility: the one-time plaintext token IS still in the
	// response (Phase 8B contract preserved).
	if !strings.Contains(string(respJSON), resp.Token) {
		t.Error("response must still include the one-time plaintext token (Phase 8B)")
	}
}

// ─── §10/§21.D — duplicate invitation stays 409, no orphan outbox ────────────

func TestCreateInvitation_DuplicatePendingLeavesNoSecondOutbox(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	first, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "dup@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("first CreateInvitation: %v", err)
	}

	// Second invite for the same email → the service-level conflict the
	// handler maps to HTTP 409 (ErrInvitationAlreadyPending).
	_, err = svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "dup@example.com", Role: model.DashboardUserRoleViewer})
	if !errors.Is(err, ErrInvitationAlreadyPending) {
		t.Fatalf("second CreateInvitation err = %v, want ErrInvitationAlreadyPending (→ 409)", err)
	}

	// Exactly one invitation and ONE outbox row — the failed duplicate
	// transaction rolled back both of its INSERTs (§10: no second outbox row).
	if len(invRepo.byID) != 1 {
		t.Errorf("invitations = %d, want 1", len(invRepo.byID))
	}
	entry := onlyOutbox(t, invRepo)
	if entry.ReferenceID == nil || *entry.ReferenceID != first.ID {
		t.Errorf("outbox reference_id = %v, want first invitation %v", entry.ReferenceID, first.ID)
	}
	if entry.Status != model.EmailOutboxStatusPending || entry.AttemptCount != 0 {
		t.Errorf("first outbox row was mutated by the failed duplicate: status=%v attempts=%d",
			entry.Status, entry.AttemptCount)
	}
}

// ─── §21.E — outbox INSERT failure rolls back the invitation ─────────────────

func TestCreateInvitation_OutboxFailureRollsBackInvitation(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	invRepo.outboxErr = errors.New("email_outbox insert failed")
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "rollback@example.com", Role: model.DashboardUserRoleAdmin})
	if resp != nil {
		t.Fatalf("response must be nil when the transaction fails, got %+v", resp)
	}
	if err == nil {
		t.Fatal("outbox INSERT failure must fail invitation creation (never swallowed)")
	}
	// A DB failure is NOT the duplicate conflict — it must not be misreported
	// as 409.
	if errors.Is(err, ErrInvitationAlreadyPending) || errors.Is(err, repository.ErrInvitationAlreadyPending) {
		t.Fatalf("outbox failure misclassified as duplicate-pending: %v", err)
	}
	// Operational error text: cause only — no invitee email, no token material.
	if strings.Contains(err.Error(), "rollback@example.com") {
		t.Error("invitee email must not appear in the error")
	}

	// Rollback verification: NEITHER row exists.
	if len(invRepo.byID) != 0 || len(invRepo.byTokenHash) != 0 {
		t.Errorf("invitation must NOT be persisted on outbox failure: byID=%d byTokenHash=%d",
			len(invRepo.byID), len(invRepo.byTokenHash))
	}
	if len(invRepo.outbox) != 0 {
		t.Errorf("outbox rows = %d, want 0", len(invRepo.outbox))
	}
	// The (merchant, email) slot was NOT burned by the failed transaction —
	// a retry succeeds once the database is healthy again.
	if _, err := invRepo.GetPendingByMerchantEmail(context.Background(), merchant.ID, "rollback@example.com"); !errors.Is(err, repository.ErrInvitationNotFound) {
		t.Fatalf("pending slot must be free after rollback, got %v", err)
	}
	invRepo.outboxErr = nil
	retried, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "rollback@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("retry after rollback must succeed: %v", err)
	}
	if retried == nil || invRepo.byID[retried.ID] == nil {
		t.Fatalf("retry must persist the invitation, got %+v", retried)
	}
	if len(invRepo.byID) != 1 || len(invRepo.outbox) != 1 {
		t.Errorf("retry must atomically persist both rows: invitations=%d outbox=%d",
			len(invRepo.byID), len(invRepo.outbox))
	}
}

// ─── §22 — content assertions moved from EmailSender to the outbox entry ─────

func TestCreateInvitation_OutboxUsesAuthenticatedMerchantName(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)

	callerMerchant := seedInviteeMerchant(t, merchantRepo) // Name = "Test Merchant"

	// A second tenant exists in the same repository — its name must never
	// leak into another merchant's queued email.
	rival := &model.Merchant{
		ID:        uuid.New(),
		Name:      "Rival Merchant",
		Code:      "rivalmerchant",
		Status:    model.MerchantStatusActive,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	merchantRepo.merchants[rival.ID] = rival

	// The request struct has no merchant field at all — tenant identity and
	// merchant name come exclusively from the authenticated caller context.
	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, callerMerchant.ID,
		model.CreateInvitationRequest{Email: "isolation@example.com", Role: model.DashboardUserRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if resp.MerchantID != callerMerchant.ID {
		t.Errorf("merchant = %v, want caller merchant %v", resp.MerchantID, callerMerchant.ID)
	}

	entry := onlyOutbox(t, invRepo)
	if !strings.Contains(entry.Subject, callerMerchant.Name) || !strings.Contains(entry.TextBody, callerMerchant.Name) {
		t.Errorf("queued email must carry the authenticated merchant's name %q", callerMerchant.Name)
	}
	if strings.Contains(entry.Subject, rival.Name) || strings.Contains(entry.TextBody, rival.Name) || strings.Contains(entry.HTMLBody, rival.Name) {
		t.Errorf("another merchant's name leaked into the queued email:\nsubject=%q", entry.Subject)
	}
}

// ─── §24 — tenant isolation of the outbox row ────────────────────────────────

func TestCreateInvitation_OutboxTenancyMatchesInvitation(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)

	callerMerchant := seedInviteeMerchant(t, merchantRepo)
	rival := &model.Merchant{
		ID:        uuid.New(),
		Name:      "Rival Merchant",
		Code:      "rivalmerchant",
		Status:    model.MerchantStatusActive,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	merchantRepo.merchants[rival.ID] = rival

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, callerMerchant.ID,
		model.CreateInvitationRequest{Email: "tenancy@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	stored := invRepo.byID[resp.ID]
	if stored == nil {
		t.Fatal("invitation not persisted")
	}
	entry := onlyOutbox(t, invRepo)

	// invitation.MerchantID == emailOutbox.MerchantID (authenticated context,
	// never the rival tenant, never client-supplied).
	if entry.MerchantID != stored.MerchantID || entry.MerchantID != resp.MerchantID || entry.MerchantID != callerMerchant.ID {
		t.Errorf("outbox merchant_id = %v, want invitation/response/caller merchant %v",
			entry.MerchantID, callerMerchant.ID)
	}
	if entry.MerchantID == rival.ID {
		t.Error("outbox row bound to the wrong merchant")
	}
	// invitation.ID == emailOutbox.ReferenceID — no cross-merchant
	// association is possible.
	if entry.ReferenceID == nil || *entry.ReferenceID != stored.ID || *entry.ReferenceID != resp.ID {
		t.Errorf("outbox reference_id = %v, want invitation id %v", entry.ReferenceID, resp.ID)
	}
}

// ─── §14/§22 — no EmailSender dependency anywhere in the flow ────────────────

func TestCreateInvitation_QueuesAndAcceptsWithoutAnyEmailSender(t *testing.T) {
	userRepo := newMockMerchantUserRepo()
	merchantRepo := newMockMerchantRepo()
	invRepo := newMockInvitationRepo(userRepo)
	// Construction takes NO EmailSender — structurally, no sender (enabled,
	// disabled, or broken SMTP) can participate in invitation creation.
	svc := newTestInvitationService(invRepo, userRepo, merchantRepo)
	merchant := seedInviteeMerchant(t, merchantRepo)

	resp, err := svc.CreateInvitation(context.Background(), model.DashboardUserRoleOwner, merchant.ID,
		model.CreateInvitationRequest{Email: "offline@example.com", Role: model.DashboardUserRoleAdmin})
	if err != nil {
		t.Fatalf("invitation creation must succeed with no sender in the system: %v", err)
	}
	if !hex64.MatchString(resp.Token) {
		t.Fatalf("token = %q, want 64 hex chars", resp.Token)
	}
	stored, ok := invRepo.byID[resp.ID]
	if !ok || stored.Status != model.InvitationStatusPending {
		t.Fatalf("invitation not persisted as PENDING: %+v", stored)
	}
	if stored.TokenHash != HashRefreshTokenPublic(resp.Token) {
		t.Error("token_hash must be stored regardless of any email concerns")
	}
	// The queued job exists and waits for the Phase 8C.3B worker.
	entry := onlyOutbox(t, invRepo)
	if entry.Status != model.EmailOutboxStatusPending {
		t.Errorf("outbox status = %v, want PENDING until 8C.3B", entry.Status)
	}

	// The token remains fully usable — the outbox never interfered.
	accepted, err := svc.AcceptInvitation(context.Background(), resp.Token,
		model.AcceptInvitationRequest{Password: "supersecretpass99"})
	if err != nil {
		t.Fatalf("AcceptInvitation with queued email: %v", err)
	}
	if accepted.Role != model.DashboardUserRoleAdmin {
		t.Errorf("role = %v, want ADMIN", accepted.Role)
	}
}
