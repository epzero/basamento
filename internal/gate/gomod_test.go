package gate

import (
	"strings"
	"testing"
)

func TestPatchOnlyGoModBump(t *testing.T) {
	const baseMod = "module example.com/foo\n\ngo 1.26.10\n"
	t.Run("patch-only bump", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1)
		if !PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected patch-only bump")
		}
	})
	t.Run("patch bump and dependency", func(t *testing.T) {
		head := baseMod + "require example.com/bar v1.0.0\n"
		head = strings.Replace(head, "go 1.26.10", "go 1.26.11", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("minor line move", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.27.0", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("downgrade", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.9", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("toolchain line", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "toolchain go1.26.11\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("comment-only", func(t *testing.T) {
		head := baseMod + "// note\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("comment change should not be patch-only")
		}
	})
	t.Run("comment-only on go line rejected by parser", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11 // note\n", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("trailing comment on go line")
		}
	})
	t.Run("whitespace-only", func(t *testing.T) {
		head := "module example.com/foo\n\ngo 1.26.10\n\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("whitespace change")
		}
	})
	t.Run("go without patch", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("release candidate", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26rc1", 1)
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("unparseable base", func(t *testing.T) {
		if PatchOnlyGoModBump([]byte("module x\n"), []byte(baseMod)) {
			t.Fatal("expected false")
		}
	})
}

func TestParseGoModDirective(t *testing.T) {
	_, err := ParseGoModDirective([]byte("module x\n\ngo 1.26\n"))
	if err == nil {
		t.Fatal("expected error for go without patch")
	}
}
