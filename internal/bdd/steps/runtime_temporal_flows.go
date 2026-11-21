package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerTemporalFlows(sc *godog.ScenarioContext) {
	sc.Step(`^the checkpoint event threshold is (\d+)$`, func(ctx context.Context, n int) error {
		w := temporalW(ctx)
		if w.rt != nil {
			return fmt.Errorf("runtime already started")
		}
		w.checkpointThreshold = n
		return nil
	})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)" and checkpoints after each, then returns "([^"]*)"$`,
		func(ctx context.Context, count int, from, to, literal string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(names...); err != nil {
				return err
			}
			return w.addFlow("steps", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
					if err := w.checkpoint(dc); err != nil {
						return nil, err
					}
				}
				return json.RawMessage(`"` + literal + `"`), nil
			})
		})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then waits at gate "([^"]*)"$`,
		func(ctx context.Context, count int, from, to, gate string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(names...); err != nil {
				return err
			}
			return w.addFlow("short", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				if err := w.checkpoint(dc); err != nil {
					return nil, err
				}
				var approved json.RawMessage
				if err := dc.Await(runtime.GateSpec{Name: gate}).Get(&approved); err != nil {
					return nil, err
				}
				return approved, nil
			})
		})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then returns "([^"]*)"$`,
		func(ctx context.Context, count int, from, to, literal string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(names...); err != nil {
				return err
			}
			return w.addFlow("steps", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				if err := w.checkpoint(dc); err != nil {
					return nil, err
				}
				return json.RawMessage(`"` + literal + `"`), nil
			})
		})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, then waits at gate "([^"]*)" and returns (\{.*\})$`,
		func(ctx context.Context, count int, from, to, gate, literal string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(names...); err != nil {
				return err
			}
			return w.addFlow("long", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				if err := w.checkpoint(dc); err != nil {
					return nil, err
				}
				if err := dc.Await(runtime.GateSpec{Name: gate}).Get(nil); err != nil {
					return nil, err
				}
				return json.RawMessage(literal), nil
			})
		})
	sc.Step(`^a flow emits (.+), calls (\d+) steps "([^"]*)" to "([^"]*)", checkpoints, calls (\d+) steps "([^"]*)" to "([^"]*)", then returns "([^"]*)"$`,
		func(ctx context.Context, events string, count int, from, to string, count2 int, tFrom, tTo, literal string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			want2, err := stepRangeCount(tFrom, tTo)
			if err != nil {
				return err
			}
			if count2 != want2 {
				return fmt.Errorf("tail step count %d, want %d from range", count2, want2)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			tail, err := stepRange(tFrom, tTo)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(append(names, tail...)...); err != nil {
				return err
			}
			types := eventsFromFlowLiteral(events)
			return w.addFlow("emit", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, typ := range types {
					if err := dc.Emit(runtime.ProgressEvent{Type: typ}); err != nil {
						return nil, err
					}
				}
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				if err := w.checkpoint(dc); err != nil {
					return nil, err
				}
				for _, name := range tail {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				return json.RawMessage(`"` + literal + `"`), nil
			})
		})
	sc.Step(`^a flow calls (\d+) steps "([^"]*)" to "([^"]*)", starts steps "([^"]*)" and "([^"]*)" in parallel, calls checkpoint while "([^"]*)" is in flight, then returns "([^"]*)"$`,
		func(ctx context.Context, count int, from, to, a, b, inflight, literal string) error {
			want, err := stepRangeCount(from, to)
			if err != nil {
				return err
			}
			if count != want {
				return fmt.Errorf("step count %d, want %d from range", count, want)
			}
			if inflight != b {
				return fmt.Errorf("inflight step %q, want %q", inflight, b)
			}
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			if err := w.ensureSteps(append(names, a, b)...); err != nil {
				return err
			}
			w.mu.Lock()
			w.flowHarness["parallel"] = flowHarnessMeta{slowStep: b, orderStep: b}
			w.mu.Unlock()
			return w.addFlow("parallel", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range names {
					if err := dc.Step(name, nil, runtime.StepOptions{}).Get(nil); err != nil {
						return nil, err
					}
				}
				fa := dc.Step(a, nil, runtime.StepOptions{})
				fb := dc.Step(b, nil, runtime.StepOptions{})
				if err := w.checkpoint(dc); err != nil {
					return nil, err
				}
				if err := fb.Get(nil); err != nil {
					return nil, err
				}
				if err := fa.Get(nil); err != nil {
					return nil, err
				}
				return json.RawMessage(`"` + literal + `"`), nil
			})
		})
}
