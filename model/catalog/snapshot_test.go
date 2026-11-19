package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestValidatePriceNegativeRateMessageIsStable(t *testing.T) {
	p := Price{Rules: []Rule{{Rates: Rates{
		"zebra": -1, "alpha": -2, "middle": -3,
	}}}}
	want := `model "sim-chat": rate "alpha" must not be negative`
	for i := 0; i < 20; i++ {
		err := validatePrice("sim-chat", p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("run %d: err=%v want substring %q", i, err, want)
		}
	}
}

func TestSnapshotLoaderRejectsNegativeRate(t *testing.T) {
	raw := []byte(`sim-chat:
  rules:
    - rates:
        input: -1
        output: 0
`)
	_, err := parseSnapshot(raw)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("parseSnapshot: code=%s err=%v, want invalid_argument", core.CodeOf(err), err)
	}
}

func TestSnapshotRejectsMisspelledRatesKey(t *testing.T) {
	raw := []byte(`sim-chat:
  rules:
    - rtes:
        input: 100
        output: 0
`)
	_, err := parseSnapshot(raw)
	if err == nil {
		t.Fatal("parseSnapshot: want error for misspelled rates key")
	}
}

func TestSnapshotRejectsUnknownFieldOnEntry(t *testing.T) {
	raw := []byte(`sim-chat:
  formulas: "usage.input_tokens"
  rules:
    - rates:
        input: 100
        output: 0
`)
	_, err := parseSnapshot(raw)
	if err == nil {
		t.Fatal("parseSnapshot: want error for unknown entry field")
	}
}

func TestEmptySnapshotLoads(t *testing.T) {
	got, err := parseSnapshot([]byte("{}"))
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0", len(got))
	}
}

func TestEmbeddedSnapshotLoadsViaNew(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if _, err := New(clock); err != nil {
		t.Fatalf("New: %v", err)
	}
}
