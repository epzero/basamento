package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testLayeringYAML = `module: example.com/mod
layers:
  low: []
  mid: [low]
  high: [low, mid]
  harness: [low, mid, high]
  support: []
test_only: [support]
test_only_allowed_from: [harness]
deny_external:
  low: [evil.com/]
`

func layeringModule(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod":        "module example.com/mod\n\ngo 1.22.0\n",
		"layering.yaml": testLayeringYAML,
	}
	for k, v := range extra {
		files[k] = v
	}
	return writeTree(t, files)
}

func TestLayeringProductionViolation(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":  "package a\n",
		"high/b/b.go": "package b\n",
		"low/c/c.go":  "package c\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/c (layer low) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringClean(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go": "package a\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want ok, got %v", got)
	}
}

func TestLayeringTestImportViolation(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":      "package a\n",
		"high/b/b.go":     "package b\n",
		"low/a/a_test.go": "package a\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in a test) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringExternalTestImportViolation(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":      "package a\n",
		"high/b/b.go":     "package b\n",
		"low/a/a_test.go": "package a_test\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in a test) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringSameViolationReportedOnce(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":        "package a\n",
		"high/b/b.go":       "package b\n",
		"low/c/c.go":        "package c\n\nimport _ \"example.com/mod/high/b\"\n",
		"low/a/a_test.go":   "package a\n\nimport _ \"example.com/mod/high/b\"\n",
		"low/a/ext_test.go": "package a_test\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"low/a (layer low, in a test) may not import high/b (layer high)",
		"low/c (layer low) may not import high/b (layer high)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringExcludedTestFileMayImportTestOnly(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go": "package a\n",
		"support/h/h.go": `package h
`,
		"low/a/other_test.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/support/h"
`),
		"low/a/prod_tagged.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/support/h"
`),
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in an excluded file prod_tagged.go) imports test-only package support/h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringTestOnly(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":      "package a\n\nimport _ \"example.com/mod/support/h\"\n",
		"low/a/a_test.go": "package a\n\nimport _ \"example.com/mod/support/h\"\n",
		"support/h/h.go":  "package h\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a imports test-only package support/h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringDenyExternalInTest(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":      "package a\n",
		"low/a/a_test.go": "package a\n\nimport _ \"evil.com/pkg\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in a test) may not import evil.com/pkg"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringExcludedFileInPackage(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/active.go": `package a
`,
		"low/a/other.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/high/b"
`),
		"high/b/b.go": "package b\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in an excluded file other.go) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringAllExcludedDirectory(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"mid/x/only_excluded.go": layeringExcludedGoFile(`package x

import _ "example.com/mod/high/b"
`),
		"high/b/b.go": "package b\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mid/x (layer mid, in an excluded file only_excluded.go) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringBuildIgnoreFile(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/ignore.go": `//go:build ignore

package main

import _ "example.com/mod/high/b"
`,
		"high/b/b.go": "package b\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"low/a (layer low, in an excluded file ignore.go) may not import high/b (layer high)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringUnlayeredExcludedDirectory(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"orphan/x/skip.go": layeringExcludedGoFile(`package x

import _ "example.com/mod/low/a"
`),
		"low/a/a.go": "package a\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"orphan/x: package not assigned to a layer in layering.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringUnparsableImport(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/active.go": "package a\n",
		"low/a/bad.go": layeringExcludedGoFile(`package a

import (
	"
`),
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "low/a/bad.go:") {
		t.Fatalf("want parse error naming file, got %v", got)
	}
}

func TestLayeringGarbageAfterImportsAccepted(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/active.go": "package a\n",
		"low/a/skip.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/low/a"

{{{ not go
`),
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want ok, got %v", got)
	}
}

func TestLayeringSkippedTrees(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go": "package a\n",
		"low/a/testdata/evil.go": `package x

import _ "example.com/mod/high/b"
`,
		"low/a/vendor/evil.go": `package x

import _ "example.com/mod/high/b"
`,
		"low/.hidden/evil.go": `package x

import _ "example.com/mod/high/b"
`,
		"low/_hidden/evil.go": `package x

import _ "example.com/mod/high/b"
`,
		"nested/go.mod": "module nested\n\ngo 1.22.0\n",
		"nested/nest/nest.go": `package nest

import _ "example.com/mod/high/b"
`,
		"high/b/b.go": "package b\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("skipped trees must not be checked, got %v", got)
	}
}

func TestLayeringDeterministicOutput(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":  "package a\n",
		"high/b/b.go": "package b\n",
		"low/c/c.go":  "package c\n\nimport _ \"example.com/mod/high/b\"\n",
		"low/d/d.go":  "package d\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	first, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("runs differ: %v vs %v", first, second)
	}
	if len(first) < 2 {
		t.Fatalf("want multiple errors, got %v", first)
	}
	for i := 1; i < len(first); i++ {
		if first[i] < first[i-1] {
			t.Fatalf("not sorted: %v", first)
		}
	}
}

func TestLayeringGoListFailure(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go": "package a\n\nimport (\n\t\"\n)\n",
	})
	_, err := Layering(root)
	if err == nil || !strings.Contains(err.Error(), "go list:") {
		t.Fatalf("want go list error, got %v", err)
	}
}

func TestLayeringSymlinkTempDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(layeringModule(t, map[string]string{
		"low/a/a.go": "package a\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Layering(root); err != nil {
		t.Fatal(err)
	}
}

func goModCannotLoadMsg(modRel, dirRel string) string {
	return fmt.Sprintf("%s: go cannot load it as a module (run `go list -m` in %s for the reason); fix the file or delete it",
		modRel, dirRel)
}

func TestLayeringJunkGoModMasksForbiddenImport(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":            "package a\n",
		"high/b/b.go":           "package b\n",
		"low/evil/go.mod":       "not a real mod\n",
		"low/evil/nest/nest.go": "package nest\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(got, goModCannotLoadMsg("low/evil/go.mod", "low/evil")) {
		t.Fatalf("missing go.mod error in %v", got)
	}
	if !containsImportViolation(got, "low/evil/nest", "high/b") {
		t.Fatalf("missing import violation for low/evil/nest in %v", got)
	}
}

func TestLayeringGoModCommentsOnlyInvalid(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/a.go":            "package a\n",
		"high/b/b.go":           "package b\n",
		"low/fake/go.mod":       "// just comments\n\n// nothing here\n",
		"low/fake/nest/nest.go": "package nest\n\nimport _ \"example.com/mod/high/b\"\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := goModCannotLoadMsg("low/fake/go.mod", "low/fake")
	if !containsString(got, want) {
		t.Fatalf("got %v, want containing %q", got, want)
	}
	if !containsImportViolation(got, "low/fake/nest", "high/b") {
		t.Fatalf("missing import violation in %v", got)
	}
}

func TestLayeringInvalidNestedGoModShapes(t *testing.T) {
	base := map[string]string{
		"low/a/a.go":  "package a\n",
		"high/b/b.go": "package b\n",
	}
	nestGo := "package nest\n\nimport _ \"example.com/mod/high/b\"\n"
	cases := []struct {
		name string
		dir  string
		mod  string
	}{
		{name: "junk_text", dir: "low/junk", mod: "not a real mod\n"},
		{name: "comments_only", dir: "low/comm", mod: "// c\n\n// d\n"},
		{name: "module_no_path", dir: "low/nopath", mod: "module\n\ngo 1.22.0\n"},
		{name: "module_empty_parens", dir: "low/empty", mod: "module ()\n\ngo 1.22.0\n"},
		{name: "module_semicolon", dir: "low/semi", mod: "module example.com/x;\n\ngo 1.22.0\n"},
		{name: "module_unclosed_block", dir: "low/unclosed", mod: "module (\nexample.com/broken\n\ngo 1.22.0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range base {
				files[k] = v
			}
			files[tc.dir+"/go.mod"] = tc.mod
			files[tc.dir+"/nest/nest.go"] = nestGo
			got, err := Layering(layeringModule(t, files))
			if err != nil {
				t.Fatal(err)
			}
			dir := tc.dir
			wantMod := goModCannotLoadMsg(dir+"/go.mod", dir)
			if !containsString(got, wantMod) {
				t.Fatalf("missing go.mod error in %v", got)
			}
			if !containsImportViolation(got, dir+"/nest", "high/b") {
				t.Fatalf("missing import violation in %v", got)
			}
		})
	}
}

func TestLayeringValidNestedGoModShapes(t *testing.T) {
	base := map[string]string{
		"low/a/a.go":  "package a\n",
		"high/b/b.go": "package b\n",
	}
	evil := "package nest\n\nimport _ \"example.com/mod/high/b\"\n"
	cases := []struct {
		name string
		dir  string
		mod  string
	}{
		{name: "plain", dir: "nested", mod: "module nested.example\n\ngo 1.22.0\n"},
		{name: "quoted", dir: "quoted", mod: "module \"example.com/q\"\n\ngo 1.22.0\n"},
		{name: "block", dir: "block", mod: "module (\n\tnested.block\n)\n\ngo 1.22.0\n"},
		{name: "comment_before", dir: "commented", mod: "// header\nmodule nested.comment\n\ngo 1.22.0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range base {
				files[k] = v
			}
			files[tc.dir+"/go.mod"] = tc.mod
			files[tc.dir+"/nest/nest.go"] = evil
			got, err := Layering(layeringModule(t, files))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("valid nested module must be skipped, got %v", got)
			}
		})
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsImportViolation(ss []string, pkg, dep string) bool {
	for _, s := range ss {
		if strings.Contains(s, pkg) && strings.Contains(s, "may not import") && strings.Contains(s, dep) {
			return true
		}
	}
	return false
}

func layeringModuleYAML(t *testing.T, yaml string) string {
	t.Helper()
	return layeringModule(t, map[string]string{
		"layering.yaml": yaml,
	})
}

func TestLayeringDenyExternalUnknownLayer(t *testing.T) {
	yaml := `module: example.com/mod
layers:
  low: []
deny_external:
  not-a-layer: [evil.com/]
`
	got, err := Layering(layeringModuleYAML(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	want := `layering.yaml: deny_external key "not-a-layer" is not a layer; fix the key or add the layer (changing the graph needs an ADR)`
	if !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("got %v, want %v", got, []string{want})
	}
}

func TestLayeringTestOnlyAllowedFromUnknownLayer(t *testing.T) {
	yaml := `module: example.com/mod
layers:
  low: []
test_only: [support]
test_only_allowed_from: [nope]
`
	got, err := Layering(layeringModuleYAML(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	want := `layering.yaml: test_only_allowed_from entry "nope" is not a layer; fix the key or add the layer (changing the graph needs an ADR)`
	if !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("got %v, want %v", got, []string{want})
	}
}

func TestLayeringUnknownRuleKeysSorted(t *testing.T) {
	yaml := `module: example.com/mod
layers:
  low: []
deny_external:
  z-bogus: [evil.com/]
test_only_allowed_from: [a-bogus]
`
	got, err := Layering(layeringModuleYAML(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`layering.yaml: deny_external key "z-bogus" is not a layer; fix the key or add the layer (changing the graph needs an ADR)`,
		`layering.yaml: test_only_allowed_from entry "a-bogus" is not a layer; fix the key or add the layer (changing the graph needs an ADR)`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLayeringRepoLayeringYAML(t *testing.T) {
	root, err := repoRootWithLayering(t)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "layering.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var r LayeringRules
	if err := yaml.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	t.Run("repository", func(t *testing.T) {
		if got := layeringRulesKeyErrors(r); len(got) != 0 {
			t.Fatalf("repository layering.yaml: want no key errors, got %v", got)
		}
	})
	t.Run("bogus_key_in_memory", func(t *testing.T) {
		r2 := r
		if r2.DenyExternal == nil {
			r2.DenyExternal = map[string][]string{}
		}
		r2.DenyExternal["not-a-real-layer"] = []string{"example.com/"}
		got := layeringRulesKeyErrors(r2)
		if len(got) == 0 {
			t.Fatal("want error for bogus deny_external key")
		}
		want := `layering.yaml: deny_external key "not-a-real-layer" is not a layer; fix the key or add the layer (changing the graph needs an ADR)`
		if !containsString(got, want) {
			t.Fatalf("got %v, want containing %q", got, want)
		}
	})
}

func repoRootWithLayering(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "layering.yaml")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
