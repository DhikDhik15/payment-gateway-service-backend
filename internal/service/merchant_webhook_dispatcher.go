package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
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
func NewMerchantWebhookDispatcher(
	deliveryRepo repository.MerchantWebhookDeliveryRepository,
	configRepo repository.MerchantWebhookConfigRepository,
	encKey []byte,
	timeout time.Duration,
	maxAttempts int,
	staleAfter time.Duration,
	batchSize int,
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
	return &merchantWebhookDispatcher{
		deliveryRepo: deliveryRepo,
		configRepo:   configRepo,
		encKey:       encKey,
		httpClient: &http.Client{
			Timeout: timeout,
			// Do not follow redirects for webhook delivery (security).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxAttempts: maxAttempts,
		staleAfter:  staleAfter,
		batchSize:   batchSize,
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
				slog.String("error", err.Error()),
			)
			continue
		}
		processed++
	}
	return processed, nil
}

func (d *merchantWebhookDispatcher) deliverOne(ctx context.Context, delivery *model.MerchantWebhookDelivery) error {
	attempt := delivery.AttemptCount + 1

	secret, err := d.resolveSecret(ctx, delivery)
	if err != nil {
		return d.failPermanent(ctx, delivery, attempt, nil, err.Error())
	}

	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	signature := SignWebhookPayload(secret, timestamp, delivery.Payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.EndpointURL, bytes.NewReader(delivery.Payload))
	if err != nil {
		return d.scheduleOrDead(ctx, delivery, attempt, nil, "build request: "+err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", webhookUserAgent)
	req.Header.Set("X-PayGate-Event-ID", delivery.EventID)
	req.Header.Set("X-PayGate-Event-Type", string(delivery.EventType))
	req.Header.Set("X-PayGate-Timestamp", timestamp)
	req.Header.Set("X-PayGate-Signature", signature)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return d.scheduleOrDead(ctx, delivery, attempt, nil, "http: "+err.Error())
	}
	defer resp.Body.Close()

	bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, webhookMaxResponseBody))
	status := resp.StatusCode

	if status >= 200 && status < 300 {
		return d.deliveryRepo.MarkDelivered(ctx, delivery.ID, status, attempt)
	}

	msg := fmt.Sprintf("http %d", status)
	if len(bodySnippet) > 0 {
		msg = fmt.Sprintf("http %d: %s", status, string(bodySnippet))
	}

	if isRetryableHTTPStatus(status) {
		return d.scheduleOrDead(ctx, delivery, attempt, &status, msg)
	}
	// Non-retryable 4xx → FAILED (dead-letter without further automatic retries).
	return d.failPermanent(ctx, delivery, attempt, &status, msg)
}

func (d *merchantWebhookDispatcher) resolveSecret(ctx context.Context, delivery *model.MerchantWebhookDelivery) (string, error) {
	cfg, err := d.configRepo.FindByMerchantID(ctx, delivery.MerchantID)
	if err != nil {
		return "", fmt.Errorf("load webhook config: %w", err)
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
