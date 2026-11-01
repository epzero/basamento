// Package gate implements the repository's QA gate checks (spec lint,
// traceability, the approval lock, layering, red→green, review plans) and the
// coordination of agent sessions (identities, worktrees, hand-offs, work
// package states). See docs/engineering/qa-gate.md and agent-workflow.md.
package gate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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

var (
	reqHeading  = regexp.MustCompile(`^###\s+(REQ-[A-Z]+-\d{3})\b`)
	reqRetired  = regexp.MustCompile(`^Status:\s*Retired\b`)
	wpHeading   = regexp.MustCompile(`^###\s+(WP-[A-Z0-9]+-\d{2})\b`)
	scenarioID  = regexp.MustCompile(`^[A-Z]+-\d{3}-\d{2}$`)
	reqTag      = regexp.MustCompile(`^@REQ-[A-Z]+-\d{3}$`)
	wpTag       = regexp.MustCompile(`^@wp:(WP-[A-Z0-9]+-\d{2})$`)
	untilTag    = regexp.MustCompile(`^@until:(\d{4}-\d{2}-\d{2})$`)
	issueTag    = regexp.MustCompile(`^@issue:[A-Za-z0-9][A-Za-z0-9-]*$`)
	simpleTags  = map[string]bool{"@live": true, "@slow": true, "@journey": true, "@eval": true, "@smoke": true, "@security": true, "@cost": true, "@contract": true, "@quarantine": true, "@planned": true}
	prefixTags  = []string{"@id:", "@wp:", "@until:", "@issue:", "@level:", "@runtime:", "@profile:"}
	enumTags    = map[string][]string{"@level:": {"component", "system"}, "@runtime:": {"any", "local", "temporal"}, "@profile:": {"embedded", "cloud"}}
	bannedTags  = map[string]bool{"@wip": true, "@skip": true, "@ignore": true, "@focus": true, "@only": true}
	maxSteps    = 12
	criticalTag = map[string]bool{"@security": true, "@cost": true, "@contract": true}
)

// Scenario is one Scenario or Scenario Outline with its effective tags.
type Scenario struct {
	ID         string
	Name       string
	File       string
	Line       int64
	Tags       []string // effective: feature + rule + scenario (+ examples)
	OwnTags    []string // declared on the scenario itself
	Reqs       []string
	IsPlanned  bool   // tagged @planned: approved, not implemented, excluded from the gate run
	WP         string // work package from @wp:WP-…
	Hash       string // normalized content hash
	Steps      int
	Capability string
}

// Spec is the loaded specs tree.
type Spec struct {
	Root         string
	Scenarios    []*Scenario
	Requirements map[string]string // REQ id -> file
	Retired      map[string]bool   // requirements marked "Status: Retired": kept for their id, covered by no scenario
	WorkPackages map[string]bool
	ParseErrors  []string
}

// Load reads requirements, features and work packages under root (the repo root).
func Load(root string) (*Spec, error) {
	s := &Spec{Root: root, Requirements: map[string]string{}, Retired: map[string]bool{}, WorkPackages: map[string]bool{}}
	specsDir := filepath.Join(root, "specs")
	err := filepath.WalkDir(specsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.Name() == "requirements.md":
			return s.loadRequirements(path, rel)
		case strings.HasSuffix(d.Name(), ".feature"):
			return s.loadFeature(path, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	roadmap, _ := filepath.Glob(filepath.Join(root, "docs", "roadmap", "L*.md"))
	for _, f := range roadmap {
		if err := scanHeadings(f, wpHeading, func(id string) { s.WorkPackages[id] = true }); err != nil {
			return nil, err
		}
	}
	sort.Slice(s.Scenarios, func(i, j int) bool { return s.Scenarios[i].ID < s.Scenarios[j].ID })
	return s, nil
}

// loadRequirements reads the "### REQ-…" headings of one requirements.md and
// the "Status: Retired" line that may follow a heading.
func (s *Spec) loadRequirements(path, rel string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	current := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch m := reqHeading.FindStringSubmatch(line); {
		case m != nil:
			current = m[1]
			if prev, ok := s.Requirements[current]; ok {
				s.ParseErrors = append(s.ParseErrors, fmt.Sprintf("%s: duplicate requirement %s (also in %s)", rel, current, prev))
			}
			s.Requirements[current] = rel
		case strings.HasPrefix(line, "#"):
			current = ""
		case current != "" && reqRetired.MatchString(line):
			s.Retired[current] = true
		}
	}
	return sc.Err()
}

func scanHeadings(path string, re *regexp.Regexp, add func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			add(m[1])
		}
	}
	return sc.Err()
}

func (s *Spec) loadFeature(path, rel string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	n := 0
	newID := func() string { n++; return strconv.Itoa(n) }
	doc, err := gherkin.ParseGherkinDocument(f, newID)
	if err != nil {
		s.ParseErrors = append(s.ParseErrors, fmt.Sprintf("%s: %v", rel, err))
		return nil
	}
	if doc.Feature == nil {
		s.ParseErrors = append(s.ParseErrors, fmt.Sprintf("%s: no Feature", rel))
		return nil
	}
	capability := filepath.Base(filepath.Dir(rel))
	byAst := map[string]*Scenario{}
	addScenario := func(sc *messages.Scenario, parentTags []*messages.Tag) {
		own := tagNames(sc.Tags)
		eff := append(tagNames(parentTags), own...)
		for _, ex := range sc.Examples {
			eff = append(eff, tagNames(ex.Tags)...)
		}
		out := &Scenario{Name: sc.Name, File: rel, Line: sc.Location.Line, Tags: uniq(eff), OwnTags: own, Steps: len(sc.Steps), Capability: capability}
		for _, t := range own {
			if strings.HasPrefix(t, "@id:") {
				if out.ID != "" {
					s.ParseErrors = append(s.ParseErrors, fmt.Sprintf("%s:%d: scenario %q has more than one @id tag", rel, sc.Location.Line, sc.Name))
				}
				out.ID = strings.TrimPrefix(t, "@id:")
			}
		}
		for _, t := range out.Tags {
			if reqTag.MatchString(t) {
				out.Reqs = append(out.Reqs, strings.TrimPrefix(t, "@"))
			}
			if m := wpTag.FindStringSubmatch(t); m != nil {
				out.WP = m[1]
			}
			if t == "@planned" {
				out.IsPlanned = true
			}
		}
		sort.Strings(out.Reqs)
		byAst[sc.Id] = out
		s.Scenarios = append(s.Scenarios, out)
	}
	for _, ch := range doc.Feature.Children {
		if ch.Scenario != nil {
			addScenario(ch.Scenario, doc.Feature.Tags)
		}
		if ch.Rule != nil {
			for _, rc := range ch.Rule.Children {
				if rc.Scenario != nil {
					addScenario(rc.Scenario, append(append([]*messages.Tag{}, doc.Feature.Tags...), ch.Rule.Tags...))
				}
			}
		}
	}
	// Hash from pickles: background steps, outline expansion and arguments are included;
	// tags and step keywords are not (they do not change meaning).
	hashes := map[string]*strings.Builder{}
	for _, p := range gherkin.Pickles(*doc, rel, newID) {
		if len(p.AstNodeIds) == 0 {
			continue
		}
		b, ok := hashes[p.AstNodeIds[0]]
		if !ok {
			b = &strings.Builder{}
			hashes[p.AstNodeIds[0]] = b
			fmt.Fprintf(b, "name:%s\n", norm(p.Name))
		}
		b.WriteString("--\n")
		for _, st := range p.Steps {
			fmt.Fprintf(b, "step:%s\n", norm(st.Text))
			if st.Argument != nil && st.Argument.DocString != nil {
				fmt.Fprintf(b, "doc:%s\n", strings.TrimSpace(st.Argument.DocString.Content))
			}
			if st.Argument != nil && st.Argument.DataTable != nil {
				for _, r := range st.Argument.DataTable.Rows {
					cells := make([]string, 0, len(r.Cells))
					for _, c := range r.Cells {
						cells = append(cells, norm(c.Value))
					}
					fmt.Fprintf(b, "row:%s\n", strings.Join(cells, "|"))
				}
			}
		}
	}
	for ast, b := range hashes {
		if sc, ok := byAst[ast]; ok {
			sum := sha256.Sum256([]byte(b.String()))
			sc.Hash = "sha256:" + hex.EncodeToString(sum[:])
		}
	}
	return nil
}

func tagNames(ts []*messages.Tag) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var spaces = regexp.MustCompile(`\s+`)

func norm(s string) string { return spaces.ReplaceAllString(strings.TrimSpace(s), " ") }

// Critical reports whether a scenario carries a tag that forces tier P0.
func (sc *Scenario) Critical() bool {
	for _, t := range sc.Tags {
		if criticalTag[t] {
			return true
		}
	}
	return false
}

// Has reports whether the scenario has the effective tag t.
func (sc *Scenario) Has(t string) bool {
	for _, x := range sc.Tags {
		if x == t {
			return true
		}
	}
	return false
}
