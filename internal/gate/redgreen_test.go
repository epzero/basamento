package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const redGreenGoMod = "module github.com/epzero/basamento\n\ngo 1.26.11\n"

const redGreenBDDTestOnly = `package bdd

import "testing"

func TestFeatures(t *testing.T) {
	t.Fatal("red at base")
}
`

const redGreenBDDPasses = `package bdd

import "testing"

func TestFeatures(t *testing.T) {}
`

func writeRedGreenBDD(t *testing.T, dir, body string) {
	t.Helper()
	bdd := filepath.Join(dir, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdd, "bdd_test.go"), []byte(body), 0o644); err != nil {
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

func TestBuildBaseForRedGreenAcceptsTestOnlyHarness(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte(redGreenGoMod), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRedGreenBDD(t, wt, redGreenBDDTestOnly)
	if err := buildBaseForRedGreen(wt); err != nil {
		t.Fatalf("test-only internal/bdd must compile: %v", err)
	}
}

func TestBuildBaseForRedGreenRejectsBrokenBase(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte(redGreenGoMod), 0o644); err != nil {
		t.Fatal(err)
	}
	bdd := filepath.Join(wt, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdd, "broken.go"), []byte("package bdd\n\nfunc {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := buildBaseForRedGreen(wt); err == nil {
		t.Fatal("expected build failure, not vacuous red→green")
	}
}

func TestBuildBaseForRedGreenRejectsBrokenTestFile(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte(redGreenGoMod), 0o644); err != nil {
		t.Fatal(err)
	}
	bdd := filepath.Join(wt, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdd, "bdd_test.go"), []byte("package bdd\n\nfunc {"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := buildBaseForRedGreen(wt)
	if err == nil || !strings.Contains(err.Error(), "redgreen: base worktree did not build") {
		t.Fatalf("broken _test.go must fail closed: %v", err)
	}
}

func TestRedGreenTestOnlyBaseHarness(t *testing.T) {
	repo := gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	writeRedGreenBDD(t, repo, redGreenBDDTestOnly)
	commitBytes(t, repo, "internal/bdd/bdd_test.go", []byte(redGreenBDDTestOnly))
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

func TestRedGreenPassingBaseReportsNoNewBehavior(t *testing.T) {
	repo := gitRepo(t)
	for name, body := range redGreenSpecTree(true) {
		commitBytes(t, repo, name, []byte(body))
	}
	writeRedGreenBDD(t, repo, redGreenBDDPasses)
	commitBytes(t, repo, "internal/bdd/bdd_test.go", []byte(redGreenBDDPasses))
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
