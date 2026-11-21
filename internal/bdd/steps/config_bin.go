package steps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/epzero/basamento/internal/bdd/modroot"
)

// kitBinaryCache names the per-user cache under os.UserCacheDir. The binary is
// large, so every redgreen process sharing one private directory avoids a
// copy in the system temp directory for each process.
const kitBinaryCache = "basamento/kit-bin"

var (
	kitOnce sync.Once
	kitPath string
	kitErr  error

	// buildKitTo is a test seam: it writes the kit binary at path.
	buildKitTo = buildKitBinary
)

// kitBinary returns the path of the kit binary built from the current module,
// building it at most once per process.
func kitBinary() (string, error) {
	kitOnce.Do(func() { kitPath, kitErr = cachedKitBinary() })
	return kitPath, kitErr
}

func cachedKitBinary() (string, error) {
	root, err := modroot.Find()
	if err != nil {
		return "", err
	}
	cacheRoot, err := kitCacheRoot(root)
	if err != nil {
		return "", err
	}
	return cachedKitBinaryIn(root, cacheRoot)
}

// cachedKitBinaryIn returns the cached binary for moduleRoot under cacheRoot,
// rebuilding when the key changes or the entry cannot be trusted.
func cachedKitBinaryIn(moduleRoot, cacheRoot string) (string, error) {
	key, err := kitSourceKey(moduleRoot)
	if err != nil {
		return "", err
	}
	path := kitCachedPath(cacheRoot, key)
	if trustedKitBinary(path) {
		pruneKitCache(cacheRoot, key)
		return path, nil
	}
	return buildCachedKit(moduleRoot, cacheRoot, key)
}

// kitCacheRoot is a private per-user directory for one module tree. A shared
// location would let another user plant the binary this process is about to
// execute (SEC-2); a directory per tree keeps parallel worktrees from pruning
// each other's entries.
func kitCacheRoot(moduleRoot string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("kit binary cache directory: %w", err)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(moduleRoot)))
	root := filepath.Join(base, kitBinaryCache, hex.EncodeToString(sum[:16]))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("kit binary cache directory: %w", err)
	}
	if err := checkPrivateDir(root); err != nil {
		return "", err
	}
	return root, nil
}

// checkPrivateDir rejects a cache root that is a symlink, not a directory, not
// owned by this user, or accessible by group or others.
func checkPrivateDir(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("kit binary cache directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("kit binary cache root %s is not a directory", root)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("kit binary cache root %s is not private (mode %o)", root, info.Mode().Perm())
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("kit binary cache root %s is not owned by this user", root)
	}
	return nil
}

// trustedKitBinary reports whether path may be executed: a regular file (not a
// symlink) owned by this user, executable, and not writable by group or
// others.
func trustedKitBinary(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return false
	}
	if info.Mode().Perm()&0o022 != 0 {
		return false
	}
	return ownedByCurrentUser(info)
}

func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// kitCachedPath is where the build for key lives under cacheRoot. It depends
// only on the key, so every process of the same source tree and environment
// resolves to the same binary.
func kitCachedPath(cacheRoot, key string) string {
	return filepath.Join(cacheRoot, key, "kit")
}

func buildCachedKit(moduleRoot, cacheRoot, key string) (string, error) {
	dir := filepath.Join(cacheRoot, key)
	path := filepath.Join(dir, "kit")
	// An untrusted binary is removed, never executed. Only the binary goes:
	// the key directory also holds the temp files of concurrent builds, which
	// must survive this process's reset. If the binary cannot be removed this
	// fails instead of running foreign code.
	if err := os.RemoveAll(path); err != nil {
		return "", fmt.Errorf("reset kit cache entry: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Build next to the target and rename on success: concurrent processes
	// then either see the finished binary or build their own, never a partial
	// one.
	tmp, err := os.CreateTemp(dir, "kit-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmpPath) }() // still there when the build fails
	if err := buildKitTo(moduleRoot, tmpPath); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return "", fmt.Errorf("install kit: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("install kit: %w", err)
	}
	pruneKitCache(cacheRoot, key)
	return path, nil
}

// pruneKitCache drops cache entries other than keep, best effort. It runs
// after a binary is usable, so a failure only leaves an old entry behind.
func pruneKitCache(cacheRoot, keep string) {
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == keep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(cacheRoot, entry.Name()))
	}
}

func buildKitBinary(moduleRoot, path string) error {
	cmd := exec.Command("go", "build", "-o", path, "./cmd/kit")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build kit: %w: %s", err, out)
	}
	return nil
}

// kitSourceKey hashes the actual inputs of the kit build: the packages
// `go list -deps ./cmd/kit` reports, the contents of their Go and embedded
// files (standard-library contents are pinned to GOVERSION), and the effective
// build environment. Editing docs, specs or a test file therefore keeps the
// key, while changing code, an embed, a build tag or CGO does not.
func kitSourceKey(moduleRoot string) (string, error) {
	env, err := kitBuildEnv(moduleRoot)
	if err != nil {
		return "", err
	}
	out, err := goListDeps(moduleRoot)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := fmt.Fprintf(h, "build\n%s\n", env); err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg goListPackage
		err := dec.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("go list output: %w", err)
		}
		if pkg.Dir == "" {
			continue
		}
		if _, err := fmt.Fprintf(h, "package %s %s standard=%t\n", pkg.ImportPath, pkg.Dir, pkg.Standard); err != nil {
			return "", err
		}
		for _, name := range packageFiles(&pkg) {
			if _, err := fmt.Fprintf(h, "file %s\n", name); err != nil {
				return "", err
			}
			if pkg.Standard {
				continue
			}
			data, err := os.ReadFile(filepath.Join(pkg.Dir, filepath.FromSlash(name)))
			if err != nil {
				return "", err
			}
			if _, err := fmt.Fprintf(h, "%d\n", len(data)); err != nil {
				return "", err
			}
			if _, err := h.Write(data); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// goListPackage is the subset of `go list -json` this file hashes.
type goListPackage struct {
	ImportPath   string
	Dir          string
	Standard     bool
	GoFiles      []string
	CgoFiles     []string
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	SysoFiles    []string
	EmbedFiles   []string
}

func packageFiles(pkg *goListPackage) []string {
	seen := map[string]bool{}
	var names []string
	for _, list := range [][]string{
		pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles,
		pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles,
		pkg.SysoFiles, pkg.EmbedFiles,
	} {
		for _, name := range list {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func goListDeps(moduleRoot string) ([]byte, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/kit")
	cmd.Dir = moduleRoot
	// Output keeps stdout clean for the JSON decoder: go list writes progress
	// and warnings such as "go: downloading ..." to stderr.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./cmd/kit: %w: %s", err, stderr.Bytes())
	}
	return out, nil
}

// kitBuildEnv returns the effective settings that change the built binary.
func kitBuildEnv(moduleRoot string) (string, error) {
	keys := []string{
		"GOFLAGS", "CGO_ENABLED", "GOTOOLCHAIN", "GOAMD64", "GOEXPERIMENT",
		"GOOS", "GOARCH", "GOVERSION",
	}
	args := append([]string{"env", "-json"}, keys...)
	cmd := exec.Command("go", args...)
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go env: %w: %s", err, out)
	}
	values := map[string]string{}
	if err := json.Unmarshal(out, &values); err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	var b strings.Builder
	for _, key := range keys {
		if _, err := fmt.Fprintf(&b, "%s=%s\n", key, values[key]); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}
