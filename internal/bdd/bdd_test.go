package bdd

import (
	"context"
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

// TestMain closes the embedded Postgres server once after every test in this
// process, so go test -count=N reuses the server InitPostgres started on the
// first repetition (ADR-0015, EP0-154).
func TestMain(m *testing.M) {
	code := m.Run()
	_ = sqltest.ClosePostgres()
	os.Exit(code)
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
func bddGateProfiles() []string {
	return []string{"embedded", "cloud"}
}

func TestBDDGateProfiles(t *testing.T) {
	got := bddGateProfiles()
	if len(got) != 2 || got[0] != "embedded" || got[1] != "cloud" {
		t.Fatalf("profiles: %v", got)
	}
}

// TestFeatures runs every scenario under specs/ in strict mode: undefined or
// pending steps fail. BDD_TAGS narrows the run (used by `gate redgreen`).
// BDD_BUS_BACKEND selects a single bus implementation when set (fake, embedded, cloud).
func TestFeatures(t *testing.T) {
	sqltest.ResetSkipCounts()
	failed := false
	defer func() {
		if err := WriteBDDReport(failed); err != nil {
			t.Errorf("bdd report: %v", err)
		}
	}()

	if err := sqltest.InitPostgres(context.Background()); err != nil {
		failed = true
		t.Fatal(err)
	}
	if sqltest.GateStrict() && sqltest.SkipTotal() > 0 {
		failed = true
	}
	enforceStrictNoPostgresSkips(t)

	tags := scenarioTags()

	// BDD_PATHS narrows the run to one subtree (blob conformance). The bus
	// backend suites below always target specs/bus, so a narrowed run leaves
	// them out; BDD_BUS_BACKEND restricts the run to one bus implementation.
	narrowed := os.Getenv("BDD_PATHS") != ""
	oneBus := os.Getenv("BDD_BUS_BACKEND")
	if narrowed || oneBus == "" || oneBus == "fake" {
		for _, profile := range bddGateProfiles() {
			runSuite(t, tags, profile)
			if t.Failed() {
				failed = true
			}
		}
	}
	if !narrowed {
		for _, backend := range []string{"embedded", "cloud"} {
			if oneBus != "" && oneBus != backend {
				continue
			}
			runBusBackendSuite(t, tags, backend)
			if t.Failed() {
				failed = true
			}
		}
	}
	if sqltest.GateStrict() && sqltest.SkipTotal() > 0 {
		failed = true
	}
	enforceStrictNoPostgresSkips(t)
}

func runBusBackendSuite(t *testing.T, tags, backend string) {
	t.Run("bus-"+backend, func(t *testing.T) {
		t.Setenv("BDD_BUS_BACKEND", backend)
		t.Setenv("BDD_SQL_PROFILE", "embedded")
		suite := godog.TestSuite{
			Name:                "basamento-bus-" + backend,
			ScenarioInitializer: steps.Initialize,
			Options: &godog.Options{
				Format:   "pretty",
				Paths:    []string{"../../specs/bus"},
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

func runSuite(t *testing.T, tags, profile string) {
	name := "basamento"
	if profile != "" {
		name = "basamento-" + profile
	}
	t.Run(profile, func(t *testing.T) {
		if profile != "" {
			t.Setenv("BDD_SQL_PROFILE", profile)
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
	})
}
