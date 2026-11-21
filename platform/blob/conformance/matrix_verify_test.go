package conformance_test

import (
	"strings"
	"testing"
)

func TestVerifyS3MatrixOutputHappy(t *testing.T) {
	t.Parallel()
	ids := []string{"BLOB-001-01", "BLOB-002-01", "BLOB-003-01", "BLOB-004-01", "BLOB-005-01"}
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(`blob-conformance backend=s3 opened=s3 id=` + id + ` scenario="x"` + "\n")
	}
	b.WriteString("5 scenarios (5 passed)\n5 steps (5 passed)\n")
	if err := verifyS3MatrixOutput(b.String(), 5, ids); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyS3MatrixOutputZeroMarkers(t *testing.T) {
	t.Parallel()
	ids := make([]string, 27)
	for i := range ids {
		ids[i] = "BLOB-XXX-01"
	}
	out := "27 scenarios (27 passed)\n27 steps (27 passed)\n"
	if err := verifyS3MatrixOutput(out, 27, ids); err == nil {
		t.Fatal("want failure without markers")
	}
}

func TestVerifyS3MatrixOutputAllowListViolation(t *testing.T) {
	t.Parallel()
	ids := []string{"BLOB-018-01", "BLOB-019-01"}
	out := `blob-conformance backend=s3 opened=s3 id=BLOB-018-01 scenario="x"
2 scenarios (2 passed)
`
	if err := verifyS3MatrixOutput(out, 2, ids); err == nil {
		t.Fatal("want failure when allow-list scenario has marker")
	}
}

func TestVerifyS3MatrixOutputMissingMarker(t *testing.T) {
	t.Parallel()
	ids := []string{"BLOB-001-01", "BLOB-002-01"}
	out := `blob-conformance backend=s3 opened=s3 id=BLOB-001-01 scenario="x"
2 scenarios (2 passed)
`
	if err := verifyS3MatrixOutput(out, 2, ids); err == nil {
		t.Fatal("want failure when marker missing for non-allow scenario")
	}
}

func TestVerifyS3MatrixOutputSummaryMismatch(t *testing.T) {
	t.Parallel()
	ids := []string{"BLOB-001-01"}
	out := `blob-conformance backend=s3 opened=s3 id=BLOB-001-01 scenario="x"
1 scenarios (0 passed)
`
	if err := verifyS3MatrixOutput(out, 1, ids); err == nil {
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
