package local

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestGateResolutionAndConflict(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		return payload, nil
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)

	if err := f.rt.Resolve(tenantCtx("globex"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":true}`)); codeOf(err) != "not_found" {
		t.Fatalf("cross-tenant Resolve = %v, want not_found", err)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":false}`)); codeOf(err) != "conflict" {
		t.Fatalf("second Resolve = %v, want conflict", err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	if out := f.output(t, "acme", "r1"); string(out) != `{"approved":true}` {
		t.Fatalf("output %s, want {\"approved\":true}", out)
	}
}

func TestGateTimeoutHelperStopsAfterEarlyResolve(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	release := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve", Timeout: time.Hour})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		<-release
		return payload, nil
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	if got := target.gateTimeouts.Load(); got != 1 {
		t.Fatalf("gate timeout helpers = %d, want 1", got)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	waitGateTimeouts(t, target, 0)
	close(release)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.clock.Advance(time.Hour)
	if got := f.state(t, "acme", "r1"); got != stateCompleted {
		t.Fatalf("state after 1h advance = %s, want completed", got)
	}
	if out := f.output(t, "acme", "r1"); string(out) != `{"ok":true}` {
		t.Fatalf("output %s, want resolve payload after clock advance", out)
	}
}

func TestResolveUnknownGateIsNotFound(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		return nil, wait.Get(nil)
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "other", json.RawMessage(`{}`))
	if codeOf(err) != "not_found" {
		t.Fatalf("Resolve unknown gate = %v, want not_found", err)
	}
	if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "test"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCancelled)
}

func TestAwaitReturnsTheGateTheJournalAlreadyHolds(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		close(atGate)
		return nil, dc.Await(runtime.GateSpec{Name: "approve"}).Get(nil)
	})
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	if err := f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)

	// A replayed wait reads the recorded payload instead of waiting again.
	replayed := f.rt.lookup("acme", "r1")
	if replayed == nil {
		t.Fatal("run not found")
	}
	future := (&durableContext{run: replayed}).Await(runtime.GateSpec{Name: "approve"})
	var payload json.RawMessage
	if err := future.Get(&payload); err != nil {
		t.Fatalf("replayed Await: %v", err)
	}
	if string(payload) != `{"approved":true}` {
		t.Fatalf("replayed payload %s, want {\"approved\":true}", payload)
	}
}

func TestConcurrentGateResolutionHasOneWinner(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		return payload, nil
	})
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)

	const resolvers = 8
	errs := make(chan error, resolvers)
	var wg sync.WaitGroup
	for i := 0; i < resolvers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))
		}(i)
	}
	wg.Wait()
	close(errs)
	winners, conflicts := 0, 0
	for err := range errs {
		switch codeOf(err) {
		case "":
			winners++
		case "conflict":
			conflicts++
		default:
			t.Fatalf("Resolve returned %v, want nil or conflict", err)
		}
	}
	if winners != 1 || conflicts != resolvers-1 {
		t.Fatalf("winners %d, conflicts %d; want 1 and %d", winners, conflicts, resolvers-1)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

