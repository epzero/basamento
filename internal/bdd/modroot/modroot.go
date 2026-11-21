// Package modroot finds the repository root (directory containing go.mod).
package modroot

import (
	"fmt"
	"os"
	"path/filepath"
)

// Find walks parents of the process working directory until go.mod is found.
func Find() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return FindFrom(dir)
}

// FindFrom walks parents of start until go.mod is found.
func FindFrom(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod in parents of %s", start)
		}
		dir = parent
	}
}
