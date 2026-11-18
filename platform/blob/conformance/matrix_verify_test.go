package conformance_test

import (
	"strings"
	"testing"
)

func TestVerifyS3MatrixOutputHappy(t *testing.T) {
	t.Parallel()
	out := stringsHappy()
	want := countS3BackendMarkers(out)
	if err := verifyS3MatrixOutput(out, want); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyS3MatrixOutputZeroMarkers(t *testing.T) {
	t.Parallel()
	out := "27 scenarios (27 passed)\n27 steps (27 passed)\n"
	if err := verifyS3MatrixOutput(out, 27); err == nil {
		t.Fatal("want failure without markers")
	}
}

func TestVerifyS3MatrixOutputFewerMarkers(t *testing.T) {
	t.Parallel()
	out := "blob-conformance backend=s3\n3 scenarios (3 passed)\n"
	if err := verifyS3MatrixOutput(out, 3); err == nil {
		t.Fatal("want failure when markers < scenarios")
	}
}

func TestVerifyS3MatrixOutputSummaryMismatch(t *testing.T) {
	t.Parallel()
	out := "blob-conformance backend=s3\n3 scenarios (2 passed)\n"
	if err := verifyS3MatrixOutput(out, 1); err == nil {
		t.Fatal("want failure on passed mismatch")
	}
}

func TestParseGodogSummaryMissing(t *testing.T) {
	t.Parallel()
	if _, _, err := parseGodogSummary("no summary here"); err == nil {
		t.Fatal("want error")
	}
}

func TestParseGodogSummaryZeroSelection(t *testing.T) {
	t.Parallel()
	if _, _, err := parseGodogSummary("0 scenarios (0 passed)"); err == nil {
		t.Fatal("want error on zero scenarios")
	}
}

func stringsHappy() string {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString("blob-conformance backend=s3\n")
	}
	b.WriteString("5 scenarios (5 passed)\n5 steps (5 passed)\n")
	return b.String()
}
