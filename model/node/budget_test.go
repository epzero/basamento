package node

import (
	"context"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestMemoryReservesWithinBudget(t *testing.T) {
	t.Parallel()
	b := NewMemory(5000)
	if err := b.Reserve("ref-1", 500); err != nil {
		t.Fatal(err)
	}
	if got := b.Held("ref-1"); got != 500 {
		t.Fatalf("held = %d, want 500", got)
	}
	if got := b.HeldTotal(); got != 500 {
		t.Fatalf("held total = %d, want 500", got)
	}
	if b.Settled("ref-1") {
		t.Fatal("reservation is already settled")
	}
}

func TestMemoryRefusesOverBudget(t *testing.T) {
	t.Parallel()
	b := NewMemory(300)
	err := b.Reserve("ref-1", 500)
	if err == nil {
		t.Fatal("reservation succeeded, want budget_exceeded")
	}
	if got := core.CodeOf(err); got != core.CodeBudgetExceeded {
		t.Fatalf("code = %s, want budget_exceeded", got)
	}
	if b.HeldTotal() != 0 {
		t.Fatal("a refused reservation was held")
	}
}

func TestMemoryReserveIsIdempotentPerKey(t *testing.T) {
	t.Parallel()
	b := NewMemory(1000)
	if err := b.Reserve("ref-1", 500); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve("ref-1", 400); err != nil {
		t.Fatal(err)
	}
	if got := b.Reserved("ref-1"); got != 500 {
		t.Fatalf("reserved = %d, want the first amount 500", got)
	}
	if got := b.HeldTotal(); got != 500 {
		t.Fatalf("held total = %d, want 500", got)
	}
}

func TestMemorySettleChargesActualAndReleasesTheRest(t *testing.T) {
	t.Parallel()
	b := NewMemory(5000)
	if err := b.Reserve("ref-1", 500); err != nil {
		t.Fatal(err)
	}
	if err := b.Settle("ref-1", 480); err != nil {
		t.Fatal(err)
	}
	if got := b.Charged("ref-1"); got != 480 {
		t.Fatalf("charged = %d, want 480", got)
	}
	if got := b.Held("ref-1"); got != 0 {
		t.Fatalf("held = %d, want 0 after settlement", got)
	}
	if !b.Settled("ref-1") {
		t.Fatal("reservation is still open")
	}
	if b.HeldTotal() != 0 {
		t.Fatal("a settled reservation is still held")
	}
}

func TestMemoryFirstSettlementWins(t *testing.T) {
	t.Parallel()
	b := NewMemory(5000)
	if err := b.Reserve("ref-1", 500); err != nil {
		t.Fatal(err)
	}
	if err := b.Settle("ref-1", 200); err != nil {
		t.Fatal(err)
	}
	if err := b.Settle("ref-1", 500); err != nil {
		t.Fatal(err)
	}
	if got := b.Charged("ref-1"); got != 200 {
		t.Fatalf("charged = %d, want the first settlement 200", got)
	}
	if got := b.ChargedTotal(); got != 200 {
		t.Fatalf("charged total = %d, want 200", got)
	}
}

func TestMemorySettleWithoutReservationIsNoOp(t *testing.T) {
	t.Parallel()
	b := NewMemory(5000)
	if err := b.Settle("missing", 100); err != nil {
		t.Fatal(err)
	}
	if got := b.ChargedTotal(); got != 0 {
		t.Fatalf("charged total = %d, want 0", got)
	}
}

func TestMemoryRejectsNegativeAmounts(t *testing.T) {
	t.Parallel()
	b := NewMemory(5000)
	if err := b.Reserve("ref-1", -1); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("negative reservation: %v", err)
	}
}

func TestMemorySettledChargesCountAgainstTheLimit(t *testing.T) {
	t.Parallel()
	b := NewMemory(1000)
	if err := b.Reserve("ref-1", 600); err != nil {
		t.Fatal(err)
	}
	if err := b.Settle("ref-1", 400); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve("ref-2", 500); err != nil {
		t.Fatalf("a released reservation must not free its charge: %v", err)
	}
	if got := b.HeldTotal(); got != 500 {
		t.Fatalf("held = %d, want 500", got)
	}
	err := b.Reserve("ref-3", 200)
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded", err)
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Details["remaining"] != int64(100) {
		t.Fatalf("remaining = %v, want 100", err)
	}
	if got := b.HeldTotal(); got != 500 {
		t.Fatalf("held = %d after a refused reservation, want 500", got)
	}
	if got := b.ChargedTotal(); got != 400 {
		t.Fatalf("charged = %d, want 400", got)
	}
}

func TestMemoryUnlimited(t *testing.T) {
	t.Parallel()
	b := NewMemory(-1)
	if err := b.Reserve("ref-1", 1<<40); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetRoundTripsThroughContext(t *testing.T) {
	t.Parallel()
	b := NewMemory(10)
	ctx := WithBudget(context.Background(), b)
	got, ok := BudgetFrom(ctx)
	if !ok || got != b {
		t.Fatalf("BudgetFrom = %v, %v", got, ok)
	}
	if _, ok := BudgetFrom(context.Background()); ok {
		t.Fatal("plain context reports a budget")
	}
}

func TestRemainingGoesNegativeAfterASettlementOverrunsTheCeiling(t *testing.T) {
	t.Parallel()
	b := NewMemory(10)
	if _, bounded := b.Remaining(); !bounded {
		t.Fatal("a finite budget is unbounded")
	}
	if err := b.Reserve("k", 1); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := b.Remaining(); remaining != 9 {
		t.Fatalf("remaining = %d, want 9", remaining)
	}
	if err := b.Settle("k", 20); err != nil {
		t.Fatal(err)
	}
	remaining, bounded := b.Remaining()
	if !bounded || remaining != -10 {
		t.Fatalf("remaining = %d bounded %v, want -10", remaining, bounded)
	}
	if _, bounded := NewMemory(-1).Remaining(); bounded {
		t.Fatal("an unlimited budget is bounded")
	}
}
