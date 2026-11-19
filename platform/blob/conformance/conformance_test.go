package conformance_test

import (
	"os/exec"
	"testing"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
	"github.com/epzero/basamento/internal/bdd/modroot"
)

// TestS3Matrix runs blob scenarios that are not @profile:embedded against the S3 simulator.
func TestS3Matrix(t *testing.T) {
	root, err := modroot.Find()
	if err != nil {
		t.Fatal(err)
	}
	matrixIDs, err := blobmatrix.PickleIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matrixIDs) < 1 {
		t.Fatalf("expected at least 1 blob matrix scenario, got %d", len(matrixIDs))
	}
	cmd := exec.Command("go", "test", "-race", "-v", "-count=1", "-run", "TestFeatures", "./internal/bdd/")
	cmd.Dir = root
	cmd.Env = bddSubprocessEnv(
		"BLOB_CONFORMANCE_BACKEND=s3",
		"BDD_PATHS=specs/blob",
		"BDD_TAGS_AND=~@profile:embedded",
	)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("s3 conformance: %v\n%s", err, text)
	}
	if err := verifyS3MatrixOutput(text, len(matrixIDs), matrixIDs); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
}
