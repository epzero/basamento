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
}

type goPkg struct {
	ImportPath string
	Imports    []string
}

// Layering checks `go list` imports against layering.yaml.
func Layering(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if err != nil {
		return nil, err
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("layering.yaml: %w", err)
	}
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, stderr.String())
	}
	var errs []string
	dec := json.NewDecoder(&out)
	for {
		var p goPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, r.Module), "/")
		layer := matchLayer(rel, keys(r.Layers))
		if layer == "" {
			errs = append(errs, fmt.Sprintf("%s: package not assigned to a layer in layering.yaml", rel))
			continue
		}
		for _, imp := range p.Imports {
			if strings.HasPrefix(imp, r.Module+"/") {
				dep := strings.TrimPrefix(imp, r.Module+"/")
				if matchLayer(dep, r.TestOnly) != "" && matchLayer(rel, r.TestOnlyOK) == "" {
					errs = append(errs, fmt.Sprintf("%s imports test-only package %s", rel, dep))
					continue
				}
				depLayer := matchLayer(dep, keys(r.Layers))
				if depLayer == layer || matchLayer(dep, r.Layers[layer]) != "" {
					continue
				}
				errs = append(errs, fmt.Sprintf("%s (layer %s) may not import %s (layer %s)", rel, layer, dep, depLayer))
				continue
			}
			for _, deny := range r.DenyExternal[layer] {
				if strings.HasPrefix(imp, deny) {
					errs = append(errs, fmt.Sprintf("%s (layer %s) may not import %s", rel, layer, imp))
				}
			}
		}
	}
	sort.Strings(errs)
	return errs, nil
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
