package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBaseModule writes a temp base worktree with a go.mod and a BDD package
// made only of _test.go files, like internal/bdd in this repository.
func writeBaseModule(t *testing.T, goDirective string) string {
	t.Helper()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n\ngo "+goDirective+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bdd := filepath.Join(wt, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "package bdd\n\nimport \"testing\"\n\nfunc TestFeatures(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(bdd, "features_test.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestBuildBaseForRedGreenAcceptsTestOnlyPackage(t *testing.T) {
	wt := writeBaseModule(t, "1.26.11")
	if err := buildBaseForRedGreen(wt); err != nil {
		t.Fatalf("a package of only _test.go files must build: %v", err)
	}
}

func TestBuildBaseForRedGreenRejectsBrokenBase(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n\ngo 1.26.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bdd := filepath.Join(wt, "internal", "bdd")
	if err := os.MkdirAll(bdd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bdd, "broken_test.go"), []byte("package bdd\n\nfunc {"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := buildBaseForRedGreen(wt)
	if err == nil {
		t.Fatal("expected build failure, not vacuous red→green")
	}
	if !strings.Contains(err.Error(), "base worktree did not build") {
		t.Fatalf("error should name the failed base build: %v", err)
	}
}

func TestBuildBaseForRedGreenRejectsNewerGoDirective(t *testing.T) {
	wt := writeBaseModule(t, "1.99")
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
