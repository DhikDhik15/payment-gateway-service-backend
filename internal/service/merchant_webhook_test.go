package service_test

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/dhikaarta/pay-gate-backend/internal/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func testEncKey() []byte {
	key, err := service.ParseWebhookEncryptionKey("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		panic(err)
	}
	return key
}

func TestWebhookCrypto_EncryptDecryptRoundTrip(t *testing.T) {
	key := testEncKey()
	plain := "whsec_abc123"
	enc, err := service.EncryptWebhookSecret(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if enc == plain {
		t.Fatal("encrypted must differ from plaintext")
	}
	got, err := service.DecryptWebhookSecret(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != plain {
		t.Fatalf("got %q want %q", got, plain)
	}
}

func TestWebhookCrypto_SignAndVerify(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"id":"evt_1"}`)
	ts := "1710000000"
	sig := service.SignWebhookPayload(secret, ts, body)
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("unexpected signature format: %s", sig)
	}
	if !service.VerifyWebhookSignature(secret, ts, body, sig) {
		t.Fatal("expected verify success")
	}
	if service.VerifyWebhookSignature(secret, ts, body, "sha256=deadbeef") {
		t.Fatal("expected verify failure")
	}
	// Constant-time compare sanity (length mismatch).
	if subtle.ConstantTimeCompare([]byte(sig), []byte("x")) == 1 {
		t.Fatal("unexpected equal")
	}
}

func TestGenerateWebhookSecretAndEventID(t *testing.T) {
	s, err := service.GenerateWebhookSecret()
	if err != nil || !strings.HasPrefix(s, "whsec_") {
		t.Fatalf("secret: %v %q", err, s)
	}
	e, err := service.GenerateWebhookEventID()
	if err != nil || !strings.HasPrefix(e, "evt_") {
		t.Fatalf("event id: %v %q", err, e)
	}
}

// ─── in-memory webhook repos ──────────────────────────────────────────────────

type memWebhookConfigRepo struct {
	mu    sync.Mutex
	byMID map[uuid.UUID]*model.MerchantWebhookConfig
}

func newMemWebhookConfigRepo() *memWebhookConfigRepo {
	return &memWebhookConfigRepo{byMID: make(map[uuid.UUID]*model.MerchantWebhookConfig)}
}

func (r *memWebhookConfigRepo) Upsert(_ context.Context, cfg *model.MerchantWebhookConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *cfg
	r.byMID[cfg.MerchantID] = &cp
	return nil
}
func (r *memWebhookConfigRepo) FindByMerchantID(_ context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.byMID[merchantID]
	if !ok {
		return nil, repository.ErrMerchantWebhookConfigNotFound
	}
	cp := *cfg
	return &cp, nil
}
func (r *memWebhookConfigRepo) FindActiveByMerchantID(ctx context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	cfg, err := r.FindByMerchantID(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	if cfg.Status != model.MerchantWebhookConfigStatusActive {
		return nil, repository.ErrMerchantWebhookConfigNotFound
	}
	return cfg, nil
}
func (r *memWebhookConfigRepo) UpdateSecret(_ context.Context, merchantID uuid.UUID, encryptedSecret string) (*model.MerchantWebhookConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.byMID[merchantID]
	if !ok {
		return nil, repository.ErrMerchantWebhookConfigNotFound
	}
	cfg.EncryptedSecret = encryptedSecret
	cfg.UpdatedAt = time.Now().UTC()
	cp := *cfg
	return &cp, nil
}
func (r *memWebhookConfigRepo) Disable(_ context.Context, merchantID uuid.UUID) (*model.MerchantWebhookConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.byMID[merchantID]
	if !ok {
		return nil, repository.ErrMerchantWebhookConfigNotFound
	}
	cfg.Status = model.MerchantWebhookConfigStatusDisabled
	cfg.UpdatedAt = time.Now().UTC()
	cp := *cfg
	return &cp, nil
}

type memWebhookDeliveryRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*model.MerchantWebhookDelivery
}

func newMemWebhookDeliveryRepo() *memWebhookDeliveryRepo {
	return &memWebhookDeliveryRepo{rows: make(map[uuid.UUID]*model.MerchantWebhookDelivery)}
}

func (r *memWebhookDeliveryRepo) Create(_ context.Context, d *model.MerchantWebhookDelivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.rows {
		if existing.MerchantID == d.MerchantID && existing.EventID == d.EventID {
			return repository.ErrMerchantWebhookDeliveryDuplicate
		}
	}
	cp := *d
	r.rows[d.ID] = &cp
	return nil
}
func (r *memWebhookDeliveryRepo) CreateInTx(ctx context.Context, _ pgx.Tx, d *model.MerchantWebhookDelivery) error {
	return r.Create(ctx, d)
}
func (r *memWebhookDeliveryRepo) FindByID(_ context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[deliveryID]
	if !ok || d.MerchantID != merchantID {
		return nil, repository.ErrMerchantWebhookDeliveryNotFound
	}
	cp := *d
	return &cp, nil
}
func (r *memWebhookDeliveryRepo) ListByMerchant(_ context.Context, merchantID uuid.UUID, limit, offset int) ([]*model.MerchantWebhookDelivery, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []*model.MerchantWebhookDelivery
	for _, d := range r.rows {
		if d.MerchantID == merchantID {
			cp := *d
			all = append(all, &cp)
		}
	}
	total := int64(len(all))
	if offset >= len(all) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}
func (r *memWebhookDeliveryRepo) ClaimPending(_ context.Context, batchSize int, _ time.Duration) ([]*model.MerchantWebhookDelivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	var out []*model.MerchantWebhookDelivery
	for _, d := range r.rows {
		if d.Status == model.MerchantWebhookDeliveryStatusPending && !d.NextAttemptAt.After(now) {
			d.Status = model.MerchantWebhookDeliveryStatusProcessing
			ts := now
			d.ProcessingAt = &ts
			cp := *d
			out = append(out, &cp)
			if len(out) >= batchSize {
				break
			}
		}
	}
	return out, nil
}
func (r *memWebhookDeliveryRepo) MarkDelivered(_ context.Context, id uuid.UUID, httpStatus int, attemptCount int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.Status != model.MerchantWebhookDeliveryStatusProcessing {
		return repository.ErrMerchantWebhookDeliveryNotFound
	}
	d.Status = model.MerchantWebhookDeliveryStatusDelivered
	d.AttemptCount = attemptCount
	d.LastHTTPStatus = &httpStatus
	now := time.Now().UTC()
	d.DeliveredAt = &now
	d.LastAttemptAt = &now
	d.ProcessingAt = nil
	return nil
}
func (r *memWebhookDeliveryRepo) MarkRetry(_ context.Context, id uuid.UUID, attemptCount int, nextAttemptAt time.Time, httpStatus *int, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.Status != model.MerchantWebhookDeliveryStatusProcessing {
		return repository.ErrMerchantWebhookDeliveryNotFound
	}
	d.Status = model.MerchantWebhookDeliveryStatusPending
	d.AttemptCount = attemptCount
	d.NextAttemptAt = nextAttemptAt
	d.LastHTTPStatus = httpStatus
	d.LastError = &lastError
	now := time.Now().UTC()
	d.LastAttemptAt = &now
	d.ProcessingAt = nil
	return nil
}
func (r *memWebhookDeliveryRepo) MarkFailed(_ context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.Status != model.MerchantWebhookDeliveryStatusProcessing {
		return repository.ErrMerchantWebhookDeliveryNotFound
	}
	d.Status = model.MerchantWebhookDeliveryStatusFailed
	d.AttemptCount = attemptCount
	d.LastHTTPStatus = httpStatus
	d.LastError = &lastError
	now := time.Now().UTC()
	d.LastAttemptAt = &now
	d.ProcessingAt = nil
	return nil
}
func (r *memWebhookDeliveryRepo) MarkDead(_ context.Context, id uuid.UUID, attemptCount int, httpStatus *int, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.Status != model.MerchantWebhookDeliveryStatusProcessing {
		return repository.ErrMerchantWebhookDeliveryNotFound
	}
	d.Status = model.MerchantWebhookDeliveryStatusDead
	d.AttemptCount = attemptCount
	d.LastHTTPStatus = httpStatus
	d.LastError = &lastError
	now := time.Now().UTC()
	d.LastAttemptAt = &now
	d.ProcessingAt = nil
	return nil
}
func (r *memWebhookDeliveryRepo) ManualRetry(_ context.Context, merchantID, deliveryID uuid.UUID) (*model.MerchantWebhookDelivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[deliveryID]
	if !ok || d.MerchantID != merchantID {
		return nil, repository.ErrMerchantWebhookDeliveryNotFound
	}
	switch d.Status {
	case model.MerchantWebhookDeliveryStatusFailed, model.MerchantWebhookDeliveryStatusDead, model.MerchantWebhookDeliveryStatusPending:
		d.Status = model.MerchantWebhookDeliveryStatusPending
		d.NextAttemptAt = time.Now().UTC()
		d.ProcessingAt = nil
		d.LastError = nil
		cp := *d
		return &cp, nil
	default:
		return nil, repository.ErrMerchantWebhookDeliveryNotFound
	}
}

type memMerchantRepoForWebhook struct {
	m *model.Merchant
}

func (r *memMerchantRepoForWebhook) Create(_ context.Context, _ *model.Merchant) error { return nil }
func (r *memMerchantRepoForWebhook) CreateInTx(_ context.Context, _ pgx.Tx, _ *model.Merchant) error {
	return nil
}
func (r *memMerchantRepoForWebhook) GetByID(_ context.Context, id uuid.UUID) (*model.Merchant, error) {
	if r.m == nil || r.m.ID != id {
		return nil, repository.ErrMerchantNotFound
	}
	return r.m, nil
}
func (r *memMerchantRepoForWebhook) GetByAPIKey(_ context.Context, _ string) (*model.Merchant, error) {
	return nil, repository.ErrMerchantNotFound
}
func (r *memMerchantRepoForWebhook) ExistsByCode(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (r *memMerchantRepoForWebhook) UpdateStatus(_ context.Context, id uuid.UUID, status model.MerchantStatus) error {
	if r.m != nil && r.m.ID == id {
		r.m.Status = status
		return nil
	}
	return repository.ErrMerchantNotFound
}

func TestMerchantWebhookConfigService_Lifecycle(t *testing.T) {
	mid := uuid.New()
	merchantRepo := &memMerchantRepoForWebhook{m: &model.Merchant{ID: mid, Status: model.MerchantStatusActive}}
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	svc := service.NewMerchantWebhookConfigService(cfgRepo, delRepo, merchantRepo, testEncKey(), false)

	created, err := svc.Upsert(context.Background(), mid, model.UpsertMerchantWebhookRequest{
		URL: "https://hooks.example.com/hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatalf("expected one-time secret, got %q", created.Secret)
	}

	got, err := svc.Get(context.Background(), mid)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure secret never appears on GET DTO via JSON.
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "whsec_") || strings.Contains(string(raw), "secret") {
		t.Fatalf("GET must not expose secret: %s", raw)
	}

	rotated, err := svc.RotateSecret(context.Background(), mid)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Secret == created.Secret {
		t.Fatal("rotated secret must differ")
	}

	disabled, err := svc.Disable(context.Background(), mid)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != model.MerchantWebhookConfigStatusDisabled {
		t.Fatalf("status=%s", disabled.Status)
	}
}

func TestMerchantWebhookPublisher_NoConfigNoOp(t *testing.T) {
	pub := service.NewMerchantWebhookPublisher(newMemWebhookConfigRepo(), newMemWebhookDeliveryRepo())
	tx := &model.Transaction{ID: uuid.New(), MerchantID: uuid.New(), Status: model.TransactionStatusPaid}
	if err := pub.Enqueue(context.Background(), tx, model.MerchantWebhookEventPaymentPaid); err != nil {
		t.Fatal(err)
	}
}

func TestMerchantWebhookPublisher_Enqueue(t *testing.T) {
	mid := uuid.New()
	cfgRepo := newMemWebhookConfigRepo()
	delRepo := newMemWebhookDeliveryRepo()
	enc, _ := service.EncryptWebhookSecret(testEncKey(), "whsec_x")
	_ = cfgRepo.Upsert(context.Background(), &model.MerchantWebhookConfig{
		ID: uuid.New(), MerchantID: mid, URL: "http://example.com/hook",
		EncryptedSecret: enc, Status: model.MerchantWebhookConfigStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	pub := service.NewMerchantWebhookPublisher(cfgRepo, delRepo)
	tx := &model.Transaction{
		ID: uuid.New(), MerchantID: mid, MerchantOrderID: "O1",
		Amount: 1000, Currency: "IDR", PaymentMethod: "QRIS",
		Status: model.TransactionStatusPaid, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := pub.Enqueue(context.Background(), tx, model.MerchantWebhookEventPaymentPaid); err != nil {
		t.Fatal(err)
	}
	_, total, err := delRepo.ListByMerchant(context.Background(), mid, 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestMerchantWebhookDispatcher_SuccessAndStableEventID(t *testing.T) {
	mid := uuid.New()
	secret := "whsec_dispatcher_test"
	enc, _ := service.EncryptWebhookSecret(testEncKey(), secret)
	cfgRepo := newMemWebhookConfigRepo()
	_ = cfgRepo.Upsert(context.Background(), &model.MerchantWebhookConfig{
		ID: uuid.New(), MerchantID: mid, URL: "", // set after server
		EncryptedSecret: enc, Status: model.MerchantWebhookConfigStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})

	var gotEventID, gotSig, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEventID = r.Header.Get("X-PayGate-Event-ID")
		gotSig = r.Header.Get("X-PayGate-Signature")
		gotType = r.Header.Get("X-PayGate-Event-Type")
		ts := r.Header.Get("X-PayGate-Timestamp")
		gotBody, _ = io.ReadAll(r.Body)
		if !service.VerifyWebhookSignature(secret, ts, gotBody, gotSig) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg, _ := cfgRepo.FindByMerchantID(context.Background(), mid)
	cfg.URL = srv.URL
	_ = cfgRepo.Upsert(context.Background(), cfg)

	delRepo := newMemWebhookDeliveryRepo()
	eventID := "evt_stable_123"
	payload, _ := json.Marshal(model.MerchantWebhookEventPayload{
		ID: eventID, Type: model.MerchantWebhookEventPaymentPaid, Version: "1",
		CreatedAt: time.Now().UTC(),
		Data:      model.MerchantWebhookPaymentData{TransactionID: uuid.New(), Amount: 1, Currency: "IDR", Status: model.TransactionStatusPaid},
	})
	delivery := &model.MerchantWebhookDelivery{
		ID: uuid.New(), MerchantID: mid, EventID: eventID,
		EventType: model.MerchantWebhookEventPaymentPaid, TransactionID: uuid.New(),
		EndpointURL: publicWebhookTestEndpoint, Payload: payload, Status: model.MerchantWebhookDeliveryStatusPending,
		NextAttemptAt: time.Now().UTC().Add(-time.Second), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = delRepo.Create(context.Background(), delivery)

	dispatcher := service.NewMerchantWebhookDispatcher(delRepo, cfgRepo, testEncKey(), 2*time.Second, 8, time.Minute, 10,
		publicWebhookTestClient(2*time.Second, srv.Listener.Addr().String()))
	n, err := dispatcher.ProcessBatch(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if gotEventID != eventID || gotType != string(model.MerchantWebhookEventPaymentPaid) {
		t.Fatalf("headers event=%s type=%s", gotEventID, gotType)
	}
	got, _ := delRepo.FindByID(context.Background(), mid, delivery.ID)
	if got.Status != model.MerchantWebhookDeliveryStatusDelivered {
		t.Fatalf("status=%s", got.Status)
	}
	if got.EventID != eventID {
		t.Fatal("event id must remain stable")
	}
}

func TestMerchantWebhookDispatcher_Retryable5xx(t *testing.T) {
	mid := uuid.New()
	secret := "whsec_retry"
	enc, _ := service.EncryptWebhookSecret(testEncKey(), secret)
	cfgRepo := newMemWebhookConfigRepo()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_ = cfgRepo.Upsert(context.Background(), &model.MerchantWebhookConfig{
		ID: uuid.New(), MerchantID: mid, URL: srv.URL,
		EncryptedSecret: enc, Status: model.MerchantWebhookConfigStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	delRepo := newMemWebhookDeliveryRepo()
	d := &model.MerchantWebhookDelivery{
		ID: uuid.New(), MerchantID: mid, EventID: "evt_retry",
		EventType: model.MerchantWebhookEventPaymentFailed, TransactionID: uuid.New(),
		EndpointURL: publicWebhookTestEndpoint, Payload: []byte(`{"id":"evt_retry"}`),
		Status: model.MerchantWebhookDeliveryStatusPending, NextAttemptAt: time.Now().UTC().Add(-time.Second),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = delRepo.Create(context.Background(), d)
	dispatcher := service.NewMerchantWebhookDispatcher(delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10,
		publicWebhookTestClient(time.Second, srv.Listener.Addr().String()))
	_, _ = dispatcher.ProcessBatch(context.Background())
	got, _ := delRepo.FindByID(context.Background(), mid, d.ID)
	if got.Status != model.MerchantWebhookDeliveryStatusPending {
		t.Fatalf("expected PENDING for retry, got %s", got.Status)
	}
	if got.AttemptCount != 1 {
		t.Fatalf("attempt_count=%d", got.AttemptCount)
	}
}

func TestMerchantWebhookDispatcher_NonRetryable4xx(t *testing.T) {
	mid := uuid.New()
	secret := "whsec_fail"
	enc, _ := service.EncryptWebhookSecret(testEncKey(), secret)
	cfgRepo := newMemWebhookConfigRepo()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_ = cfgRepo.Upsert(context.Background(), &model.MerchantWebhookConfig{
		ID: uuid.New(), MerchantID: mid, URL: srv.URL,
		EncryptedSecret: enc, Status: model.MerchantWebhookConfigStatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	delRepo := newMemWebhookDeliveryRepo()
	d := &model.MerchantWebhookDelivery{
		ID: uuid.New(), MerchantID: mid, EventID: "evt_fail",
		EventType: model.MerchantWebhookEventPaymentPaid, TransactionID: uuid.New(),
		EndpointURL: publicWebhookTestEndpoint, Payload: []byte(`{"id":"evt_fail"}`),
		Status: model.MerchantWebhookDeliveryStatusPending, NextAttemptAt: time.Now().UTC().Add(-time.Second),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = delRepo.Create(context.Background(), d)
	dispatcher := service.NewMerchantWebhookDispatcher(delRepo, cfgRepo, testEncKey(), time.Second, 8, time.Minute, 10,
		publicWebhookTestClient(time.Second, srv.Listener.Addr().String()))
	_, _ = dispatcher.ProcessBatch(context.Background())
	got, _ := delRepo.FindByID(context.Background(), mid, d.ID)
	if got.Status != model.MerchantWebhookDeliveryStatusFailed {
		t.Fatalf("expected FAILED, got %s", got.Status)
	}
}

func TestMerchantWebhookConfig_RequireHTTPS(t *testing.T) {
	mid := uuid.New()
	merchantRepo := &memMerchantRepoForWebhook{m: &model.Merchant{ID: mid, Status: model.MerchantStatusActive}}
	svc := service.NewMerchantWebhookConfigService(newMemWebhookConfigRepo(), newMemWebhookDeliveryRepo(), merchantRepo, testEncKey(), true)
	_, err := svc.Upsert(context.Background(), mid, model.UpsertMerchantWebhookRequest{URL: "http://insecure.example/hook"})
	if !errors.Is(err, service.ErrWebhookInvalidURL) {
		t.Fatalf("want ErrWebhookInvalidURL, got %v", err)
	}
}

func TestEventTypeForStatus(t *testing.T) {
	cases := map[model.TransactionStatus]model.MerchantWebhookEventType{
		model.TransactionStatusCreated:   model.MerchantWebhookEventPaymentCreated,
		model.TransactionStatusPending:   model.MerchantWebhookEventPaymentPending,
		model.TransactionStatusPaid:      model.MerchantWebhookEventPaymentPaid,
		model.TransactionStatusFailed:    model.MerchantWebhookEventPaymentFailed,
		model.TransactionStatusExpired:   model.MerchantWebhookEventPaymentExpired,
		model.TransactionStatusCancelled: model.MerchantWebhookEventPaymentCancelled,
	}
	for st, want := range cases {
		got, ok := model.EventTypeForStatus(st)
		if !ok || got != want {
			t.Fatalf("%s -> %s ok=%v", st, got, ok)
		}
	}
}
