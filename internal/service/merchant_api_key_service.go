package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

// ─── Argon2id parameters ──────────────────────────────────────────────────────
//
// OWASP recommended minimums for Argon2id (2023):
//   - time=1, memory=64MB, parallelism=4
//
// We use slightly conservative values for production-safe defaults.
// These are intentionally hardcoded; changing them would invalidate existing hashes.

const (
	argon2Time    = 1
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16

	// Key format constants.
	apiKeyIDPrefix     = "pk_" // public routing identifier
	apiKeySecretPrefix = "sk_" // secret credential
	keyIDBytes         = 20    // 160-bit public ID → hex = 40 chars
	secretBytes        = 32    // 256-bit secret → hex = 64 chars
)

// ─── Service errors ───────────────────────────────────────────────────────────

var (
	ErrAPIKeyNotFound           = errors.New("api key not found")
	ErrAPIKeyAlreadyRevoked     = errors.New("api key already revoked")
	ErrAPIKeyOwnershipViolation = errors.New("api key does not belong to this merchant")
	ErrAPIKeyExpirationPast     = errors.New("expires_at must be in the future")
	ErrMerchantNotFoundForKey   = errors.New("merchant not found for api key")
)

// ─── Interface ────────────────────────────────────────────────────────────────

// MerchantAPIKeyService defines the business operations for merchant API key lifecycle.
type MerchantAPIKeyService interface {
	// CreateKey generates a new API key for the given merchant.
	// The plaintext secret is returned only in the response DTO.
	CreateKey(ctx context.Context, merchantID uuid.UUID, req model.CreateMerchantAPIKeyRequest) (*model.CreateMerchantAPIKeyResponse, error)

	// ListKeys returns all keys for merchantID (no secrets).
	ListKeys(ctx context.Context, merchantID uuid.UUID) ([]model.MerchantAPIKeyResponse, error)

	// RevokeKey transitions the key to REVOKED.
	RevokeKey(ctx context.Context, merchantID, keyID uuid.UUID) error

	// RotateKey atomically revokes the old key and creates a replacement.
	// The plaintext secret of the new key is returned once only.
	RotateKey(ctx context.Context, merchantID, keyID uuid.UUID) (*model.RotateMerchantAPIKeyResponse, error)

	// AuthenticateByAPIKey looks up the key by keyID, verifies the secret,
	// checks status and expiry, updates last_used_at, and returns the merchant.
	// Returns (nil, ErrAPIKeyNotFound) or a sentinel error on any failure —
	// the caller must map all non-nil errors to 401.
	AuthenticateByAPIKey(ctx context.Context, rawKey string) (*model.Merchant, error)
}

// ─── Implementation ───────────────────────────────────────────────────────────

type merchantAPIKeyService struct {
	keyRepo      repository.MerchantAPIKeyRepository
	merchantRepo repository.MerchantRepository
}

// NewMerchantAPIKeyService returns a new MerchantAPIKeyService.
func NewMerchantAPIKeyService(
	keyRepo repository.MerchantAPIKeyRepository,
	merchantRepo repository.MerchantRepository,
) MerchantAPIKeyService {
	return &merchantAPIKeyService{
		keyRepo:      keyRepo,
		merchantRepo: merchantRepo,
	}
}

// ─── CreateKey ────────────────────────────────────────────────────────────────

func (s *merchantAPIKeyService) CreateKey(ctx context.Context, merchantID uuid.UUID, req model.CreateMerchantAPIKeyRequest) (*model.CreateMerchantAPIKeyResponse, error) {
	// Validate expiration.
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrAPIKeyExpirationPast
	}

	// Verify merchant exists.
	if _, err := s.merchantRepo.GetByID(ctx, merchantID); err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, repository.ErrMerchantNotFound
		}
		return nil, fmt.Errorf("create key: verify merchant: %w", err)
	}

	// Generate credentials.
	keyID, plaintextSecret, secretHash, err := generateAPIKeyPair()
	if err != nil {
		return nil, fmt.Errorf("create key: generate credentials: %w", err)
	}

	now := time.Now().UTC()
	key := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: merchantID,
		KeyID:      keyID,
		SecretHash: secretHash,
		Name:       req.Name,
		Status:     model.MerchantAPIKeyStatusActive,
		ExpiresAt:  req.ExpiresAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := s.keyRepo.Create(ctx, key); err != nil {
		return nil, fmt.Errorf("create key: persist: %w", err)
	}

	slog.Info("merchant api key created",
		slog.String("key_id", keyID),
		slog.String("merchant_id", merchantID.String()),
		slog.String("name", req.Name),
		// plaintext secret is NEVER logged
	)

	return &model.CreateMerchantAPIKeyResponse{
		ID:         key.ID,
		MerchantID: key.MerchantID,
		Name:       key.Name,
		KeyID:      keyID,
		Secret:     plaintextSecret, // one-time disclosure
		Status:     key.Status,
		ExpiresAt:  key.ExpiresAt,
		CreatedAt:  key.CreatedAt,
	}, nil
}

// ─── ListKeys ─────────────────────────────────────────────────────────────────

func (s *merchantAPIKeyService) ListKeys(ctx context.Context, merchantID uuid.UUID) ([]model.MerchantAPIKeyResponse, error) {
	keys, err := s.keyRepo.ListByMerchant(ctx, merchantID)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}

	out := make([]model.MerchantAPIKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyResponse(k))
	}
	return out, nil
}

// ─── RevokeKey ────────────────────────────────────────────────────────────────

func (s *merchantAPIKeyService) RevokeKey(ctx context.Context, merchantID, keyID uuid.UUID) error {
	err := s.keyRepo.Revoke(ctx, merchantID, keyID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
			return ErrAPIKeyNotFound
		}
		if errors.Is(err, repository.ErrMerchantAPIKeyAlreadyRevoked) {
			return ErrAPIKeyAlreadyRevoked
		}
		return fmt.Errorf("revoke key: %w", err)
	}

	slog.Info("merchant api key revoked",
		slog.String("key_uuid", keyID.String()),
		slog.String("merchant_id", merchantID.String()),
	)
	return nil
}

// ─── RotateKey ────────────────────────────────────────────────────────────────

func (s *merchantAPIKeyService) RotateKey(ctx context.Context, merchantID, keyID uuid.UUID) (*model.RotateMerchantAPIKeyResponse, error) {
	// Verify old key exists and belongs to merchant.
	oldKey, err := s.keyRepo.GetByID(ctx, merchantID, keyID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("rotate key: get old key: %w", err)
	}
	if oldKey.Status == model.MerchantAPIKeyStatusRevoked {
		return nil, ErrAPIKeyAlreadyRevoked
	}

	// Generate new credentials.
	newKeyID, plaintextSecret, secretHash, err := generateAPIKeyPair()
	if err != nil {
		return nil, fmt.Errorf("rotate key: generate credentials: %w", err)
	}

	now := time.Now().UTC()
	newKey := &model.MerchantAPIKey{
		ID:         uuid.New(),
		MerchantID: merchantID,
		KeyID:      newKeyID,
		SecretHash: secretHash,
		Name:       oldKey.Name, // inherit name from old key
		Status:     model.MerchantAPIKeyStatusActive,
		ExpiresAt:  oldKey.ExpiresAt, // inherit expiry from old key
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// Atomic: revoke old key and insert new key in one transaction.
	if err := s.keyRepo.Rotate(ctx, merchantID, keyID, newKey); err != nil {
		if errors.Is(err, repository.ErrMerchantAPIKeyNotFound) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("rotate key: atomic rotate: %w", err)
	}

	slog.Info("merchant api key rotated",
		slog.String("old_key_uuid", keyID.String()),
		slog.String("new_key_id", newKeyID),
		slog.String("merchant_id", merchantID.String()),
		// secrets are NEVER logged
	)

	return &model.RotateMerchantAPIKeyResponse{
		ID:         newKey.ID,
		MerchantID: newKey.MerchantID,
		Name:       newKey.Name,
		KeyID:      newKeyID,
		Secret:     plaintextSecret, // one-time disclosure of new secret
		Status:     newKey.Status,
		ExpiresAt:  newKey.ExpiresAt,
		CreatedAt:  newKey.CreatedAt,
	}, nil
}

// ─── AuthenticateByAPIKey ─────────────────────────────────────────────────────
//
// Authentication flow:
//  1. Parse key: the value supplied in X-API-Key is "keyID:secret" where
//     keyID = "pk_<hex>" and secret = "sk_<hex>".
//  2. Look up the key record by keyID.
//  3. Verify the secret using Argon2id constant-time comparison.
//  4. Check IsActive (status + expiry).
//  5. Update last_used_at (best-effort; auth still succeeds on metadata failure).
//  6. Load and return the associated merchant.

func (s *merchantAPIKeyService) AuthenticateByAPIKey(ctx context.Context, rawKey string) (*model.Merchant, error) {
	keyID, secret, ok := parseRawKey(rawKey)
	if !ok {
		return nil, ErrAPIKeyNotFound
	}

	keyRecord, err := s.keyRepo.GetByKeyID(ctx, keyID)
	if err != nil {
		// Both "not found" and DB errors map to a generic auth failure.
		return nil, ErrAPIKeyNotFound
	}

	// Verify secret — constant-time Argon2id check.
	if !verifyArgon2id(secret, keyRecord.SecretHash) {
		return nil, ErrAPIKeyNotFound
	}

	// Check status and expiry.
	if !keyRecord.IsActive() {
		return nil, ErrAPIKeyNotFound
	}

	// Update last_used_at — best-effort.
	if err := s.keyRepo.UpdateLastUsedAt(ctx, keyRecord.ID); err != nil {
		slog.Warn("merchant api key: failed to update last_used_at",
			slog.String("key_id", keyID),
			slog.String("error", err.Error()),
		)
		// Do not fail authentication for a metadata update failure.
	}

	// Load the merchant.
	merchant, err := s.merchantRepo.GetByID(ctx, keyRecord.MerchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			return nil, ErrMerchantNotFoundForKey
		}
		return nil, fmt.Errorf("authenticate by api key: load merchant: %w", err)
	}

	return merchant, nil
}

// ─── Credential generation ────────────────────────────────────────────────────

// generateAPIKeyPair creates a new (keyID, plaintextSecret, secretHash) triplet.
//   - keyID is the public routing token stored in the DB for lookup.
//   - plaintextSecret is returned once to the caller and is NEVER stored.
//   - secretHash is the Argon2id hash of plaintextSecret; stored in the DB.
//
// Full credential sent to the client:  "<keyID>:<plaintextSecret>"
func generateAPIKeyPair() (keyID, plaintextSecret, secretHash string, err error) {
	// Generate public key ID.
	kidBytes := make([]byte, keyIDBytes)
	if _, err = rand.Read(kidBytes); err != nil {
		return "", "", "", fmt.Errorf("generate key id: %w", err)
	}
	keyID = apiKeyIDPrefix + hex.EncodeToString(kidBytes)

	// Generate secret.
	secBytes := make([]byte, secretBytes)
	if _, err = rand.Read(secBytes); err != nil {
		return "", "", "", fmt.Errorf("generate secret: %w", err)
	}
	plaintextSecret = apiKeySecretPrefix + hex.EncodeToString(secBytes)

	// Hash secret with Argon2id.
	secretHash, err = hashArgon2id(plaintextSecret)
	if err != nil {
		return "", "", "", fmt.Errorf("hash secret: %w", err)
	}
	return keyID, plaintextSecret, secretHash, nil
}

// parseRawKey splits "pk_<hex>:sk_<hex>" into (keyID, secret).
// The separator is ":" — the first colon after the key_id prefix.
func parseRawKey(raw string) (keyID, secret string, ok bool) {
	idx := strings.Index(raw, ":")
	if idx < 0 {
		return "", "", false
	}
	keyID = raw[:idx]
	secret = raw[idx+1:]
	if !strings.HasPrefix(keyID, apiKeyIDPrefix) || !strings.HasPrefix(secret, apiKeySecretPrefix) {
		return "", "", false
	}
	return keyID, secret, true
}

// ─── Argon2id hashing ─────────────────────────────────────────────────────────

// hashArgon2id produces an encoded Argon2id hash string in the format:
//
//	"$argon2id$v=19$m=<m>,t=<t>,p=<p>$<base64-salt>$<base64-hash>"
func hashArgon2id(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2id salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads, b64Salt, b64Hash), nil
}

// verifyArgon2id returns true when password matches the encoded Argon2id hash.
// Uses subtle.ConstantTimeCompare for the final comparison to mitigate timing attacks.
func verifyArgon2id(password, encodedHash string) bool {
	parts := strings.Split(encodedHash, "$")
	// Format: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
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

	computedHash := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expectedHash)))
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}

// ─── DTO conversion ───────────────────────────────────────────────────────────

// toAPIKeyResponse converts the domain entity to the safe list/get DTO.
// SecretHash is intentionally excluded.
func toAPIKeyResponse(k *model.MerchantAPIKey) model.MerchantAPIKeyResponse {
	return model.MerchantAPIKeyResponse{
		ID:         k.ID,
		MerchantID: k.MerchantID,
		Name:       k.Name,
		KeyID:      k.KeyID,
		Status:     k.Status,
		ExpiresAt:  k.ExpiresAt,
		RevokedAt:  k.RevokedAt,
		LastUsedAt: k.LastUsedAt,
		CreatedAt:  k.CreatedAt,
		UpdatedAt:  k.UpdatedAt,
	}
}
