package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime/local"
)

// clockNowOnly implements core.Clock but cannot schedule timers (RT-054-01).
type clockNowOnly struct {
	now time.Time
}

func (c clockNowOnly) Now() time.Time { return c.now }

func registerLocalClock(sc *godog.ScenarioContext) {
	sc.Step(`^a clock that is not a timer clock$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		w.constructionClock = clockNowOnly{now: world(ctx).Core.clock.Now()}
		return nil
	})
	sc.Step(`^the local runtime is constructed with that clock$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if w.constructionClock == nil {
			return fmt.Errorf("no clock was stored for construction")
		}
		_, err := local.New(local.Options{Clock: w.constructionClock})
		w.lastErr = err
		return nil
	})
	sc.Step(`^construction fails with an error$`, func(ctx context.Context) error {
		if localStepWorld(ctx).lastErr == nil {
			return fmt.Errorf("construction succeeded, want an error")
		}
		return nil
	})
	sc.Step(`^the error says the clock cannot schedule timers$`, func(ctx context.Context) error {
		err := localStepWorld(ctx).lastErr
		if err == nil {
			return fmt.Errorf("construction succeeded, want an error")
		}
		var platform *core.Error
		if !errors.As(err, &platform) {
			return fmt.Errorf("construction error %T, want *core.Error", err)
		}
		if !strings.Contains(platform.Message, "cannot schedule timers") {
			return fmt.Errorf("error message %q, want substring %q", platform.Message, "cannot schedule timers")
		}
		return nil
	})
}
