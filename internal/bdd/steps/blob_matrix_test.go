package steps

import (
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
)

func TestMatrixIDFromGodogPickleKey(t *testing.T) {
	root := moduleRoot(t)
	keys, err := blobmatrix.PickleKeyMap(root)
	if err != nil {
		t.Fatal(err)
	}
	blobPickleMatrixKeys = keys

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
	if got := matrixIDFromGodogScenario(scFile); got != "BLOB-018-01" {
		t.Fatalf("file example: %q", got)
	}
	if got := matrixIDFromGodogScenario(scS3); got != "BLOB-018-01#01" {
		t.Fatalf("s3 example: %q", got)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := moduleRootForBlobMatrix()
	if err != nil {
		t.Fatal(err)
	}
	return root
}