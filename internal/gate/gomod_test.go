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

func TestCheckSameGoModLine(t *testing.T) {
	root := t.TempDir()
	track := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const mod = "module x\n\ngo 1.26.10\n"
	initGit(t, root)
	track("go.mod", mod)
	gitAddCommit(t, root, "go.mod")
	track("other/go.mod", mod)
	gitAddCommit(t, root, "other/go.mod")

	errs, err := CheckSameGoModLine(root)
	if err != nil || len(errs) != 0 {
		t.Fatalf("same lines: %v %v", errs, err)
	}

	track("other/go.mod", strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1))
	gitAddCommit(t, root, "other/go.mod")
	errs, err = CheckSameGoModLine(root)
	if err != nil || len(errs) != 1 {
		t.Fatalf("want mismatch error: %v %v", errs, err)
	}
}

func initGit(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")
}

func gitAddCommit(t *testing.T, root, name string) {
	t.Helper()
	runGit(t, root, "add", name)
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", name)
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	if _, err := git(root, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}