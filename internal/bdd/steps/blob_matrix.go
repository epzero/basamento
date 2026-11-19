package steps

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
)

var (
	blobPickleMatrixOnce sync.Once
	blobPickleMatrixKeys map[string]string
)

func ensureBlobPickleMatrixKeys() {
	blobPickleMatrixOnce.Do(func() {
		root, err := moduleRootForBlobMatrix()
		if err != nil {
			return
		}
		keys, err := blobmatrix.PickleKeyMap(root)
		if err != nil {
			return
		}
		blobPickleMatrixKeys = keys
	})
}

func moduleRootForBlobMatrix() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod in parents of %s", dir)
		}
		dir = parent
	}
}

func matrixIDFromGodogScenario(sc *godog.Scenario) string {
	ensureBlobPickleMatrixKeys()
	k := pickleKeyForGodog(sc)
	if blobPickleMatrixKeys != nil {
		if v, ok := blobPickleMatrixKeys[k]; ok {
			return v
		}
	}
	var id string
	for _, tag := range sc.Tags {
		if strings.HasPrefix(tag.Name, "@id:") {
			id = strings.TrimPrefix(tag.Name, "@id:")
			break
		}
	}
	if id == "" {
		return sc.Name
	}
	return id
}

func pickleKeyForGodog(sc *godog.Scenario) string {
	var line int64
	if sc.Location != nil {
		line = sc.Location.Line
	}
	return blobmatrix.PickleKeyForRuntime(sc.Uri, line)
}