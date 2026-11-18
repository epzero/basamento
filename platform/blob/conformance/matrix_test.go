package conformance_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
)

// excludedScenarioTags mirrors defaultTags plus @profile:embedded in internal/bdd/bdd_test.go.
var excludedScenarioTags = map[string]bool{
	"@planned": true, "@live": true, "@eval": true, "@journey": true,
	"@slow": true, "@quarantine": true, "@profile:embedded": true,
}

var (
	scenarioSummaryRE = regexp.MustCompile(`(\d+)\s+scenarios`)
	passedSummaryRE   = regexp.MustCompile(`(\d+)\s+passed`)
	backendMarkerRE   = regexp.MustCompile(`(?m)^blob-conformance backend=s3\s*$`)
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

func countS3BackendMarkers(output string) int {
	return len(backendMarkerRE.FindAllString(output, -1))
}

func verifyS3MatrixOutput(output string, wantScenarios int) error {
	got, passed, err := parseGodogSummary(output)
	if err != nil {
		return err
	}
	if got != passed || got != wantScenarios {
		return fmt.Errorf("godog ran %d scenarios (%d passed), want exactly %d", got, passed, wantScenarios)
	}
	markers := countS3BackendMarkers(output)
	if markers != wantScenarios {
		return fmt.Errorf("s3 backend markers %d, want %d", markers, wantScenarios)
	}
	return nil
}

func expectedBlobMatrixScenarios(root string) (int, error) {
	blobDir := filepath.Join(root, "specs", "blob")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".feature") {
			continue
		}
		rel := filepath.Join("specs", "blob", e.Name())
		n, err := countFeaturePickles(filepath.Join(blobDir, e.Name()), rel)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", e.Name(), err)
		}
		total += n
	}
	return total, nil
}

func countFeaturePickles(path, rel string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	doc, err := gherkin.ParseGherkinDocument(f, func() string { n++; return strconv.Itoa(n) })
	if err != nil {
		return 0, err
	}
	if doc.Feature == nil {
		return 0, nil
	}
	count := 0
	for _, p := range gherkin.Pickles(*doc, rel, func() string { n++; return strconv.Itoa(n) }) {
		tags := pickleTagNames(p.Tags)
		if excluded(tags) {
			continue
		}
		count++
	}
	return count, nil
}

func excluded(tags []string) bool {
	for _, t := range tags {
		if excludedScenarioTags[t] {
			return true
		}
	}
	return false
}

func pickleTagNames(tags []*messages.PickleTag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}
