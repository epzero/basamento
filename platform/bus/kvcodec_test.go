package bus

import (
	"math"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
)

func TestKVDeadlineArithmetic(t *testing.T) {
	clk := coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	ttl := 30 * time.Second
	rec := kvRecord{Value: []byte("ana"), Deadline: clk.Now().Add(ttl)}
	b, err := decodeKV(encodeKV(rec))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Deadline.Equal(rec.Deadline) {
		t.Fatalf("deadline %v, want %v", b.Deadline, rec.Deadline)
	}
	clk.Advance(29 * time.Second)
	if expired(clk, b) {
		t.Fatal("expected not expired before deadline")
	}
	clk.Advance(1 * time.Second)
	if !expired(clk, b) {
		t.Fatal("expected expired at deadline")
	}
}

func TestDeadlineFromTTLSaturatesHugeTTL(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	d := deadlineFromTTL(clk.Now(), time.Duration(math.MaxInt64))
	if !d.IsZero() {
		t.Fatalf("expected no expiry, got %v", d)
	}
	rec := kvRecord{Value: []byte("x"), Deadline: d}
	b := encodeKV(rec)
	got, err := decodeKV(b)
	if err != nil {
		t.Fatal(err)
	}
	if expired(clk, got) {
		t.Fatal("saturated ttl should not expire immediately")
	}
}
