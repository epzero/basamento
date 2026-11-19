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

// PickleEntry is one matrix scenario example: stable id and godog lookup key (feature uri + line).
type PickleEntry struct {
	ID  string
	Key string
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

// PickleKeyForRuntime builds the lookup key for a godog scenario pickle.
func PickleKeyForRuntime(uri string, line int64) string {
	return normalizePickleURI(uri) + "|" + strconv.FormatInt(line, 10)
}

func normalizePickleURI(uri string) string {
	uri = strings.TrimPrefix(uri, "../../")
	uri = strings.TrimPrefix(uri, "../")
	uri = strings.TrimPrefix(uri, "/")
	return uri
}

// Load scans specs/blob once and returns every non-excluded matrix pickle.
func Load(root string) ([]PickleEntry, error) {
	blobDir := filepath.Join(root, "specs", "blob")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return nil, err
	}
	var out []PickleEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".feature") {
			continue
		}
		rel := filepath.Join("specs", "blob", e.Name())
		part, err := pickleEntriesInFeature(filepath.Join(blobDir, e.Name()), rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out = append(out, part...)
	}
	return out, nil
}

// PickleKeyMap maps a godog pickle identity (uri and line) to its matrix id.
func PickleKeyMap(root string) (map[string]string, error) {
	entries, err := Load(root)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Key] = e.ID
	}
	return m, nil
}

// PickleIDs returns matrix ids for every non-excluded pickle under specs/blob.
func PickleIDs(root string) ([]string, error) {
	entries, err := Load(root)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	sort.Strings(ids)
	return ids, nil
}

func pickleEntriesInFeature(path, rel string) ([]PickleEntry, error) {
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
	var out []PickleEntry
	for _, p := range pickles {
		if excluded(pickleTagNames(p.Tags)) {
			continue
		}
		key := pickleKeyForPickle(p)
		id := scenarioIDTag(p.Tags)
		matrixID := p.Name
		if id != "" {
			idx := seq[id]
			seq[id]++
			matrixID = MatrixKey(id, idx, outlineTotal[id])
		}
		out = append(out, PickleEntry{ID: matrixID, Key: key})
	}
	return out, nil
}

func pickleKeyForPickle(p *messages.Pickle) string {
	var line int64
	if p.Location != nil {
		line = p.Location.Line
	}
	return PickleKeyForRuntime(p.Uri, line)
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
