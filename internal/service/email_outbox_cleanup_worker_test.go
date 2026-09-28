package service

// email_outbox_cleanup_worker_test.go — Phase 8C.3C tests for the terminal
// retention cleanup worker: SENT/DEAD age eligibility (strict cutoffs),
// the NON-NEGOTIABLE PENDING/PROCESSING safety property, mixed-state
// filtering, bounded batching/draining, error survivability, concurrency
// with the delivery worker, graceful shutdown, immediate first run, and
// counts-only logging.
//
// Uses the same mockEmailOutboxRepo in-memory convention as
// email_outbox_worker_test.go (same package), whose DeleteExpiredTerminal
// mirrors the production SQL semantics — strict (<) cutoffs, deterministic
// (updated_at, id) ordering, LIMIT per call. The SQL itself is additionally
// exercised by the Phase 8C.3C manual verification against the real
// database. Config validation (§12.I) lives in
// internal/config/config_email_test.go.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
)

const (
	day7  = 7 * 24 * time.Hour  // approved SENT retention
	day30 = 30 * 24 * time.Hour // approved DEAD retention
)

// seedAged stores a row in the given status whose lifecycle timestamps are
// `age` in the past (created_at/updated_at always; sent_at for SENT;
// processing_at for PROCESSING). Called before any worker starts, so the
// direct pointer mutation follows the existing test convention.
func seedAged(repo *mockEmailOutboxRepo, status model.EmailOutboxStatus, age time.Duration, recipient, subject, body string) *model.EmailOutbox {
	e := repo.seed(seedMerchantA, recipient, subject, body, "<p>"+body+"</p>")
	old := time.Now().UTC().Add(-age)
	e.Status = status
	e.CreatedAt = old
	e.UpdatedAt = old
	switch status {
	case model.EmailOutboxStatusSent:
		e.SentAt = &old
	case model.EmailOutboxStatusProcessing:
		pa := old
		e.ProcessingAt = &pa
	}
	return e
}

// seedTerminal seeds an old terminal row with harmless default payload.
func seedTerminal(repo *mockEmailOutboxRepo, status model.EmailOutboxStatus, age time.Duration) *model.EmailOutbox {
	return seedAged(repo, status, age, "retention-aged@example.com", "Invitation", "invite-body")
}

// newCleanupWorker builds the worker with the approved retention windows and
// a long interval — direct tick() calls make ticker timing irrelevant.
func newCleanupWorker(repo *mockEmailOutboxRepo, batchSize int) *EmailOutboxCleanupWorker {
	return NewEmailOutboxCleanupWorker(repo, time.Hour, batchSize, day7, day30)
}

// countingOutboxRepo records the limit and deleted count of every
// DeleteExpiredTerminal call so batching/drain/bound behavior is assertable.
type countingOutboxRepo struct {
	*mockEmailOutboxRepo
	mu      sync.Mutex
	limits  []int
	deleted []int
}

func (c *countingOutboxRepo) DeleteExpiredTerminal(ctx context.Context, sentCutoff, deadCutoff time.Time, limit int) (int64, int64, error) {
	s, d, err := c.mockEmailOutboxRepo.DeleteExpiredTerminal(ctx, sentCutoff, deadCutoff, limit)
	c.mu.Lock()
	c.limits = append(c.limits, limit)
	c.deleted = append(c.deleted, int(s+d))
	c.mu.Unlock()
	return s, d, err
}

func (c *countingOutboxRepo) calls() (limits, deleted []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.limits...), append([]int(nil), c.deleted...)
}

// ─── A. SENT eligibility ─────────────────────────────────────────────────────

func TestEmailOutboxCleanup_SentEligibility(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldSent := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour) // A.1 old → delete
	recentSent := seedTerminal(repo, model.EmailOutboxStatusSent, time.Hour)   // A.2 recent → retain

	w := newCleanupWorker(repo, 100)
	w.tick(context.Background())

	if repo.get(oldSent.ID) != nil {
		t.Error("old SENT row retained, want deleted")
	}
	if repo.get(recentSent.ID) == nil {
		t.Error("recent SENT row deleted, want retained")
	}
}

// A.3/B.6 — strict cutoff semantics: a timestamp EQUAL to the cutoff is
// retained (`sent_at < cutoff`, never `<=`); only strictly older rows go.
// Exercised directly against the repository with an exact cutoff (the worker
// computes its cutoff from time.Now(), so equality is not seedable there).
func TestDeleteExpiredTerminal_StrictCutoff(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	cutoff := time.Now().UTC().Add(-day7)

	seed := func(status model.EmailOutboxStatus, ts time.Time) *model.EmailOutbox {
		e := repo.seed(seedMerchantA, "strict@example.com", "Invitation", "b", "")
		e.Status = status
		e.UpdatedAt = ts
		if status == model.EmailOutboxStatusSent {
			e.SentAt = &ts
		}
		return e
	}
	sentExact := seed(model.EmailOutboxStatusSent, cutoff)                         // equal → retained
	sentJustOld := seed(model.EmailOutboxStatusSent, cutoff.Add(-time.Nanosecond)) // strictly older → deleted
	sentJustNew := seed(model.EmailOutboxStatusSent, cutoff.Add(time.Nanosecond))  // newer → retained
	deadExact := seed(model.EmailOutboxStatusDead, cutoff)
	deadJustOld := seed(model.EmailOutboxStatusDead, cutoff.Add(-time.Nanosecond))
	deadJustNew := seed(model.EmailOutboxStatusDead, cutoff.Add(time.Nanosecond))

	sent, dead, err := repo.DeleteExpiredTerminal(context.Background(), cutoff, cutoff, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sent != 1 || dead != 1 {
		t.Errorf("deleted sent=%d dead=%d, want sent=1 dead=1 (strict <)", sent, dead)
	}
	for _, tc := range []struct {
		name string
		row  *model.EmailOutbox
		gone bool
	}{
		{"SENT exactly at cutoff", sentExact, false},
		{"SENT just older than cutoff", sentJustOld, true},
		{"SENT just newer than cutoff", sentJustNew, false},
		{"DEAD exactly at cutoff", deadExact, false},
		{"DEAD just older than cutoff", deadJustOld, true},
		{"DEAD just newer than cutoff", deadJustNew, false},
	} {
		gone := repo.get(tc.row.ID) == nil
		if gone != tc.gone {
			t.Errorf("%s: deleted=%v, want %v", tc.name, gone, tc.gone)
		}
	}
}

// ─── B. DEAD eligibility ─────────────────────────────────────────────────────

func TestEmailOutboxCleanup_DeadEligibility(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldDead := seedTerminal(repo, model.EmailOutboxStatusDead, day30+time.Hour) // B.4 → delete
	recentDead := seedTerminal(repo, model.EmailOutboxStatusDead, time.Hour)    // B.5 → retain

	w := newCleanupWorker(repo, 100)
	w.tick(context.Background())

	if repo.get(oldDead.ID) != nil {
		t.Error("old DEAD row retained, want deleted")
	}
	if repo.get(recentDead.ID) == nil {
		t.Error("recent DEAD row deleted, want retained")
	}
}

// ─── C + §13. THE safety property: non-terminal rows are NEVER deleted ──────

func TestEmailOutboxCleanup_NeverTouchesNonTerminal(t *testing.T) {
	repo := newMockEmailOutboxRepo()

	// C.7 old PENDING (created/updated 100 days ago).
	oldPending := seedTerminal(repo, model.EmailOutboxStatusPending, 100*24*time.Hour)
	// C.9 old PENDING carrying a last_error.
	errMsg := "smtp 451 try later"
	oldPendingErr := seedTerminal(repo, model.EmailOutboxStatusPending, 100*24*time.Hour)
	oldPendingErr.LastError = &errMsg
	// C.8 + C.10 old PROCESSING with a stale processing_at (10 days).
	oldProcessing := seedTerminal(repo, model.EmailOutboxStatusProcessing, 10*24*time.Hour)
	if oldProcessing.ProcessingAt == nil {
		t.Fatal("fixture missing processing_at")
	}

	w := newCleanupWorker(repo, 100)
	w.tick(context.Background())
	w.tick(context.Background()) // two full runs — still nothing eligible

	for _, tc := range []struct {
		name string
		row  *model.EmailOutbox
	}{
		{"old PENDING", oldPending},
		{"old PENDING with last_error", oldPendingErr},
		{"old PROCESSING with stale processing_at", oldProcessing},
	} {
		if repo.get(tc.row.ID) == nil {
			t.Errorf("%s was DELETED by cleanup — must never happen", tc.name)
		}
	}
}

// ─── D. Mixed state ──────────────────────────────────────────────────────────

func TestEmailOutboxCleanup_MixedStateDeletesOnlyEligibleTerminal(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldSent1 := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour)
	oldSent2 := seedTerminal(repo, model.EmailOutboxStatusSent, 40*24*time.Hour)
	recentSent := seedTerminal(repo, model.EmailOutboxStatusSent, 2*time.Hour)
	oldDead1 := seedTerminal(repo, model.EmailOutboxStatusDead, day30+time.Hour)
	oldDead2 := seedTerminal(repo, model.EmailOutboxStatusDead, 90*24*time.Hour)
	recentDead := seedTerminal(repo, model.EmailOutboxStatusDead, 2*time.Hour)
	oldPending := seedTerminal(repo, model.EmailOutboxStatusPending, 120*24*time.Hour)
	oldProcessing := seedTerminal(repo, model.EmailOutboxStatusProcessing, 30*24*time.Hour)

	w := newCleanupWorker(repo, 100)
	logs := captureLogs(t, func() { w.tick(context.Background()) })

	if repo.get(oldSent1.ID) != nil || repo.get(oldSent2.ID) != nil {
		t.Error("old SENT rows retained, want deleted")
	}
	if repo.get(oldDead1.ID) != nil || repo.get(oldDead2.ID) != nil {
		t.Error("old DEAD rows retained, want deleted")
	}
	for _, keep := range []*model.EmailOutbox{recentSent, recentDead, oldPending, oldProcessing} {
		if repo.get(keep.ID) == nil {
			t.Errorf("row %s (%s) deleted, want retained", keep.ID, keep.Status)
		}
	}
	for _, want := range []string{`"sent_deleted":2`, `"dead_deleted":2`, `"total_deleted":4`} {
		if !strings.Contains(logs, want) {
			t.Errorf("run log missing %s\nlogs: %s", want, logs)
		}
	}
}

// ─── E. Batching ─────────────────────────────────────────────────────────────

// E.12 + E.13 + E.14 — every repository call is LIMIT-bounded, repeated
// batches drain the eligible rows, and the run stops on the first zero batch.
func TestEmailOutboxCleanup_BatchingDrainsToZero(t *testing.T) {
	base := newMockEmailOutboxRepo()
	for i := 0; i < 5; i++ {
		seedTerminal(base, model.EmailOutboxStatusSent, day7+time.Hour)
	}
	repo := &countingOutboxRepo{mockEmailOutboxRepo: base}
	w := NewEmailOutboxCleanupWorker(repo, time.Hour, 2, day7, day30)
	w.tick(context.Background())

	limits, deleted := repo.calls()
	if len(deleted) != 4 {
		t.Fatalf("repository calls = %d, want 4 (2,2,1,0)", len(deleted))
	}
	for i, l := range limits {
		if l != 2 {
			t.Errorf("call %d limit = %d, want batchSize 2 on every call", i, l)
		}
	}
	wantDeleted := []int{2, 2, 1, 0}
	for i, d := range deleted {
		if d != wantDeleted[i] {
			t.Errorf("call %d deleted = %d, want %d", i, d, wantDeleted[i])
		}
	}
	// E.13 — everything eligible is gone after the drain.
	for _, e := range base.rows {
		if e.Status == model.EmailOutboxStatusSent {
			t.Error("eligible SENT row survived the drain")
		}
	}
}

// §5 — one run cannot loop forever: the drain stops at
// cleanupMaxBatchesPerRun even when eligible rows remain (they wait for the
// next tick), so a pathological backlog can never monopolize the worker.
func TestEmailOutboxCleanup_RunStopsAtBatchBound(t *testing.T) {
	base := newMockEmailOutboxRepo()
	total := cleanupMaxBatchesPerRun + 5
	for i := 0; i < total; i++ {
		seedTerminal(base, model.EmailOutboxStatusSent, day7+time.Hour)
	}
	repo := &countingOutboxRepo{mockEmailOutboxRepo: base}
	w := NewEmailOutboxCleanupWorker(repo, time.Hour, 1, day7, day30)
	w.tick(context.Background())

	limits, deleted := repo.calls()
	if len(limits) != cleanupMaxBatchesPerRun {
		t.Errorf("repository calls = %d, want %d (run bound)", len(limits), cleanupMaxBatchesPerRun)
	}
	remaining := 0
	for _, e := range base.rows {
		if e.Status == model.EmailOutboxStatusSent {
			remaining++
		}
	}
	if remaining != 5 {
		t.Errorf("remaining eligible rows = %d, want 5 (deferred to next tick)", remaining)
	}
	sum := 0
	for _, d := range deleted {
		sum += d
	}
	if sum != cleanupMaxBatchesPerRun {
		t.Errorf("total deleted = %d, want %d", sum, cleanupMaxBatchesPerRun)
	}
}

// ─── F. Error handling ───────────────────────────────────────────────────────

// F.15 + H.22 — a repository failure is logged with operational metadata
// only; sensitive row content never reaches the log, rows stay untouched.
func TestEmailOutboxCleanup_RepoErrorLoggedSafely(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sensitive := seedAged(repo, model.EmailOutboxStatusSent, day7+time.Hour,
		"leak-target@confidential.example", "Team invitation", "RESET-TOKEN-8c3c-abcdef123456")
	repo.deleteErr = errors.New("connection refused")

	w := newCleanupWorker(repo, 100)
	logs := captureLogs(t, func() { w.tick(context.Background()) })

	if !strings.Contains(logs, "delete batch failed") || !strings.Contains(logs, "connection refused") {
		t.Errorf("error not logged\nlogs: %s", logs)
	}
	for _, secret := range []string{"leak-target", "confidential.example", "Team invitation", "RESET-TOKEN-8c3c"} {
		if strings.Contains(logs, secret) {
			t.Errorf("cleanup error log leaked %q\nlogs: %s", secret, logs)
		}
	}
	if repo.get(sensitive.ID) == nil {
		t.Error("row deleted despite repository error")
	}
}

// F.16 — one failed run must not kill the worker: the next tick succeeds.
func TestEmailOutboxCleanup_ErrorDoesNotKillWorker(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldSent := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour)
	repo.deleteErr = errors.New("transient db blip")

	w := newCleanupWorker(repo, 100)
	_ = captureLogs(t, func() { w.tick(context.Background()) }) // first run fails
	if repo.get(oldSent.ID) == nil {
		t.Fatal("row deleted despite repository error")
	}

	repo.clearAllHooks() // failure clears
	_ = captureLogs(t, func() { w.tick(context.Background()) })
	if repo.get(oldSent.ID) != nil {
		t.Error("worker did not recover after a failed run")
	}
}

// ─── G. Concurrency / lifecycle ──────────────────────────────────────────────

// G.17 — cleanup runs safely while the delivery worker is active: delivery
// drives its own due PENDING row to SENT, cleanup deletes only the old
// terminal row, and neither disturbs the other's states. Delivery's
// staleAfter is set far beyond the old PROCESSING row's age so its stale
// recovery deliberately does not reach it — cleanup must leave it alone
// regardless.
func TestEmailOutboxCleanup_RunsAlongsideDeliveryWorker(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	sender := NewMockEmailSender()
	d := NewEmailOutboxDispatcher(repo, sender, 8, 365*24*time.Hour, 20) // staleAfter ≫ row age
	delivery := NewEmailOutboxWorker(d, 10*time.Millisecond)
	cleanup := NewEmailOutboxCleanupWorker(repo, 10*time.Millisecond, 10, day7, day30)

	due := repo.seed(seedMerchantA, "due@example.com", "Invitation", "body", "") // claimable now
	oldSent := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour)
	oldProcessing := seedTerminal(repo, model.EmailOutboxStatusProcessing, 10*24*time.Hour)
	oldPending := seedTerminal(repo, model.EmailOutboxStatusPending, 90*24*time.Hour)
	oldPending.NextAttemptAt = time.Now().UTC().Add(time.Hour) // not due — delivery must ignore it

	ctx, cancel := context.WithCancel(context.Background())
	delivery.Start(ctx)
	cleanup.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for repo.get(oldSent.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for sender.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	delivery.Wait()
	cleanup.Wait()

	if repo.get(oldSent.ID) != nil {
		t.Error("cleanup did not delete old SENT while delivery was running")
	}
	if sender.Len() == 0 {
		t.Error("delivery worker never sent while cleanup was running")
	}
	if got := repo.get(due.ID); got == nil || got.Status != model.EmailOutboxStatusSent {
		t.Errorf("due row = %v, want survived + SENT", got)
	}
	if got := repo.get(oldProcessing.ID); got == nil || got.Status != model.EmailOutboxStatusProcessing {
		t.Errorf("old PROCESSING = %v, want untouched PROCESSING", got)
	}
	if got := repo.get(oldPending.ID); got == nil || got.Status != model.EmailOutboxStatusPending {
		t.Errorf("old non-due PENDING = %v, want untouched PENDING", got)
	}
}

// G.18 — cancellation stops the worker cleanly and nothing runs afterwards.
func TestEmailOutboxCleanup_ShutdownStopsWorker(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldSent := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour)
	w := NewEmailOutboxCleanupWorker(repo, 20*time.Millisecond, 10, day7, day30)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for repo.get(oldSent.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if repo.get(oldSent.ID) != nil {
		cancel()
		w.Wait()
		t.Fatal("immediate first run never deleted the eligible row")
	}

	cancel()
	stopped := make(chan struct{})
	go func() { w.Wait(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup worker did not stop after context cancellation")
	}

	// No further runs after Wait returns.
	after := seedTerminal(repo, model.EmailOutboxStatusSent, day7+time.Hour)
	time.Sleep(60 * time.Millisecond) // > 3 intervals
	if repo.get(after.ID) == nil {
		t.Error("cleanup worker kept deleting after shutdown")
	}
}

// G.19 — the first run happens immediately: with a 1h interval only the
// startup pass could have deleted the row.
func TestEmailOutboxCleanup_ImmediateFirstRun(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	oldDead := seedTerminal(repo, model.EmailOutboxStatusDead, day30+time.Hour)
	w := NewEmailOutboxCleanupWorker(repo, time.Hour, 10, day7, day30)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for repo.get(oldDead.ID) != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	present := repo.get(oldDead.ID) != nil
	cancel()
	w.Wait()
	if present {
		t.Error("row not deleted within 2s despite 1h interval — first run was not immediate")
	}
}

// G.20 — a disabled worker never starts: main.go gates Start() on
// cfg.EmailCleanup.Enabled; the default is asserted in
// internal/config/config_email_test.go (TestParseEmailCleanupConfig_Defaults).

// ─── H. Security / logging ──────────────────────────────────────────────────

// H.21 + H.22 — successful runs log counts/operational metadata only.
func TestEmailOutboxCleanup_LogsContainCountsOnly(t *testing.T) {
	repo := newMockEmailOutboxRepo()
	for i := 0; i < 2; i++ {
		seedAged(repo, model.EmailOutboxStatusSent, day7+time.Hour,
			"recipient-secret@example.com", "You are invited", "hunter2-never-log-me token=INVITE-TOKEN-9f8e7d")
	}
	seedAged(repo, model.EmailOutboxStatusDead, day30+time.Hour,
		"dead-recipient@confidential.example", "Delivery failed", "dead-body-secret")

	w := newCleanupWorker(repo, 100)
	logs := captureLogs(t, func() { w.tick(context.Background()) })

	for _, want := range []string{
		"email outbox cleanup completed",
		`"sent_deleted":2`, `"dead_deleted":1`, `"total_deleted":3`, `"batches":`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("cleanup log missing %s\nlogs: %s", want, logs)
		}
	}
	for _, secret := range []string{
		"recipient-secret@example.com", "dead-recipient@confidential.example",
		"You are invited", "hunter2-never-log-me", "INVITE-TOKEN-9f8e7d",
		"dead-body-secret",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("cleanup log leaked %q\nlogs: %s", secret, logs)
		}
	}
}
