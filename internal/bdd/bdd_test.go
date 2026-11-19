package bdd

import (
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/sqltest"
	"github.com/epzero/basamento/internal/bdd/steps"
)

// defaultTags excludes scenarios that are not part of the deterministic gate run.
// @planned: approved but not implemented yet; @live/@eval/@journey/@slow: nightly lanes;
// @quarantine: temporarily excluded with an issue and an expiry date (linted).
const defaultTags = "~@planned && ~@live && ~@eval && ~@journey && ~@slow && ~@quarantine"

// scenarioTags builds godog's tag filter: comma-separated OR of AND chains (&&).
func scenarioTags() string {
	tags := defaultTags
	if extra := os.Getenv("BDD_TAGS"); extra != "" {
		tags = extra
	}
	and := os.Getenv("BDD_TAGS_AND")
	if and == "" {
		return tags
	}
	tagParts := splitTagCSV(tags)
	andParts := splitTagCSV(and)
	if len(tagParts) == 1 && len(andParts) == 1 {
		return tags + " && " + and
	}
	var clauses []string
	for _, t := range tagParts {
		for _, a := range andParts {
			clauses = append(clauses, t+" && "+a)
		}
	}
	return strings.Join(clauses, ",")
}

func splitTagCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func scenarioPaths() []string {
	if p := os.Getenv("BDD_PATHS"); p != "" {
		parts := strings.Split(p, ",")
		out := make([]string, 0, len(parts))
		for _, s := range parts {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, "../../"+strings.TrimPrefix(s, "/"))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"../../specs"}
}

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

// TestFeatures runs every scenario under specs/ in strict mode: undefined or
// pending steps fail. BDD_TAGS narrows the run (used by `gate redgreen`).
func TestFeatures(t *testing.T) {
	tags := scenarioTags()
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
					Paths:    scenarioPaths(),
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
			Paths:    scenarioPaths(),
			Tags:     tags,
			Strict:   true,
			TestingT: t,
		},
	}
	if code := suite.Run(); code != 0 {
		t.Fatalf("godog exited with status %d", code)
	}
}
