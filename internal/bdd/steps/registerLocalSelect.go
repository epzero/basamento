package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalSelect(sc *godog.ScenarioContext) {
	// Select.
	sc.Step(`^a flow is waiting on the first of a "([^"]*)" timer and gate "([^"]*)", in that order$`,
		func(ctx context.Context, text, gate string) error {
			w := localStepWorld(ctx)
			d, err := localDuration(text)
			if err != nil {
				return err
			}
			w.setFlow("select", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				timer := dc.Sleep(d)
				wait := dc.Await(runtime.GateSpec{Name: gate})
				e.update(func() {
					e.waiting = true
					e.atGate = gate
				})
				index, err := dc.Select(timer, wait)
				if err != nil {
					return nil, err
				}
				var payload json.RawMessage
				chosen := []runtime.Future{timer, wait}[index]
				if err := chosen.Get(&payload); err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
					e.selectPayload = payload
				})
				return payload, nil
			})
			_, exec, err := w.start(ctx, "acme", "", "select")
			if err != nil {
				return err
			}
			return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
		})
	sc.Step(`^a flow is waiting on the first of a "([^"]*)" timer and a "([^"]*)" timer, in that order$`,
		func(ctx context.Context, first, second string) error {
			w := localStepWorld(ctx)
			one, err := localDuration(first)
			if err != nil {
				return err
			}
			two, err := localDuration(second)
			if err != nil {
				return err
			}
			w.setFlow("select", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				firstTimer := dc.Sleep(one)
				secondTimer := dc.Sleep(two)
				e.update(func() { e.waiting = true })
				index, err := dc.Select(firstTimer, secondTimer)
				if err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
				})
				return json.RawMessage(`{}`), nil
			})
			_, exec, err := w.start(ctx, "acme", "", "select")
			if err != nil {
				return err
			}
			return exec.wait("the run to wait on its timers", func() bool { return exec.waiting })
		})
	sc.Step(`^the run answers its pending wait on the first ready operation$`, func(ctx context.Context) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})
	sc.Step(`^(\d+) hours? pass and the run answers its pending wait on the first ready operation$`, func(ctx context.Context, count int) error {
		w := localStepWorld(ctx)
		world(ctx).Core.clock.Advance(time.Duration(count) * time.Hour)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})
	sc.Step(`^the wait returns index (\d+)$`, func(ctx context.Context, want int) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		got := exec.selectIndex
		exec.mu.Unlock()
		if got != want {
			return fmt.Errorf("the wait returned index %d, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^the result at index (\d+) is (\{.*\})$`, func(ctx context.Context, index int, want string) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		payload := exec.selectPayload
		got := exec.selectIndex
		exec.mu.Unlock()
		if got != index {
			return fmt.Errorf("the wait returned index %d, want %d", got, index)
		}
		if !jsonEquivalent(payload, json.RawMessage(want)) {
			return fmt.Errorf("the result is %s, want %s", payload, want)
		}
		return nil
	})
	sc.Step(`^the wait answers$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if len(w.execsOf("select-steps")) == 0 {
			if _, _, err := w.start(ctx, "acme", "", "select-steps"); err != nil {
				return err
			}
		}
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})

}
