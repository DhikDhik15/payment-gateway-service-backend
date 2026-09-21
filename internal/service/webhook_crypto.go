package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	webhookSecretPrefix = "whsec_"
	webhookSecretBytes  = 32 // 256-bit
	eventIDPrefix       = "evt_"
	eventIDBytes        = 16 // 128-bit
)

var (
	ErrInvalidEncryptionKey = errors.New("webhook encryption key must be 32 bytes (64 hex chars or 44-char base64)")
	ErrDecryptFailed        = errors.New("failed to decrypt webhook secret")
)

// ParseWebhookEncryptionKey accepts a 32-byte key as hex (64 chars) or standard base64.
func ParseWebhookEncryptionKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrInvalidEncryptionKey
	}
	if len(raw) == 64 {
		key, err := hex.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, ErrInvalidEncryptionKey
		}
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, ErrInvalidEncryptionKey
	}
	return key, nil
}

// EncryptWebhookSecret encrypts plaintext with AES-256-GCM.
// Output format: base64(nonce || ciphertext||tag).
func EncryptWebhookSecret(key []byte, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidEncryptionKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptWebhookSecret decrypts a value produced by EncryptWebhookSecret.
func DecryptWebhookSecret(key []byte, encoded string) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidEncryptionKey
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrDecryptFailed
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", ErrDecryptFailed
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plain), nil
}

// GenerateWebhookSecret returns a cryptographically random signing secret (whsec_...).
func GenerateWebhookSecret() (string, error) {
	buf := make([]byte, webhookSecretBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return webhookSecretPrefix + hex.EncodeToString(buf), nil
}

// GenerateWebhookEventID returns a stable unique event id (evt_...).
func GenerateWebhookEventID() (string, error) {
	buf := make([]byte, eventIDBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return eventIDPrefix + hex.EncodeToString(buf), nil
}

// SignWebhookPayload computes HMAC-SHA256 over timestamp + "." + rawBody.
// Returns "sha256=<hex>".
func SignWebhookPayload(secret, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(rawBody)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature verifies an HMAC-SHA256 signature using constant-time compare.
func VerifyWebhookSignature(secret, timestamp string, rawBody []byte, signature string) bool {
	expected := SignWebhookPayload(secret, timestamp, rawBody)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}
