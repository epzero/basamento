package conformance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestS3Matrix runs blob scenarios that are not @profile:embedded against the S3 simulator.
func TestS3Matrix(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "TestFeatures", "./internal/bdd/")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"BLOB_CONFORMANCE_BACKEND=s3",
		"BDD_TAGS=~@planned && ~@live && ~@eval && ~@journey && ~@slow && ~@quarantine && ~@profile:embedded",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("s3 conformance: %v\n%s", err, out)
	}
}

func findModuleRoot() (string, error) {
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
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
