package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/ssrf"
)

// Default retry backoff schedule (attempt number → delay before next attempt).
// Attempt 1 is immediate (next_attempt_at = now at enqueue).
// After attempt N fails, schedule delay[N] before attempt N+1.
var defaultWebhookBackoff = []time.Duration{
	0, // unused (attempt 0)
	10 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
}

const (
	webhookUserAgent       = "PayGate-Webhook/1.0"
	webhookMaxResponseBody = 8 << 10 // 8 KiB bound for error diagnostics
)

// MerchantWebhookDispatcher claims and delivers outbound webhook jobs.
type MerchantWebhookDispatcher interface {
	ProcessBatch(ctx context.Context) (int, error)
}

type merchantWebhookDispatcher struct {
	deliveryRepo repository.MerchantWebhookDeliveryRepository
	configRepo   repository.MerchantWebhookConfigRepository
	encKey       []byte
	httpClient   *http.Client
	maxAttempts  int
	staleAfter   time.Duration
	batchSize    int
}

// NewMerchantWebhookDispatcher constructs the HTTP delivery dispatcher.
//
// client is the outbound HTTP client. Passing nil builds the Phase 8D.2
// SSRF-guarded default (ssrf.NewHTTPClient): destination validation at the
// dial boundary (DNS-rebinding safe), no environment HTTP(S)_PROXY, redirects
// refused, TLS verification untouched. An injected client takes over those
// responsibilities for the caller — the same convention as
// NewMidtransProvider (tests inject a plain client to reach loopback
// httptest servers; production always passes nil).
func NewMerchantWebhookDispatcher(
	deliveryRepo repository.MerchantWebhookDeliveryRepository,
	configRepo repository.MerchantWebhookConfigRepository,
	encKey []byte,
	timeout time.Duration,
	maxAttempts int,
	staleAfter time.Duration,
	batchSize int,
	client *http.Client,
) MerchantWebhookDispatcher {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	if staleAfter <= 0 {
		staleAfter = 2 * timeout
	}
	if batchSize <= 0 {
		batchSize = 20
	}
	if client == nil {
		client = ssrf.NewHTTPClient(timeout, nil)
	}
	return &merchantWebhookDispatcher{
		deliveryRepo: deliveryRepo,
		configRepo:   configRepo,
		encKey:       encKey,
		httpClient:   client,
		maxAttempts:  maxAttempts,
		staleAfter:   staleAfter,
		batchSize:    batchSize,
	}
}

func (d *merchantWebhookDispatcher) ProcessBatch(ctx context.Context) (int, error) {
	claimed, err := d.deliveryRepo.ClaimPending(ctx, d.batchSize, d.staleAfter)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, delivery := range claimed {
		if err := d.deliverOne(ctx, delivery); err != nil {
			slog.Error("merchant webhook delivery error",
				slog.String("delivery_id", delivery.ID.String()),
				slog.String("event_id", delivery.EventID),
				slog.String("failure", "delivery_failed"),
			)
			continue
		}
		processed++
	}
	return processed, nil
}

func (d *merchantWebhookDispatcher) deliverOne(ctx context.Context, delivery *model.MerchantWebhookDelivery) error {
	attempt := delivery.AttemptCount + 1

	// Phase 8D.2 (SSRF): re-validate the STORED endpoint on every delivery —
	// the row may predate the destination policy (legacy configuration) or
	// may have been changed out of band. A policy violation never reaches the
	// network and fails permanently with a SAFE diagnostic (no resolved
	// addresses, no resolver output, no internal detail). The guarded dialer
	// enforces the same policy again at connection time, where DNS is
	// actually resolved (DNS rebinding / changed DNS).
	if err := ssrf.ValidateURL(delivery.EndpointURL); err != nil {
		slog.Warn("merchant webhook destination rejected",
			slog.String("delivery_id", delivery.ID.String()),
			slog.String("event_id", delivery.EventID),
		)
		return d.failPermanent(ctx, delivery, attempt, nil, safeDestinationError(err))
	}

	secret, err := d.resolveSecret(ctx, delivery)
	if err != nil {
		// Do not persist repository/decryption diagnostics in a merchant-visible
		// delivery record. The secret itself is never included, and the stable
		// category is sufficient for operations to retry/repair configuration.
		return d.failPermanent(ctx, delivery, attempt, nil, "webhook secret unavailable")
	}

	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	signature := SignWebhookPayload(secret, timestamp, delivery.Payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.EndpointURL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return d.scheduleOrDead(ctx, delivery, attempt, nil, "build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", webhookUserAgent)
	req.Header.Set("X-PayGate-Event-ID", delivery.EventID)
	req.Header.Set("X-PayGate-Event-Type", string(delivery.EventType))
	req.Header.Set("X-PayGate-Timestamp", timestamp)
	req.Header.Set("X-PayGate-Signature", signature)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		// Connection-time destination enforcement fired: DNS changed or a
		// rebinding answer appeared between validation and connection. The
		// socket was never opened — permanent policy failure, no retry.
		if errors.Is(err, ssrf.ErrDestinationBlocked) {
			slog.Warn("merchant webhook destination blocked at connection time",
				slog.String("delivery_id", delivery.ID.String()),
				slog.String("event_id", delivery.EventID),
			)
			return d.failPermanent(ctx, delivery, attempt, nil, safeDestinationError(err))
		}
		// Non-policy transport errors stay retryable, but raw client errors may
		// contain internal URLs or provider diagnostics. Persist only a stable
		// category; the controlled error logger above records the delivery ID.
		return d.scheduleOrDead(ctx, delivery, attempt, nil, "http delivery failed")
	}
	defer resp.Body.Close()

	// Bounded (webhookMaxResponseBody) AND sanitised: only printable ASCII
	// plus newline/tab survive, so a binary response can never persist
	// garbage. Never logged in full — stored only as the retry diagnostic.
	bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, webhookMaxResponseBody))
	status := resp.StatusCode

	if status >= 200 && status < 300 {
		return d.deliveryRepo.MarkDelivered(ctx, delivery.ID, status, attempt)
	}

	msg := fmt.Sprintf("http %d", status)
	if len(bodySnippet) > 0 {
		msg = fmt.Sprintf("http %d: %s", status, sanitizeWebhookSnippet(bodySnippet))
	}

	if isRetryableHTTPStatus(status) {
		return d.scheduleOrDead(ctx, delivery, attempt, &status, msg)
	}
	// Non-retryable 4xx → FAILED (dead-letter without further automatic retries).
	return d.failPermanent(ctx, delivery, attempt, &status, msg)
}

// safeDestinationError reduces an SSRF validation/connection error to its
// STABLE safe message. The ssrf sentinels are deliberately free of addresses
// and resolver detail; anything wrapped underneath is discarded so internal
// network information can never enter last_error, API responses, or logs.
func safeDestinationError(err error) string {
	if errors.Is(err, ssrf.ErrDestinationBlocked) {
		return ssrf.ErrDestinationBlocked.Error()
	}
	return ssrf.ErrInvalidURL.Error()
}

// sanitizeWebhookSnippet replaces non-printable bytes (everything outside
// printable ASCII, newline, and tab) with '?' so the persisted response
// snippet cannot contain binary garbage. The read itself is already bounded
// by webhookMaxResponseBody, so the result stays within the 8 KiB cap.
func sanitizeWebhookSnippet(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch {
		case c >= 0x20 && c <= 0x7e, c == '\n', c == '\t':
			out = append(out, c)
		default:
			out = append(out, '?')
		}
	}
	return out
}

func (d *merchantWebhookDispatcher) resolveSecret(ctx context.Context, delivery *model.MerchantWebhookDelivery) (string, error) {
	cfg, err := d.configRepo.FindByMerchantID(ctx, delivery.MerchantID)
	if err != nil {
		return "", fmt.Errorf("load webhook config: %w", err)
	}
	if cfg.Status != model.MerchantWebhookConfigStatusActive {
		return "", errors.New("webhook configuration is disabled")
	}
	return DecryptWebhookSecret(d.encKey, cfg.EncryptedSecret)
}

func (d *merchantWebhookDispatcher) scheduleOrDead(ctx context.Context, delivery *model.MerchantWebhookDelivery, attempt int, httpStatus *int, lastError string) error {
	if attempt >= d.maxAttempts {
		return d.deliveryRepo.MarkDead(ctx, delivery.ID, attempt, httpStatus, lastError)
	}
	delay := backoffForAttempt(attempt)
	next := time.Now().UTC().Add(delay)
	return d.deliveryRepo.MarkRetry(ctx, delivery.ID, attempt, next, httpStatus, lastError)
}

func (d *merchantWebhookDispatcher) failPermanent(ctx context.Context, delivery *model.MerchantWebhookDelivery, attempt int, httpStatus *int, lastError string) error {
	return d.deliveryRepo.MarkFailed(ctx, delivery.ID, attempt, httpStatus, lastError)
}

func isRetryableHTTPStatus(code int) bool {
	switch code {
	case 408, 429:
		return true
	}
	return code >= 500 && code <= 599
}

func backoffForAttempt(attempt int) time.Duration {
	base := 6 * time.Hour
	if attempt >= 0 && attempt < len(defaultWebhookBackoff) {
		base = defaultWebhookBackoff[attempt]
	}
	// Small jitter (±10%) to avoid thundering herd.
	return applyJitter(base)
}

func applyJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// jitter in [0, 10%] of d
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d/10)+1))
	if err != nil {
		return d
	}
	return d + time.Duration(n.Int64())
}

// ─── Worker ───────────────────────────────────────────────────────────────────

// MerchantWebhookWorker runs the dispatcher on an interval.
type MerchantWebhookWorker struct {
	dispatcher MerchantWebhookDispatcher
	interval   time.Duration
	done       chan struct{}
}

// NewMerchantWebhookWorker constructs a background delivery worker.
func NewMerchantWebhookWorker(dispatcher MerchantWebhookDispatcher, interval time.Duration) *MerchantWebhookWorker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &MerchantWebhookWorker{
		dispatcher: dispatcher,
		interval:   interval,
		done:       make(chan struct{}),
	}
}

// Start launches the worker goroutine.
func (w *MerchantWebhookWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

// Wait blocks until the worker has stopped.
func (w *MerchantWebhookWorker) Wait() {
	<-w.done
}

func (w *MerchantWebhookWorker) run(ctx context.Context) {
	defer close(w.done)
	slog.Info("merchant webhook worker started", slog.Duration("interval", w.interval))
	w.tick(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("merchant webhook worker stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *MerchantWebhookWorker) tick(ctx context.Context) {
	n, err := w.dispatcher.ProcessBatch(ctx)
	if err != nil {
		slog.Error("merchant webhook worker: batch error", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		slog.Info("merchant webhook worker: deliveries processed", slog.Int("count", n))
	}
}
