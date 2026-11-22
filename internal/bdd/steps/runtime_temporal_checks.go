package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerTemporalChecks(sc *godog.ScenarioContext) {
	sc.Step(`^the run was continued as new at least once$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		if w.continuations < 1 {
			return fmt.Errorf("continued %d times", w.continuations)
		}
		if w.continueFromExec < 0 {
			return fmt.Errorf("continue-as-new happened but no execution index was recorded")
		}
		return nil
	})
	sc.Step(`^the run was continued as new (\d+) times?$`, func(ctx context.Context, want int) error {
		w := temporalW(ctx)
		if w.continuations != want {
			return fmt.Errorf("continued %d times, want %d", w.continuations, want)
		}
		if want == 0 && w.continueFromExec >= 0 {
			return fmt.Errorf("continued 0 times but continue-as-new execution %d was recorded", w.continueFromExec)
		}
		if want > 0 && w.continueFromExec < 0 {
			return fmt.Errorf("continued %d times but no continue-as-new execution was recorded", want)
		}
		return nil
	})
	sc.Step(`^the run's durable events are (.+)$`, func(ctx context.Context, want string) error {
		events := temporalW(ctx).bddOutbox().eventsInSeqOrder()
		got := make([]string, len(events))
		for i, ev := range events {
			got[i] = ev.Type
		}
		types := eventsFromFlowLiteral(want)
		if len(got) != len(types) {
			return fmt.Errorf("events %v, want %v", got, types)
		}
		for i := range types {
			if got[i] != types[i] {
				return fmt.Errorf("events %v, want %v", got, types)
			}
		}
		return nil
	})
	sc.Step(`^steps "([^"]*)" to "([^"]*)" were executed once each$`, func(ctx context.Context, from, to string) error {
		w := temporalW(ctx)
		names, err := stepRange(from, to)
		if err != nil {
			return err
		}
		for _, name := range names {
			if got := w.stepCalls(name); got != 1 {
				return fmt.Errorf("step %q ran %d times", name, got)
			}
		}
		return nil
	})
	sc.Step(`^steps "([^"]*)" to "([^"]*)", "([^"]*)" and "([^"]*)" were executed once each$`,
		func(ctx context.Context, from, to, a, b string) error {
			w := temporalW(ctx)
			names, err := stepRange(from, to)
			if err != nil {
				return err
			}
			names = append(names, a, b)
			for _, name := range names {
				if got := w.stepCalls(name); got != 1 {
					return fmt.Errorf("step %q ran %d times", name, got)
				}
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" completed before the continue-as-new$`, func(ctx context.Context, name string) error {
		w := temporalW(ctx)
		if w.stepCalls(name) != 1 {
			return fmt.Errorf("step %q ran %d times", name, w.stepCalls(name))
		}
		if w.continueFromExec < 0 {
			return fmt.Errorf("no continue-as-new execution recorded")
		}
		if w.stepBDoneExec != w.continueFromExec {
			return fmt.Errorf("step %q completed in execution %d, continue-as-new in execution %d", name, w.stepBDoneExec, w.continueFromExec)
		}
		return nil
	})
}
