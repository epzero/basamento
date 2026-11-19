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
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "bdd.json")) })

	if err := WriteBDDReport(false); err != nil {
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
