package conformance_test

import (
	"fmt"
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
	want, err := expectedBlobMatrixScenarios(root)
	if err != nil {
		t.Fatal(err)
	}
	if want < 1 {
		t.Fatalf("expected at least 1 blob matrix scenario, got %d", want)
	}
	cmd := exec.Command("go", "test", "-race", "-v", "-count=1", "-run", "TestFeatures", "./internal/bdd/")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"BLOB_CONFORMANCE_BACKEND=s3",
		"BDD_PATHS=specs/blob",
		"BDD_TAGS_AND=~@profile:embedded",
	)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("s3 conformance: %v\n%s", err, text)
	}
	if err := verifyS3MatrixOutput(text, want); err != nil {
		t.Fatalf("%v\n%s", err, text)
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
			return "", fmt.Errorf("no go.mod")
		}
		dir = parent
	}
}
