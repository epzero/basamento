package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTreeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKitSourceKeyTracksBuildInputs(t *testing.T) {
	root := t.TempDir()
	writeTreeFile(t, root, "go.mod", "module example.com/kit\n")
	writeTreeFile(t, root, "cmd/kit/main.go", "package main\n\nfunc main() {}\n")
	writeTreeFile(t, root, "cmd/kit/main_test.go", "package main\n")

	first, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("key changed between runs: %s then %s", first, again)
	}

	writeTreeFile(t, root, "cmd/kit/main_test.go", "package main\n\n// edited\n")
	if got, err := kitSourceKey(root); err != nil || got != first {
		t.Fatalf("editing a test file changed the key: %s, %v", got, err)
	}

	writeTreeFile(t, root, ".gitignore", "*.tmp\n")
	if got, err := kitSourceKey(root); err != nil || got != first {
		t.Fatalf("a dot file changed the key: %s, %v", got, err)
	}

	writeTreeFile(t, root, "cmd/kit/main.go", "package main\n\nfunc main() { println() }\n")
	changed, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("editing a built file did not change the key")
	}

	writeTreeFile(t, root, "cmd/kit/migrations/001.sql", "select 1;\n")
	if got, err := kitSourceKey(root); err != nil || got == changed {
		t.Fatalf("an embedded asset did not change the key: %s, %v", got, err)
	}
}

func TestKitCachedPathIsStable(t *testing.T) {
	key := "0123456789abcdef"
	path := kitCachedPath(key)
	if path != kitCachedPath(key) {
		t.Fatalf("cache path is not stable: %s", path)
	}
	if !strings.HasPrefix(path, filepath.Join(os.TempDir(), kitBinaryCache)) {
		t.Fatalf("cache path %s is outside %s", path, filepath.Join(os.TempDir(), kitBinaryCache))
	}
	if base := filepath.Base(filepath.Dir(path)); base != key {
		t.Fatalf("cache path %s is not keyed by %s", path, key)
	}
	if other := kitCachedPath("fedcba9876543210"); filepath.Dir(other) == filepath.Dir(path) {
		t.Fatalf("different keys share a directory: %s", filepath.Dir(path))
	}
}
