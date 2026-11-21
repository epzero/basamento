package steps

import (
	"context"

	"github.com/cucumber/godog"
)

// registerFlowHost wires every host scenario step.
func registerFlowHost(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Host = newHostWorld()
		return ctx, nil
	})
	registerFlowHostSteps(sc)
	registerFlowHostDecisions(sc)
	registerFlowHostCost(sc)
}
