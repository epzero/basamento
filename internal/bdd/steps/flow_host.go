package steps

import (
	"github.com/cucumber/godog"
)

// registerFlowHost wires every host scenario step.
func registerFlowHost(sc *godog.ScenarioContext) {
	registerFlowHostSteps(sc)
	registerFlowHostDecisions(sc)
	registerFlowHostCost(sc)
}
