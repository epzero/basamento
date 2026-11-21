package gate

import (
	"os"
	"path/filepath"
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
	t.Run("indented toolchain line", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "\ttoolchain go1.26.11\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("space-indented toolchain line", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "  toolchain go1.26.11\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("require path containing toolchain", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "require github.com/foo/toolchain/pkg v1.0.0\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("require line is not patch-only")
		}
		_, err := ParseGoModDirective([]byte(head))
		if err != nil {
			t.Fatal("module path containing toolchain must parse")
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
	t.Run("patch bump and replace", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "replace x.com/y => x.com/z v0.0.0\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("patch bump and exclude", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "exclude x.com/y v1.0.0\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("patch bump and retract", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "retract x.com/y v1.0.0\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("patch bump and godebug", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "godebug default=go1.21\n"
		if PatchOnlyGoModBump([]byte(baseMod), []byte(head)) {
			t.Fatal("expected high")
		}
	})
	t.Run("patch bump and tool", func(t *testing.T) {
		head := strings.Replace(baseMod, "go 1.26.10", "go 1.26.11", 1) + "tool example.com/tool\n"
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

func TestCheckSameGoModLineNonASCIIPath(t *testing.T) {
	const mod = "module x\n\ngo 1.26.11\n"
	const other = "module y\n\ngo 1.26.10\n"
	root := gitRepo(t)
	commitBytes(t, root, "go.mod", []byte(mod))
	commitBytes(t, root, "nested/é/go.mod", []byte(other))
	errs, err := CheckSameGoModLine(root)
	if err != nil || len(errs) != 1 {
		t.Fatalf("want mismatch on non-ASCII path: %v %v", errs, err)
	}
}

func TestModuleRootsCoverEveryGoMod(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "go.mod", []byte("module x\n\ngo 1.26.11\n"))
	commitBytes(t, root, "runtime/temporal/embedded/go.mod", []byte("module x/e\n\ngo 1.26.11\n"))
	roots, err := ModuleRoots(root)
	if err != nil {
		t.Fatal(err)
	}
	errs, err := vulnScopeErrors(root, roots)
	if err != nil || len(errs) != 0 {
		t.Fatalf("ModuleRoots should cover every go.mod: %v %v", errs, err)
	}
	if len(roots) != 2 {
		t.Fatalf("want 2 module roots, got %v", roots)
	}
	errs, err = vulnScopeErrors(root, []string{"."})
	if err != nil || len(errs) != 1 {
		t.Fatalf("want missing embedded root error: %v %v", errs, err)
	}
}

func TestCheckSameGoModLine(t *testing.T) {
	const mod = "module x\n\ngo 1.26.10\n"
	root := gitRepo(t)
	commitBytes(t, root, "go.mod", []byte(mod))
	commitBytes(t, root, "other/go.mod", []byte(mod))

	errs, err := CheckSameGoModLine(root)
	if err != nil || len(errs) != 0 {
		t.Fatalf("same lines: %v %v", errs, err)
	}

	commitBytes(t, root, "other/go.mod", []byte(strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)))
	errs, err = CheckSameGoModLine(root)
	if err != nil || len(errs) != 1 {
		t.Fatalf("want mismatch error: %v %v", errs, err)
	}

	root2 := gitRepo(t)
	commitBytes(t, root2, "go.mod", []byte(mod))
	commitBytes(t, root2, "phantom/go.mod", []byte(mod))
	if err := os.Remove(filepath.Join(root2, "phantom/go.mod")); err != nil {
		t.Fatal(err)
	}
	errs, err = CheckSameGoModLine(root2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "tracked but missing") {
		t.Fatalf("want missing diagnostic: %v", errs)
	}
}
