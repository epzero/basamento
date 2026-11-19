package conformance_test

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/epzero/basamento/internal/bdd/blobmatrix"
)

// s3MatrixNoS3Marker lists matrix scenario ids that must not emit an S3 open marker.
var s3MatrixNoS3Marker = map[string]string{
	"BLOB-018-01":      "explicit file:// open in scheme outline",
	"BLOB-019-01":      "unsupported URL; fails before Open",
	"BLOB-019-01#01":   "unsupported URL; fails before Open",
	"BLOB-019-01#02":   "unsupported URL; fails before Open",
	"BLOB-019-01#03":   "unsupported URL; fails before Open",
	"BLOB-020-01":      "explicit file URL of subdirectory",
}

var (
	scenarioSummaryRE = regexp.MustCompile(`(\d+)\s+scenarios`)
	passedSummaryRE   = regexp.MustCompile(`(\d+)\s+passed`)
	s3OpenMarkerRE    = regexp.MustCompile(`(?m)^blob-conformance backend=s3 opened=s3 id=([^\s]+)`)
)

func parseGodogSummary(output string) (scenarios, passed int, err error) {
	sm := scenarioSummaryRE.FindStringSubmatch(output)
	if sm == nil {
		return 0, 0, fmt.Errorf("no scenario count in godog output")
	}
	scenarios, err = strconv.Atoi(sm[1])
	if err != nil {
		return 0, 0, err
	}
	pm := passedSummaryRE.FindStringSubmatch(output)
	if pm == nil {
		return scenarios, 0, fmt.Errorf("no passed count in godog output")
	}
	passed, err = strconv.Atoi(pm[1])
	if err != nil {
		return 0, 0, err
	}
	if scenarios == 0 {
		return 0, 0, fmt.Errorf("godog selected zero scenarios")
	}
	return scenarios, passed, nil
}

func parseS3OpenMarkers(output string) map[string]int {
	out := make(map[string]int)
	for _, m := range s3OpenMarkerRE.FindAllStringSubmatch(output, -1) {
		if len(m) < 2 {
			continue
		}
		out[m[1]]++
	}
	return out
}

func verifyS3MatrixOutput(output string, wantScenarios int, matrixIDs []string) error {
	got, passed, err := parseGodogSummary(output)
	if err != nil {
		return err
	}
	if got != passed || got != wantScenarios {
		return fmt.Errorf("godog ran %d scenarios (%d passed), want exactly %d", got, passed, wantScenarios)
	}
	if len(matrixIDs) != wantScenarios {
		return fmt.Errorf("matrix id list length %d != want %d", len(matrixIDs), wantScenarios)
	}
	markers := parseS3OpenMarkers(output)
	marked := 0
	allow := 0
	for _, id := range matrixIDs {
		n := markers[id]
		if reason, skip := s3MatrixNoS3Marker[id]; skip {
			allow++
			if n > 0 {
				return fmt.Errorf("scenario %s on allow-list (%s) but has %d s3 marker(s)", id, reason, n)
			}
			continue
		}
		if n < 1 {
			return fmt.Errorf("scenario %s expected s3 open marker, got none", id)
		}
		marked++
	}
	if marked+allow != len(matrixIDs) {
		return fmt.Errorf("marked %d + allow-list %d != selected %d", marked, allow, len(matrixIDs))
	}
	return nil
}

func blobMatrixPickleIDs(root string) ([]string, error) {
	return blobmatrix.PickleIDs(root)
}