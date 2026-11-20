package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const redGreenGoMod = "module github.com/epzero/basamento\n\ngo 1.26.11\n"

const (
	// redGreenBDDExecutes is for compile-check tests: if the test binary ever
	// runs, init prints to stderr and exits non-zero.
	redGreenBDDExecutes = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n)\n\nfunc init() {\n\tfmt.Fprintln(os.Stderr, \"harness ran during the compile check\")\n\tos.Exit(1)\n}\n\nfunc TestFeatures(t *testing.T) {}\n"
	// redGreenBDDRed is a base harness whose TestFeatures fails when run: red
	// at base in red→green scenario tests.
	redGreenBDDRed = "package bdd\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {\n\tt.Fatal(\"red at base\")\n}\n"
	// redGreenBDDPasses prints the pretty formatter's step summary a passing
	// base run shows.
	redGreenBDDPasses = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFeatures(t *testing.T) {\n\tfmt.Println(\"1 scenarios (1 passed)\")\n\tfmt.Println(\"1 steps (1 passed)\")\n}\n"
	// redGreenBDDInfra fails at the Postgres bootstrap, as a base run without
	// supervisor or bundle does under GATE_STRICT=1.
	redGreenBDDInfra = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFeatures(t *testing.T) {\n\tfmt.Println(\"embedded postgres unavailable (supervisor missing); run make pg-prefetch\")\n\tt.Fatal(\"postgres unavailable\")\n}\n"
	// redGreenBDDStrictSkip fails on the strict skip rule: a skipped Postgres
	// pass that exits non-zero is not a red proof either.
	redGreenBDDStrictSkip = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFeatures(t *testing.T) {\n\tfmt.Println(\"gate strict: 1 skipped postgres runs\")\n\tt.Fatal(\"skips\")\n}\n"
	// redGreenBDDSkipped exits zero with every step skipped, as godog prints a
	// scenario whose Before hook returned ErrSkip (and counts it passed).
	redGreenBDDSkipped = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc TestFeatures(t *testing.T) {\n\tfmt.Println(\"1 scenarios (1 passed)\")\n\tfmt.Println(\"1 steps (1 skipped)\")\n}\n"
	// redGreenBDDReportSkip exits zero and writes the gate report with a skipped
	// Postgres run, which names the reason.
	redGreenBDDReportSkip = "package bdd\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestFeatures(t *testing.T) {\n\tfmt.Println(\"1 scenarios (1 passed)\")\n\tfmt.Println(\"1 steps (1 skipped)\")\n\tif err := os.MkdirAll(\"../../.gate\", 0o755); err != nil {\n\t\tt.Fatal(err)\n\t}\n\tif err := os.WriteFile(\"../../.gate/bdd.json\", []byte(`{\"outcome\":\"pass\",\"skip_runs\":{\"postgres-unavailable\":1}}`), 0o644); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n"
)

// writeBaseModule writes a temp base worktree with a go.mod and a BDD package
// made only of _test.go files, like internal/bdd in this repository.
func writeBaseModule(t *testing.T, goDirective, body string) string {
	t.Helper()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n\ngo "+goDirective+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBaseHarness(t, wt, body)
	return wt
}

func writeBaseHarness(t *testing.T, dir, body string) {
	t.Helper()
	bdd := filepath.Join(dir, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdd, "features_test.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func redGreenSpecTree(planned bool) map[string]string {
	tag := "@REQ-X-001 @id:X-RG-001"
	if planned {
		tag += " @planned @wp:WP-L1-01"
	}
	return map[string]string{
		"go.mod":                  redGreenGoMod,
		"specs/x/requirements.md": reqs,
		"specs/x/a.feature":       feature(tag + "\n  Scenario: s\n    Given a\n"),
	}
}

// TestBuildBaseForRedGreenAcceptsTestOnlyPackage proves the compile step
// writes a test binary and returns without running it: the fixture's init
// exits non-zero if the binary ever executes.
func TestBuildBaseForRedGreenAcceptsTestOnlyPackage(t *testing.T) {
	wt := writeBaseModule(t, "1.26.11", redGreenBDDExecutes)
	if err := buildBaseForRedGreen(wt); err != nil {
		t.Fatalf("a package of only _test.go files must compile: %v", err)
	}
}

func TestBuildBaseForRedGreenRejectsBrokenTestFile(t *testing.T) {
	wt := writeBaseModule(t, "1.26.11", "package bdd\n\nfunc {")
	err := buildBaseForRedGreen(wt)
	if err == nil {
		t.Fatal("expected build failure, not vacuous red→green")
	}
	if !strings.Contains(err.Error(), "redgreen: base worktree did not build") {
		t.Fatalf("error should name the failed base build: %v", err)
	}
}

// TestBuildBaseForRedGreenRejectsNewerGoDirective uses go 1.999: no release
// will reach that directive.
func TestBuildBaseForRedGreenRejectsNewerGoDirective(t *testing.T) {
	wt := writeBaseModule(t, "1.999", redGreenBDDPasses)
	// An empty file proxy keeps the toolchain switch offline: looking up the
	// missing toolchain fails at once instead of reaching the network.
	t.Setenv("GOPROXY", "file://"+t.TempDir())
	t.Setenv("GOSUMDB", "off")
	err := buildBaseForRedGreen(wt)
	if err == nil {
		t.Fatal("expected a go directive the toolchain cannot satisfy to fail closed")
	}
	if !strings.Contains(err.Error(), "base worktree did not build") {
		t.Fatalf("error should name the failed base build: %v", err)
	}
}

// TestRedGreenTestOnlyBaseHarness activates a scenario over a base whose
// harness is test-only and whose TestFeatures fails there: the scenario is
// red at base and must pass through without a vacuous-red report.
func TestRedGreenTestOnlyBaseHarness(t *testing.T) {
	repo := gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	commitBytes(t, repo, "internal/bdd/features_test.go", []byte(redGreenBDDRed))
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tag := "@REQ-X-001 @id:X-RG-001\n  Scenario: s\n    Given a\n"
	commitBytesMsg(t, repo, "specs/x/a.feature", []byte(feature(tag)), "activate scenario")

	activated, errs, err := RedGreen(repo, base)
	if err != nil {
		t.Fatalf("redgreen on test-only base harness: %v", err)
	}
	if len(activated) != 1 || activated[0] != "X-RG-001" {
		t.Fatalf("activated = %v, want [X-RG-001]", activated)
	}
	if len(errs) != 0 {
		t.Fatalf("scenario should fail at base (errs empty): %v", errs)
	}
}

// TestRedGreenPassingBaseReportsNoNewBehavior keeps the check honest: a base
// whose harness already passes the scenario is reported, not called red.
func TestRedGreenPassingBaseReportsNoNewBehavior(t *testing.T) {
	repo := gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	commitBytes(t, repo, "internal/bdd/features_test.go", []byte(redGreenBDDPasses))
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tag := "@REQ-X-001 @id:X-RG-001\n  Scenario: s\n    Given a\n"
	commitBytesMsg(t, repo, "specs/x/a.feature", []byte(feature(tag)), "activate scenario")

	activated, errs, err := RedGreen(repo, base)
	if err != nil {
		t.Fatalf("redgreen: %v", err)
	}
	if len(activated) != 1 {
		t.Fatalf("activated = %v", activated)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "does not test the new behavior") {
		t.Fatalf("passing base must report vacuous red→green: %v", errs)
	}
}

// TestRedGreenBrokenBaseFailsClosed walks the whole check: a base harness
// that does not compile must error, never be treated as red.
func TestRedGreenBrokenBaseFailsClosed(t *testing.T) {
	repo := gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	commitBytes(t, repo, "internal/bdd/features_test.go", []byte("package bdd\n\nfunc {"))
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tag := "@REQ-X-001 @id:X-RG-001\n  Scenario: s\n    Given a\n"
	commitBytesMsg(t, repo, "specs/x/a.feature", []byte(feature(tag)), "activate scenario")

	_, _, err = RedGreen(repo, base)
	if err == nil || !strings.Contains(err.Error(), "redgreen: base worktree did not build") {
		t.Fatalf("a broken base harness must fail closed: %v", err)
	}
}

// activateOverBase leaves a repository whose base commit holds harness, then
// commits the scenario as active at head.
func activateOverBase(t *testing.T, harness string) (repo, base string) {
	t.Helper()
	repo = gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	commitBytes(t, repo, "internal/bdd/features_test.go", []byte(harness))
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tag := "@REQ-X-001 @id:X-RG-001\n  Scenario: s\n    Given a\n"
	commitBytesMsg(t, repo, "specs/x/a.feature", []byte(feature(tag)), "activate scenario")
	return repo, base
}

// TestBaseRunPostgresInfraFailure pins the classifier: a base run that could
// not start Postgres, or failed the strict skip rule, is never a red proof.
func TestBaseRunPostgresInfraFailure(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{name: "supervisor missing", out: "embedded postgres unavailable (supervisor missing); run make pg-prefetch", want: true},
		{name: "url unavailable", out: "BDD_POSTGRES_URL is set but the server is unavailable (connection refused)", want: true},
		{name: "strict skip", out: "gate strict: 1 skipped postgres runs", want: true},
		{name: "behavior failure", out: "--- FAIL: TestFeatures\n    features_test.go:5: red at base", want: false},
		{name: "passing run", out: "1 scenario (1 passed)", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := baseRunPostgresInfraFailure([]byte(tc.out)); got != tc.want {
				t.Fatalf("baseRunPostgresInfraFailure(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}

// TestStepSummary judges a base run's zero exit by its step summary: a
// scenario skipped by a Before hook prints no passed step.
func TestStepSummary(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		wantRan    bool
		wantPassed bool
	}{
		{name: "passed", out: "1 scenarios (1 passed)\n3 steps (3 passed)", wantRan: true, wantPassed: true},
		{name: "skipped", out: "1 scenarios (1 passed)\n1 steps (1 skipped)", wantRan: true},
		{name: "mixed profiles", out: "1 scenarios (1 passed)\n1 steps (1 passed)\n1 scenarios (1 passed)\n1 steps (1 skipped)", wantRan: true, wantPassed: true},
		{name: "no suite", out: "no tests to run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran, passed := stepSummary([]byte(tc.out))
			if ran != tc.wantRan || passed != tc.wantPassed {
				t.Fatalf("stepSummary(%q) = (%v, %v), want (%v, %v)", tc.out, ran, passed, tc.wantRan, tc.wantPassed)
			}
		})
	}
}

// TestClassifyBaseRun covers the report path and the older base that writes no
// report.
func TestClassifyBaseRun(t *testing.T) {
	wt := t.TempDir()
	passing := []byte("1 scenarios (1 passed)\n1 steps (1 passed)")
	skipped := []byte("1 scenarios (1 passed)\n1 steps (1 skipped)")

	if got, _ := classifyBaseRun(wt, passing); got != baseRunPassed {
		t.Fatalf("a passing base run: %v", got)
	}
	if got, reason := classifyBaseRun(wt, skipped); got != baseRunSkipped || reason == "" {
		t.Fatalf("a base run with no passed step must be a skip: %v %q", got, reason)
	}
	if got, _ := classifyBaseRun(wt, []byte("no tests to run")); got != baseRunUnproven {
		t.Fatalf("a base run that executed nothing must not be a red proof: %v", got)
	}
	if err := os.MkdirAll(filepath.Join(wt, ".gate"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := `{"outcome":"pass","postgres_server":"","skip_runs":{"postgres-unavailable":2}}`
	if err := os.WriteFile(filepath.Join(wt, ".gate", "bdd.json"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	got, reason := classifyBaseRun(wt, skipped)
	if got != baseRunSkipped || !strings.Contains(reason, "postgres-unavailable: 2") {
		t.Fatalf("the report names the reason: %v %q", got, reason)
	}
}

// TestRedGreenBaseInfraFailureFailsClosed walks the check: a base run that
// cannot start Postgres exits non-zero, and must error instead of counting as
// the red proof.
func TestRedGreenBaseInfraFailureFailsClosed(t *testing.T) {
	repo, base := activateOverBase(t, redGreenBDDInfra)
	_, _, err := RedGreen(repo, base)
	if err == nil || !strings.Contains(err.Error(), "base run could not run Postgres") {
		t.Fatalf("an infra failure at base must fail closed, not pass as red: %v", err)
	}
	if !strings.Contains(err.Error(), "make pg-prefetch") {
		t.Fatalf("the error must name the remedy: %v", err)
	}
}

// TestRedGreenBaseStrictSkipFailsClosed covers a base that fails the strict
// skip rule: also not a red proof.
func TestRedGreenBaseStrictSkipFailsClosed(t *testing.T) {
	repo, base := activateOverBase(t, redGreenBDDStrictSkip)
	_, _, err := RedGreen(repo, base)
	if err == nil || !strings.Contains(err.Error(), "base run could not run Postgres") {
		t.Fatalf("a strict skip at base must fail closed: %v", err)
	}
}

// TestRedGreenBaseSkipReportedNotPassing pins the second half of the review
// finding: a base run that skips and exits zero is not "passes at base".
func TestRedGreenBaseSkipReportedNotPassing(t *testing.T) {
	repo, base := activateOverBase(t, redGreenBDDSkipped)
	activated, errs, err := RedGreen(repo, base)
	if err == nil || !strings.Contains(err.Error(), "skipped the Postgres pass") {
		t.Fatalf("a skipped base run must be named, not reported as passing: errs=%v err=%v", errs, err)
	}
	if len(activated) != 1 {
		t.Fatalf("activated = %v", activated)
	}
}

// TestRedGreenBaseReportNamesSkipReason uses the base's .gate/bdd.json when it
// writes one, so the operator sees the reason.
func TestRedGreenBaseReportNamesSkipReason(t *testing.T) {
	repo, base := activateOverBase(t, redGreenBDDReportSkip)
	_, _, err := RedGreen(repo, base)
	if err == nil || !strings.Contains(err.Error(), "postgres-unavailable: 1") {
		t.Fatalf("the skip reason must reach the error: %v", err)
	}
}
