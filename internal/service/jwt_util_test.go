package service

import (
	"testing"
	"time"
)

// ─── JWT tests ────────────────────────────────────────────────────────────────

func TestSignAndVerifyJWT(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-enough!")

	claims := jwtClaims{
		Subject:    "user-id-1",
		MerchantID: "merchant-id-1",
		Role:       "OWNER",
		IssuedAt:   time.Now().Unix(),
		ExpiresAt:  time.Now().Add(15 * time.Minute).Unix(),
		JTI:        "jti-1",
		SessionID:  "session-1",
	}

	token, err := signJWT(claims, secret)
	if err != nil {
		t.Fatalf("signJWT failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	got, err := verifyJWT(token, secret)
	if err != nil {
		t.Fatalf("verifyJWT failed: %v", err)
	}

	if got.Subject != claims.Subject {
		t.Errorf("subject: got %q, want %q", got.Subject, claims.Subject)
	}
	if got.MerchantID != claims.MerchantID {
		t.Errorf("merchantID: got %q, want %q", got.MerchantID, claims.MerchantID)
	}
	if got.Role != claims.Role {
		t.Errorf("role: got %q, want %q", got.Role, claims.Role)
	}
	if got.SessionID != claims.SessionID {
		t.Errorf("sessionID: got %q, want %q", got.SessionID, claims.SessionID)
	}
}

func TestVerifyJWT_WrongSecret(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-enough!")
	wrongSecret := []byte("wrong-secret-32-bytes-long-enuf!")

	claims := jwtClaims{
		Subject:   "user-id",
		IssuedAt:  time.Now().Unix(),
		ExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
		JTI:       generateJTI(),
	}

	token, err := signJWT(claims, secret)
	if err != nil {
		t.Fatalf("signJWT failed: %v", err)
	}

	_, err = verifyJWT(token, wrongSecret)
	if err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}

func TestVerifyJWT_ExpiredToken(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-enough!")

	claims := jwtClaims{
		Subject:   "user-id",
		IssuedAt:  time.Now().Add(-1 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-1 * time.Second).Unix(), // already expired
		JTI:       generateJTI(),
	}

	token, err := signJWT(claims, secret)
	if err != nil {
		t.Fatalf("signJWT failed: %v", err)
	}

	_, err = verifyJWT(token, secret)
	if err == nil {
		t.Fatal("expected expiry error, got nil")
	}
}

func TestVerifyJWT_Malformed(t *testing.T) {
	secret := []byte("test-secret")

	cases := []string{
		"",
		"onlyone",
		"only.two",
		"too.many.parts.here",
		"bad_header.payload.sig",
	}
	for _, tc := range cases {
		_, err := verifyJWT(tc, secret)
		if err == nil {
			t.Errorf("expected error for malformed token %q, got nil", tc)
		}
	}
}

// ─── Password hashing tests ───────────────────────────────────────────────────

func TestHashAndVerifyPassword(t *testing.T) {
	password := "supersecretpassword"

	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash == password {
		t.Fatal("hash must not equal plaintext")
	}

	if !verifyPassword(password, hash) {
		t.Error("verifyPassword returned false for correct password")
	}
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := hashPassword("correct-password")
	if err != nil {
		t.Fatalf("hashPassword failed: %v", err)
	}

	if verifyPassword("wrong-password", hash) {
		t.Error("verifyPassword returned true for wrong password")
	}
}

func TestVerifyPassword_InvalidHash(t *testing.T) {
	cases := []string{
		"",
		"notahash",
		"$notargon2id$v=19$...",
	}
	for _, tc := range cases {
		if verifyPassword("password", tc) {
			t.Errorf("expected false for invalid hash %q, got true", tc)
		}
	}
}

func TestHashPassword_Unique(t *testing.T) {
	password := "same-password"
	hash1, err := hashPassword(password)
	if err != nil {
		t.Fatalf("first hashPassword: %v", err)
	}
	hash2, err := hashPassword(password)
	if err != nil {
		t.Fatalf("second hashPassword: %v", err)
	}
	// Two hashes of the same password should differ (different random salts).
	if hash1 == hash2 {
		t.Error("hashes must differ due to different salts (random salt per call)")
	}
}

// ─── Refresh token tests ──────────────────────────────────────────────────────

func TestGenerateRefreshToken(t *testing.T) {
	tok1, err := generateRefreshToken()
	if err != nil {
		t.Fatalf("generateRefreshToken: %v", err)
	}
	tok2, err := generateRefreshToken()
	if err != nil {
		t.Fatalf("generateRefreshToken second: %v", err)
	}

	if tok1 == "" || tok2 == "" {
		t.Error("tokens must not be empty")
	}
	if tok1 == tok2 {
		t.Error("tokens must be unique")
	}
	// 32 bytes → 64 hex chars
	if len(tok1) != 64 {
		t.Errorf("expected 64-char hex token, got %d chars", len(tok1))
	}
}

func TestHashRefreshToken_Deterministic(t *testing.T) {
	token := "abc123"
	h1 := hashRefreshToken(token)
	h2 := hashRefreshToken(token)
	if h1 != h2 {
		t.Error("hashRefreshToken must be deterministic")
	}
}

func TestHashRefreshToken_DifferentInputs(t *testing.T) {
	h1 := hashRefreshToken("token-a")
	h2 := hashRefreshToken("token-b")
	if h1 == h2 {
		t.Error("different tokens must produce different hashes")
	}
}

func TestHashRefreshTokenPublic(t *testing.T) {
	token := "test-token"
	if HashRefreshTokenPublic(token) != hashRefreshToken(token) {
		t.Error("HashRefreshTokenPublic must equal hashRefreshToken")
	}
}
