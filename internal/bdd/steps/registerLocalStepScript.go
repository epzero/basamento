package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerLocalStepScript(sc *godog.ScenarioContext) {
	// Scripted step behavior.
	sc.Step(`^step "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).defaultResult = json.RawMessage(result)
		return nil
	})
	sc.Step(`^step "([^"]*)" returns (\{.*\}) for tenant "([^"]*)" and (\{.*\}) for tenant "([^"]*)"$`,
		func(ctx context.Context, name, first, firstTenant, second, secondTenant string) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			w.behavior(name).tenantResults = map[string]json.RawMessage{
				firstTenant:  json.RawMessage(first),
				secondTenant: json.RawMessage(second),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" returns (\{.*\}) on execution (\d+) and (\{.*\}) on execution (\d+)$`,
		func(ctx context.Context, name, first string, firstN int, second string, secondN int) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			w.behavior(name).results = map[int]json.RawMessage{
				firstN:  json.RawMessage(first),
				secondN: json.RawMessage(second),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false)$`,
		func(ctx context.Context, name, code, retryable string) error {
			return localStepError(ctx, name, code, retryable, 0)
		})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" on execution (\d+) and with code "([^"]*)" on execution (\d+), each marked retryable (true|false)$`,
		func(ctx context.Context, name, first string, firstN int, second string, secondN int, retryable string) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			behavior := w.behavior(name)
			behavior.errors = map[int]*core.Error{
				firstN:  localPlatformError(first, retryable),
				secondN: localPlatformError(second, retryable),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false) on execution (\d+) and returns (\{.*\}) on execution (\d+)$`,
		func(ctx context.Context, name, code, retryable string, at int, result string, atResult int) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			behavior := w.behavior(name)
			behavior.errors = map[int]*core.Error{at: localPlatformError(code, retryable)}
			behavior.results = map[int]json.RawMessage{atResult: json.RawMessage(result)}
			return nil
		})
	sc.Step(`^the step lists "([^"]*)" as non-retryable$`, func(ctx context.Context, code string) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		w.stepOptions["charge"] = runtime.StepOptions{NonRetryable: []string{code}}
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^the step allows (\d+) attempts$`, func(ctx context.Context, attempts int) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		options := w.stepOptions["charge"]
		options.MaxAttempts = attempts
		w.stepOptions["charge"] = options
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^the step leaves its attempt limit at 0$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		options := w.stepOptions["charge"]
		options.MaxAttempts = 0
		w.stepOptions["charge"] = options
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^step "([^"]*)" was executed (\d+) times?$`, func(ctx context.Context, name string, want int) error {
		w := localStepWorld(ctx)
		if got := w.executionsOf(name); got != want {
			return fmt.Errorf("step %q was executed %d times, want %d", name, got, want)
		}
		return nil
	})
	sc.Step(`^the step fails with code "([^"]*)"$`, localStepFailed)
	sc.Step(`^the step fails with the error of its last attempt with code "([^"]*)"$`, localStepFailed)
	sc.Step(`^no retry waited for the clock to advance$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if now := world(ctx).Core.clock.Now(); !now.Equal(w.clockBefore) {
			return fmt.Errorf("the clock advanced from %s to %s during retries", w.clockBefore, now)
		}
		return nil
	})

	// Flows over steps.
	sc.Step(`^a flow calls step "([^"]*)" once and returns its result$`, func(ctx context.Context, name string) error {
		localStepWorld(ctx).setFlow("once", localStepWorld(ctx).stepFlow(name))
		return nil
	})
	sc.Step(`^a flow calls step "([^"]*)" twice with the same name and returns both results$`, func(ctx context.Context, name string) error {
		localStepWorld(ctx).setFlow("twice", localStepWorld(ctx).twiceFlow(name))
		return nil
	})
	sc.Step(`^a flow calls step "([^"]*)" that returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).defaultResult = json.RawMessage(result)
		w.setFlow(name, w.stepFlow(name))
		return nil
	})
	sc.Step(`^a flow runs step "([^"]*)" that blocks until cancelled$`, func(ctx context.Context, name string) error {
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).block = true
		w.setFlow(name, w.blockingStepFlow(name))
		return nil
	})
	sc.Step(`^a flow starts steps "([^"]*)" and "([^"]*)" and waits on the first to finish, in that order$`,
		func(ctx context.Context, first, second string) error {
			w := localStepWorld(ctx)
			w.setFlow("select-steps", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range []string{first, second} {
					if err := w.ensureStep(dc.Context(), name); err != nil {
						return nil, err
					}
				}
				var released chan struct{}
				w.mu.Lock()
				ordered := w.stepWaits[first] == second
				if ordered {
					released = make(chan struct{})
					w.stepFutureDone[second] = released
				}
				w.mu.Unlock()
				one := dc.Step(first, nil, w.options(first))
				two := dc.Step(second, nil, w.options(second))
				if released != nil {
					go func() {
						var out json.RawMessage
						_ = two.Get(&out)
						close(released)
					}()
				}
				index, err := dc.Select(one, two)
				if err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
				})
				return json.RawMessage(`{}`), nil
			})
			return nil
		})
	sc.Step(`^step "([^"]*)" finishes only after step "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		w.stepWaits[first] = second
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^a flow starts steps "([^"]*)", "([^"]*)" and "([^"]*)" and waits on all three$`,
		func(ctx context.Context, first, second, third string) error {
			w := localStepWorld(ctx)
			w.setFlow("fanout", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				var futures []runtime.Future
				for _, name := range []string{first, second, third} {
					if err := w.ensureStep(dc.Context(), name); err != nil {
						return nil, err
					}
					futures = append(futures, dc.Step(name, nil, w.options(name)))
				}
				for range futures {
					if _, err := dc.Select(futures...); err != nil {
						return nil, err
					}
				}
				return json.RawMessage(`{}`), nil
			})
			return nil
		})
	sc.Step(`^each of steps "([^"]*)", "([^"]*)" and "([^"]*)" waits until all (\d+) steps are executing$`,
		func(ctx context.Context, first, second, third string, want int) error {
			w := localStepWorld(ctx)
			barrier := newLocalBarrier(want)
			for _, name := range []string{first, second, third} {
				if err := w.ensureStep(ctx, name); err != nil {
					return err
				}
				w.behavior(name).barrier = barrier
			}
			return nil
		})
	sc.Step(`^(\d+) steps executed concurrently$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		peak := w.peakSteps
		w.mu.Unlock()
		if peak != want {
			return fmt.Errorf("peak concurrent steps = %d, want %d", peak, want)
		}
		return nil
	})

}
