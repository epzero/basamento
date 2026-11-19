// Package blobmatrix derives stable per-example ids for blob matrix verification.
package blobmatrix

import (
	"fmt"
	"os"
	"path/filepath"
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

// MatrixKey returns the id used in S3 conformance markers for one scenario example.
func MatrixKey(id string, exampleIndex, examplesForID int) string {
	if examplesForID <= 1 {
		return id
	}
	if exampleIndex == 0 {
		return id
	}
	return id + fmt.Sprintf("#%02d", exampleIndex)
}

// PickleKeyMap maps a godog pickle identity (uri and ast node ids) to its matrix id.
func PickleKeyMap(root string) (map[string]string, error) {
	blobDir := filepath.Join(root, "specs", "blob")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".feature") {
			continue
		}
		rel := filepath.Join("specs", "blob", e.Name())
		part, err := pickleKeyMapInFeature(filepath.Join(blobDir, e.Name()), rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		for k, v := range part {
			out[k] = v
		}
	}
	return out, nil
}

// PickleKeyForRuntime builds the lookup key for a godog scenario pickle.
func PickleKeyForRuntime(uri string, line int64) string {
	return normalizePickleURI(uri) + "|" + strconv.FormatInt(line, 10)
}

func pickleKeyForPickle(p *messages.Pickle) string {
	var line int64
	if p.Location != nil {
		line = p.Location.Line
	}
	return PickleKeyForRuntime(p.Uri, line)
}

func normalizePickleURI(uri string) string {
	uri = strings.TrimPrefix(uri, "../../")
	uri = strings.TrimPrefix(uri, "../")
	uri = strings.TrimPrefix(uri, "/")
	return uri
}

func pickleKeyMapInFeature(path, rel string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	doc, err := gherkin.ParseGherkinDocument(f, func() string { n++; return strconv.Itoa(n) })
	if err != nil {
		return nil, err
	}
	if doc.Feature == nil {
		return nil, nil
	}
	pickles := gherkin.Pickles(*doc, rel, func() string { n++; return strconv.Itoa(n) })
	outlineTotal := map[string]int{}
	for _, p := range pickles {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		if id := scenarioIDTag(p.Tags); id != "" {
			outlineTotal[id]++
		}
	}
	seq := map[string]int{}
	out := map[string]string{}
	for _, p := range pickles {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		id := scenarioIDTag(p.Tags)
		key := pickleKeyForPickle(p)
		if id == "" {
			out[key] = p.Name
			continue
		}
		idx := seq[id]
		seq[id]++
		out[key] = MatrixKey(id, idx, outlineTotal[id])
	}
	return out, nil
}

// ExampleCounts returns how many matrix pickles each @id scenario tag has under specs/blob.
func ExampleCounts(root string) (map[string]int, error) {
	blobDir := filepath.Join(root, "specs", "blob")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".feature") {
			continue
		}
		rel := filepath.Join("specs", "blob", e.Name())
		part, err := exampleCountsInFeature(filepath.Join(blobDir, e.Name()), rel)
		if err != nil {
			return nil, err
		}
		for k, v := range part {
			counts[k] += v
		}
	}
	return counts, nil
}

func exampleCountsInFeature(path, rel string) (map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	doc, err := gherkin.ParseGherkinDocument(f, func() string { n++; return strconv.Itoa(n) })
	if err != nil {
		return nil, err
	}
	if doc.Feature == nil {
		return nil, nil
	}
	out := map[string]int{}
	for _, p := range gherkin.Pickles(*doc, rel, func() string { n++; return strconv.Itoa(n) }) {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		if id := scenarioIDTag(p.Tags); id != "" {
			out[id]++
		}
	}
	return out, nil
}

// PickleIDs returns matrix ids for every non-excluded pickle under specs/blob.
func PickleIDs(root string) ([]string, error) {
	blobDir := filepath.Join(root, "specs", "blob")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".feature") {
			continue
		}
		rel := filepath.Join("specs", "blob", e.Name())
		part, err := pickleIDsInFeature(filepath.Join(blobDir, e.Name()), rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		ids = append(ids, part...)
	}
	sort.Strings(ids)
	return ids, nil
}

func pickleIDsInFeature(path, rel string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	doc, err := gherkin.ParseGherkinDocument(f, func() string { n++; return strconv.Itoa(n) })
	if err != nil {
		return nil, err
	}
	if doc.Feature == nil {
		return nil, nil
	}
	pickles := gherkin.Pickles(*doc, rel, func() string { n++; return strconv.Itoa(n) })
	outlineTotal := map[string]int{}
	for _, p := range pickles {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		if id := scenarioIDTag(p.Tags); id != "" {
			outlineTotal[id]++
		}
	}
	seq := map[string]int{}
	var ids []string
	for _, p := range pickles {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		id := scenarioIDTag(p.Tags)
		if id == "" {
			ids = append(ids, p.Name)
			continue
		}
		idx := seq[id]
		seq[id]++
		ids = append(ids, MatrixKey(id, idx, outlineTotal[id]))
	}
	return ids, nil
}

func scenarioIDTag(tags []*messages.PickleTag) string {
	for _, t := range tags {
		if strings.HasPrefix(t.Name, "@id:") {
			return strings.TrimPrefix(t.Name, "@id:")
		}
	}
	return ""
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