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
// Timers and ticks wait on the real clock, so they are sleeps too.
// context.WithTimeout and context.WithDeadline stay allowed: they read the
// wall clock, but a test uses them only as a hang guard, never as a source of
// time (qa-gate.md G1).
var bannedTestTimeFuncs = map[string]bool{
	"Now":       true,
	"Since":     true,
	"Until":     true,
	"Sleep":     true,
	"After":     true,
	"AfterFunc": true,
	"NewTimer":  true,
	"NewTicker": true,
	"Tick":      true,
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
//   - test determinism: no wall clock, sleeps or timers, crypto/rand, or
//     global math/rand in *_test.go or fake packages from layering.yaml's
//     test_only (for example core/coretest);
//   - production isolation: no "testing" / "testing/*" import or exported
//     *ForTest* hooks in production Go files (matched by name). Packages in
//     test_only_allowed_from (the BDD harness) are neither fakes nor
//     production: non-test files there skip both checks so ADR-0015
//     crypto/rand and harness ForTest helpers stay allowed;
//   - merge conflict markers in any tracked text file (qa-gate.md § Conflict
//     markers);
//   - when base is non-empty, spec/code separation: a change to any file
//     outside specs/ and docs/ must not edit specs/*/design.md
//     (agent-workflow.md §7.3).
func Hygiene(root, base string) ([]string, error) {
	errs, err := CheckCodeHygiene(root)
	if err != nil {
		return nil, err
	}
	markers, err := CheckConflictMarkers(root)
	if err != nil {
		return nil, err
	}
	errs = append(errs, markers...)
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

// hygienePackageClasses holds the layering.yaml prefixes CheckCodeHygiene uses
// to classify non-test .go files. *_test.go always gets determinism rules.
type hygienePackageClasses struct {
	// fakes are test_only prefixes that are not also test_only_allowed_from
	// (for example core/coretest): non-test files get determinism rules.
	fakes []string
	// harness is test_only_allowed_from (for example internal/bdd): non-test
	// files skip both determinism and production-isolation. After ADR-0018 the
	// harness may also appear under test_only so production cannot import it;
	// that does not make its helpers subject to fake or production rules.
	harness []string
}

// loadHygienePackageClasses reads layering.yaml's test_only and
// test_only_allowed_from. test_only_allowed_from is an import permission
// (layering.go), never unioned into the fake set. When layering.yaml is
// absent, the repository default (core/coretest as a fake) is returned.
func loadHygienePackageClasses(root string) (hygienePackageClasses, error) {
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if os.IsNotExist(err) {
		return hygienePackageClasses{fakes: []string{"core/coretest"}}, nil
	}
	if err != nil {
		return hygienePackageClasses{}, err
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		return hygienePackageClasses{}, fmt.Errorf("layering.yaml: %w", err)
	}
	allowedFrom := make(map[string]bool, len(r.TestOnlyOK))
	for _, p := range r.TestOnlyOK {
		allowedFrom[p] = true
	}
	fakes := make([]string, 0, len(r.TestOnly))
	for _, p := range r.TestOnly {
		if !allowedFrom[p] {
			fakes = append(fakes, p)
		}
	}
	harness := make([]string, len(r.TestOnlyOK))
	copy(harness, r.TestOnlyOK)
	return hygienePackageClasses{fakes: fakes, harness: harness}, nil
}

// CheckCodeHygiene parses Go files under root and reports violations of
// AGENTS.md's test-determinism and production-isolation rules.
func CheckCodeHygiene(root string) ([]string, error) {
	classes, err := loadHygienePackageClasses(root)
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
		switch {
		case strings.HasSuffix(rel, "_test.go") || matchLayer(pkgDir, classes.fakes) != "":
			errs = append(errs, checkTestFile(fset, f, rel)...)
		case matchLayer(pkgDir, classes.harness) != "":
			// BDD harness: neither fake nor production.
		default:
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
				errs = append(errs, forTestExportError(fset, rel, d.Name.Pos(), d.Name.Name))
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if isForTestExport(s.Name.Name) {
						errs = append(errs, forTestExportError(fset, rel, s.Name.Pos(), s.Name.Name))
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if isForTestExport(name.Name) {
							errs = append(errs, forTestExportError(fset, rel, name.Pos(), name.Name))
						}
					}
				}
			}
		}
	}
	return errs
}

func forTestExportError(fset *token.FileSet, rel string, pos token.Pos, name string) string {
	return fmt.Sprintf("%s:%d: exported identifier %q is test-only (AGENTS.md); move it to a test-only package (core/coretest, internal/bdd); export_test.go only lifts it for its own package's tests", rel, fset.Position(pos).Line, name)
}

// forTestExportPattern matches the test-hook naming convention at a word
// boundary, so ForTestament and ParseForTestimony pass.
var forTestExportPattern = regexp.MustCompile(`ForTest(s|ing)?($|[A-Z0-9_])`)

func isForTestExport(name string) bool {
	return ast.IsExported(name) && forTestExportPattern.MatchString(name)
}

// CheckSpecSeparation enforces agent-workflow.md §7.3: an implementation
// change, any file outside specs/ and docs/, must not edit
// specs/<capability>/design.md. Design text belongs to spec pull requests
// owned by the designer.
func CheckSpecSeparation(changed []string) []string {
	hasImplementation := false
	seen := map[string]bool{}
	var designs []string
	for _, f := range changed {
		f = filepath.ToSlash(strings.TrimSpace(f))
		if f != "" && outsideSpecsAndDocs(f) {
			hasImplementation = true
		}
		if isSpecDesignFile(f) && !seen[f] {
			seen[f] = true
			designs = append(designs, f)
		}
	}
	if !hasImplementation || len(designs) == 0 {
		return nil
	}
	sort.Strings(designs)
	var errs []string
	for _, d := range designs {
		errs = append(errs, fmt.Sprintf("%s: an implementation change (a file outside specs/ and docs/) must not edit specs/*/design.md (agent-workflow.md §7.3); revert this file, describe the correction in the pull request and hand it to the designer (specs-needed)", d))
	}
	return errs
}

// outsideSpecsAndDocs reports whether p lies outside the two trees a spec pull
// request may touch.
func outsideSpecsAndDocs(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) < 2 || (parts[0] != "specs" && parts[0] != "docs")
}

func isSpecDesignFile(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 3 && parts[0] == "specs" && parts[1] != "" && parts[2] == "design.md"
}

var specsReadmeRow = regexp.MustCompile(`^\|\s*\[` + "`([^`]+)`" + `\]\(([a-z0-9-]+)/requirements\.md\)\s*\|\s*([^|]+?)\s*\|`)

// CheckSpecsReadme verifies that specs/README.md lists every capability under
// specs/<capability>/requirements.md (with no duplicates or orphan rows) and
// that its Status column agrees with its scenarios: all active means
// Implemented, and any @planned scenario means Planned or In progress.
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
		case c.active > 0 && c.planned == 0 && (strings.HasPrefix(lower, "planned") || strings.HasPrefix(lower, "in progress")):
			errs = append(errs, fmt.Sprintf("specs/README.md: capability %q has all %d scenario(s) active, but its status still says %q; update it to Implemented", capName, c.active, status))
		case c.planned > 0 && strings.HasPrefix(lower, "implemented"):
			errs = append(errs, fmt.Sprintf("specs/README.md: capability %q has %d @planned scenario(s), but its status says %q; use Planned (or In progress) until every scenario is active", capName, c.planned, status))
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
