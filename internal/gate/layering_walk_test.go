package gate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseGoImportsUnquote(t *testing.T) {
	root := layeringModule(t, map[string]string{
		"low/a/active.go": "package a\n",
		"high/b/b.go":     "package b\n",
		"low/a/quoted.go": layeringExcludedGoFile(`package a

import _ "example.com/mod/high/b"
`),
		"low/a/rawmod.go": layeringExcludedGoFile(`package a

import _ ` + "`example.com/mod/high/b`" + `
`),
		"low/a/vendor.go": layeringExcludedGoFile(`package a

import _ ` + "`evil.com/pkg/extra`" + `
`),
		"low/a/badlit.go": layeringExcludedGoFile(`package a

import ` + "`unterminated" + `
`),
	})
	quotedWant := "low/a (layer low, in an excluded file quoted.go) may not import high/b (layer high)"
	rawModWant := "low/a (layer low, in an excluded file rawmod.go) may not import high/b (layer high)"
	vendorWant := "low/a (layer low, in an excluded file vendor.go) may not import evil.com/pkg/extra"

	got, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(got, quotedWant) {
		t.Fatalf("quoted import: got %v, want containing %q", got, quotedWant)
	}
	if !containsString(got, rawModWant) {
		t.Fatalf("raw backtick module import: got %v, want containing %q", got, rawModWant)
	}
	if !containsString(got, vendorWant) {
		t.Fatalf("raw backtick deny_external: got %v, want containing %q", got, vendorWant)
	}

	badPath := filepath.Join(root, "low/a/badlit.go")
	if _, err := parseGoImports(badPath); err == nil {
		t.Fatal("want parse error for invalid import literal")
	}
	got2, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	var parseNamed bool
	for _, msg := range got2 {
		if strings.HasPrefix(msg, "low/a/badlit.go:") {
			parseNamed = true
			break
		}
	}
	if !parseNamed {
		t.Fatalf("want parse failure naming badlit.go, got %v", got2)
	}
}

func TestParseGoImportsTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	const imp = "evil.com/x"
	contents := "package p\n\nimport _ \"" + imp + "\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := parseGoImports(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{imp}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("quoted: got %v, want %v", got, want)
	}

	rawPath := filepath.Join(dir, "g.go")
	raw := "package p\n\nimport _ `" + imp + "`\n"
	if err := os.WriteFile(rawPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = parseGoImports(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backtick: got %v, want %v", got, want)
	}
}

func TestDirPathSkippedSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	pkg := filepath.Join(real, "pkg")
	hidden := filepath.Join(real, ".hidden")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if dirPathSkipped(link, filepath.Join(real, "pkg")) {
		t.Fatal("pkg under real path must not be skipped when root is symlink to real")
	}
	if !dirPathSkipped(link, filepath.Join(real, ".hidden")) {
		t.Fatal("dot-prefixed directory must be skipped")
	}
	outside := t.TempDir()
	if dirPathSkipped(link, outside) {
		t.Fatal("directory outside module root must not be treated as skipped")
	}
	if dirPathSkipped(real, real) {
		t.Fatal("root equal to dir must not skip")
	}
	missing := filepath.Join(real, "nope")
	if dirPathSkipped(link, missing) {
		t.Fatal("non-existent dir: cleanAbs fails so skip is false (package not checked via rel)")
	}
}
