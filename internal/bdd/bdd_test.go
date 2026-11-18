package bdd

import (
	"os"
	"testing"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/sqltest"
	"github.com/epzero/basamento/internal/bdd/steps"
)

// defaultTags excludes scenarios that are not part of the deterministic gate.
const defaultTags = "~@planned && ~@live && ~@eval && ~@journey && ~@slow && ~@quarantine"

// TestFeatures runs every scenario under specs/ in strict mode.
func TestFeatures(t *testing.T) {
	if extra := os.Getenv("BDD_TAGS"); extra != "" {
		runSuite(t, extra, "")
		return
	}
	runSuite(t, defaultTags, "embedded")
	if sqltest.PostgresConfigured() {
		runSuite(t, defaultTags, "cloud")
	} else {
		t.Log("gate: skip BDD postgres pass (BDD_POSTGRES_URL not set)")
	}
}

func runSuite(t *testing.T, tags, profile string) {
	name := "basamento"
	if profile != "" {
		name = "basamento-" + profile
	}
	if profile != "" {
		t.Run(profile, func(t *testing.T) {
			if err := os.Setenv("BDD_SQL_PROFILE", profile); err != nil {
				t.Fatal(err)
			}
			suite := godog.TestSuite{
				Name:                name,
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
		return
	}
	suite := godog.TestSuite{
		Name:                name,
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
}
