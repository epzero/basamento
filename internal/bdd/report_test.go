package bdd

import (
	"context"
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

func TestBDDReportStrictURLProbeFailSecretFree(t *testing.T) {
	sqltest.ResetPostgresForTest()
	sqltest.ResetSkipCounts()
	root := t.TempDir()
	t.Setenv("GATE_STRICT", "1")
	t.Setenv("BDD_POSTGRES_URL", "postgres://leakuser:leakpass@leak-host.invalid:9/leakdb?connect_timeout=1")
	initErr := sqltest.InitPostgres(context.Background())
	if initErr == nil {
		t.Fatal("expected init failure")
	}
	if err := writeBDDReportAt(root, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".gate", "bdd.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, forbidden := range []string{"leakuser", "leakpass", "leak-host", "leakdb", "postgres://", "127.0.0.1"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("bdd.json leaks %q: %s", forbidden, raw)
		}
	}
	if strings.Contains(initErr.Error(), "leakpass") {
		t.Fatalf("init error leaked secret: %v", initErr)
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
