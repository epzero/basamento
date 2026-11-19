package gate

import (
	"fmt"
	"os"
	"path/filepath"
)

// RepoRoot walks parents of start until a go.mod file is found.
func RepoRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("gate: no go.mod in parents of %s", start)
		}
		dir = parent
	}
}