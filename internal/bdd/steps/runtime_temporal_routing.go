package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
)

// registerTemporalLocalRunSteps owns phrases shared between local and temporal
// runtime scenarios; godog allows one regex per text.
func registerTemporalLocalRunSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the run is "([^"]*)"$`, func(ctx context.Context, want string) error {
		if w := world(ctx).Temporal; w != nil && w.rt != nil {
			if got := w.runState(); got != want {
				return fmt.Errorf("run state %q, want %q", got, want)
			}
			return nil
		}
		return localRunState(ctx, want)
	})
	sc.Step(`^the run returns (.+)$`, func(ctx context.Context, want string) error {
		if w := world(ctx).Temporal; w != nil && w.rt != nil {
			w.waitDone()
			if !jsonEquivalent(w.lastOutput, temporalValue(want)) {
				return fmt.Errorf("run returns %s, want %s", w.lastOutput, want)
			}
			return nil
		}
		w := localStepWorld(ctx)
		if w.current.RunID == "" {
			return fmt.Errorf("no run has started")
		}
		if err := w.waitRunState(ctx, w.current, "completed"); err != nil {
			return err
		}
		got := w.output(w.current)
		wantRaw := temporalValue(want)
		if !jsonEquivalent(got, wantRaw) {
			return fmt.Errorf("the run returns %s, want %s", got, want)
		}
		return nil
	})
	sc.Step(`^the step allows (\d+) attempts$`, func(ctx context.Context, attempts int) error {
		if w := world(ctx).Temporal; w != nil {
			w.attempts = attempts
			return nil
		}
		w := localStepWorld(ctx)
		w.mu.Lock()
		options := w.stepOptions["charge"]
		options.MaxAttempts = attempts
		w.stepOptions["charge"] = options
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false)$`,
		func(ctx context.Context, name, code, retryable string) error {
			if world(ctx).Temporal != nil {
				return temporalStepRetryable(ctx, name, code, retryable, 0)
			}
			return localStepError(ctx, name, code, retryable, 0)
		})
	sc.Step(`^step "([^"]*)" was executed (\d+) times?$`, func(ctx context.Context, name string, want int) error {
		if w := world(ctx).Temporal; w != nil && w.rt != nil {
			if got := w.stepCalls(name); got != want {
				return fmt.Errorf("step %q ran %d times, want %d", name, got, want)
			}
			return nil
		}
		w := localStepWorld(ctx)
		if got := w.executionsOf(name); got != want {
			return fmt.Errorf("step %q was executed %d times, want %d", name, got, want)
		}
		return nil
	})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		w := temporalW(ctx)
		if w.rt == nil {
			return fmt.Errorf("step %q failure is only on the temporal runtime", name)
		}
		return temporalStepFailure(w, code)
	})
}

func temporalStepRetryable(ctx context.Context, name, code, retryable string, times int) error {
	w := temporalW(ctx)
	if err := w.ensureStep(name); err != nil {
		return err
	}
	w.mu.Lock()
	b := w.behaviors[name]
	b.code = core.Code(code)
	b.retryable = retryable == "true"
	if times == 0 {
		b.failFirst = -1
	} else {
		b.failFirst = times
	}
	w.mu.Unlock()
	return nil
}
