package middleware

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

func priceFixture(u model.Usage) int64 {
	return (u.InputTokens*1 + u.OutputTokens*8) / 1000
}

func TestMetersEveryAttemptThatReachedTheProvider(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	meter := &meterCollector{}
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		switch a.Number {
		case 1:
			return model.Result{Usage: &model.Usage{InputTokens: 1000}},
				coreErr(core.CodeVendor, true, 0)
		default:
			emit(model.Event{Kind: model.EventText, Text: "Hello"})
			return succeeded("Hello", &model.Usage{InputTokens: 2000, OutputTokens: 500}), nil
		}
	}
	cfg := baseConfig(rt, p)
	cfg.Meter = meter
	cfg.Price = priceFixture
	if _, err := Call(context.Background(), cfg); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(meter.entries) != 2 {
		t.Fatalf("metered %d attempts, want 2", len(meter.entries))
	}
	if meter.entries[0].Usage.InputTokens != 1000 || meter.entries[0].CostMpts != 1 {
		t.Fatalf("attempt 1 meter = %+v, want 1000 input and 1 milli-point", meter.entries[0])
	}
	if meter.entries[1].Usage.InputTokens != 2000 || meter.entries[1].Usage.OutputTokens != 500 || meter.entries[1].CostMpts != 6 {
		t.Fatalf("attempt 2 meter = %+v, want 2000 input, 500 output and 6 milli-points", meter.entries[1])
	}
	in, out, _ := meter.total()
	if in != 3000 || out != 500 {
		t.Fatalf("metered total = %d/%d, want 3000/500", in, out)
	}
}

func TestAttemptWithoutUsageCostsNothing(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	meter := &meterCollector{}
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			return model.Result{}, coreErr(core.CodeTimeout, true, 0)
		}
		return succeeded("Hello", &model.Usage{InputTokens: 2000, OutputTokens: 500}), nil
	}
	cfg := baseConfig(rt, p)
	cfg.Meter = meter
	cfg.Price = priceFixture
	if _, err := Call(context.Background(), cfg); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if meter.entries[0].Usage != (model.Usage{}) || meter.entries[0].CostMpts != 0 {
		t.Fatalf("attempt 1 meter = %+v, want zero", meter.entries[0])
	}
	if meter.entries[1].CostMpts != 6 {
		t.Fatalf("attempt 2 cost = %d, want 6", meter.entries[1].CostMpts)
	}
}

func TestCallLogHasOneEntryPerAttempt(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	calls := &logCollector{}
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			return model.Result{}, coreErr(core.CodeVendor, true, 0)
		}
		return succeeded("Hi", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.ClientRef = "call-log-1"
	cfg.CallLog = calls
	if _, err := Call(context.Background(), cfg); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(calls.entries) != 2 {
		t.Fatalf("call log = %d entries, want 2", len(calls.entries))
	}
	first, second := calls.entries[0], calls.entries[1]
	if first.ClientRef != "call-log-1" {
		t.Fatalf("entry 1 ref = %q, want call-log-1", first.ClientRef)
	}
	wantRef := structured.ClientRef("call-log-1", 2)
	if second.ClientRef != wantRef || second.ClientRef == "call-log-1" {
		t.Fatalf("entry 2 ref = %q, want %q", second.ClientRef, wantRef)
	}
	for _, e := range calls.entries {
		if e.Model != "sim-chat" || e.Rung != "none" || e.Start.IsZero() || e.State == "" {
			t.Fatalf("incomplete entry: %+v", e)
		}
	}
}

func TestCallLogNamesOnlyTheCredentialReference(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	calls := &logCollector{}
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		emit(model.Event{Kind: model.EventText, Text: "my secret note"})
		return succeeded("my secret note", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.CredentialRef = "cred://openai-main"
	cfg.Request.Messages = json.RawMessage(`[{"role":"user","content":"transfer 100 to bob"}]`)
	cfg.CallLog = calls
	if _, err := Call(context.Background(), cfg); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(calls.entries) != 1 {
		t.Fatalf("call log = %d entries, want 1", len(calls.entries))
	}
	encoded, err := json.Marshal(calls.entries[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"sk-live-42", "my secret note", "transfer 100 to bob"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("call log leaks %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "cred://openai-main") {
		t.Fatalf("call log does not name the credential: %s", text)
	}
}

func TestUnacknowledgedStartReusesTheAttemptRef(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	var refs []string
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		refs = append(refs, a.ClientRef)
		if a.Number == 1 {
			return model.Result{}, &StartError{Err: coreErr(core.CodeUnavailable, true, 0)}
		}
		return succeeded("Hi", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.ClientRef = "call-1"
	if _, err := Call(context.Background(), cfg); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(refs) != 2 || refs[0] != "call-1" || refs[1] != "call-1" {
		t.Fatalf("start refs = %v, want [call-1 call-1]", refs)
	}
}

func TestFallbackIsNotTriggeredWithoutAFailure(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(context.Context, *scriptedProvider, Attempt, func(model.Event)) (model.Result, error) {
		return succeeded("Hi", nil), nil
	}
	triggered := false
	cfg := baseConfig(rt, p)
	cfg.Fallback = func(error) bool { triggered = true; return true }
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if triggered || res.FallbackTriggered {
		t.Fatal("fallback ran on a successful call")
	}
}
