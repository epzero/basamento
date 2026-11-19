package steps

import (
	"sync"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
	"github.com/epzero/basamento/internal/bdd/modroot"
)

func TestMatrixIDFromGodogPickleKey(t *testing.T) {
	root, err := modroot.Find()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := blobmatrix.PickleKeyMap(root)
	if err != nil {
		t.Fatal(err)
	}
	blobPickleMatrixKeys = keys
	blobPickleMatrixErr = nil
	blobPickleMatrixOnce = sync.Once{}

	scFile := &godog.Scenario{
		Uri:      "../../specs/blob/opening.feature",
		Location: &messages.Location{Line: 14},
		Tags:     []*messages.PickleTag{{Name: "@id:BLOB-018-01"}},
	}
	scS3 := &godog.Scenario{
		Uri:      "../../specs/blob/opening.feature",
		Location: &messages.Location{Line: 15},
		Tags:     []*messages.PickleTag{{Name: "@id:BLOB-018-01"}},
	}
	got, err := matrixIDFromGodogScenario(scFile, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if got != "BLOB-018-01" {
		t.Fatalf("file example: %q", got)
	}
	got, err = matrixIDFromGodogScenario(scS3, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if got != "BLOB-018-01#01" {
		t.Fatalf("s3 example: %q", got)
	}
	got, err = matrixIDFromGodogScenario(scFile, "file")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("non-s3 backend: %q", got)
	}
}
