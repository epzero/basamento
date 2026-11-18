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

// bddGateProfiles returns SQL dialect profiles to run in the component gate.
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

type bddPass struct {
	name       string
	busBackend string
	sqlProfile string
	paths      []string
}

func gatePasses(postgresConfigured bool) []bddPass {
	oneBus := os.Getenv("BDD_BUS_BACKEND")
	var out []bddPass

	if oneBus == "" || oneBus == "fake" {
		out = append(out, bddPass{
			name:       "default",
			busBackend: "fake",
			sqlProfile: "embedded",
			paths:      []string{"../../specs"},
		})
		if postgresConfigured && (oneBus == "" || oneBus == "fake") {
			out = append(out, bddPass{
				name:       "sql-cloud",
				busBackend: "fake",
				sqlProfile: "cloud",
				paths:      []string{"../../specs"},
			})
		}
	}
	for _, bb := range []string{"embedded", "cloud"} {
		if oneBus != "" && oneBus != bb {
			continue
		}
		out = append(out, bddPass{
			name:       "bus-" + bb,
			busBackend: bb,
			sqlProfile: "embedded",
			paths:      []string{"../../specs/bus"},
		})
	}
	return out
}

// TestFeatures runs scenarios in strict mode. BDD_TAGS narrows the run (gate redgreen).
// BDD_BUS_BACKEND selects a single bus implementation when set (fake, embedded, cloud).
func TestFeatures(t *testing.T) {
	tags := defaultTags
	if extra := os.Getenv("BDD_TAGS"); extra != "" {
		tags = extra
	}
	postgres := sqltest.PostgresConfigured()
	for _, pass := range gatePasses(postgres) {
		pass := pass
		t.Run(pass.name, func(t *testing.T) {
			t.Setenv("BDD_BUS_BACKEND", pass.busBackend)
			t.Setenv("BDD_SQL_PROFILE", pass.sqlProfile)
			suite := godog.TestSuite{
				Name:                "basamento-" + pass.name,
				ScenarioInitializer: steps.Initialize,
				Options: &godog.Options{
					Format:   "pretty",
					Paths:    pass.paths,
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
	if !postgres {
		t.Log("gate: skip BDD postgres pass (BDD_POSTGRES_URL not set)")
	}
}