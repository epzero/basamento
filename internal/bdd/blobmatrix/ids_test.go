package blobmatrix_test

import (
	"testing"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
	"github.com/epzero/basamento/internal/bdd/modroot"
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
	root, err := modroot.Find()
	if err != nil {
		t.Fatal(err)
	}
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
