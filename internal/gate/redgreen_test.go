package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildBaseForRedGreenRejectsBrokenBase(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x\n\ngo 1.26.11\n"), 0o644); err != nil {
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
