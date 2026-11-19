package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LayeringRules is layering.yaml: which module packages may import which.
type LayeringRules struct {
	Module string `yaml:"module"`
	// Layers maps a package path (relative to the module, prefix match on
	// whole path segments) to the module packages it may import.
	Layers map[string][]string `yaml:"layers"`
	// TestOnly packages may be imported only from _test.go files and from the
	// listed exceptions (e.g. internal/bdd).
	TestOnly   []string `yaml:"test_only"`
	TestOnlyOK []string `yaml:"test_only_allowed_from"`
	// DenyExternal maps a layer to import path prefixes it must not use.
	DenyExternal map[string][]string `yaml:"deny_external"`
	// known is the set of package paths in this module, filled by Layering
	// from go list. A module-path import that is not in it belongs to a
	// nested module (for example presets/codegen), which is external to this
	// graph like any other dependency.
	known map[string]bool
}

type goPkg struct {
	ImportPath     string
	Dir            string
	GoFiles        []string
	CgoFiles       []string
	TestGoFiles    []string
	XTestGoFiles   []string
	IgnoredGoFiles []string
	Imports        []string
	TestImports    []string
	XTestImports   []string
}

type importKind int

const (
	kindProduction importKind = iota
	kindTest
	kindExcluded
)

// importCheck describes which import list or file is being checked.
type importCheck struct {
	kind         importKind
	fileName     string
	useTestRules bool
	unlayered    bool
	parseErr     error
}

func (c importCheck) suffix() string {
	switch c.kind {
	case kindTest:
		return ", in a test"
	case kindExcluded:
		return ", in an excluded file " + c.fileName
	default:
		return ""
	}
}

func (c importCheck) testRules() bool {
	if c.kind == kindTest || c.useTestRules {
		return true
	}
	return false
}

// Layering checks production and test imports from go list and from every other
// module .go file (build-excluded sources included) against layering.yaml.
func Layering(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if err != nil {
		return nil, err
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("layering.yaml: %w", err)
	}
	errs := layeringRulesKeyErrors(r)
	pkgs, err := goListPackages(root)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		known[p.ImportPath] = true
	}
	r.known = known
	attributed, err := attributedGoFiles(pkgs)
	if err != nil {
		return nil, err
	}
	add := func(msg string) {
		if msg != "" {
			errs = append(errs, msg)
		}
	}
	checkOne := func(rel, layer string, imports []string, ctx importCheck) {
		if ctx.unlayered {
			add(fmt.Sprintf("%s: package not assigned to a layer in layering.yaml", rel))
			return
		}
		if ctx.parseErr != nil {
			add(fmt.Sprintf("%s: %v", rel, ctx.parseErr))
			return
		}
		for _, imp := range imports {
			add(checkImport(r, rel, layer, imp, ctx))
		}
	}
	layerKeys := keys(r.Layers)
	for _, p := range pkgs {
		if p.Dir != "" && dirPathSkipped(root, p.Dir) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, r.Module), "/")
		layer := matchLayer(rel, layerKeys)
		if layer == "" {
			add(fmt.Sprintf("%s: package not assigned to a layer in layering.yaml", rel))
			continue
		}
		for _, imp := range p.Imports {
			add(checkImport(r, rel, layer, imp, importCheck{kind: kindProduction}))
		}
		for _, imp := range p.TestImports {
			add(checkImport(r, rel, layer, imp, importCheck{kind: kindTest}))
		}
		for _, imp := range p.XTestImports {
			add(checkImport(r, rel, layer, imp, importCheck{kind: kindTest}))
		}
	}
	if err := layeringWalkExtra(root, r, attributed, checkOne, add); err != nil {
		return nil, err
	}
	sort.Strings(errs)
	// De-duplicate while keeping order stable after sort.
	out := errs[:0]
	for i, e := range errs {
		if i == 0 || e != errs[i-1] {
			out = append(out, e)
		}
	}
	return out, nil
}

func goListPackages(root string) ([]goPkg, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	cmd.Env = layeringGoEnv()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, stderr.String())
	}
	var pkgs []goPkg
	dec := json.NewDecoder(&out)
	for {
		var p goPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// checkImport returns a violation message for one import, or "" if allowed.
func checkImport(r LayeringRules, rel, layer, imp string, ctx importCheck) string {
	if strings.HasPrefix(imp, r.Module+"/") && (r.known == nil || r.known[imp]) {
		dep := strings.TrimPrefix(imp, r.Module+"/")
		testOnly := matchLayer(dep, r.TestOnly) != ""
		if testOnly && !ctx.testRules() && matchLayer(rel, r.TestOnlyOK) == "" {
			sfx := ctx.suffix()
			if sfx == "" {
				return fmt.Sprintf("%s imports test-only package %s", rel, dep)
			}
			return fmt.Sprintf("%s (layer %s%s) imports test-only package %s", rel, layer, sfx, dep)
		}
		if testOnly && ctx.testRules() {
			return ""
		}
		depLayer := matchLayer(dep, keys(r.Layers))
		if depLayer == layer || matchLayer(dep, r.Layers[layer]) != "" {
			return ""
		}
		sfx := ctx.suffix()
		if sfx == "" {
			return fmt.Sprintf("%s (layer %s) may not import %s (layer %s)", rel, layer, dep, depLayer)
		}
		return fmt.Sprintf("%s (layer %s%s) may not import %s (layer %s)", rel, layer, sfx, dep, depLayer)
	}
	for _, deny := range r.DenyExternal[layer] {
		if strings.HasPrefix(imp, deny) {
			sfx := ctx.suffix()
			if sfx == "" {
				return fmt.Sprintf("%s (layer %s) may not import %s", rel, layer, imp)
			}
			return fmt.Sprintf("%s (layer %s%s) may not import %s", rel, layer, sfx, imp)
		}
	}
	return ""
}

// layeringRulesKeyErrors reports deny_external and test_only_allowed_from entries
// that do not name a layer key.
func layeringRulesKeyErrors(r LayeringRules) []string {
	layers := make(map[string]bool, len(r.Layers))
	for k := range r.Layers {
		layers[k] = true
	}
	var errs []string
	for k := range r.DenyExternal {
		if !layers[k] {
			errs = append(errs, fmt.Sprintf(
				`layering.yaml: deny_external key %q is not a layer; fix the key or add the layer (changing the graph needs an ADR)`, k))
		}
	}
	for _, k := range r.TestOnlyOK {
		if !layers[k] {
			errs = append(errs, fmt.Sprintf(
				`layering.yaml: test_only_allowed_from entry %q is not a layer; fix the key or add the layer (changing the graph needs an ADR)`, k))
		}
	}
	sort.Strings(errs)
	return errs
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// matchLayer returns the longest pattern that equals rel or is a path-segment prefix of it.
func matchLayer(rel string, patterns []string) string {
	best := ""
	for _, p := range patterns {
		if (rel == p || strings.HasPrefix(rel, p+"/")) && len(p) > len(best) {
			best = p
		}
	}
	return best
}
