package sqltest

import "testing"

func TestGateStrict(t *testing.T) {
	t.Setenv("GATE_STRICT", "")
	if GateStrict() {
		t.Fatal("empty")
	}
	t.Setenv("GATE_STRICT", "1")
	if !GateStrict() {
		t.Fatal("1")
	}
}

func TestSkipRunsAccounting(t *testing.T) {
	ResetSkipCounts()
	RecordSkipRun(SkipPostgresUnavailable)
	RecordSkipRun(SkipPostgresUnavailable)
	if SkipTotal() != 2 {
		t.Fatalf("total %d", SkipTotal())
	}
	s := SortedSkipSummary()
	if s != "(postgres-unavailable: 2)" {
		t.Fatalf("summary %q", s)
	}
}
