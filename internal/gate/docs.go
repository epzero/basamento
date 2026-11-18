package gate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Generated blocks keep diagrams in the docs equal to their sources:
// the module map comes from layering.yaml, the roadmap graphs from the
// work-package headings and "Depends:" lines in docs/roadmap/*.md.

type generatedBlock struct {
	name   string
	file   string
	render func(root string) (string, error)
}

var blocks = []generatedBlock{
	{"modules", "docs/architecture/overview.md", ModuleMermaid},
	{"epics", "docs/roadmap/README.md", EpicMermaid},
	{"work-packages", "docs/roadmap/README.md", WorkPackageMermaid},
}

func markers(name string) (string, string) {
	return "<!-- BEGIN GENERATED: " + name + " (go tool gate docs -write) -->", "<!-- END GENERATED: " + name + " -->"
}

// OwnershipOverlaps lists pairs of work packages that own the same directory,
// or one inside the other, without one depending on the other: they could be
// scheduled in parallel and collide.
func OwnershipOverlaps(epics []Epic) []string {
	var wps []WorkPackage
	deps := map[string][]string{}
	for _, e := range epics {
		for _, w := range e.WPs {
			wps = append(wps, w)
			deps[w.ID] = w.Depends
		}
	}
	var reaches func(from, to string, seen map[string]bool) bool
	reaches = func(from, to string, seen map[string]bool) bool {
		for _, d := range deps[from] {
			if d == to || (!seen[d] && reaches(d, to, mark(seen, d))) {
				return true
			}
		}
		return false
	}
	var out []string
	for i, a := range wps {
		for _, b := range wps[i+1:] {
			if reaches(a.ID, b.ID, map[string]bool{}) || reaches(b.ID, a.ID, map[string]bool{}) {
				continue
			}
			for _, x := range a.Owns {
				for _, y := range b.Owns {
					if matchLayer(x, []string{y}) != "" || matchLayer(y, []string{x}) != "" {
						out = append(out, fmt.Sprintf("docs/roadmap: %s owns %s and %s owns %s, and neither depends on the other", a.ID, x, b.ID, y))
					}
				}
			}
		}
	}
	return out
}

func mark(seen map[string]bool, id string) map[string]bool { seen[id] = true; return seen }

// Docs checks (or, with write, refreshes) every generated block, CODEOWNERS,
// the roadmap's ownership rule, and specs/README.md capability statuses.
func Docs(root string, write bool) ([]string, error) {
	var stale []string
	epics, err := LoadRoadmap(root)
	if err != nil {
		return nil, err
	}
	stale = append(stale, OwnershipOverlaps(epics)...)
	readmeErrs, err := CheckSpecsReadme(root)
	if err != nil {
		return nil, err
	}
	stale = append(stale, readmeErrs...)
	if raw, err := os.ReadFile(filepath.Join(root, "review.yaml")); err == nil {
		var rules ReviewRules
		if err := yaml.Unmarshal(raw, &rules); err != nil {
			return nil, fmt.Errorf("review.yaml: %w", err)
		}
		if rules.HighOwner != "" {
			path, want := filepath.Join(root, ".github", "CODEOWNERS"), rules.CodeOwners()
			if have, _ := os.ReadFile(path); string(have) != want {
				if !write {
					stale = append(stale, ".github/CODEOWNERS does not match review.yaml; run `go tool gate docs -write`")
				} else if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, b := range blocks {
		path := filepath.Join(root, b.file)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		begin, end := markers(b.name)
		s := string(content)
		i, j := strings.Index(s, begin), strings.Index(s, end)
		if i < 0 || j < i {
			return nil, fmt.Errorf("%s: missing generated block %q", b.file, b.name)
		}
		body, err := b.render(root)
		if err != nil {
			return nil, err
		}
		want := s[:i+len(begin)] + "\n```mermaid\n" + body + "```\n" + s[j:]
		if want == s {
			continue
		}
		if write {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				return nil, err
			}
			continue
		}
		stale = append(stale, fmt.Sprintf("%s: block %q is out of date; run `go tool gate docs -write`", b.file, b.name))
	}
	return stale, nil
}

// reduce removes edges implied by longer paths (transitive reduction of a DAG).
func reduce(edges map[string]map[string]bool) {
	reach := func(from, to string, skip [2]string) bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for m := range edges[n] {
				if [2]string{n, m} == skip || seen[m] {
					continue
				}
				if m == to {
					return true
				}
				seen[m] = true
				stack = append(stack, m)
			}
		}
		return false
	}
	for u, vs := range edges {
		for v := range vs {
			if reach(u, v, [2]string{u, v}) {
				delete(vs, v)
			}
		}
	}
}

func nodeID(s string) string { return strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(s) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderEdges(b *strings.Builder, edges map[string]map[string]bool) {
	for _, u := range sortedKeys(edges) {
		for _, v := range sortedKeys(edges[u]) {
			fmt.Fprintf(b, "  %s --> %s\n", nodeID(u), nodeID(v))
		}
	}
}

// devLayers are build tooling, not part of the product module map.
var devLayers = map[string]bool{"tools/gate": true, "internal/gate": true, "internal/bdd": true}

// ModuleMermaid renders the product package graph from layering.yaml:
// an arrow means "may import", with implied arrows omitted.
func ModuleMermaid(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if err != nil {
		return "", err
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return "", err
	}
	edges := map[string]map[string]bool{}
	for layer, deps := range r.Layers {
		if devLayers[layer] {
			continue
		}
		edges[layer] = map[string]bool{}
		for _, d := range deps {
			if !devLayers[d] {
				edges[layer][d] = true
			}
		}
	}
	reduce(edges)
	var out strings.Builder
	out.WriteString("flowchart TD\n")
	for _, n := range sortedKeys(edges) {
		fmt.Fprintf(&out, "  %s[\"%s\"]\n", nodeID(n), n)
	}
	renderEdges(&out, edges)
	return out.String(), nil
}

// Epic is one roadmap file; WorkPackage one "### WP-…" heading in it.
type Epic struct {
	ID, Title, File string
	WPs             []WorkPackage
}

// WorkPackage is a roadmap entry with its dependencies and owned directories.
type WorkPackage struct {
	ID, Title string
	Depends   []string
	Owns      []string
}

var (
	epicFile    = regexp.MustCompile(`^(L\d+)-[a-z0-9-]+\.md$`)
	epicHeading = regexp.MustCompile(`^#\s+L\d+\s+—\s+(.+)$`)
	wpTitle     = regexp.MustCompile(`^###\s+(WP-[A-Z0-9]+-\d{2})\s+—\s+(.+)$`)
	wpRef       = regexp.MustCompile(`WP-[A-Z0-9]+-\d{2}`)
	codeSpan    = regexp.MustCompile("`([^`]+)`")
)

// LoadRoadmap parses docs/roadmap/L*-*.md.
func LoadRoadmap(root string) ([]Epic, error) {
	dir := filepath.Join(root, "docs", "roadmap")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var epics []Epic
	for _, e := range entries {
		m := epicFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		ep := Epic{ID: m[1], File: e.Name()}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case epicHeading.MatchString(line):
				ep.Title = epicHeading.FindStringSubmatch(line)[1]
			case wpTitle.MatchString(line):
				t := wpTitle.FindStringSubmatch(line)
				ep.WPs = append(ep.WPs, WorkPackage{ID: t[1], Title: strings.ReplaceAll(t[2], "`", "")})
			case strings.HasPrefix(line, "Depends:") && len(ep.WPs) > 0:
				ep.WPs[len(ep.WPs)-1].Depends = wpRef.FindAllString(line, -1)
			case strings.HasPrefix(line, "Owns:") && len(ep.WPs) > 0:
				for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
					ep.WPs[len(ep.WPs)-1].Owns = append(ep.WPs[len(ep.WPs)-1].Owns, m[1])
				}
			}
		}
		_ = f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
		epics = append(epics, ep)
	}
	// By number, so that L10 follows L9.
	num := func(e Epic) int { n, _ := strconv.Atoi(e.ID[1:]); return n }
	sort.Slice(epics, func(i, j int) bool { return num(epics[i]) < num(epics[j]) })
	return epics, nil
}

// EpicMermaid renders epic-level dependencies (derived from work packages).
func EpicMermaid(root string) (string, error) {
	epics, err := LoadRoadmap(root)
	if err != nil {
		return "", err
	}
	epicOf := map[string]string{}
	for _, e := range epics {
		for _, w := range e.WPs {
			epicOf[w.ID] = e.ID
		}
	}
	edges := map[string]map[string]bool{}
	var out strings.Builder
	out.WriteString("flowchart LR\n")
	for _, e := range epics {
		edges[e.ID] = map[string]bool{}
		fmt.Fprintf(&out, "  %s[\"%s %s\"]\n", nodeID(e.ID), e.ID, e.Title)
	}
	for _, e := range epics {
		for _, w := range e.WPs {
			for _, d := range w.Depends {
				if de, ok := epicOf[d]; ok && de != e.ID {
					edges[de][e.ID] = true
				}
			}
		}
	}
	reduce(edges)
	renderEdges(&out, edges)
	return out.String(), nil
}

// WorkPackageMermaid renders every work package grouped by epic.
func WorkPackageMermaid(root string) (string, error) {
	epics, err := LoadRoadmap(root)
	if err != nil {
		return "", err
	}
	edges := map[string]map[string]bool{}
	var out strings.Builder
	out.WriteString("flowchart LR\n")
	for _, e := range epics {
		fmt.Fprintf(&out, "  subgraph %s[\"%s %s\"]\n", nodeID(e.ID+"-epic"), e.ID, e.Title)
		for _, w := range e.WPs {
			fmt.Fprintf(&out, "    %s[\"%s<br/>%s\"]\n", nodeID(w.ID), w.ID, w.Title)
			if edges[w.ID] == nil {
				edges[w.ID] = map[string]bool{}
			}
		}
		out.WriteString("  end\n")
	}
	for _, e := range epics {
		for _, w := range e.WPs {
			for _, d := range w.Depends {
				if edges[d] == nil {
					edges[d] = map[string]bool{}
				}
				edges[d][w.ID] = true
			}
		}
	}
	reduce(edges)
	renderEdges(&out, edges)
	return out.String(), nil
}
