package steps

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registerViolationsInFile reports every disallowed top-level statement in register*
// functions that take *godog.ScenarioContext (see TestRegisterStateCheckerCases).
func registerViolationsInFile(fset *token.FileSet, f *ast.File) []error {
	var out []error
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "register") {
			continue
		}
		scIdent := scenarioContextParam(fn)
		if scIdent == "" {
			continue
		}
		for _, stmt := range fn.Body.List {
			if err := checkRegisterStmt(fset, stmt, scIdent); err != nil {
				out = append(out, err)
			}
		}
	}
	return out
}

// scanRegisterFunctions parses non-test .go files under dir and returns a joined error
// when any register* function captures scenario state at registration time.
func scanRegisterFunctions(dir string) error {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var all []error
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		all = append(all, registerViolationsInFile(fset, f)...)
	}
	if len(all) == 0 {
		return nil
	}
	return errors.Join(all...)
}

func scenarioContextParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		if !isScenarioContextType(field.Type) {
			continue
		}
		if len(field.Names) == 0 {
			return ""
		}
		return field.Names[0].Name
	}
	return ""
}

func isScenarioContextType(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "godog" && sel.Sel.Name == "ScenarioContext"
}

func registerViolation(fset *token.FileSet, stmt ast.Stmt, detail string) error {
	pos := fset.Position(stmt.Pos())
	loc := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
	return fmt.Errorf(
		"%s: keep per-scenario state in World (created by the Before hook) and read it through the context in the step; "+
			"a register function runs once per godog suite, so a variable it declares is shared by every scenario. %s",
		loc, detail)
}

func checkRegisterStmt(fset *token.FileSet, stmt ast.Stmt, scIdent string) error {
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			if callRootedAtScenarioContext(call.Fun, scIdent) ||
				isNestedRegisterCall(call, scIdent) ||
				isRegisterFailureSourceCall(call) {
				return nil
			}
		}
		return registerViolation(fset, stmt,
			"only calls on ScenarioContext, nested register*(sc), or registerFailureSource are allowed")
	case *ast.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Tok != token.DEFINE {
			return registerViolation(fset, stmt,
				"only name := func(...) { ... } helper assignments are allowed")
		}
		if _, ok := s.Rhs[0].(*ast.FuncLit); !ok {
			return registerViolation(fset, stmt,
				"only name := func(...) { ... } helper assignments are allowed")
		}
		return nil
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			return nil
		}
		return registerViolation(fset, stmt,
			"only const declarations are allowed besides step registration")
	default:
		return registerViolation(fset, stmt,
			"disallowed statement (var, loop, if, defer, go, etc.)")
	}
}

func callRootedAtScenarioContext(expr ast.Expr, scIdent string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == scIdent
	case *ast.SelectorExpr:
		return callRootedAtScenarioContext(e.X, scIdent)
	case *ast.CallExpr:
		return callRootedAtScenarioContext(e.Fun, scIdent)
	default:
		return false
	}
}

func isNestedRegisterCall(call *ast.CallExpr, scIdent string) bool {
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || !strings.HasPrefix(fn.Name, "register") {
		return false
	}
	for _, arg := range call.Args {
		if id, ok := arg.(*ast.Ident); ok && id.Name == scIdent {
			return true
		}
	}
	return false
}

func isRegisterFailureSourceCall(call *ast.CallExpr) bool {
	fn, ok := call.Fun.(*ast.Ident)
	return ok && fn.Name == "registerFailureSource"
}

const probeFilePreamble = `package steps

import (
	"context"

	"github.com/cucumber/godog"
)

func registerProbe(sc *godog.ScenarioContext) {
`

const probeFileSuffix = `
}
`

// TestRegisterStateCheckerCases pins the register-function AST rule on in-memory snippets.
func TestRegisterStateCheckerCases(t *testing.T) {
	type caseT struct {
		name     string
		body     string
		whole    string
		accept   bool
		wantLine int
	}
	cases := []caseT{
		{name: "sc_step", body: "\tsc.Step(`^x$`, func(ctx context.Context) error { return nil })\n", accept: true},
		{name: "sc_before", body: "\tsc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { return ctx, nil })\n", accept: true},
		{name: "sc_after", body: "\tsc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) { return ctx, nil })\n", accept: true},
		{name: "nested_register", body: "\tregisterNested(sc)\n", accept: true},
		{name: "failure_source", body: "\tregisterFailureSource(\"phrase\", \"cap\", nil)\n", accept: true},
		{name: "helper_func_lit", body: "\thelper := func(ctx context.Context) error { return nil }\n", accept: true},
		{name: "const", body: "\tconst key = \"k\"\n", accept: true},
		{name: "sc_call_chain", body: "\tsc.StepContext().Before(func(ctx context.Context, st *godog.Step, _ error) (context.Context, error) { return ctx, nil })\n", accept: true},
		{name: "non_register_func_ignored", body: "", accept: true, whole: `package steps

import "github.com/cucumber/godog"

func helper(sc *godog.ScenarioContext) {
	var n int
}
`},
		{name: "register_without_sc_ignored", body: "", accept: true, whole: `package steps

func registerOther() {
	var n int
}
`},
		{name: "var", body: "\tvar n int\n", accept: false, wantLine: 10},
		{name: "map_literal", body: "\tcache := map[string]int{}\n", accept: false, wantLine: 10},
		{name: "short_assign_int", body: "\tn := 0\n", accept: false, wantLine: 10},
		{name: "short_assign_multi", body: "\ta, b := 1, 2\n", accept: false, wantLine: 10},
		{name: "go_stmt", body: "\tgo func() {}()\n", accept: false, wantLine: 10},
		{name: "defer_stmt", body: "\tdefer func() {}()\n", accept: false, wantLine: 10},
		{name: "for_loop", body: "\tfor i := 0; i < 1; i++ {}\n", accept: false, wantLine: 10},
		{name: "if_stmt", body: "\tif true {}\n", accept: false, wantLine: 10},
		{name: "unrelated_call", body: "\thelper()\n", accept: false, wantLine: 10},
		{name: "setup_not_register", body: "\tsetup(sc)\n", accept: false, wantLine: 10},
		{name: "assign_eq", body: "\thelper := func() int { return 0 }\n\tx = helper\n", accept: false, wantLine: 11},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.whole
			if src == "" {
				src = probeFilePreamble + tc.body + probeFileSuffix
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "case.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			violations := registerViolationsInFile(fset, f)
			if tc.accept {
				if len(violations) > 0 {
					t.Fatalf("accepted snippet reported violations: %v", violations)
				}
				return
			}
			if len(violations) == 0 {
				t.Fatal("expected violation")
			}
			msg := violations[0].Error()
			if !strings.Contains(msg, "World") {
				t.Fatalf("violation should mention World: %q", msg)
			}
			wantLoc := fmt.Sprintf("case.go:%d", tc.wantLine)
			if !strings.HasPrefix(msg, wantLoc) {
				t.Fatalf("want location prefix %q, got %q", wantLoc, msg)
			}
		})
	}
}

// TestRegisterViolationsReportInSourceOrder ensures violations are not sorted by error text.
func TestRegisterViolationsReportInSourceOrder(t *testing.T) {
	const head = `package steps

import "github.com/cucumber/godog"

`
	padding := strings.Repeat("\n", 93)
	const tail = `func registerProbe(sc *godog.ScenarioContext) {
	var n int
	go func() {}()
}
`
	src := head + padding + tail
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "case.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations := registerViolationsInFile(fset, f)
	if len(violations) != 2 {
		t.Fatalf("want 2 violations, got %d: %v", len(violations), violations)
	}
	if !strings.HasPrefix(violations[0].Error(), "case.go:99:") {
		t.Fatalf("first violation want line 99, got %q", violations[0])
	}
	if !strings.HasPrefix(violations[1].Error(), "case.go:100:") {
		t.Fatalf("second violation want line 100, got %q", violations[1])
	}
}

// TestRegisterFunctionsNoCapturedState runs the same checker over this package's step sources.
func TestRegisterFunctionsNoCapturedState(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := scanRegisterFunctions(dir); err != nil {
		t.Fatal(err)
	}
}
