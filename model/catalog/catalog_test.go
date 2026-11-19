package catalog_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
)

func newCatalog(t *testing.T) (*catalog.Catalog, *coretest.FakeClock) {
	t.Helper()
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	c, err := catalog.New(clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, clock
}

func mustAdd(t *testing.T, c *catalog.Catalog, d model.Descriptor) {
	t.Helper()
	if err := c.AddDescriptor(d); err != nil {
		t.Fatalf("AddDescriptor: %v", err)
	}
}

func mustSetSnapshot(t *testing.T, c *catalog.Catalog, modelID string, p catalog.Price) {
	t.Helper()
	if err := c.SetSnapshotPrice(modelID, p); err != nil {
		t.Fatalf("SetSnapshotPrice: %v", err)
	}
}

func mustSetDeployment(t *testing.T, c *catalog.Catalog, modelID string, p catalog.Price) {
	t.Helper()
	if err := c.SetDeploymentPrice(modelID, p); err != nil {
		t.Fatalf("SetDeploymentPrice: %v", err)
	}
}

func chatDescriptor(id string) model.Descriptor {
	return model.Descriptor{
		ID:       id,
		Provider: "sim",
		Caps:     []model.Capability{model.CapChat},
		Inputs: []model.InputSlot{
			{Name: "messages", Media: "messages", Required: true},
			{Name: "prompt", Media: "text"},
		},
		Protocol: model.ProtocolStream,
		Locality: model.LocalityCloud,
	}
}

func TestLookupReturnsDescriptor(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))

	got, err := c.Lookup(context.Background(), "sim-chat")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got.Caps) != 1 || got.Caps[0] != model.CapChat {
		t.Fatalf("capability = %v, want [chat]", got.Caps)
	}
	if len(got.Inputs) != 2 || got.Inputs[0].Name != "messages" || got.Inputs[1].Name != "prompt" {
		t.Fatalf("input slots = %v, want messages then prompt", got.Inputs)
	}
	if got.Protocol != model.ProtocolStream || got.Locality != model.LocalityCloud {
		t.Fatalf("protocol/locality = %v/%v", got.Protocol, got.Locality)
	}
}

func TestLookupUnknownModelIsNotFound(t *testing.T) {
	c, _ := newCatalog(t)
	_, err := c.Lookup(context.Background(), "no-such-model")
	if got := core.CodeOf(err); got != core.CodeNotFound {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeNotFound, err)
	}
}

func TestPriceLayersPrecedence(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8"})

	req := catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500}

	snapshot, err := c.Estimate(context.Background(), req)
	if err != nil {
		t.Fatalf("Estimate snapshot: %v", err)
	}
	if snapshot != 5000 {
		t.Fatalf("snapshot estimate = %d, want 5000", snapshot)
	}

	mustSetDeployment(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 2 + usage.output_tokens * 8"})
	if err := c.SetTenantPrice("acme", "sim-chat", catalog.Price{Formula: "usage.input_tokens * 3 + usage.output_tokens * 8"}); err != nil {
		t.Fatalf("SetTenantPrice: %v", err)
	}

	deployment, err := c.Estimate(context.Background(), req)
	if err != nil {
		t.Fatalf("Estimate deployment: %v", err)
	}
	if deployment != 6000 {
		t.Fatalf("deployment estimate = %d, want 6000", deployment)
	}

	acme, err := c.Estimate(core.WithTenant(context.Background(), "acme"), req)
	if err != nil {
		t.Fatalf("Estimate tenant: %v", err)
	}
	if acme != 7000 {
		t.Fatalf("tenant estimate = %d, want 7000", acme)
	}
}

func TestTenantOverrideIsIsolated(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8"})
	if err := c.SetTenantPrice("acme", "sim-chat", catalog.Price{Formula: "usage.input_tokens * 3 + usage.output_tokens * 8"}); err != nil {
		t.Fatalf("SetTenantPrice: %v", err)
	}
	req := catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500}

	globex, err := c.Estimate(core.WithTenant(context.Background(), "globex"), req)
	if err != nil {
		t.Fatalf("Estimate globex: %v", err)
	}
	if globex != 5000 {
		t.Fatalf("globex estimate = %d, want 5000", globex)
	}

	noTenant, err := c.Estimate(context.Background(), req)
	if err != nil {
		t.Fatalf("Estimate without tenant: %v", err)
	}
	if noTenant != 5000 {
		t.Fatalf("tenantless estimate = %d, want 5000", noTenant)
	}
}

func TestFirstHoldingRateRuleWins(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "at.getHours('UTC') >= 17", Rates: catalog.Rates{"input": 140, "output": 550}},
		{When: "usage.input_tokens > 200000", Rates: catalog.Rates{"input": 560, "output": 2200}},
		{Rates: catalog.Rates{"input": 280, "output": 1100}},
	}})

	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 250000, OutputLimit: 20000})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if q.Rule != "usage.input_tokens > 200000" {
		t.Fatalf("rule = %q, want long-context rule", q.Rule)
	}
	charge, err := c.Settle(q, model.Usage{InputTokens: 250000, OutputTokens: 20000})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if charge.MilliPoints != 184 || charge.Rule != "usage.input_tokens > 200000" {
		t.Fatalf("charge = %+v, want 184 on long-context rule", charge)
	}

	clock2 := coretest.NewFakeClock(time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC))
	c2, err := catalog.New(clock2)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, c2, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c2, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "at.getHours('UTC') >= 17", Rates: catalog.Rates{"input": 140, "output": 550}},
		{When: "usage.input_tokens > 200000", Rates: catalog.Rates{"input": 560, "output": 2200}},
		{Rates: catalog.Rates{"input": 280, "output": 1100}},
	}})
	q2, err := c2.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 250000, OutputLimit: 20000})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	charge2, err := c2.Settle(q2, model.Usage{InputTokens: 250000, OutputTokens: 20000})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if charge2.MilliPoints != 46 || charge2.Rule != "at.getHours('UTC') >= 17" {
		t.Fatalf("charge = %+v, want 46 on off-peak rule", charge2)
	}
}

func TestRateRuleSeesTenant(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: `tenant == "acme"`, Rates: catalog.Rates{"input": 1000, "output": 0}},
		{Rates: catalog.Rates{"input": 5000, "output": 0}},
	}})
	req := catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 0}

	acme, err := c.Estimate(core.WithTenant(context.Background(), "acme"), req)
	if err != nil {
		t.Fatalf("Estimate acme: %v", err)
	}
	if acme != 1 {
		t.Fatalf("acme estimate = %d, want 1", acme)
	}
	globex, err := c.Estimate(core.WithTenant(context.Background(), "globex"), req)
	if err != nil {
		t.Fatalf("Estimate globex: %v", err)
	}
	if globex != 5 {
		t.Fatalf("globex estimate = %d, want 5", globex)
	}
}

func TestFormulaDoesNotBypassRuleSelection(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{
		Formula: "usage.input_tokens * 1",
		Rules: []catalog.Rule{
			{When: "usage.input_tokens > 1000000", Rates: catalog.Rates{"input": 560, "output": 2200}},
		},
	})
	_, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if got := core.CodeOf(err); got != core.CodeUnsupported {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeUnsupported, err)
	}
}

func TestOverrideRulesDoNotFallThrough(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8"})
	if err := c.SetTenantPrice("acme", "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "usage.input_tokens > 1000000", Rates: catalog.Rates{"input": 560, "output": 2200}},
	}}); err != nil {
		t.Fatalf("SetTenantPrice: %v", err)
	}
	_, err := c.Estimate(core.WithTenant(context.Background(), "acme"), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if got := core.CodeOf(err); got != core.CodeUnsupported {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeUnsupported, err)
	}
}

func TestNoPriceEntryIsUnsupported(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-noprice"))
	_, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-noprice", InputTokens: 1000, OutputLimit: 500})
	if got := core.CodeOf(err); got != core.CodeUnsupported {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeUnsupported, err)
	}
}

func TestDefaultTokenFormula(t *testing.T) {
	tests := []struct {
		name  string
		rates catalog.Rates
		usage model.Usage
		want  int64
	}{
		{
			name:  "per-million input and output",
			rates: catalog.Rates{"input": 250, "output": 10000},
			usage: model.Usage{InputTokens: 4000, OutputTokens: 300},
			want:  4,
		},
		{
			name:  "cached rate applies once",
			rates: catalog.Rates{"input": 2500, "output": 10000, "cached": 1250},
			usage: model.Usage{InputTokens: 4000, OutputTokens: 1000, CachedTokens: 1000, ReasoningTokens: 100},
			want:  19,
		},
		{
			name:  "no cached rate prices cached tokens at input",
			rates: catalog.Rates{"input": 2000, "output": 10000},
			usage: model.Usage{InputTokens: 6000, OutputTokens: 1200, CachedTokens: 4000, ReasoningTokens: 300},
			want:  24,
		},
		{
			name:  "zero cached rate is free",
			rates: catalog.Rates{"input": 3000, "output": 10000, "cached": 0},
			usage: model.Usage{InputTokens: 5000, OutputTokens: 400, CachedTokens: 2000},
			want:  13,
		},
		{
			name:  "reasoning is inside output",
			rates: catalog.Rates{"input": 4000, "output": 10000},
			usage: model.Usage{InputTokens: 3000, OutputTokens: 600, ReasoningTokens: 300},
			want:  18,
		},
		{
			name:  "an exact half rounds up",
			rates: catalog.Rates{"input": 250, "output": 0},
			usage: model.Usage{InputTokens: 2000},
			want:  1,
		},
		{
			name:  "under a half rounds down",
			rates: catalog.Rates{"input": 250, "output": 0},
			usage: model.Usage{InputTokens: 1999},
			want:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newCatalog(t)
			mustAdd(t, c, chatDescriptor("sim-chat"))
			mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: tt.rates}}})
			q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: tt.usage.InputTokens, OutputLimit: tt.usage.OutputTokens})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			charge, err := c.Settle(q, tt.usage)
			if err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if charge.MilliPoints != tt.want {
				t.Fatalf("charge = %d, want %d", charge.MilliPoints, tt.want)
			}
		})
	}
}

func TestDivideRoundedNamesModelOnOverflow(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	// Rates large enough that multiply exceeds int64 after rounding.
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{
		"input":  9223372036854775807,
		"output": 0,
	}}}})
	_, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 2_000_000, OutputLimit: 0})
	if err == nil {
		t.Fatal("want error for out-of-range amount")
	}
	if !strings.Contains(err.Error(), `sim-chat`) {
		t.Fatalf("error=%v, want model id in message", err)
	}
}

func TestSettleRejectsQuoteNotFromStart(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8"})
	usage := model.Usage{InputTokens: 1000, OutputTokens: 500}
	_, err := c.Settle(catalog.Quote{Model: "sim-chat"}, usage)
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("synthetic quote: code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}

	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped catalog.Quote
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	_, err = c.Settle(roundTripped, usage)
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("round-tripped quote: code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}

	charge, err := c.Settle(q, usage)
	if err != nil {
		t.Fatalf("Settle from Start: %v", err)
	}
	if charge.MilliPoints != 5000 {
		t.Fatalf("charge = %d, want 5000", charge.MilliPoints)
	}
}

func TestMissingInputRateRefusedAtUse(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"output": 10000}}}})
	_, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 3_000_000, OutputLimit: 0})
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}
	if err != nil && !strings.Contains(err.Error(), "input") {
		t.Fatalf("error = %v, want input rate named", err)
	}
}

func TestInputOnlyRatePricesWhenOutputIsZero(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 1000}}}})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1_000_000, OutputLimit: 0})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	charge, err := c.Settle(q, model.Usage{InputTokens: 1_000_000, OutputTokens: 0})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if charge.MilliPoints != 1000 {
		t.Fatalf("charge = %d, want 1000", charge.MilliPoints)
	}
}

func TestFormulaOnlyUnaffectedByMissingRates(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 10"})
	got, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 7, OutputLimit: 0})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if got != 70 {
		t.Fatalf("estimate = %d, want 70", got)
	}
}

func TestUsageValidatedBeforeRuleSelectionOnSettle(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: `usage.input_tokens > 1000000`, Rates: catalog.Rates{"input": 560, "output": 2200}},
	}})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 2_000_000, OutputLimit: 100})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = c.Settle(q, model.Usage{InputTokens: 1000, OutputTokens: 100, CachedTokens: 2000})
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}
}

func TestUsageValidatedBeforeRuleSelectionOnStart(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{Rates: catalog.Rates{"input": 1000, "output": 0}},
	}})
	_, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: -1, OutputLimit: 0})
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}
}

func TestMoreCachedThanInputFails(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 4000, "output": 10000}}}})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 3000, OutputLimit: 400})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = c.Settle(q, model.Usage{InputTokens: 3000, OutputTokens: 400, CachedTokens: 3500})
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}
}

func TestUsageCheckRunsBeforeOwnFormula(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1"})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 10})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = c.Settle(q, model.Usage{InputTokens: 1000, OutputTokens: 10, CachedTokens: 2000})
	if got := core.CodeOf(err); got != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
	}
}

func TestEstimateUsesParamsAndOutputLimit(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8 + (params.tier == 'priority' ? 100 : 0)"})

	got, err := c.Estimate(context.Background(), catalog.Request{
		Model:       "sim-chat",
		Params:      json.RawMessage(`{"tier":"priority"}`),
		InputTokens: 1000,
		OutputLimit: 500,
	})
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}
	if got != 5100 {
		t.Fatalf("estimate = %d, want 5100", got)
	}
}

func TestDescriptorFormulaUsesChosenRates(t *testing.T) {
	c, _ := newCatalog(t)
	d := chatDescriptor("sim-video")
	d.Caps = []model.Capability{model.CapVideoGenerate}
	d.Price = "usage.output_tokens * rates.output"
	mustAdd(t, c, d)
	mustSetSnapshot(t, c, "sim-video", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 10, "output": 200}}}})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-video", InputTokens: 0, OutputLimit: 10})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	charge, err := c.Settle(q, model.Usage{OutputTokens: 10})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if charge.MilliPoints != 2000 {
		t.Fatalf("charge = %d, want 2000", charge.MilliPoints)
	}
}

func TestPriceChangeAfterStartDoesNotReprice(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 1 + usage.output_tokens * 8"})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "usage.input_tokens * 9 + usage.output_tokens * 8"})

	charge, err := c.Settle(q, model.Usage{InputTokens: 1000, OutputTokens: 200})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if q.Estimate != 5000 {
		t.Fatalf("estimate = %d, want 5000", q.Estimate)
	}
	if charge.MilliPoints != 2600 {
		t.Fatalf("charge = %d, want 2600", charge.MilliPoints)
	}
}

type manualClock struct{ at time.Time }

func (c *manualClock) Now() time.Time { return c.at }

func TestSettlementUsesStartTime(t *testing.T) {
	clock := &manualClock{at: time.Date(2026, 10, 1, 16, 59, 0, 0, time.UTC)}
	c, err := catalog.New(clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "at.getHours('UTC') >= 17", Rates: catalog.Rates{"input": 140, "output": 550}},
		{Rates: catalog.Rates{"input": 280, "output": 1100}},
	}})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	clock.at = time.Date(2026, 10, 1, 17, 5, 0, 0, time.UTC)
	charge, err := c.Settle(q, model.Usage{InputTokens: 250000, OutputTokens: 20000})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if q.Estimate != 1 {
		t.Fatalf("estimate = %d, want 1", q.Estimate)
	}
	if charge.MilliPoints != 92 {
		t.Fatalf("charge = %d, want 92", charge.MilliPoints)
	}
}

func TestNegativeRateRefusedAtSetNotStored(t *testing.T) {
	good := catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 1000, "output": 0}}}}
	bad := catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": -1000, "output": 0}}}}
	req := catalog.Request{Model: "sim-chat", InputTokens: 1_000_000, OutputLimit: 0}

	t.Run("snapshot", func(t *testing.T) {
		c, _ := newCatalog(t)
		mustAdd(t, c, chatDescriptor("sim-chat"))
		mustSetSnapshot(t, c, "sim-chat", good)
		if err := c.SetSnapshotPrice("sim-chat", bad); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("SetSnapshotPrice bad: code=%s err=%v", core.CodeOf(err), err)
		}
		got, err := c.Estimate(context.Background(), req)
		if err != nil || got != 1000 {
			t.Fatalf("Estimate after rejected set = %d, %v; want 1000, nil", got, err)
		}
	})

	t.Run("deployment", func(t *testing.T) {
		c, _ := newCatalog(t)
		mustAdd(t, c, chatDescriptor("sim-chat"))
		mustSetSnapshot(t, c, "sim-chat", good)
		mustSetDeployment(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 2000, "output": 0}}}})
		if err := c.SetDeploymentPrice("sim-chat", bad); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("SetDeploymentPrice bad: code=%s err=%v", core.CodeOf(err), err)
		}
		got, err := c.Estimate(context.Background(), req)
		if err != nil || got != 2000 {
			t.Fatalf("Estimate after rejected set = %d, %v; want 2000, nil", got, err)
		}
	})

	t.Run("tenant", func(t *testing.T) {
		c, _ := newCatalog(t)
		mustAdd(t, c, chatDescriptor("sim-chat"))
		mustSetSnapshot(t, c, "sim-chat", good)
		if err := c.SetTenantPrice("acme", "sim-chat", catalog.Price{Rules: []catalog.Rule{{Rates: catalog.Rates{"input": 3000, "output": 0}}}}); err != nil {
			t.Fatalf("SetTenantPrice good: %v", err)
		}
		if err := c.SetTenantPrice("acme", "sim-chat", bad); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("SetTenantPrice bad: code=%s err=%v", core.CodeOf(err), err)
		}
		got, err := c.Estimate(core.WithTenant(context.Background(), "acme"), req)
		if err != nil || got != 3000 {
			t.Fatalf("Estimate after rejected set = %d, %v; want 3000, nil", got, err)
		}
	})
}

func TestNegativeUsageAndAmountRefused(t *testing.T) {
	tests := []struct {
		name  string
		price catalog.Price
		start catalog.Request
		usage model.Usage
	}{
		{
			name:  "negative input tokens",
			price: catalog.Price{Formula: "usage.input_tokens * 1"},
			start: catalog.Request{Model: "sim-chat", InputTokens: 100, OutputLimit: 0},
			usage: model.Usage{InputTokens: -1},
		},
		{
			name:  "negative output tokens offset charge",
			price: catalog.Price{Formula: "usage.input_tokens * 10"},
			start: catalog.Request{Model: "sim-chat", InputTokens: 100, OutputLimit: 0},
			usage: model.Usage{InputTokens: 100, OutputTokens: -100},
		},
		{
			name:  "negative formula result",
			price: catalog.Price{Formula: "-usage.input_tokens"},
			start: catalog.Request{Model: "sim-chat", InputTokens: 100, OutputLimit: 0},
			usage: model.Usage{InputTokens: 100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newCatalog(t)
			mustAdd(t, c, chatDescriptor("sim-chat"))
			mustSetSnapshot(t, c, "sim-chat", tt.price)
			q, err := c.Start(context.Background(), tt.start)
			if tt.name == "negative formula result" {
				if got := core.CodeOf(err); got != core.CodeInvalidArgument {
					t.Fatalf("Start code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			_, err = c.Settle(q, tt.usage)
			if got := core.CodeOf(err); got != core.CodeInvalidArgument {
				t.Fatalf("code = %s, want %s (%v)", got, core.CodeInvalidArgument, err)
			}
		})
	}
}

func TestZeroAmountAccepted(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Formula: "0"})
	q, err := c.Start(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1000, OutputLimit: 500})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	charge, err := c.Settle(q, model.Usage{InputTokens: 1000, OutputTokens: 500})
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if charge.MilliPoints != 0 {
		t.Fatalf("charge = %d, want 0", charge.MilliPoints)
	}
}

func TestBadWhenExpressionRefusedAtSet(t *testing.T) {
	c, _ := newCatalog(t)
	err := c.SetSnapshotPrice("sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "usage.input_tokens >", Rates: catalog.Rates{"input": 1000, "output": 0}},
	}})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("SetSnapshotPrice: code=%s err=%v, want invalid_argument", core.CodeOf(err), err)
	}
}

func TestFailingRateConditionFailsPricingNotFallback(t *testing.T) {
	c, _ := newCatalog(t)
	mustAdd(t, c, chatDescriptor("sim-chat"))
	mustSetSnapshot(t, c, "sim-chat", catalog.Price{Rules: []catalog.Rule{
		{When: "1/0 == 1", Rates: catalog.Rates{"input": 9000, "output": 0}},
		{Rates: catalog.Rates{"input": 1000, "output": 0}},
	}})
	_, err := c.Estimate(context.Background(), catalog.Request{Model: "sim-chat", InputTokens: 1_000_000, OutputLimit: 0})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code=%s err=%v, want invalid_argument", core.CodeOf(err), err)
	}
}

func TestFormulaAmountMustBeExactInteger(t *testing.T) {
	tests := []struct {
		name    string
		formula string
		input   int64
		want    int64
		refuse  bool
	}{
		{
			name:    "double above exact range",
			formula: "double(9007199254740993)",
			refuse:  true,
		},
		{
			name:    "decimal multiplication refused",
			formula: "usage.input_tokens * 0.145",
			input:   100,
			refuse:  true,
		},
		{
			name:    "integral double arithmetic",
			formula: "double(5) * 2.0",
			want:    10,
		},
		{
			name:    "integer formula unchanged",
			formula: "usage.input_tokens * 10",
			input:   7,
			want:    70,
		},
		{
			name:    "uint too large",
			formula: "uint(9223372036854775808)",
			refuse:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newCatalog(t)
			mustAdd(t, c, chatDescriptor("sim-chat"))
			if err := c.SetSnapshotPrice("sim-chat", catalog.Price{Formula: tt.formula}); err != nil {
				if tt.refuse {
					if core.CodeOf(err) != core.CodeInvalidArgument {
						t.Fatalf("SetSnapshotPrice: code=%s err=%v, want invalid_argument", core.CodeOf(err), err)
					}
					return
				}
				t.Fatalf("SetSnapshotPrice: %v", err)
			}
			got, err := c.Estimate(context.Background(), catalog.Request{
				Model: "sim-chat", InputTokens: tt.input, OutputLimit: 0,
			})
			if tt.refuse {
				if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
					t.Fatalf("code=%s err=%v, want invalid_argument", core.CodeOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Estimate: %v", err)
			}
			if got != tt.want {
				t.Fatalf("estimate=%d want=%d", got, tt.want)
			}
		})
	}
}
