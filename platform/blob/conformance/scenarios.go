package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
)

var excludedTags = map[string]bool{
	"@planned": true, "@live": true, "@eval": true, "@journey": true,
	"@slow": true, "@quarantine": true, "@profile:embedded": true,
}

// ExpectedBlobMatrixScenarios counts specs/blob scenarios that match the S3
// conformance tag expression (gate tags plus ~@profile:embedded).
func ExpectedBlobMatrixScenarios(root string) (int, error) {
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
		n, err := countFeature(filepath.Join(blobDir, e.Name()))
		if err != nil {
			return 0, fmt.Errorf("%s: %w", e.Name(), err)
		}
		total += n
	}
	return total, nil
}

func countFeature(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	doc, err := gherkin.ParseGherkinDocument(f, func() string { n++; return strconvItoa(n) })
	if err != nil {
		return 0, err
	}
	if doc.Feature == nil {
		return 0, nil
	}
	count := 0
	add := func(sc *messages.Scenario, parent []*messages.Tag) {
		eff := append(tagNames(parent), tagNames(sc.Tags)...)
		for _, ex := range sc.Examples {
			eff = append(eff, tagNames(ex.Tags)...)
		}
		if excluded(eff) {
			return
		}
		count++
	}
	for _, ch := range doc.Feature.Children {
		if ch.Scenario != nil {
			add(ch.Scenario, doc.Feature.Tags)
		}
		if ch.Rule != nil {
			for _, rc := range ch.Rule.Children {
				if rc.Scenario != nil {
					tags := append(append([]*messages.Tag{}, doc.Feature.Tags...), ch.Rule.Tags...)
					add(rc.Scenario, tags)
				}
			}
		}
	}
	return count, nil
}

func excluded(tags []string) bool {
	for _, t := range tags {
		if excludedTags[t] {
			return true
		}
	}
	return false
}

func tagNames(tags []*messages.Tag) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

func strconvItoa(n int) string {
	return fmt.Sprintf("%d", n)
}
