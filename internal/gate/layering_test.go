package gate

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
		"low/a/other.go": `//go:build windows

package a

import _ "example.com/mod/high/b"
`,
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
		"mid/x/only_windows.go": `//go:build windows

package x

import _ "example.com/mod/high/b"
`,
		"high/b/b.go": "package b\n",
	})
	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mid/x (layer mid, in an excluded file only_windows.go) may not import high/b (layer high)"}
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
		"orphan/x/skip.go": `//go:build windows

package x

import _ "example.com/mod/low/a"
`,
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
		"low/a/bad.go": `//go:build windows

package a

import (
	"
`,
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
		"low/a/skip.go": `//go:build windows

package a

import _ "example.com/mod/low/a"

{{{ not go
`,
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
