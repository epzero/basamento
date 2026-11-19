package gate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoRoot(t *testing.T) {
	modRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(modRoot, "go.mod"), []byte("module x\n\ngo 1.26.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(modRoot, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != modRoot {
		t.Fatalf("got %q want %q", got, modRoot)
	}
}
