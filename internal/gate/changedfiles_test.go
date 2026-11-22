package gate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestChangedFilesKeepsNonASCIIPaths(t *testing.T) {
	root := gitRepo(t)
	commitBytes(t, root, "go.mod", []byte("module x\n\ngo 1.26.11\n"))
	rel := "nested/é/note.txt"
	commitBytes(t, root, rel, []byte("a\n"))
	base := gitHead(t, root)
	if err := os.WriteFile(filepath.Join(root, rel), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := ChangedFiles(root, base, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if f == rel {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing %q in %v", rel, files)
	}
}

// isolateGitConfigForRename forces rename detection on so the test fails when
// ChangedFiles omits --no-renames, regardless of the developer's git config.
func isolateGitConfigForRename(t *testing.T) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "empty-gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.renames")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
}

func copyRealReviewYAML(t *testing.T, root string) {
	t.Helper()
	src := filepath.Join(makefileRoot(t), "review.yaml")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "review.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "review.yaml")
	mustGit(t, root, "commit", "-q", "-m", "review.yaml")
}

func assertChangedContainsBoth(t *testing.T, files []string, oldPath, newPath string) {
	t.Helper()
	hasOld := slices.Contains(files, oldPath)
	hasNew := slices.Contains(files, newPath)
	if !hasOld || !hasNew {
		t.Fatalf("want both %q and %q in changed list, got %v", oldPath, newPath, files)
	}
}

func TestChangedFilesRenameReportsBothPaths(t *testing.T) {
	const mod = "module example.com/foo\n\ngo 1.26.11\n"
	isolateGitConfigForRename(t)

	root := gitRepo(t)
	copyRealReviewYAML(t, root)
	commitBytes(t, root, "go.mod", []byte(mod))
	base := gitHead(t, root)

	mustGit(t, root, "checkout", "-q", "-b", "feat")
	mustGit(t, root, "mv", "go.mod", "go.mod.bak")
	mustGit(t, root, "commit", "-q", "-m", "rename go.mod")

	t.Run("committed head", func(t *testing.T) {
		head := gitHead(t, root)
		files, err := ChangedFiles(root, base, head)
		if err != nil {
			t.Fatal(err)
		}
		assertChangedContainsBoth(t, files, "go.mod", "go.mod.bak")
		plan, err := PlanReview(root, base, head, files)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Tier != "high" {
			t.Fatalf("tier: got %q want high", plan.Tier)
		}
		if plan.Files["go.mod"] != "high" {
			t.Fatalf("go.mod file tier: got %q", plan.Files["go.mod"])
		}
	})

	t.Run("working tree rename", func(t *testing.T) {
		root2 := gitRepo(t)
		copyRealReviewYAML(t, root2)
		commitBytes(t, root2, "go.mod", []byte(mod))
		base2 := gitHead(t, root2)
		mustGit(t, root2, "mv", "go.mod", "go.mod.bak")
		files, err := ChangedFiles(root2, base2, "")
		if err != nil {
			t.Fatal(err)
		}
		assertChangedContainsBoth(t, files, "go.mod", "go.mod.bak")
	})

	t.Run("workflow path rename", func(t *testing.T) {
		root3 := gitRepo(t)
		copyRealReviewYAML(t, root3)
		dir := filepath.Join(root3, ".github", "workflows")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		commitBytes(t, root3, ".github/workflows/gate.yml", []byte("name: g\n"))
		base3 := gitHead(t, root3)
		mustGit(t, root3, "checkout", "-q", "-b", "feat")
		if err := os.MkdirAll(filepath.Join(root3, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustGit(t, root3, "mv", ".github/workflows/gate.yml", "docs/gate.yml")
		mustGit(t, root3, "commit", "-q", "-m", "move workflow")
		head := gitHead(t, root3)
		files, err := ChangedFiles(root3, base3, head)
		if err != nil {
			t.Fatal(err)
		}
		assertChangedContainsBoth(t, files, ".github/workflows/gate.yml", "docs/gate.yml")
		plan, err := PlanReview(root3, base3, head, files)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Tier != "high" {
			t.Fatalf("tier: got %q want high", plan.Tier)
		}
		if plan.Files[".github/workflows/gate.yml"] != "high" {
			t.Fatalf(".github/workflows/gate.yml tier: got %q", plan.Files[".github/workflows/gate.yml"])
		}
	})
}
