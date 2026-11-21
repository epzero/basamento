package steps

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func fakeKitModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTreeFile(t, root, "go.mod", "module example.com/kit\n\ngo 1.21\n")
	writeTreeFile(t, root, "cmd/kit/main.go", `package main

import _ "embed"

//go:embed assets/*.txt
var asset string

func main() { println(asset) }
`)
	writeTreeFile(t, root, "cmd/kit/assets/a.txt", "a\n")
	return root
}

func useFakeKitBuilder(t *testing.T, builds *int) {
	t.Helper()
	previous := buildKitTo
	buildKitTo = func(_, path string) error {
		*builds++
		return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)
	}
	t.Cleanup(func() { buildKitTo = previous })
}

func TestKitSourceKeyTracksBuildInputs(t *testing.T) {
	root := fakeKitModule(t)
	writeTreeFile(t, root, "cmd/kit/main_test.go", "package main\n")
	writeTreeFile(t, root, "cmd/kit/assets/a.txt", "a\n")
	writeTreeFile(t, root, "docs/readme.md", "docs\n")
	writeTreeFile(t, root, "specs/x.feature", "spec\n")
	writeTreeFile(t, root, ".gitignore", "*.tmp\n")

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
	writeTreeFile(t, root, "docs/readme.md", "edited\n")
	if got, err := kitSourceKey(root); err != nil || got != first {
		t.Fatalf("editing docs changed the key: %s, %v", got, err)
	}
	writeTreeFile(t, root, "specs/x.feature", "edited\n")
	if got, err := kitSourceKey(root); err != nil || got != first {
		t.Fatalf("editing specs changed the key: %s, %v", got, err)
	}
	writeTreeFile(t, root, ".gitignore", "*.unused\n")
	if got, err := kitSourceKey(root); err != nil || got != first {
		t.Fatalf("a dot file changed the key: %s, %v", got, err)
	}

	writeTreeFile(t, root, "cmd/kit/assets/a.txt", "changed\n")
	changed, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("editing an embedded asset did not change the key")
	}

	writeTreeFile(t, root, "cmd/kit/main.go", `package main

func main() { println() }
`)
	if got, err := kitSourceKey(root); err != nil || got == changed {
		t.Fatalf("editing a built file did not change the key: %s, %v", got, err)
	}

	t.Setenv("GOFLAGS", "")
	noFlags, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-tags=kitcachetest")
	withFlags, err := kitSourceKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if noFlags == withFlags {
		t.Fatal("GOFLAGS did not change the key")
	}
}

func TestKitCachedPathIsStable(t *testing.T) {
	cacheRoot := t.TempDir()
	key := "0123456789abcdef"
	path := kitCachedPath(cacheRoot, key)
	if path != kitCachedPath(cacheRoot, key) {
		t.Fatalf("cache path is not stable: %s", path)
	}
	if want := filepath.Join(cacheRoot, key, "kit"); path != want {
		t.Fatalf("cache path %s, want %s", path, want)
	}
	if other := kitCachedPath(cacheRoot, "fedcba9876543210"); filepath.Dir(other) == filepath.Dir(path) {
		t.Fatalf("different keys share a directory: %s", filepath.Dir(path))
	}
}

func TestCachedKitBinaryReusesTrustedEntry(t *testing.T) {
	moduleRoot := fakeKitModule(t)
	cacheRoot := t.TempDir()
	builds := 0
	useFakeKitBuilder(t, &builds)

	first, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	second, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("trusted entry rebuilt: builds = %d, want 1", builds)
	}
	if second != first {
		t.Fatalf("cache path changed: %s then %s", first, second)
	}
	info, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("cached binary mode = %o, want a regular executable not group/other writable", info.Mode().Perm())
	}
}

func TestCachedKitBinaryRebuildsLooseEntry(t *testing.T) {
	moduleRoot := fakeKitModule(t)
	cacheRoot := t.TempDir()
	builds := 0
	useFakeKitBuilder(t, &builds)

	path, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedKitBinaryIn(moduleRoot, cacheRoot); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("builds = %d, want the loose entry rebuilt", builds)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("rebuilt entry mode = %o, want a regular file not group/other writable", info.Mode().Perm())
	}
}

func TestCachedKitBinaryRebuildKeepsConcurrentTemps(t *testing.T) {
	moduleRoot := fakeKitModule(t)
	cacheRoot := t.TempDir()
	builds := 0
	useFakeKitBuilder(t, &builds)

	path, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	concurrent := filepath.Join(filepath.Dir(path), "kit-other.tmp")
	if err := os.WriteFile(concurrent, []byte("partial build"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedKitBinaryIn(moduleRoot, cacheRoot); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("builds = %d, want the loose entry rebuilt", builds)
	}
	if _, err := os.Stat(concurrent); err != nil {
		t.Fatalf("rebuilding removed another process's temp file: %v", err)
	}
}

func TestGoListDepsKeepsStderrOutOfJSON(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo 'go: downloading example.com/x' >&2\n" +
		"printf '%s\\n' '{\"ImportPath\":\"example.com/kit\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := goListDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	var pkg goListPackage
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatalf("go list output is not clean JSON: %v: %s", err, out)
	}
	if pkg.ImportPath != "example.com/kit" {
		t.Fatalf("ImportPath = %q, want example.com/kit", pkg.ImportPath)
	}
}

func TestCachedKitBinaryRebuildsSymlinkEntry(t *testing.T) {
	moduleRoot := fakeKitModule(t)
	cacheRoot := t.TempDir()
	builds := 0
	useFakeKitBuilder(t, &builds)

	path, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cacheRoot, "planted")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := cachedKitBinaryIn(moduleRoot, cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("builds = %d, want the symlink rebuilt", builds)
	}
	info, err := os.Lstat(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("rebuilt entry is not a regular file: %v", info.Mode())
	}
}

func TestPruneKitCacheKeepsCurrentKey(t *testing.T) {
	cacheRoot := t.TempDir()
	keep := filepath.Join(cacheRoot, "keep")
	other := filepath.Join(cacheRoot, "other")
	for _, dir := range []string{keep, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pruneKitCache(cacheRoot, "keep")
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("current entry was pruned: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("old entry was not pruned: %v", err)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(root); err != nil {
		t.Fatalf("private directory rejected: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(root); err == nil {
		t.Fatal("group/other-readable cache root was accepted")
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(link); err == nil {
		t.Fatal("symlinked cache root was accepted")
	}
}
