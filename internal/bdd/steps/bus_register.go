package steps

import (
	"context"

	"github.com/cucumber/godog"
)

func registerBus(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, scn *godog.Scenario) (context.Context, error) {
		if !scenarioUsesBus(scn) {
			return ctx, nil
		}
		w := world(ctx)
		if w.Bus == nil {
			w.Bus = newBusWorld()
		} else {
			w.Bus.teardown()
			w.Bus = newBusWorld()
		}
		if err := skipBusByProfile(scn, w.Bus.backend); err != nil {
			return ctx, err
		}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Bus; w != nil {
			w.teardown()
		}
		return ctx, nil
	})
	registerBusPublish(sc)
	registerBusKV(sc)
}
