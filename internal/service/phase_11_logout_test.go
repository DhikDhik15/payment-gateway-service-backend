package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
)

// TestPhase11LogoutSessionModelA verifies the selected bounded-stateless
// contract against the real PostgreSQL session repository: logout revokes the
// refresh session, while an already-issued access JWT remains verifiable until
// its exp claim.
func TestPhase11LogoutSessionModelA(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	userID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "phase11-logout-password")
	email := "phase8d5-" + userID.String() + "@example.invalid"

	authSvc := NewAuthService(f.userRepo, f.sessionRepo, AuthConfig{
		JWTSecret:       []byte("phase11-logout-test-secret-32b!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 24 * time.Hour,
	}, f.merchantRepo)

	loginResp, oldRefresh, err := authSvc.Login(context.Background(), model.LoginRequest{
		Email: email, Password: "phase11-logout-password",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	firstClaims, err := authSvc.VerifyAccessToken(loginResp.AccessToken)
	if err != nil {
		t.Fatalf("verify login token: %v", err)
	}
	firstSessionID, err := uuid.Parse(firstClaims.SessionID)
	if err != nil {
		t.Fatalf("parse login sid: %v", err)
	}

	refreshed, replacementRefresh, err := authSvc.RefreshAccessToken(context.Background(), oldRefresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	refreshedClaims, err := authSvc.VerifyAccessToken(refreshed.AccessToken)
	if err != nil {
		t.Fatalf("verify refreshed token: %v", err)
	}
	if refreshedClaims.SessionID != firstClaims.SessionID {
		t.Fatalf("sid changed during refresh: first=%q refreshed=%q", firstClaims.SessionID, refreshedClaims.SessionID)
	}

	if err := authSvc.LogoutSession(context.Background(), firstSessionID, userID); err != nil {
		t.Fatalf("logout session: %v", err)
	}
	if _, err := f.sessionRepo.GetByRefreshTokenHash(context.Background(), HashRefreshTokenPublic(replacementRefresh)); !errors.Is(err, repository.ErrSessionNotFound) {
		t.Fatalf("replacement refresh session survived logout: %v", err)
	}
	if _, _, err := authSvc.RefreshAccessToken(context.Background(), replacementRefresh); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("refresh after logout = %v, want ErrInvalidCredentials", err)
	}
	// Deliberately assert Model A: the old access JWT is not blacklisted.
	if _, err := authSvc.VerifyAccessToken(loginResp.AccessToken); err != nil {
		t.Fatalf("old access JWT should remain valid before exp: %v", err)
	}
}

// TestPhase11RefreshLogoutRaceLeavesNoSession proves the stable sid deletion
// serializes with refresh rotation. Exactly one operation may report success,
// but the logical session row must be gone regardless of which linearizes first.
func TestPhase11RefreshLogoutRaceLeavesNoSession(t *testing.T) {
	f := newPhase8D5Fixture(t)
	merchantID := phase8D5SeedMerchant(t, f, model.LegacyCredentialStateMigrated)
	userID := phase8D5SeedUser(t, f, merchantID, model.DashboardUserRoleOwner, "phase11-race-password")
	email := "phase8d5-" + userID.String() + "@example.invalid"
	authSvc := NewAuthService(f.userRepo, f.sessionRepo, AuthConfig{
		JWTSecret:       []byte("phase11-race-test-secret-32b!!!"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 24 * time.Hour,
	}, f.merchantRepo)

	loginResp, oldRefresh, err := authSvc.Login(context.Background(), model.LoginRequest{
		Email: email, Password: "phase11-race-password",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	claims, err := authSvc.VerifyAccessToken(loginResp.AccessToken)
	if err != nil {
		t.Fatalf("verify login token: %v", err)
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var refreshErr, logoutErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, refreshErr = authSvc.RefreshAccessToken(context.Background(), oldRefresh)
	}()
	go func() {
		defer wg.Done()
		<-start
		logoutErr = authSvc.LogoutSession(context.Background(), sessionID, userID)
	}()
	close(start)
	wg.Wait()

	if refreshErr != nil && !errors.Is(refreshErr, ErrInvalidCredentials) {
		t.Fatalf("unexpected refresh race error: %v", refreshErr)
	}
	if logoutErr != nil {
		t.Fatalf("logout race returned error: %v", logoutErr)
	}
	var remaining int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM dashboard_sessions WHERE merchant_user_id = $1
	`, userID).Scan(&remaining); err != nil {
		t.Fatalf("count sessions after race: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("refresh/logout race left %d session rows, want 0", remaining)
	}
}
