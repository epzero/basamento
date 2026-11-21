package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalGates(sc *godog.ScenarioContext) {
	// Gates.
	sc.Step(`^a run waiting at gate "([^"]*)" that accepts payloads where "([^"]*)"$`, func(ctx context.Context, gate, accept string) error {
		w := localStepWorld(ctx)
		w.setFlow("waiting", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			wait := dc.Await(runtime.GateSpec{Name: gate, AcceptIf: accept})
			e.update(func() {
				e.waiting = true
				e.atGate = gate
			})
			var payload json.RawMessage
			if err := wait.Get(&payload); err != nil {
				e.update(func() { e.waitErr = err })
				return nil, err
			}
			return payload, nil
		})
		ref, exec, err := w.start(ctx, "acme", "", "waiting")
		if err != nil {
			return err
		}
		w.current = ref
		w.currentExec = exec
		return exec.wait("the run to await the gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a flow waits at gate "([^"]*)" and then waits at gate "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := localStepWorld(ctx)
		w.setFlow("publish", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			approve := dc.Await(runtime.GateSpec{Name: first})
			e.update(func() {
				e.waiting = true
				e.atGate = first
			})
			if err := approve.Get(nil); err != nil {
				e.update(func() { e.waitErr = err })
				return nil, err
			}
			publish := dc.Await(runtime.GateSpec{Name: second})
			e.update(func() { e.atGate = second })
			var payload json.RawMessage
			if err := publish.Get(&payload); err != nil {
				e.update(func() { e.waitErr = err })
				return nil, err
			}
			return payload, nil
		})
		return nil
	})
	sc.Step(`^a run of "publish" resolved gate "([^"]*)" and received (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, "acme", "", "publish")
		if err != nil {
			return err
		}
		if err := exec.wait("the run to await "+gate, func() bool { return exec.atGate == gate }); err != nil {
			return err
		}
		if err := w.runtime(ctx).Resolve(w.ctx("acme"), ref, gate, json.RawMessage(payload)); err != nil {
			return err
		}
		if err := exec.wait("the run to await the next gate", func() bool { return exec.atGate != gate }); err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^the gate is resolved with payload (\{.*\})$`, func(ctx context.Context, payload string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		gate := exec.atGate
		exec.mu.Unlock()
		return w.runtime(ctx).Resolve(w.ctx(string(w.current.TenantID)), w.current, gate, json.RawMessage(payload))
	})
	sc.Step(`^the gate "([^"]*)" is resolved again with payload (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := localStepWorld(ctx)
		err := w.runtime(ctx).Resolve(w.ctx(string(w.current.TenantID)), w.current, gate, json.RawMessage(payload))
		w.noteError(err)
		return nil
	})
	sc.Step(`^the run is "running" at gate "([^"]*)"$`, func(ctx context.Context, gate string) error {
		w := localStepWorld(ctx)
		state, err := w.stateOf(ctx, "acme", w.current.RunID)
		if err != nil {
			return err
		}
		if state != "running" {
			return fmt.Errorf("the run is %q, want running", state)
		}
		exec := w.currentExec
		exec.mu.Lock()
		at := exec.atGate
		exec.mu.Unlock()
		if at != gate {
			return fmt.Errorf("the run waits at gate %q, want %q", at, gate)
		}
		return nil
	})

}
