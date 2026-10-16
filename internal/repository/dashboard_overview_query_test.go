package repository

import (
	"testing"
	"time"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
)

// Phase 10J: the overview "recent in period" list must carry the selected
// half-open window on created_at — same predicates as the aggregates.
func TestBuildListQuery_OverviewPeriodUsesHalfOpenCreatedAt(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	q, args := buildListQuery(false, uuid.New(), model.TransactionListFilter{
		Page: 1, Limit: 10, CreatedFrom: &from, CreatedTo: &to,
	})
	for _, want := range []string{"created_at >= $", "created_at < $"} {
		found := false
		for _, frag := range []string{want} {
			if contains(q, frag) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing half-open predicate %q in: %s", want, q)
		}
	}
	if len(args) != 5 || args[1] != from || args[2] != to {
		t.Fatalf("range must be bound as parameters in order, got %#v", args)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
