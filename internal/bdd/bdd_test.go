package bdd

import (
	"os"
	"testing"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/steps"
)

// defaultTags excludes scenarios that are not part of the deterministic gate.
// @planned: approved but not implemented yet; @live/@eval/@journey/@slow: nightly lanes;
// @quarantine: temporarily excluded with an issue and an expiry date (linted).
const defaultTags = "~@planned && ~@live && ~@eval && ~@journey && ~@slow && ~@quarantine"

// TestFeatures runs every scenario under specs/ in strict mode: undefined or
// pending steps fail. BDD_TAGS narrows the run (used by `gate redgreen`).
// BDD_BUS_BACKEND selects the bus implementation: fake (default), embedded, or cloud.
func TestFeatures(t *testing.T) {
	backends := []string{"fake", "embedded", "cloud"}
	if one := os.Getenv("BDD_BUS_BACKEND"); one != "" {
		backends = []string{one}
	}
	for _, backend := range backends {
		backend := backend
		t.Run("bus-backend="+backend, func(t *testing.T) {
			t.Setenv("BDD_BUS_BACKEND", backend)
			tags := defaultTags
			if extra := os.Getenv("BDD_TAGS"); extra != "" {
				tags = extra
			}
			suite := godog.TestSuite{
				Name:                "basamento",
				ScenarioInitializer: steps.Initialize,
				Options: &godog.Options{
					Format:   "pretty",
					Paths:    []string{"../../specs"},
					Tags:     tags,
					Strict:   true,
					TestingT: t,
				},
			}
			if code := suite.Run(); code != 0 {
				t.Fatalf("godog exited with status %d", code)
			}
		})
	}
}
