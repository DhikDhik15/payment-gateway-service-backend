package service

// jwt_util.go — JWT (HS256) and password hashing utilities for Phase 8 dashboard auth.
//
// Design goals:
//   - Zero external JWT dependencies: uses only stdlib crypto packages.
//   - Argon2id parameters match merchant_api_key_service.go exactly so the
//     same tuning applies uniformly across the project.
//   - Refresh tokens are opaque random bytes stored only as their SHA-256 hash.
//   - Constant-time signature comparison prevents timing side-channels.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// ─── JWT ──────────────────────────────────────────────────────────────────────

// jwtHeaderB64 is the constant base64url-encoded JWT header for HS256.
// {"alg":"HS256","typ":"JWT"}
const jwtHeaderB64 = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"

var (
	// ErrJWTInvalid is returned for malformed tokens or bad signatures.
	ErrJWTInvalid = errors.New("invalid jwt")
	// ErrJWTExpired is returned for tokens past their exp claim.
	ErrJWTExpired = errors.New("jwt expired")
)

// jwtClaims is the JWT payload for a dashboard access token.
// Fields intentionally minimal — never include sensitive data.
type jwtClaims struct {
	Subject    string `json:"sub"`  // MerchantUser UUID
	MerchantID string `json:"mid"`  // Merchant UUID (for fast isolation checks)
	Role       string `json:"role"` // DashboardUserRole string
	IssuedAt   int64  `json:"iat"`  // Unix timestamp
	ExpiresAt  int64  `json:"exp"`  // Unix timestamp
	JTI        string `json:"jti"`  // Unique token ID (JWT ID)
}

// signJWT creates a signed HS256 JWT using only standard library packages.
// The signing key must be at least 32 bytes.
func signJWT(claims jwtClaims, secret []byte) (string, error) {
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jwt sign marshal: %w", err)
	}

	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := jwtHeaderB64 + "." + payloadB64

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput)) //nolint:errcheck // hash.Write never returns an error
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + sig, nil
}

// verifyJWT validates the HS256 signature, structural integrity, and expiry.
// Returns ErrJWTInvalid for malformed/bad-signature tokens, ErrJWTExpired
// when exp is in the past.
func verifyJWT(token string, secret []byte) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrJWTInvalid
	}

	// Header must match exactly (alg=HS256, typ=JWT).
	if parts[0] != jwtHeaderB64 {
		return nil, ErrJWTInvalid
	}

	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput)) //nolint:errcheck
	expectedSig := mac.Sum(nil)

	providedSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrJWTInvalid
	}

	// Constant-time comparison to prevent timing side-channel attacks.
	if subtle.ConstantTimeCompare(expectedSig, providedSig) != 1 {
		return nil, ErrJWTInvalid
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrJWTInvalid
	}

	var claims jwtClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, ErrJWTInvalid
	}

	if time.Now().UTC().Unix() > claims.ExpiresAt {
		return nil, ErrJWTExpired
	}

	return &claims, nil
}

// ─── Refresh token ────────────────────────────────────────────────────────────

// generateRefreshToken returns a cryptographically random 32-byte opaque token
// encoded as lowercase hex (64 characters). This is the value sent to the client.
// It is NEVER stored in the database — only its SHA-256 hash is stored.
func generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashRefreshToken returns the SHA-256 hex hash of the plaintext token.
// This is the value stored in dashboard_sessions.refresh_token_hash.
func hashRefreshToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// HashRefreshTokenPublic is the exported wrapper around hashRefreshToken.
// Handlers and tests may use this to compute the expected hash without
// depending on unexported service internals.
func HashRefreshTokenPublic(token string) string {
	return hashRefreshToken(token)
}

// generateJTI returns a random JWT ID (used to give each token a unique identity).
func generateJTI() string {
	return uuid.New().String()
}

// ─── Argon2id password hashing ────────────────────────────────────────────────
//
// Parameters match merchant_api_key_service.go exactly:
//   - time    = 1
//   - memory  = 64 MiB
//   - threads = 4
//   - keyLen  = 32
//   - saltLen = 16
//
// Hash format (PHC-like):
//   $argon2id$v=19$m=65536,t=1,p=4$<salt_base64>$<hash_base64>

const (
	pwArgon2Time    uint32 = 1
	pwArgon2Memory  uint32 = 64 * 1024 // 64 MiB
	pwArgon2Threads uint8  = 4
	pwArgon2KeyLen  uint32 = 32
	pwArgon2SaltLen int    = 16
)

// hashPassword hashes a plaintext password using Argon2id and returns the
// encoded hash string. The hash includes the salt so no separate salt storage
// is needed.
func hashPassword(password string) (string, error) {
	salt := make([]byte, pwArgon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hash password generate salt: %w", err)
	}

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		pwArgon2Time,
		pwArgon2Memory,
		pwArgon2Threads,
		pwArgon2KeyLen,
	)

	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		pwArgon2Memory, pwArgon2Time, pwArgon2Threads,
		saltB64, hashB64,
	), nil
}

// verifyPassword checks a plaintext password against an Argon2id hash string.
// Returns false for any mismatch or parse error — never returns an error to
// prevent information leakage to the caller.
func verifyPassword(password, encodedHash string) bool {
	parts := strings.Split(encodedHash, "$")
	// Expected format: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	computedHash := argon2.IDKey(
		[]byte(password),
		salt,
		iterations,
		memory,
		parallelism,
		uint32(len(expectedHash)),
	)

	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}
