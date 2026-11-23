package bdd

import (
	"testing"
)

// TestSuiteWiringUsesTestSuiteInitializer ensures newSuite registers steps once per suite.
func TestSuiteWiringUsesTestSuiteInitializer(t *testing.T) {
	suite := newSuite(t, "wiring-probe", []string{"../../specs/expr"}, "~@planned")
	if suite.ScenarioInitializer != nil {
		t.Fatalf("ScenarioInitializer is set: steps.Initialize would run before every scenario; " +
			"godog compiles each step pattern with regexp.MustCompile, so registering before every scenario costs patterns times scenarios")
	}
	if suite.TestSuiteInitializer == nil {
		t.Fatal("TestSuiteInitializer is nil: wire the suite with TestSuiteInitializer registerSteps in newSuite")
	}
}
