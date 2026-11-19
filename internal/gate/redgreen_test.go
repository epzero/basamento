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
	redGreenBDDRed    = "package bdd\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {\n\tt.Fatal(\"red at base\")\n}\n"
	redGreenBDDPasses = "package bdd\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {}\n"
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
