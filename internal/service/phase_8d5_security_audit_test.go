package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/audit"
	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type phase8D5Fixture struct {
	pool         *pgxpool.Pool
	auditRepo    repository.AuditLogRepository
	auditSvc     AuditService
	merchantRepo repository.MerchantRepository
	userRepo     repository.MerchantUserRepository
	keyRepo      repository.MerchantAPIKeyRepository
	inviteRepo   repository.MerchantInvitationRepository
	webhookRepo  repository.MerchantWebhookConfigRepository
	deliveryRepo repository.MerchantWebhookDeliveryRepository
	sessionRepo  repository.DashboardSessionRepository
}

func phase8D5Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping Phase 8D.5 PostgreSQL audit test")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping test database: %v", err)
	}
	var version int
	var dirty bool
	if err := pool.QueryRow(context.Background(), `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		pool.Close()
		t.Fatalf("read migration state: %v", err)
	}
	if dirty || version < 18 {
		pool.Close()
		t.Fatalf("migration state version=%d dirty=%t, want version>=18 and clean", version, dirty)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newPhase8D5Fixture(t *testing.T) phase8D5Fixture {
	t.Helper()
	pool := phase8D5Pool(t)
	auditRepo := repository.NewAuditLogRepository(pool)
	auditSvc := NewAuditService(auditRepo)
	return phase8D5Fixture{
		pool:         pool,
		auditRepo:    auditRepo,
		auditSvc:     auditSvc,
		merchantRepo: repository.NewMerchantRepository(pool, auditSvc),
		userRepo:     repository.NewMerchantUserRepository(pool, auditSvc),
		keyRepo:      repository.NewMerchantAPIKeyRepository(pool, auditSvc),
		inviteRepo: repository.NewMerchantInvitationRepository(
			pool, repository.NewEmailOutboxRepository(pool), auditSvc,
		),
		webhookRepo:  repository.NewMerchantWebhookConfigRepository(pool, auditSvc),
		deliveryRepo: repository.NewMerchantWebhookDeliveryRepository(pool),
		sessionRepo:  repository.NewDashboardSessionRepository(pool),
	}
}

func phase8D5SeedMerchant(t *testing.T, f phase8D5Fixture, state model.LegacyCredentialState) uuid.UUID {
	t.Helper()
	merchantID := uuid.New()
	now := time.Now().UTC()
	var legacyKey any
	var legacySecret any
	if state == model.LegacyCredentialStateLegacy {
		legacyKey = "pk_TEST_LEGACY_CREDENTIAL_456_" + uuid.NewString()[:8]
		sum := sha256.Sum256([]byte("legacy-secret-for-test"))
		legacySecret = hex.EncodeToString(sum[:])
	}
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO merchants
		    (id, name, code, api_key, api_secret, status,
		     legacy_credential_state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $7, $7)
	`, merchantID, "Phase 8D.5 Audit Merchant", "phase8d5-"+merchantID.String(), legacyKey, legacySecret, state, now)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id = $1`, merchantID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, merchantID)
	})
	return merchantID
}

func phase8D5SeedUser(t *testing.T, f phase8D5Fixture, merchantID uuid.UUID, role model.DashboardUserRole, password string) uuid.UUID {
	t.Helper()
	userID := uuid.New()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hash seed password: %v", err)
	}
	_, err = f.pool.Exec(context.Background(), `
		INSERT INTO merchant_users
		    (id, merchant_id, email, password_hash, role, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'ACTIVE', $6, $6)
	`, userID, merchantID, "phase8d5-"+userID.String()+"@example.invalid", hash, role, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed dashboard user: %v", err)
	}
	return userID
}

func phase8D5Context(actorType audit.ActorType, userID, merchantID *uuid.UUID) context.Context {
	ctx := audit.WithRequestMetadata(context.Background(), audit.RequestMetadata{
		RequestID: "req_phase8d5_" + strings.ToLower(string(actorType)),
		IP:        "203.0.113.44",
	})
	return audit.WithActor(ctx, audit.Actor{Type: actorType, UserID: userID, MerchantID: merchantID})
}

func phase8D5AuditRows(t *testing.T, f phase8D5Fixture, merchantID uuid.UUID) []model.AuditLog {
	t.Helper()
	rows, err := f.auditRepo.ListByMerchant(context.Background(), merchantID, 100, 0)
	if err != nil {
		t.Fatalf("list audit rows: %v", err)
	}
	return rows
}

func phase8D5RequireActions(t *testing.T, rows []model.AuditLog, actions ...audit.Action) {
	t.Helper()
	seen := make(map[audit.Action]bool, len(rows))
	for _, row := range rows {
		seen[audit.Action(row.Action)] = true
	}
	for _, action := range actions {
		if !seen[action] {
			t.Errorf("missing audit action %q; rows=%#v", action, rows)
		}
	}
}

func phase8D5AssertNoSecrets(t *testing.T, rows []model.AuditLog, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal audit rows: %v", err)
	}
	text := string(raw)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Errorf("audit rows contain secret %q: %s", secret, text)
		}
	}
}

func TestPhase8D5OnboardingAndMerchantStatusAudit(t *testing.T) {
	f := newPhase8D5Fixture(t)
	ctx := phase8D5Context(audit.ActorTypeAdmin, nil, nil)
	password := "TEST_PASSWORD_SECRET_123"
	onboarding := NewOnboardingService(NewPGOnboardingProvisioner(
		f.pool, f.merchantRepo, f.userRepo, f.keyRepo, f.auditSvc,
	))
	created, err := onboarding.OnboardMerchant(ctx, model.OnboardMerchantRequest{
		Name:          "Audited Onboarding Merchant",
		Code:          "phase8d5-onboard-" + uuid.NewString()[:8],
		OwnerEmail:    "phase8d5-onboard-" + uuid.NewString() + "@example.invalid",
		OwnerPassword: password,
	})
	if err != nil {
		t.Fatalf("onboard merchant: %v", err)
	}
	merchantID := created.Merchant.ID
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE merchant_id = $1`, merchantID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM merchants WHERE id = $1`, merchantID)
	})

	rows := phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows, audit.ActionMerchantCreated)
	phase8D5AssertNoSecrets(t, rows, password, created.APICredential.Secret)

	merchantSvc := NewMerchantServiceWithSessions(f.merchantRepo, f.sessionRepo)
	if _, err := merchantSvc.UpdateMerchantStatus(ctx, merchantID, model.MerchantStatusSuspended); err != nil {
		t.Fatalf("update merchant status: %v", err)
	}
	rows = phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows, audit.ActionMerchantCreated, audit.ActionMerchantStatusChanged)

	// Same-status requests are idempotent and must not manufacture a second
	// transition event.
	if _, err := merchantSvc.UpdateMerchantStatus(ctx, merchantID, model.MerchantStatusSuspended); err != nil {
		t.Fatalf("idempotent merchant status update: %v", err)
	}
	if got := len(phase8D5AuditRows(t, f, merchantID)); got != 2 {
		t.Fatalf("audit row count after idempotent status update = %d, want 2", got)
	}
}

func TestPhase8D5TeamAndPasswordAudit(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	callerID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "caller-password-123")
	targetID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "target-password-123")
	callerCtx := phase8D5Context(audit.ActorTypeDashboardUser, &callerID, &merchantID)
	adminCtx := phase8D5Context(audit.ActorTypeAdmin, nil, nil)

	dashboardSvc := NewDashboardUserService(f.userRepo, f.sessionRepo, f.merchantRepo)
	if _, err := dashboardSvc.CreateUser(adminCtx, merchantID, model.CreateDashboardUserRequest{
		Email:    "phase8d5-created-" + uuid.NewString() + "@example.invalid",
		Password: "created-password-123",
		Role:     model.DashboardUserRoleViewer,
	}); err != nil {
		t.Fatalf("create dashboard user: %v", err)
	}
	if _, err := dashboardSvc.UpdateUserStatus(callerCtx, callerID, model.DashboardUserRoleOwner, merchantID, targetID, model.DashboardUserStatusDisabled); err != nil {
		t.Fatalf("disable dashboard user: %v", err)
	}
	if _, err := dashboardSvc.UpdateUserRole(callerCtx, callerID, model.DashboardUserRoleOwner, merchantID, targetID, model.DashboardUserRoleAdmin); err != nil {
		t.Fatalf("change dashboard user role: %v", err)
	}
	if _, err := dashboardSvc.ChangePassword(callerCtx, callerID, model.ChangePasswordRequest{
		CurrentPassword: "caller-password-123",
		NewPassword:     "new-password-456",
	}); err != nil {
		t.Fatalf("change password: %v", err)
	}

	rows := phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows,
		audit.ActionUserCreated,
		audit.ActionUserStatusChanged,
		audit.ActionUserRoleChanged,
		audit.ActionPasswordChanged,
	)
	phase8D5AssertNoSecrets(t, rows,
		"caller-password-123", "target-password-123", "created-password-123", "new-password-456",
	)
}

func TestPhase8D5InvitationAuditNeverStoresBearerToken(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	callerID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "owner-password-123")
	invitationSvc := NewInvitationService(
		f.inviteRepo, f.userRepo, f.merchantRepo, time.Hour, "https://dashboard.example.invalid",
	)
	invitedEmail := "phase8d5-invitee-" + uuid.NewString() + "@example.invalid"
	created, err := invitationSvc.CreateInvitation(
		phase8D5Context(audit.ActorTypeDashboardUser, &callerID, &merchantID),
		model.DashboardUserRoleOwner,
		merchantID,
		model.CreateInvitationRequest{Email: invitedEmail, Role: model.DashboardUserRoleViewer},
	)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	password := "TEST_INVITATION_SECRET_ABC"
	if _, err := invitationSvc.AcceptInvitation(
		phase8D5Context(audit.ActorTypeUnauthenticated, nil, nil),
		created.Token,
		model.AcceptInvitationRequest{Password: password},
	); err != nil {
		t.Fatalf("accept invitation: %v", err)
	}

	rows := phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows, audit.ActionInvitationCreated, audit.ActionInvitationAccepted)
	phase8D5AssertNoSecrets(t, rows, created.Token, password)
}

func TestPhase8D5CredentialAndLegacyAuditNeverStoresSecrets(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	callerID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "owner-password-123")
	ctx := phase8D5Context(audit.ActorTypeDashboardUser, &callerID, &merchantID)
	apiSvc := NewMerchantAPIKeyService(f.keyRepo, f.merchantRepo)
	created, err := apiSvc.CreateKey(ctx, merchantID, model.CreateMerchantAPIKeyRequest{Name: "audited key"})
	if err != nil {
		t.Fatalf("create API key: %v", err)
	}
	rotated, err := apiSvc.RotateKey(ctx, merchantID, created.ID)
	if err != nil {
		t.Fatalf("rotate API key: %v", err)
	}
	if err := apiSvc.RevokeKey(ctx, merchantID, rotated.ID); err != nil {
		t.Fatalf("revoke API key: %v", err)
	}
	rows := phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows, audit.ActionAPIKeyCreated, audit.ActionAPIKeyRotated, audit.ActionAPIKeyRevoked)
	phase8D5AssertNoSecrets(t, rows, created.Secret, rotated.Secret)

	legacyMerchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateLegacy)
	legacySvc := NewLegacyCredentialService(repository.NewPGLegacyCredentialStore(f.pool, f.keyRepo, f.auditSvc))
	legacyCtx := phase8D5Context(audit.ActorTypeDashboardUser, &callerID, &legacyMerchantID)
	migrated, err := legacySvc.Migrate(legacyCtx, legacyMerchantID)
	if err != nil {
		t.Fatalf("migrate legacy credential: %v", err)
	}
	if _, err := legacySvc.Disable(legacyCtx, legacyMerchantID); err != nil {
		t.Fatalf("disable legacy credential: %v", err)
	}
	legacyRows := phase8D5AuditRows(t, f, legacyMerchantID)
	phase8D5RequireActions(t, legacyRows, audit.ActionLegacyCredentialMigrated, audit.ActionLegacyCredentialDisabled)
	phase8D5AssertNoSecrets(t, legacyRows, migrated.Secret, "pk_TEST_LEGACY_CREDENTIAL_456")
}

func TestPhase8D5WebhookAuditStoresOnlySafeConfigurationState(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	callerID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "owner-password-123")
	ctx := phase8D5Context(audit.ActorTypeDashboardUser, &callerID, &merchantID)
	webhookSvc := NewMerchantWebhookConfigService(
		f.webhookRepo, f.deliveryRepo, f.merchantRepo,
		[]byte("01234567890123456789012345678901"), true,
	)
	created, err := webhookSvc.Upsert(ctx, merchantID, model.UpsertMerchantWebhookRequest{
		URL: "https://hooks.example.com/audit?query=not-stored",
	})
	if err != nil {
		t.Fatalf("upsert webhook: %v", err)
	}
	rotated, err := webhookSvc.RotateSecret(ctx, merchantID)
	if err != nil {
		t.Fatalf("rotate webhook secret: %v", err)
	}
	if _, err := webhookSvc.Disable(ctx, merchantID); err != nil {
		t.Fatalf("disable webhook: %v", err)
	}
	rows := phase8D5AuditRows(t, f, merchantID)
	phase8D5RequireActions(t, rows, audit.ActionWebhookConfigChanged)
	for _, row := range rows {
		if row.TargetID == nil || *row.TargetID != created.ID {
			t.Fatalf("webhook audit target ID = %v, want committed config ID %s", row.TargetID, created.ID)
		}
	}
	phase8D5AssertNoSecrets(t, rows, created.Secret, rotated.Secret, "query=not-stored", "TEST_WEBHOOK_SECRET_DEF")
	if strings.Contains(string(rows[0].Metadata), "hooks.example.com") == false {
		t.Log("webhook host was not retained; safe metadata remains valid")
	}
}
