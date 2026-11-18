package conformance_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/platform/blob"
	"github.com/epzero/basamento/platform/blob/conformance"
)

// TestS3Matrix runs blob scenarios that are not @profile:embedded against the S3 simulator.
func TestS3Matrix(t *testing.T) {
	root, err := findModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	want, err := conformance.ExpectedBlobMatrixScenarios(root)
	if err != nil {
		t.Fatal(err)
	}
	if want < 1 {
		t.Fatalf("expected at least 1 blob matrix scenario, got %d", want)
	}
	tags := blob.GateScenarioTags + " && ~@profile:embedded"
	cmd := exec.Command("go", "test", "-race", "-v", "-count=1", "-run", "TestFeatures", "./internal/bdd/")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"BLOB_CONFORMANCE_BACKEND=s3",
		"BDD_TAGS="+tags,
	)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("s3 conformance: %v\n%s", err, text)
	}
	got, passed, err := conformance.ParseGodogSummary(text)
	if err != nil {
		t.Fatalf("parse summary: %v\n%s", err, text)
	}
	if got != passed || got < want {
		t.Fatalf("godog ran %d scenarios (%d passed), want at least %d all passing", got, passed, want)
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

func TestParseGodogSummaryRejectsEmptySelection(t *testing.T) {
	t.Parallel()
	_, _, err := conformance.ParseGodogSummary(strings.TrimSpace(`
	testing: warning: no tests to run
	`))
	if err == nil {
		t.Fatal("want error when summary missing")
	}
}
