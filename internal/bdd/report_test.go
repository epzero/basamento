package bdd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epzero/basamento/internal/bdd/sqltest"
)

func TestWriteBDDReportSecretFree(t *testing.T) {
	sqltest.ResetSkipCounts()
	root := t.TempDir()
	dir := filepath.Join(root, ".gate")
	if err := writeBDDReportAt(root, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bdd.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, forbidden := range []string{"postgres://", "kit:secret", "127.0.0.1:5432"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("report leaks %q in %s", forbidden, raw)
		}
	}
	var rep map[string]any
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if _, ok := rep["outcome"]; !ok {
		t.Fatal("missing outcome")
	}
}

func TestWriteBDDReportMarksFailure(t *testing.T) {
	sqltest.ResetSkipCounts()
	root := t.TempDir()
	if err := writeBDDReportAt(root, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".gate", "bdd.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep["outcome"] != "fail" {
		t.Fatalf("outcome %v", rep["outcome"])
	}
}
