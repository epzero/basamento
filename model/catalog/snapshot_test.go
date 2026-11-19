package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
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

func TestBareEmptySnapshotLoads(t *testing.T) {
	got, err := parseSnapshot(nil)
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0", len(got))
	}
	got, err = parseSnapshot([]byte{})
	if err != nil {
		t.Fatalf("parseSnapshot empty bytes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0", len(got))
	}
}

func TestCommentOnlySnapshotLoads(t *testing.T) {
	raw := []byte("# no prices yet\n# still empty\n")
	got, err := parseSnapshot(raw)
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0", len(got))
	}
}

func TestSnapshotBlockScalarFormulaPrices(t *testing.T) {
	raw := []byte("sim-chat:\n  formula: |\n    usage.input_tokens * 2\n")
	prices, err := parseSnapshot(raw)
	if err != nil {
		t.Fatalf("parseSnapshot: %v", err)
	}
	p, ok := prices["sim-chat"]
	if !ok {
		t.Fatal("want sim-chat entry")
	}
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	c, err := New(clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	desc := model.Descriptor{
		ID: "sim-chat", Provider: "sim",
		Caps:     []model.Capability{model.CapChat},
		Protocol: model.ProtocolStream, Locality: model.LocalityCloud,
	}
	if err := c.AddDescriptor(desc); err != nil {
		t.Fatalf("AddDescriptor: %v", err)
	}
	if err := c.SetSnapshotPrice("sim-chat", p); err != nil {
		t.Fatalf("SetSnapshotPrice: %v", err)
	}
	got, err := c.Estimate(context.Background(), Request{
		Model: "sim-chat", InputTokens: 1000, OutputLimit: 0,
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if got != 2000 {
		t.Fatalf("estimate = %d, want 2000", got)
	}
}

func TestParseSnapshotNegativeRateCarriesDetails(t *testing.T) {
	raw := []byte(`sim-chat:
  rules:
    - rates:
        cached: -1
`)
	_, err := parseSnapshot(raw)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code=%s err=%v", core.CodeOf(err), err)
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("not core.Error: %v", err)
	}
	if pe.Details["model"] != "sim-chat" || pe.Details["rate"] != "cached" {
		t.Fatalf("details=%v", pe.Details)
	}
}

func TestEmbeddedSnapshotLoadsViaNew(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if _, err := New(clock); err != nil {
		t.Fatalf("New: %v", err)
	}
}
