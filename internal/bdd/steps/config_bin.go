package steps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/epzero/basamento/internal/bdd/modroot"
)

// kitBinaryCache names the shared cache root for built kit binaries. The
// binary is large, so every redgreen process reusing a temporary directory of
// its own would leave a copy behind; one directory per source tree lets the
// whole machine share a single build.
const kitBinaryCache = "basamento-kit-bin"

var (
	kitOnce sync.Once
	kitPath string
	kitErr  error
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
	key, err := kitSourceKey(root)
	if err != nil {
		return "", err
	}
	path := kitCachedPath(key)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	cmd := exec.Command("go", "build", "-o", tmpPath, "github.com/epzero/basamento/cmd/kit")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build kit: %w: %s", err, out)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("install kit: %w", err)
	}
	return path, nil
}

// kitCachedPath is where the build for key lives. It depends only on the key,
// so every process of the same source tree resolves to the same binary.
func kitCachedPath(key string) string {
	return filepath.Join(os.TempDir(), kitBinaryCache, key, "kit")
}

// kitSourceKey hashes the inputs of the kit build: every regular file in the
// module except Go test files and dot files. Editing code or an embedded asset
// therefore selects a new cache entry, while editing tests does not rebuild.
func kitSourceKey(root string) (string, error) {
	h := sha256.New()
	if _, err := fmt.Fprintf(h, "go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH); err != nil {
		return "", err
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(h, "%s %d\n", filepath.ToSlash(rel), len(data)); err != nil {
			return err
		}
		_, err = h.Write(data)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
