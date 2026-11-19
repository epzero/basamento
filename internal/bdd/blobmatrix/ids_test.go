package blobmatrix_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
)

func TestMatrixKeyOutline(t *testing.T) {
	t.Parallel()
	if got := blobmatrix.MatrixKey("BLOB-018-01", 0, 2); got != "BLOB-018-01" {
		t.Fatalf("first outline: %q", got)
	}
	if got := blobmatrix.MatrixKey("BLOB-018-01", 1, 2); got != "BLOB-018-01#01" {
		t.Fatalf("second outline: %q", got)
	}
	if got := blobmatrix.MatrixKey("BLOB-020-01", 0, 1); got != "BLOB-020-01" {
		t.Fatalf("single: %q", got)
	}
}

func TestPickleIDsOpeningFeature(t *testing.T) {
	root := moduleRoot(t)
	ids, err := blobmatrix.PickleIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"BLOB-018-01": true, "BLOB-018-01#01": true,
		"BLOB-019-01": true, "BLOB-019-01#01": true, "BLOB-019-01#02": true, "BLOB-019-01#03": true,
		"BLOB-020-01": true,
	}
	for _, id := range ids {
		if want[id] {
			delete(want, id)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing ids: %v", want)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod")
		}
		dir = parent
	}
}