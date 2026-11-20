package steps

import "github.com/cucumber/godog"

// registerFlowHostSteps wires host scenario steps split across flow_host_steps_*.go files.
func registerFlowHostSteps(sc *godog.ScenarioContext) {
	registerFlowHostStepBasics(sc)
	registerFlowHostStepRuntime(sc)
}
