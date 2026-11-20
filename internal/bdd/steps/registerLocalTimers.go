package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerLocalTimers(sc *godog.ScenarioContext) {
	// Timers.
	sc.Step(`^a flow waits on a "([^"]*)" timer and then completes$`, func(ctx context.Context, text string) error {
		d, err := localDuration(text)
		if err != nil {
			return err
		}
		localStepWorld(ctx).setFlow("timer", localStepWorld(ctx).timerFlow(d))
		return nil
	})
	sc.Step(`^a run of "([^"]*)" starts and (\d+) (minute|minutes|hour|hours) pass$`,
		func(ctx context.Context, flow string, count int, unit string) error {
			w := localStepWorld(ctx)
			_, exec, err := w.start(ctx, "acme", "", flow)
			if err != nil {
				return err
			}
			if err := exec.wait("the run to wait on its timer", func() bool { return exec.waiting }); err != nil {
				return err
			}
			world(ctx).Core.clock.Advance(localUnit(count, unit))
			return nil
		})
	sc.Step(`^the run has completed (\d+) timers?$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		got := exec.timers
		exec.mu.Unlock()
		if got != want {
			return fmt.Errorf("the run completed %d timers, want %d", got, want)
		}
		return nil
	})

}
