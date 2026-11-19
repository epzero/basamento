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

// bddGateProfiles returns dialect profiles to run in the component gate for one tag filter pass.
func bddGateProfiles(postgresConfigured bool) []string {
	out := []string{"embedded"}
	if postgresConfigured {
		out = append(out, "cloud")
	}
	return out
}

func TestBDDGateProfiles(t *testing.T) {
	got := bddGateProfiles(false)
	if len(got) != 1 || got[0] != "embedded" {
		t.Fatalf("without postgres: %v", got)
	}
	got = bddGateProfiles(true)
	if len(got) != 2 || got[1] != "cloud" {
		t.Fatalf("with postgres: %v", got)
	}
}

// TestFeatures runs every scenario under specs/ in strict mode.
func TestFeatures(t *testing.T) {
	tags := defaultTags
	if extra := os.Getenv("BDD_TAGS"); extra != "" {
		tags = extra
	}
	configured := sqltest.PostgresConfigured()
	for _, profile := range bddGateProfiles(configured) {
		runSuite(t, tags, profile)
	}
	if !configured {
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
			t.Setenv("BDD_SQL_PROFILE", profile)
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
