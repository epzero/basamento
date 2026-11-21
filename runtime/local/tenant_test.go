package local

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestTenantIsolationAndListing(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	if err := f.rt.RegisterStep("fetch", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		tenant, _ := core.TenantFrom(ctx)
		return json.RawMessage(fmt.Sprintf(`{"tenant":%q}`, tenant)), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("work", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.flow("review", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})

	f.start(t, "acme", "shared-1", "work", `{}`)
	f.start(t, "globex", "shared-1", "work", `{}`)
	f.waitState(t, "acme", "shared-1", stateCompleted)
	f.waitState(t, "globex", "shared-1", stateCompleted)

	if acmeOut, globexOut := f.output(t, "acme", "shared-1"), f.output(t, "globex", "shared-1"); string(acmeOut) != `{"tenant":"acme"}` || string(globexOut) != `{"tenant":"globex"}` {
		t.Fatalf("outputs %s and %s, want each run's own tenant", acmeOut, globexOut)
	}

	f.start(t, "acme", "review-1", "review", `{}`)
	waitOn(t, "the review run to await its gate", atGate)
	if status, err := f.rt.Describe(tenantCtx("globex"), runtime.RunRef{RunID: "review-1"}); codeOf(err) != "not_found" {
		t.Fatalf("Describe cross-tenant = (%+v, %v), want not_found", status, err)
	}
	if err := f.rt.Cancel(tenantCtx("globex"), runtime.RunRef{RunID: "review-1"}, "x"); codeOf(err) != "not_found" {
		t.Fatalf("Cancel cross-tenant = %v, want not_found", err)
	}
	runs, _, err := f.rt.List(tenantCtx("globex"), runtime.RunQuery{Flow: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("globex lists %d review runs, want 0", len(runs))
	}
}
func TestTenantFieldMustAgreeWithContext(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("render", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("render", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})

	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:     runtime.FlowRef{Name: "render"},
		RunID:    "r1",
		TenantID: "globex",
	})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("mismatched Start = %v, want invalid_argument", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("step executed %d times, want 0", got)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}); codeOf(err) != "not_found" {
		t.Fatalf("acme describes r1: %v, want not_found", err)
	}
	if _, err := f.rt.Describe(tenantCtx("globex"), runtime.RunRef{RunID: "r1"}); codeOf(err) != "not_found" {
		t.Fatalf("globex describes r1: %v, want not_found", err)
	}

	ref, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "render"}, RunID: "r2"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ref.TenantID != "acme" {
		t.Fatalf("ref tenant %q, want acme", ref.TenantID)
	}
	f.waitState(t, "acme", "r2", stateCompleted)
	if _, err := f.rt.Describe(context.Background(), runtime.RunRef{RunID: "r2"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("Describe without tenant = %v, want invalid_argument", err)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r2", TenantID: "globex"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("Describe with a foreign ref tenant = %v, want invalid_argument", err)
	}
	if _, _, err := f.rt.List(context.Background(), runtime.RunQuery{}); codeOf(err) != "invalid_argument" {
		t.Fatalf("List without tenant = %v, want invalid_argument", err)
	}
}
