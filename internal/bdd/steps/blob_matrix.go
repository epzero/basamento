package steps

import (
	"fmt"
	"sync"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
	"github.com/epzero/basamento/internal/bdd/modroot"
)

var (
	blobPickleMatrixOnce sync.Once
	blobPickleMatrixKeys map[string]string
	blobPickleMatrixErr  error
)

func ensureBlobPickleMatrixKeys() error {
	blobPickleMatrixOnce.Do(func() {
		root, err := modroot.Find()
		if err != nil {
			blobPickleMatrixErr = err
			return
		}
		blobPickleMatrixKeys, blobPickleMatrixErr = blobmatrix.PickleKeyMap(root)
	})
	return blobPickleMatrixErr
}

func matrixIDFromGodogScenario(sc *godog.Scenario, backend string) (string, error) {
	if backend != "s3" {
		return "", nil
	}
	if err := ensureBlobPickleMatrixKeys(); err != nil {
		return "", fmt.Errorf("blob s3 matrix ids: %w", err)
	}
	k := pickleKeyForGodog(sc)
	v, ok := blobPickleMatrixKeys[k]
	if !ok {
		return "", fmt.Errorf("blob s3 matrix ids: no entry for pickle %q", k)
	}
	return v, nil
}

func pickleKeyForGodog(sc *godog.Scenario) string {
	var line int64
	if sc.Location != nil {
		line = sc.Location.Line
	}
	return blobmatrix.PickleKeyForRuntime(sc.Uri, line)
}
