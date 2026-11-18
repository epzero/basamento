package gate

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// bannedTestTimeFuncs lists wall-clock and sleep functions forbidden in tests,
// test-only packages and BDD step code (AGENTS.md: use coretest.FakeClock).
var bannedTestTimeFuncs = map[string]bool{
	"Now":   true,
	"Since": true,
	"Until": true,
	"Sleep": true,
}

// bannedTestRandFuncs lists top-level functions on math/rand and math/rand/v2
// that read or mutate process-global random state. Seeded constructors such as
// rand.New, rand.NewSource, rand.NewPCG and rand.NewChaCha8 stay allowed.
var bannedTestRandFuncs = map[string]bool{
	"ExpFloat64":  true,
	"Float32":     true,
	"Float64":     true,
	"Int":         true,
	"Int31":       true,
	"Int31n":      true,
	"Int32":       true,
	"Int32N":      true,
	"Int63":       true,
	"Int63n":      true,
	"Int64":       true,
	"Int64N":      true,
	"IntN":        true,
	"Intn":        true,
	"N":           true,
	"NormFloat64": true,
	"Perm":        true,
	"Read":        true,
	"Seed":        true,
	"Shuffle":     true,
	"Uint":        true,
	"Uint32":      true,
	"Uint32N":     true,
	"Uint64":      true,
	"Uint64N":     true,
	"UintN":       true,
}

// Hygiene runs deterministic repository hygiene checks that catch mechanical
// AGENTS.md and workflow rule violations before human or agent code review:
//   - test determinism: no wall clock, sleeps, crypto/rand, or global math/rand
//     in *_test.go or test-only packages (layering.yaml's test_only and
//     test_only_allowed_from);
//   - production isolation: no "testing" / "testing/*" import or exported
//     *ForTest* hooks in production Go files;
//   - when base is non-empty, spec/code separation: changes that modify *.go
//     files must not edit specs/*/design.md (agent-workflow.md §7.3).
func Hygiene(root, base string) ([]string, error) {
	errs, err := CheckCodeHygiene(root)
	if err != nil {
		return nil, err
	}
	if base != "" {
		changed, err := ChangedFiles(root, base, "")
		if err != nil {
			return nil, err
		}
		untracked, err := untrackedFiles(root)
		if err != nil {
			return nil, err
		}
		errs = append(errs, CheckSpecSeparation(append(changed, untracked...))...)
	}
	sort.Strings(errs)
	return errs, nil
}

func untrackedFiles(root string) ([]string, error) {
	out, err := git(root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// loadTestOnlyPrefixes returns package prefixes that are test-only (from
// layering.yaml's test_only and test_only_allowed_from when present, falling
// back to the repository defaults for minimal test fixtures).
func loadTestOnlyPrefixes(root string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if os.IsNotExist(err) {
		return []string{"core/coretest", "internal/bdd"}, nil
	}
	if err != nil {
		return nil, err
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("layering.yaml: %w", err)
	}
	out := make([]string, 0, len(r.TestOnly)+len(r.TestOnlyOK))
	out = append(out, r.TestOnly...)
	out = append(out, r.TestOnlyOK...)
	return out, nil
}

// CheckCodeHygiene parses Go files under root and reports violations of
// AGENTS.md's test-determinism and production-isolation rules.
func CheckCodeHygiene(root string) ([]string, error) {
	testOnlyPrefixes, err := loadTestOnlyPrefixes(root)
	if err != nil {
		return nil, err
	}
	var errs []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		pkgDir := path.Dir(rel)
		inTestOnlyPkg := matchLayer(pkgDir, testOnlyPrefixes) != ""
		if strings.HasSuffix(rel, "_test.go") || inTestOnlyPkg {
			errs = append(errs, checkTestFile(fset, f, rel)...)
		} else {
			errs = append(errs, checkProdFile(fset, f, rel)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(errs)
	return errs, nil
}

func checkTestFile(fset *token.FileSet, f *ast.File, rel string) []string {
	var errs []string
	timeAliases := map[string]bool{}
	randAliases := map[string]bool{}

	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		line := fset.Position(imp.Pos()).Line
		var alias string
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		switch importPath {
		case "crypto/rand":
			errs = append(errs, fmt.Sprintf("%s:%d: test imports %q; use seeded randomness from core/coretest (AGENTS.md)", rel, line, importPath))
		case "time":
			if alias == "" {
				alias = "time"
			}
			if alias == "." {
				errs = append(errs, fmt.Sprintf("%s:%d: test must not dot-import %q; use a named import and coretest.FakeClock (AGENTS.md)", rel, line, importPath))
			} else if alias != "_" {
				timeAliases[alias] = true
			}
		case "math/rand", "math/rand/v2":
			if alias == "" {
				alias = "rand"
			}
			if alias == "." {
				errs = append(errs, fmt.Sprintf("%s:%d: test must not dot-import %q; use a named import with a seeded generator from core/coretest (AGENTS.md)", rel, line, importPath))
			} else if alias != "_" {
				randAliases[alias] = true
			}
		}
	}

	if len(timeAliases) == 0 && len(randAliases) == 0 {
		return errs
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		// parser.ParseFile populates id.Obj for file-local variables, parameters
		// and receivers, leaving imported package names with id.Obj == nil.
		if !ok || id.Obj != nil {
			return true
		}
		line := fset.Position(sel.Pos()).Line
		if timeAliases[id.Name] && bannedTestTimeFuncs[sel.Sel.Name] {
			errs = append(errs, fmt.Sprintf("%s:%d: test uses %s.%s; use coretest.FakeClock instead of wall clock or sleeps (AGENTS.md)", rel, line, id.Name, sel.Sel.Name))
		}
		if randAliases[id.Name] && bannedTestRandFuncs[sel.Sel.Name] {
			errs = append(errs, fmt.Sprintf("%s:%d: test uses global %s.%s; use a seeded generator from core/coretest (AGENTS.md)", rel, line, id.Name, sel.Sel.Name))
		}
		return true
	})
	return errs
}

func checkProdFile(fset *token.FileSet, f *ast.File, rel string) []string {
	var errs []string
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if importPath == "testing" || strings.HasPrefix(importPath, "testing/") {
			line := fset.Position(imp.Pos()).Line
			errs = append(errs, fmt.Sprintf("%s:%d: production code must not import %q (AGENTS.md); move test helpers to *_test.go or internal/bdd", rel, line, importPath))
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if isForTestExport(d.Name.Name) {
				line := fset.Position(d.Name.Pos()).Line
				errs = append(errs, fmt.Sprintf("%s:%d: exported identifier %q is test-only (AGENTS.md); move test hooks to *_test.go (e.g. export_test.go) or a test-only package", rel, line, d.Name.Name))
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if isForTestExport(s.Name.Name) {
						line := fset.Position(s.Name.Pos()).Line
						errs = append(errs, fmt.Sprintf("%s:%d: exported identifier %q is test-only (AGENTS.md); move test hooks to *_test.go (e.g. export_test.go) or a test-only package", rel, line, s.Name.Name))
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if isForTestExport(name.Name) {
							line := fset.Position(name.Pos()).Line
							errs = append(errs, fmt.Sprintf("%s:%d: exported identifier %q is test-only (AGENTS.md); move test hooks to *_test.go (e.g. export_test.go) or a test-only package", rel, line, name.Name))
						}
					}
				}
			}
		}
	}
	return errs
}

func isForTestExport(name string) bool {
	return ast.IsExported(name) && strings.Contains(name, "ForTest")
}

// CheckSpecSeparation enforces agent-workflow.md §7.3: implementation changes
// (which modify *.go files) must not edit specs/<capability>/design.md.
// Design text belongs to spec pull requests owned by the designer.
func CheckSpecSeparation(changed []string) []string {
	hasGo := false
	seen := map[string]bool{}
	var designs []string
	for _, f := range changed {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if strings.HasSuffix(f, ".go") {
			hasGo = true
		}
		if isSpecDesignFile(f) && !seen[f] {
			seen[f] = true
			designs = append(designs, f)
		}
	}
	if !hasGo || len(designs) == 0 {
		return nil
	}
	sort.Strings(designs)
	var errs []string
	for _, d := range designs {
		errs = append(errs, fmt.Sprintf("%s: implementation changes (*.go) must not edit specs/*/design.md (agent-workflow.md §7.3); revert %s and open a separate spec PR or hand off to the designer", d, d))
	}
	return errs
}

func isSpecDesignFile(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 3 && parts[0] == "specs" && parts[1] != "" && parts[2] == "design.md"
}

var specsReadmeRow = regexp.MustCompile(`^\|\s*\[` + "`([^`]+)`" + `\]\(([a-z0-9-]+)/requirements\.md\)\s*\|\s*([^|]+?)\s*\|`)

// CheckSpecsReadme verifies that specs/README.md lists every capability under
// specs/<capability>/requirements.md (with no duplicates or orphan rows) and
// that its Status column agrees with whether all scenarios are active or still
// @planned.
func CheckSpecsReadme(root string) ([]string, error) {
	specsDir := filepath.Join(root, "specs")
	if _, err := os.Stat(specsDir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	readmePath := filepath.Join(specsDir, "README.md")
	f, err := os.Open(readmePath)
	if os.IsNotExist(err) {
		return []string{"specs/README.md: missing capability index; create specs/README.md with a Capabilities table"}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var errs []string
	rows := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := specsReadmeRow.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		label, dir, status := m[1], m[2], strings.TrimSpace(m[3])
		if label != dir {
			errs = append(errs, fmt.Sprintf("specs/README.md: row %q links to %s/requirements.md; fix the label or link target so they match", label, dir))
			continue
		}
		if _, dup := rows[dir]; dup {
			errs = append(errs, fmt.Sprintf("specs/README.md: duplicate capability row for %q; keep one row per capability", dir))
			continue
		}
		rows[dir] = status
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	spec, err := Load(root)
	if err != nil {
		return nil, err
	}

	type counts struct{ active, planned int }
	caps := map[string]*counts{}
	for _, reqFile := range spec.Requirements {
		parts := strings.Split(filepath.ToSlash(reqFile), "/")
		if len(parts) >= 3 && parts[0] == "specs" {
			if caps[parts[1]] == nil {
				caps[parts[1]] = &counts{}
			}
		}
	}
	for _, s := range spec.Scenarios {
		if s.Capability == "" {
			continue
		}
		c := caps[s.Capability]
		if c == nil {
			c = &counts{}
			caps[s.Capability] = c
		}
		if s.IsPlanned {
			c.planned++
		} else {
			c.active++
		}
	}

	for _, capName := range sortedKeys(caps) {
		c := caps[capName]
		status, ok := rows[capName]
		if !ok {
			errs = append(errs, fmt.Sprintf("specs/README.md: missing capability row for %q; add it to the Capabilities table", capName))
			continue
		}
		lower := strings.ToLower(status)
		switch {
		case c.active > 0 && c.planned == 0 && strings.HasPrefix(lower, "planned"):
			errs = append(errs, fmt.Sprintf("specs/README.md: capability %q has all %d scenario(s) active, but its status still says %q; update it to Implemented", capName, c.active, status))
		case c.active == 0 && c.planned > 0 && strings.HasPrefix(lower, "implemented"):
			errs = append(errs, fmt.Sprintf("specs/README.md: capability %q has no active scenarios (%d planned), but its status says %q; update it to Planned", capName, c.planned, status))
		}
	}
	for _, capName := range sortedKeys(rows) {
		if _, ok := caps[capName]; !ok {
			errs = append(errs, fmt.Sprintf("specs/README.md: orphan capability row %q has no specs/%s/requirements.md; remove the row or add the spec", capName, capName))
		}
	}
	sort.Strings(errs)
	return errs, nil
}
