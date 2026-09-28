package service

// email_outbox_worker_test.go — Phase 8C.3B tests for the asynchronous email
// outbox worker: claim → send → SENT / retry / DEAD, stale PROCESSING
// recovery, permanent-vs-retryable classification, max attempts, concurrency,
// graceful shutdown, and error/log sanitization.
//
// The mockEmailOutboxRepo below is the project's in-memory-repository testing
// convention (same style as mockInvitationRepo). It mirrors the production
// ClaimPending/Mark* SQL semantics exactly — mutex-serialized atomic claim
// (the same contract FOR UPDATE SKIP LOCKED provides), PROCESSING-guarded
// transitions, stale-PROCESSING recovery — so worker behavior can be asserted
// without a live PostgreSQL. The SQL itself is additionally exercised by the
// Phase 8C.3B manual verification against the real database.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/dhikaarta/pay-gate-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ─── In-memory outbox repository ──────────────────────────────────────────────

type mockEmailOutboxRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*model.EmailOutbox

	// Failure hooks (§18 injection points). claimErr also covers stale
	// recovery failures — recovery runs inside ClaimPending. deleteErr
	// covers Phase 8C.3C retention failures.
	claimErr     error
	markSentErr  error
	markRetryErr error
	markDeadErr  error
	deleteErr    error
}

func newMockEmailOutboxRepo() *mockEmailOutboxRepo {
	return &mockEmailOutboxRepo{rows: make(map[uuid.UUID]*model.EmailOutbox)}
}

// seed stores a PENDING entry (Phase 8C.3A enqueue shape) and returns the
// stored pointer — tests may adjust it before the first claim.
func (m *mockEmailOutboxRepo) seed(merchantID uuid.UUID, recipient, subject, textBody, htmlBody string) *model.EmailOutbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	e := &model.EmailOutbox{
		ID:            uuid.New(),
		MerchantID:    merchantID,
		Type:          model.EmailOutboxTypeInvitation,
		Recipient:     recipient,
		Subject:       subject,
		TextBody:      textBody,
		HTMLBody:      htmlBody,
		Status:        model.EmailOutboxStatusPending,
		AttemptCount:  0,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.rows[e.ID] = e
	return e
}

// get returns the stored row (for assertions).
func (m *mockEmailOutboxRepo) get(id uuid.UUID) *model.EmailOutbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

func (m *mockEmailOutboxRepo) EnqueueInTx(_ context.Context, _ pgx.Tx, entry *model.EmailOutbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[entry.ID] = entry
	return nil
}

func (m *mockEmailOutboxRepo) ClaimPending(_ context.Context, batchSize int, staleAfter time.Duration) ([]*model.EmailOutbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	now := time.Now().UTC()

	// Stale PROCESSING recovery (same policy as webhook ClaimPending).
	for _, e := range m.rows {
		if e.Status == model.EmailOutboxStatusProcessing &&
			e.ProcessingAt != nil &&
			e.ProcessingAt.Before(now.Add(-staleAfter)) {
			e.Status = model.EmailOutboxStatusPending
			e.ProcessingAt = nil
			e.UpdatedAt = now
		}
	}

	// Due PENDING rows, ORDER BY next_attempt_at, id.
	var due []*model.EmailOutbox
	for _, e := range m.rows {
		if e.Status == model.EmailOutboxStatusPending && !e.NextAttemptAt.After(now) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].NextAttemptAt.Equal(due[j].NextAttemptAt) {
			return due[i].NextAttemptAt.Before(due[j].NextAttemptAt)
		}
		return due[i].ID.String() < due[j].ID.String()
	})
	if len(due) > batchSize {
		due = due[:batchSize]
	}

	claimed := make([]*model.EmailOutbox, 0, len(due))
	for _, e := range due {
		e.Status = model.EmailOutboxStatusProcessing
		pa, la := now, now
		e.ProcessingAt = &pa
		e.LastAttemptAt = &la
		e.AttemptCount++ // the claim IS the attempt
		e.UpdatedAt = now
		c := *e // RETURNING-style snapshot
		claimed = append(claimed, &c)
	}
	return claimed, nil
}

// mark applies a PROCESSING-guarded transition (mirror of the SQL WHERE
// id = $1 AND status = 'PROCESSING').
func (m *mockEmailOutboxRepo) mark(id uuid.UUID, hook *error, apply func(*model.EmailOutbox)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if *hook != nil {
		return *hook // injection: transition fails, row stays PROCESSING
	}
	e, ok := m.rows[id]
	if !ok || e.Status != model.EmailOutboxStatusProcessing {
		return repository.ErrEmailOutboxNotFound
	}
	apply(e)
	e.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *mockEmailOutboxRepo) MarkSent(_ context.Context, id uuid.UUID) error {
	return m.mark(id, &m.markSentErr, func(e *model.EmailOutbox) {
		now := time.Now().UTC()
		e.Status = model.EmailOutboxStatusSent
		e.SentAt = &now
		e.ProcessingAt = nil
		e.LastError = nil
	})
}

func (m *mockEmailOutboxRepo) MarkRetry(_ context.Context, id uuid.UUID, nextAttemptAt time.Time, lastError string) error {
	return m.mark(id, &m.markRetryErr, func(e *model.EmailOutbox) {
		e.Status = model.EmailOutboxStatusPending
		e.NextAttemptAt = nextAttemptAt
		e.LastError = &lastError
		e.ProcessingAt = nil
	})
}

func (m *mockEmailOutboxRepo) MarkDead(_ context.Context, id uuid.UUID, lastError string) error {
	return m.mark(id, &m.markDeadErr, func(e *model.EmailOutbox) {
		e.Status = model.EmailOutboxStatusDead
		e.LastError = &lastError
		e.ProcessingAt = nil
	})
}

// DeleteExpiredTerminal (Phase 8C.3C) mirrors the retention SQL exactly:
// only SENT (sent_at strictly before sentCutoff) and DEAD (updated_at
// strictly before deadCutoff) are eligible, ordered by (updated_at, id) ASC
// and limited — PENDING and PROCESSING are never touched regardless of age.
// Strict comparisons mean a timestamp EQUAL to the cutoff is retained.
func (m *mockEmailOutboxRepo) DeleteExpiredTerminal(_ context.Context, sentCutoff time.Time, deadCutoff time.Time, limit int) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return 0, 0, m.deleteErr
	}
	if limit <= 0 {
		return 0, 0, fmt.Errorf("email outbox delete expired terminal: invalid limit")
	}

	var eligible []*model.EmailOutbox
	for _, e := range m.rows {
		switch e.Status {
		case model.EmailOutboxStatusSent:
			if e.SentAt != nil && e.SentAt.Before(sentCutoff) {
				eligible = append(eligible, e)
			}
		case model.EmailOutboxStatusDead:
			if e.UpdatedAt.Before(deadCutoff) {
				eligible = append(eligible, e)
			}
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if !eligible[i].UpdatedAt.Equal(eligible[j].UpdatedAt) {
			return eligible[i].UpdatedAt.Before(eligible[j].UpdatedAt)
		}
		return eligible[i].ID.String() < eligible[j].ID.String()
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}

	var sentDeleted, deadDeleted int64
	for _, e := range eligible {
		switch e.Status {
		case model.EmailOutboxStatusSent:
			sentDeleted++
		case model.EmailOutboxStatusDead:
			deadDeleted++
		}
		delete(m.rows, e.ID)
	}
	return sentDeleted, deadDeleted, nil
}

// clearAllHooks disables every failure injection (used to let a recovery
// pass complete after a deliberately failed transition).
func (m *mockEmailOutboxRepo) clearAllHooks() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimErr = nil
	m.markSentErr = nil
	m.markRetryErr = nil
	m.markDeadErr = nil
	m.deleteErr = nil
}

var (
	seedMerchantA = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	seedMerchantB = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

// captureLogs runs fn with a JSON slog handler capturing output into a buffer.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// ─── Error classification (§6) ────────────────────────────────────────────────

func TestIsPermanentEmailError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		permanent bool
	}{
		{"message invalid (pre-I/O validation)", fmt.Errorf("%w: subject is required", ErrEmailMessageInvalid), true},
		{"smtp 5xx permanent", fmt.Errorf("%w: rcpt: %w", ErrEmailSendFailed, &textproto.Error{Code: 550, Msg: "5.1.1 unknown"}), true},
		{"smtp 4xx temporary", fmt.Errorf("%w: rcpt: %w", ErrEmailSendFailed, &textproto.Error{Code: 451, Msg: "4.3.0 try later"}), false},
		{"dial failure retryable", fmt.Errorf("%w: dial tcp: connection refused", ErrEmailSendFailed), false},
		{"context deadline retryable", fmt.Errorf("%w: %w", ErrEmailSendFailed, context.DeadlineExceeded), false},
		{"unknown error defaults retryable", errors.New("something odd"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermanentEmailError(tc.err); got != tc.permanent {
				t.Errorf("isPermanentEmailError(%v) = %v, want %v", tc.err, got, tc.permanent)
			}
		})
	}
}

// ─── Success path (§4/§5/§17.1,2,10,11,12) ────────────────────────────────────

func TestEmailOutboxDispatcher_SendsExactPersistedPayloadAndMarksSent(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	const token = "a1b2c3d4e5f6789012345678a1b2c3d4e5f6789012345678a1b2c3d4e5f67890"
	entry := repo.seed(seedMerchantA, "invitee@example.com", "Acme invited you",
		"Accept: https://dashboard.example.test/accept-invitation?token="+token,
		`<a href="https://dashboard.example.test/accept-invitation?token=`+token+`">Join</a>`)
	invID := uuid.New()
	entry.ReferenceID = &invID

	n, err := d.ProcessBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed = %d, want 1", n)
	}

	// §17.10: EmailSender received EXACTLY the persisted payload — no
	// re-render, no invitation lookup, no regenerated token (§17.11/§17.12:
	// the dispatcher has no invitation dependency at all).
	msgs := sender.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	got := msgs[0]
	if len(got.To) != 1 || got.To[0] != entry.Recipient {
		t.Errorf("To = %v, want [%s]", got.To, entry.Recipient)
	}
	if got.Subject != entry.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, entry.Subject)
	}
	if got.TextBody != entry.TextBody || got.HTMLBody != entry.HTMLBody {
		t.Error("bodies must be byte-identical to the persisted payload")
	}
	if got.From != "" {
		t.Errorf("From = %q, want empty (resolved by EmailSender at send time)", got.From)
	}

	// §5: PROCESSING → SENT with the full §5 field set.
	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusSent {
		t.Fatalf("status = %v, want SENT", stored.Status)
	}
	if stored.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", stored.AttemptCount)
	}
	if stored.SentAt == nil {
		t.Error("sent_at must be populated")
	}
	if stored.ProcessingAt != nil {
		t.Errorf("processing_at = %v, want NULL", stored.ProcessingAt)
	}
	if stored.LastError != nil {
		t.Errorf("last_error = %v, want NULL", stored.LastError)
	}
	if stored.LastAttemptAt == nil {
		t.Error("last_attempt_at must be populated by the claim")
	}

	// A SENT row is never claimed again.
	n2, err := d.ProcessBatch(context.Background())
	if err != nil || n2 != 0 {
		t.Errorf("second ProcessBatch = (%d, %v), want (0, nil) — SENT must not be re-claimed", n2, err)
	}
	if sender.Len() != 1 {
		t.Errorf("messages = %d, want 1 — no duplicate send", sender.Len())
	}
}

// ─── Retry path (§7/§17.3,4 + §22 cycle) ─────────────────────────────────────

func TestEmailOutboxDispatcher_RetryableErrorSchedulesFutureRetry(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	sender.Err = fmt.Errorf("%w: dial tcp 127.0.0.1:25: connect: connection refused", ErrEmailSendFailed)
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	entry := repo.seed(seedMerchantA, "retry.me@example.com", "s", "text", "")
	before := time.Now().UTC()

	if _, err := d.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}

	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusPending {
		t.Fatalf("status = %v, want PENDING (retryable → back to queue)", stored.Status)
	}
	if stored.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", stored.AttemptCount)
	}
	if stored.ProcessingAt != nil {
		t.Errorf("processing_at = %v, want NULL", stored.ProcessingAt)
	}
	if stored.LastError == nil || *stored.LastError == "" {
		t.Error("last_error must persist a safe summary")
	}
	// §7: backoff[1] = 10s + 0..10% jitter (shared webhook schedule).
	lo, hi := before.Add(9*time.Second), before.Add(12*time.Second)
	if stored.NextAttemptAt.Before(lo) || stored.NextAttemptAt.After(hi) {
		t.Errorf("next_attempt_at = %v, want within [%v, %v]", stored.NextAttemptAt, lo, hi)
	}

	// Not eligible yet → no immediate re-send (no hot loop on a failing row).
	if n, err := d.ProcessBatch(context.Background()); err != nil || n != 0 {
		t.Errorf("immediate re-run = (%d, %v), want (0, nil)", n, err)
	}
	if stored.AttemptCount != 1 {
		t.Errorf("attempt_count = %d after ineligible run, want 1", stored.AttemptCount)
	}
}

func TestEmailOutboxDispatcher_RetryBecomesSentWhenEligible(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	sender.Err = fmt.Errorf("%w: 451 temporary", ErrEmailSendFailed)
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	entry := repo.seed(seedMerchantA, "later@example.com", "s", "text", "")
	if _, err := d.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := repo.get(entry.ID).Status; got != model.EmailOutboxStatusPending {
		t.Fatalf("after retryable failure status = %v, want PENDING", got)
	}

	// Simulate the backoff elapsing (fast-forward next_attempt_at).
	repo.mu.Lock()
	entry.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	repo.mu.Unlock()

	sender.Err = nil // SMTP recovered
	if n, err := d.ProcessBatch(context.Background()); err != nil || n != 1 {
		t.Fatalf("second pass = (%d, %v), want (1, nil)", n, err)
	}
	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusSent {
		t.Errorf("status = %v, want SENT after eligible retry", stored.Status)
	}
	if stored.AttemptCount != 2 {
		t.Errorf("attempt_count = %d, want 2", stored.AttemptCount)
	}
	if sender.Len() != 1 {
		t.Errorf("successful messages = %d, want 1", sender.Len())
	}
}

// ─── Permanent + max attempts (§8/§9/§17.5,6) ────────────────────────────────

func TestEmailOutboxDispatcher_PermanentErrorDeadImmediately(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	sender.Err = fmt.Errorf("%w: subject is required", ErrEmailMessageInvalid)
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	entry := repo.seed(seedMerchantA, "permanent@example.com", "s", "text", "")
	if _, err := d.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}

	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusDead {
		t.Fatalf("status = %v, want DEAD (permanent, no retry)", stored.Status)
	}
	if stored.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1 — permanent failures never retry", stored.AttemptCount)
	}
	if stored.LastError == nil || !strings.Contains(*stored.LastError, "permanent failure") {
		t.Errorf("last_error = %v, want permanent-failure summary", stored.LastError)
	}
	if stored.ProcessingAt != nil || stored.SentAt != nil {
		t.Errorf("processing_at=%v sent_at=%v, both want NULL", stored.ProcessingAt, stored.SentAt)
	}

	// DEAD is never claimed again.
	if n, err := d.ProcessBatch(context.Background()); err != nil || n != 0 {
		t.Errorf("re-run = (%d, %v), want (0, nil) — DEAD must not be re-claimed", n, err)
	}
}

func TestEmailOutboxDispatcher_Smtp5xxDead_4xxRetries(t *testing.T) {
	t.Run("5xx permanent", func(t *testing.T) {
		repo := newMockEmailOutboxRepo()
		sender := NewMockEmailSender()
		sender.Err = fmt.Errorf("%w: rcpt: %w", ErrEmailSendFailed, &textproto.Error{Code: 550, Msg: "5.1.1 user unknown"})
		d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

		entry := repo.seed(seedMerchantA, "bounced@example.com", "s", "text", "")
		if _, err := d.ProcessBatch(context.Background()); err != nil {
			t.Fatalf("ProcessBatch: %v", err)
		}
		if got := repo.get(entry.ID).Status; got != model.EmailOutboxStatusDead {
			t.Errorf("status = %v, want DEAD for SMTP 5xx", got)
		}
	})
	t.Run("4xx retryable", func(t *testing.T) {
		repo := newMockEmailOutboxRepo()
		sender := NewMockEmailSender()
		sender.Err = fmt.Errorf("%w: rcpt: %w", ErrEmailSendFailed, &textproto.Error{Code: 451, Msg: "4.3.0 temporary"})
		d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

		entry := repo.seed(seedMerchantA, "tempfail@example.com", "s", "text", "")
		if _, err := d.ProcessBatch(context.Background()); err != nil {
			t.Fatalf("ProcessBatch: %v", err)
		}
		stored := repo.get(entry.ID)
		if stored.Status != model.EmailOutboxStatusPending {
			t.Errorf("status = %v, want PENDING for SMTP 4xx", stored.Status)
		}
		if stored.NextAttemptAt.Before(time.Now().UTC()) {
			t.Error("retry must be scheduled in the future")
		}
	})
}

func TestEmailOutboxDispatcher_MaxAttemptsDead(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	sender.Err = fmt.Errorf("%w: connection reset", ErrEmailSendFailed)
	d := NewEmailOutboxDispatcher(repo, sender, 3, time.Minute, 20)

	entry := repo.seed(seedMerchantA, "exhausted@example.com", "s", "text", "")
	entry.AttemptCount = 2 // two prior attempts failed; this claim makes 3/3

	if _, err := d.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusDead {
		t.Fatalf("status = %v, want DEAD at max attempts", stored.Status)
	}
	if stored.AttemptCount != 3 {
		t.Errorf("attempt_count = %d, want 3", stored.AttemptCount)
	}
	if stored.LastError == nil || !strings.Contains(*stored.LastError, "max attempts (3) exhausted") {
		t.Errorf("last_error = %v, want max-attempts summary", stored.LastError)
	}
	if stored.ProcessingAt != nil {
		t.Error("processing_at must be cleared")
	}
}

// ─── Stale PROCESSING recovery (§3/§17.7) ────────────────────────────────────

func TestEmailOutboxDispatcher_StaleProcessingRecoveredFreshUntouched(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	staleAfter := time.Minute
	d := NewEmailOutboxDispatcher(repo, sender, 8, staleAfter, 20)

	// Crashed worker: claimed once (attempt already counted), stuck 10 minutes.
	stale := repo.seed(seedMerchantA, "stuck@example.com", "stale", "stale text", "")
	stale.Status = model.EmailOutboxStatusProcessing
	stale.AttemptCount = 1
	stuckAt := time.Now().UTC().Add(-10 * time.Minute)
	stale.ProcessingAt = &stuckAt

	// Fresh in-flight claim — must not be touched.
	fresh := repo.seed(seedMerchantB, "inflight@example.com", "fresh", "fresh text", "")
	fresh.Status = model.EmailOutboxStatusProcessing
	fresh.AttemptCount = 1
	now := time.Now().UTC()
	fresh.ProcessingAt = &now

	n, err := d.ProcessBatch(context.Background())
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed = %d, want 1 (only the stale row)", n)
	}

	recovered := repo.get(stale.ID)
	if recovered.Status != model.EmailOutboxStatusSent {
		t.Errorf("stale row status = %v, want SENT after recovery", recovered.Status)
	}
	if recovered.AttemptCount != 2 {
		t.Errorf("stale row attempt_count = %d, want 2 (recovered claim counts)", recovered.AttemptCount)
	}
	if recovered.ProcessingAt != nil {
		t.Error("recovered row processing_at must be cleared")
	}

	untouched := repo.get(fresh.ID)
	if untouched.Status != model.EmailOutboxStatusProcessing || untouched.AttemptCount != 1 {
		t.Errorf("fresh PROCESSING row was touched: status=%v attempts=%d", untouched.Status, untouched.AttemptCount)
	}
	if sender.Len() != 1 {
		t.Errorf("messages = %d, want 1 — only the stale row is delivered", sender.Len())
	}
}

// ─── Concurrency (§11/§17.8) ─────────────────────────────────────────────────

// slowEmailSender widens the race window between claim and mark.
type slowEmailSender struct {
	delegate EmailSender
	delay    time.Duration
}

func (s *slowEmailSender) Send(ctx context.Context, msg EmailMessage) error {
	time.Sleep(s.delay)
	return s.delegate.Send(ctx, msg)
}

func TestEmailOutboxDispatcher_ConcurrentWorkersNeverDoubleSend(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	base := NewMockEmailSender()
	sender := &slowEmailSender{delegate: base, delay: 5 * time.Millisecond}

	const rows = 10
	for i := 0; i < rows; i++ {
		repo.seed(seedMerchantA, fmt.Sprintf("user%d@example.com", i), fmt.Sprintf("subject %d", i), "t", "")
	}

	// Two independent workers over one shared repository — the SQL claim's
	// FOR UPDATE SKIP LOCKED contract is mirrored by the mock's serialized
	// claim; disjoint sets must result.
	w1 := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 5)
	w2 := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 5)

	var wg sync.WaitGroup
	for _, d := range []EmailOutboxDispatcher{w1, w2} {
		wg.Add(1)
		go func(d EmailOutboxDispatcher) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := d.ProcessBatch(context.Background()); err != nil {
					t.Errorf("ProcessBatch: %v", err)
				}
			}
		}(d)
	}
	wg.Wait()

	// Every row delivered exactly once.
	seen := map[string]int{}
	for _, m := range base.Messages() {
		seen[m.Subject]++
	}
	for i := 0; i < rows; i++ {
		if got := seen[fmt.Sprintf("subject %d", i)]; got != 1 {
			t.Errorf("subject %d delivered %d times, want exactly 1", i, got)
		}
	}
	if base.Len() != rows {
		t.Errorf("total deliveries = %d, want %d", base.Len(), rows)
	}
}

// ─── Tenant isolation (§23) ──────────────────────────────────────────────────

func TestEmailOutboxDispatcher_TenantPayloadsUnmixed(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	repo.seed(seedMerchantA, "alice@a.example", "From A", "body-a", "")
	repo.seed(seedMerchantB, "bob@b.example", "From B", "body-b", "")

	if _, err := d.ProcessBatch(context.Background()); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	msgs := sender.Messages()
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	bySubject := map[string]EmailMessage{}
	for _, m := range msgs {
		bySubject[m.Subject] = m
	}
	if m := bySubject["From A"]; len(m.To) != 1 || m.To[0] != "alice@a.example" || m.TextBody != "body-a" {
		t.Errorf("merchant A payload mixed: %+v", m)
	}
	if m := bySubject["From B"]; len(m.To) != 1 || m.To[0] != "bob@b.example" || m.TextBody != "body-b" {
		t.Errorf("merchant B payload mixed: %+v", m)
	}
	// Each row still belongs to its original merchant.
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, e := range repo.rows {
		want := seedMerchantA
		if e.Recipient == "bob@b.example" {
			want = seedMerchantB
		}
		if e.MerchantID != want {
			t.Errorf("row %s merchant = %v, want %v", e.ID, e.MerchantID, want)
		}
	}
}

// ─── Error sanitization (§7/§16/§17.13) ──────────────────────────────────────

func TestEmailOutboxDispatcher_ErrorStateAndLogsLeakNothing(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	// Pathological sender error echoing the recipient (SMTP servers do this).
	sender.Err = fmt.Errorf("rcpt to secret.recipient@example.com rejected: 450 try later, rcpt to secret.recipient@example.com again")
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	const token = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	const subject = "Confidential subject line"
	entry := repo.seed(seedMerchantA, "secret.recipient@example.com", subject,
		"token="+token, "<p>token="+token+"</p>")

	logs := captureLogs(t, func() {
		if _, err := d.ProcessBatch(context.Background()); err != nil {
			t.Errorf("ProcessBatch: %v", err)
		}
	})

	stored := repo.get(entry.ID)
	if stored.Status != model.EmailOutboxStatusPending || stored.LastError == nil {
		t.Fatalf("status = %v lastError = %v, want retryable PENDING with summary", stored.Status, stored.LastError)
	}
	// Recipient redacted to a domain marker; content never present.
	if strings.Contains(*stored.LastError, "secret.recipient@example.com") {
		t.Errorf("last_error contains the full recipient address: %q", *stored.LastError)
	}
	if !strings.Contains(*stored.LastError, "[recipient@example.com]") {
		t.Errorf("last_error should carry the redacted domain marker: %q", *stored.LastError)
	}
	for name, secret := range map[string]string{"token": token, "subject": subject, "text_body": entry.TextBody} {
		if strings.Contains(*stored.LastError, secret) {
			t.Errorf("last_error leaked %s", name)
		}
	}
	if len(*stored.LastError) > maxEmailErrorLen {
		t.Errorf("last_error length = %d, want <= %d", len(*stored.LastError), maxEmailErrorLen)
	}

	// Logs: same secrets must never appear (IDs/attempt/domain/summary only).
	for name, secret := range map[string]string{"token": token, "subject": subject, "full recipient": "secret.recipient@example.com"} {
		if strings.Contains(logs, secret) {
			t.Errorf("worker logs leaked %s:\n%s", name, logs)
		}
	}
}

// ─── Failure injection at every DB transition (§18) ──────────────────────────

func TestEmailOutboxDispatcher_ClaimFailureSurfaces(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	repo.claimErr = errors.New("db connection lost")
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)
	repo.seed(seedMerchantA, "never@example.com", "s", "t", "")

	n, err := d.ProcessBatch(context.Background())
	if err == nil {
		t.Fatal("claim failure must surface, not be swallowed")
	}
	if n != 0 {
		t.Errorf("processed = %d, want 0", n)
	}
	if sender.Len() != 0 {
		t.Errorf("messages = %d, want 0 — nothing can be sent without a claim", sender.Len())
	}
}

func TestEmailOutboxDispatcher_TransitionFailuresRecoverViaStaleRecovery(t *testing.T) {
	cases := []struct {
		name          string
		senderErr     error
		recoverSender bool // clear the sender error before the recovery pass
		hook          func(*mockEmailOutboxRepo)
		finalState    model.EmailOutboxStatus
	}{
		{
			name:          "SENT update failure",
			senderErr:     nil,
			recoverSender: false,
			hook:          func(r *mockEmailOutboxRepo) { r.markSentErr = errors.New("sent update failed") },
			finalState:    model.EmailOutboxStatusSent,
		},
		{
			name:          "retry update failure",
			senderErr:     fmt.Errorf("%w: reset by peer", ErrEmailSendFailed),
			recoverSender: true, // SMTP recovered by the time the row is recovered
			hook:          func(r *mockEmailOutboxRepo) { r.markRetryErr = errors.New("retry update failed") },
			finalState:    model.EmailOutboxStatusSent,
		},
		{
			name:          "DEAD update failure",
			senderErr:     fmt.Errorf("%w: rejected", ErrEmailMessageInvalid),
			recoverSender: false, // still permanently broken after recovery
			hook:          func(r *mockEmailOutboxRepo) { r.markDeadErr = errors.New("dead update failed") },
			finalState:    model.EmailOutboxStatusDead, // recovered, fails permanently again
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockEmailOutboxRepo()
			sender := NewMockEmailSender()
			sender.Err = tc.senderErr
			d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)
			entry := repo.seed(seedMerchantA, "flaky@example.com", "s", "t", "")

			// Pass 1: send outcome happens but the DB transition fails —
			// the row MUST remain PROCESSING (not silently terminal).
			tc.hook(repo)
			logs := captureLogs(t, func() {
				if _, err := d.ProcessBatch(context.Background()); err != nil {
					t.Errorf("transition failure should be logged per-row, batch continues: %v", err)
				}
			})
			if !strings.Contains(logs, "email outbox delivery error") {
				t.Errorf("transition failure must be logged:\n%s", logs)
			}
			if got := repo.get(entry.ID).Status; got != model.EmailOutboxStatusProcessing {
				t.Fatalf("status after failed transition = %v, want PROCESSING (stuck, awaiting recovery)", got)
			}

			// Pass 2: stale recovery re-claims the stuck row → terminal state.
			if tc.recoverSender {
				sender.Err = nil
			}
			repo.clearAllHooks()
			repo.mu.Lock()
			stuck := time.Now().UTC().Add(-time.Hour)
			repo.rows[entry.ID].ProcessingAt = &stuck
			repo.mu.Unlock()

			if _, err := d.ProcessBatch(context.Background()); err != nil {
				t.Fatalf("recovery pass: %v", err)
			}
			final := repo.get(entry.ID)
			if final.Status != tc.finalState {
				t.Errorf("final status = %v, want %v", final.Status, tc.finalState)
			}
			if final.ProcessingAt != nil {
				t.Error("processing_at must be cleared on the terminal/retry state")
			}
			// Stale-recovery claim counted as an extra attempt (at-least-once).
			if final.AttemptCount != 2 {
				t.Errorf("attempt_count = %d, want 2", final.AttemptCount)
			}
		})
	}
}

func TestEmailOutboxDispatcher_StaleRecoveryFailureSurfaces(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	repo.claimErr = errors.New("recovery query failed") // recovery runs inside ClaimPending
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)

	if _, err := d.ProcessBatch(context.Background()); err == nil {
		t.Fatal("stale-recovery/claim failure must surface")
	}
}

// ─── Worker lifecycle (§10/§17.9,14) ─────────────────────────────────────────

func TestEmailOutboxWorker_ShutdownStopsPolling(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)
	w := NewEmailOutboxWorker(d, 10*time.Millisecond)

	for i := 0; i < 3; i++ {
		repo.seed(seedMerchantA, fmt.Sprintf("shutdown%d@example.com", i), fmt.Sprintf("s%d", i), "t", "")
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Wait until at least one batch was processed.
	deadline := time.Now().Add(2 * time.Second)
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sender.Len() == 0 {
		cancel()
		w.Wait()
		t.Fatal("worker never processed the queued rows")
	}

	cancel()
	stopped := make(chan struct{})
	go func() { w.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}

	// No further polling after Wait returns.
	settled := sender.Len()
	time.Sleep(60 * time.Millisecond) // > 5 intervals
	if sender.Len() != settled {
		t.Errorf("worker kept sending after shutdown: %d → %d", settled, sender.Len())
	}
}

func TestEmailOutboxWorker_EmptyQueueIsIdle(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	d := NewEmailOutboxDispatcher(repo, sender, 8, time.Minute, 20)
	w := NewEmailOutboxWorker(d, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	time.Sleep(90 * time.Millisecond) // several intervals with no work
	cancel()
	w.Wait()

	if sender.Len() != 0 {
		t.Errorf("messages = %d, want 0 — nothing to send on an empty queue", sender.Len())
	}
	// The loop is ticker-driven (interval-bounded), not a hot spin — the
	// worker survives idle periods and stops cleanly, matching
	// MerchantWebhookWorker.
}

func TestNewEmailOutboxWorker_DefaultsInterval(t *testing.T) {
	w := NewEmailOutboxWorker(nil, 0)
	if w.interval != 5*time.Second {
		t.Errorf("interval = %v, want 5s default", w.interval)
	}
}
