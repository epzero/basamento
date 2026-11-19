package conformance_test

import (
	"fmt"
	"regexp"
	"strconv"
)

// s3MatrixNoS3Marker lists matrix scenario ids that must not emit an S3 open marker.
// Keys are MatrixKey ids (first outline example bare id, rest #NN). Reasons quote the
// examples row from specs/blob/opening.feature. Ids are bound to feature line via
// blobmatrix.PickleKeyForRuntime; inserting or reordering outline rows changes line
// keys and TestS3Matrix fails until this map is updated (it must not pass silently).
var s3MatrixNoS3Marker = map[string]string{
	"BLOB-018-01":    `opening.feature row: "a file URL of a temporary directory"`,
	"BLOB-019-01":    `opening.feature row: "mem://"`,
	"BLOB-019-01#01": `opening.feature row: "ftp://example.test/blobs"`,
	"BLOB-019-01#02": `opening.feature row: "/var/lib/kit/blobs"`,
	"BLOB-019-01#03": `opening.feature row: "" (empty url)`,
	"BLOB-020-01":    `opening.feature: explicit file URL of subdirectory "blobs"`,
}

var (
	scenarioSummaryRE = regexp.MustCompile(`(\d+)\s+scenarios`)
	passedSummaryRE   = regexp.MustCompile(`(\d+)\s+passed`)
	s3OpenMarkerRE    = regexp.MustCompile(`(?m)^blob-conformance backend=s3 opened=s3 id=([^\s]+) scenario="([^"]*)"`)
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

type s3Marker struct {
	count int
	names []string
}

func parseS3OpenMarkers(output string) map[string]s3Marker {
	out := make(map[string]s3Marker)
	for _, m := range s3OpenMarkerRE.FindAllStringSubmatch(output, -1) {
		if len(m) < 3 {
			continue
		}
		id := m[1]
		name := m[2]
		e := out[id]
		e.count++
		e.names = append(e.names, name)
		out[id] = e
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
		m := markers[id]
		if reason, skip := s3MatrixNoS3Marker[id]; skip {
			allow++
			if m.count > 0 {
				return fmt.Errorf("scenario %s on allow-list (%s) but has %d s3 marker(s) (godog: %q)",
					id, reason, m.count, firstMarkerName(m))
			}
			continue
		}
		if m.count < 1 {
			return fmt.Errorf("scenario %s expected s3 open marker, got none", id)
		}
		marked++
	}
	if marked+allow != len(matrixIDs) {
		return fmt.Errorf("marked %d + allow-list %d != selected %d", marked, allow, len(matrixIDs))
	}
	return nil
}

func firstMarkerName(m s3Marker) string {
	if len(m.names) == 0 {
		return ""
	}
	return m.names[0]
}
