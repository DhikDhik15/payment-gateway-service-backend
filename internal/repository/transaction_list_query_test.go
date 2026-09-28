package repository

import (
	"strings"
	"testing"

	"github.com/dhikaarta/pay-gate-backend/internal/model"
	"github.com/google/uuid"
)

func TestBuildListQuery_Page1OffsetZero(t *testing.T) {
	mid := uuid.New()
	q, args := buildListQuery(false, mid, model.TransactionListFilter{Page: 1, Limit: 10})
	if !strings.Contains(q, "WHERE merchant_id = $1") {
		t.Fatalf("missing merchant filter: %s", q)
	}
	if !strings.Contains(q, "LIMIT $2 OFFSET $3") {
		t.Fatalf("unexpected limit/offset positions: %s", q)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[1] != 10 || args[2] != 0 {
		t.Fatalf("page=1 limit=10 → LIMIT 10 OFFSET 0, got limit=%v offset=%v", args[1], args[2])
	}
}

func TestBuildListQuery_Page2OffsetTen(t *testing.T) {
	mid := uuid.New()
	_, args := buildListQuery(false, mid, model.TransactionListFilter{Page: 2, Limit: 10})
	if args[1] != 10 || args[2] != 10 {
		t.Fatalf("page=2 limit=10 → LIMIT 10 OFFSET 10, got limit=%v offset=%v", args[1], args[2])
	}
}

func TestBuildListQuery_SearchDoesNotUseBarePercentWildcardAlone(t *testing.T) {
	mid := uuid.New()
	search := "ORDER-001"
	q, args := buildListQuery(false, mid, model.TransactionListFilter{Page: 1, Limit: 10, Search: &search})
	if !strings.Contains(q, "merchant_order_id ILIKE") {
		t.Fatalf("expected search ILIKE clause: %s", q)
	}
	// Search value is bound as a parameter; wildcards are SQL literals around $N.
	found := false
	for _, a := range args {
		if s, ok := a.(string); ok && s == "ORDER-001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("search term not bound as arg: %#v", args)
	}
}

func TestBuildListQuery_NoJoin(t *testing.T) {
	mid := uuid.New()
	q, _ := buildListQuery(false, mid, model.TransactionListFilter{Page: 1, Limit: 10})
	upper := strings.ToUpper(q)
	if strings.Contains(upper, " JOIN ") {
		t.Fatalf("list query must not JOIN (would drop txs without attempts): %s", q)
	}
	if !strings.Contains(upper, "FROM TRANSACTIONS") {
		t.Fatalf("expected FROM transactions: %s", q)
	}
}
