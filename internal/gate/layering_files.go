package gate

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// cleanAbs returns the absolute, cleaned path with symlinks resolved.
func cleanAbs(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// attributedGoFiles maps absolute paths of .go files already checked through go list.
func attributedGoFiles(pkgs []goPkg) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, p := range pkgs {
		dir := p.Dir
		if dir == "" {
			continue
		}
		names := append([]string{}, p.GoFiles...)
		names = append(names, p.CgoFiles...)
		names = append(names, p.TestGoFiles...)
		names = append(names, p.XTestGoFiles...)
		for _, name := range names {
			abs, err := cleanAbs(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			out[abs] = true
		}
	}
	return out, nil
}

func parseGoImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	imports := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		imp := strings.Trim(spec.Path.Value, `"`)
		imports = append(imports, imp)
	}
	return imports, nil
}

func dirShouldSkip(name string) bool {
	return name == "testdata" || name == "vendor" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func relPathSkipped(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if dirShouldSkip(seg) {
			return true
		}
	}
	return false
}

func dirPathSkipped(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return relPathSkipped(filepath.ToSlash(rel))
}

// layeringWalkExtra parses and checks .go files go list did not attribute to a package.
func layeringWalkExtra(root string, r LayeringRules, attributed map[string]bool, check func(rel, layer string, imports []string, ctx importCheck)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		base := filepath.Base(path)
		if d.IsDir() {
			if dirShouldSkip(base) {
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(base, ".go") {
			return nil
		}
		abs, err := cleanAbs(path)
		if err != nil {
			return nil
		}
		if attributed[abs] {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dirRel := filepath.ToSlash(filepath.Dir(relPath))
		if dirRel == "." {
			dirRel = ""
		}
		layer := matchLayer(dirRel, keys(r.Layers))
		if layer == "" {
			check(dirRel, "", nil, importCheck{kind: kindExcluded, fileName: base, unlayered: true})
			return nil
		}
		imports, err := parseGoImports(path)
		if err != nil {
			check(filepath.ToSlash(relPath), layer, nil, importCheck{kind: kindExcluded, fileName: base, parseErr: err})
			return nil
		}
		ctx := importCheck{kind: kindExcluded, fileName: base}
		if strings.HasSuffix(base, "_test.go") {
			ctx.useTestRules = true
		}
		check(dirRel, layer, imports, ctx)
		return nil
	})
}
