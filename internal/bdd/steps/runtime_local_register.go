package steps

import (
	"context"

	"github.com/cucumber/godog"
)

func registerLocal(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if value, ok := ctx.Value(worldKey{}).(*World); ok && value.Local != nil {
			value.Local.cleanup()
		}
		return ctx, nil
	})

	registerLocalCapabilities(sc)
	registerLocalPersistence(sc)
	registerLocalStepScript(sc)
	registerLocalTimers(sc)
	registerLocalRuns(sc)
	registerLocalGates(sc)
	registerLocalSelect(sc)
	registerLocalCancel(sc)
	registerLocalChildren(sc)
	registerLocalTenant(sc)
	registerLocalShutdown(sc)
	registerLocalStartIDs(sc)
	registerLocalChildIDs(sc)
	registerLocalClock(sc)
}
