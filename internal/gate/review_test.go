package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReviewRepo(t *testing.T) string {
	t.Helper()
	root := gitRepo(t)
	writeYAML(t, root, `default: medium
high_owner: "@a @b"
go_mod_patch_bump:
  - go.mod
  - "**/go.mod"
tiers:
  high:
    - go.mod
    - "**/go.mod"
    - review.yaml
  medium:
    - specs/**
  low:
    - README.md
`)
	return root
}

func writeYAML(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "review.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitReview(t, root, "add", "review.yaml")
	runGitReview(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "review.yaml")
}

func writeGoMod(t *testing.T, root, path, body string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitReview(t, root, "add", path)
	runGitReview(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", path)
}

func runGitReview(t *testing.T, root string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	if out, err := git(root, args...); err != nil {
		t.Fatalf("git %v: %v %s", cmdArgs, err, out)
	}
}

func TestPlanReviewGoModTiers(t *testing.T) {
	const mod = "module example.com/foo\n\ngo 1.26.10\n"
	cases := []struct {
		name string
		path string
		edit func(string) string
		want string
	}{
		{"patch-only root", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		}, "medium"},
		{"patch-only nested", "runtime/foo/go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		}, "medium"},
		{"patch and require", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "require x.com/y v1.0.0\n"
		}, "high"},
		{"minor move", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.27.0", 1)
		}, "high"},
		{"downgrade", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.9", 1)
		}, "high"},
		{"toolchain", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1) + "toolchain go1.26.11\n"
		}, "high"},
		{"comment-only", "go.mod", func(_ string) string {
			return mod + "// note\n"
		}, "high"},
		{"whitespace-only", "go.mod", func(_ string) string {
			return mod + "\n"
		}, "high"},
		{"go without patch", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26", 1)
		}, "high"},
		{"go rc", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26rc1", 1)
		}, "high"},
		{"trailing comment on go", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11 // x", 1)
		}, "high"},
		{"not a go.mod", "README.md", func(_ string) string { return "hi\n" }, "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeReviewRepo(t)
			writeGoMod(t, root, "go.mod", mod)
			writeGoMod(t, root, "runtime/foo/go.mod", mod)
			base := gitHead(t, root)

			var body string
			if strings.HasSuffix(tc.path, "go.mod") {
				cur, _ := os.ReadFile(filepath.Join(root, tc.path))
				body = tc.edit(string(cur))
			} else {
				body = tc.edit("")
			}
			p := filepath.Join(root, tc.path)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			plan, err := PlanReview(root, base, "", []string{tc.path})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Files[tc.path] != tc.want {
				t.Fatalf("got %s want %s (plan tier %s)", plan.Files[tc.path], tc.want, plan.Tier)
			}
		})
	}

	t.Run("new go.mod in new dir", func(t *testing.T) {
		root := writeReviewRepo(t)
		writeGoMod(t, root, "go.mod", mod)
		base := gitHead(t, root)
		newPath := "pkg/new/go.mod"
		writeGoMod(t, root, newPath, strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1))
		plan, err := PlanReview(root, base, gitHead(t, root), []string{newPath})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files[newPath] != "high" {
			t.Fatalf("new file should be high, got %s", plan.Files[newPath])
		}
	})

	t.Run("deleted go.mod", func(t *testing.T) {
		root := writeReviewRepo(t)
		writeGoMod(t, root, "go.mod", mod)
		writeGoMod(t, root, "extra/go.mod", mod)
		base := gitHead(t, root)
		runGitReview(t, root, "rm", "extra/go.mod")
		plan, err := PlanReview(root, base, "", []string{"extra/go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["extra/go.mod"] != "high" {
			t.Fatalf("deleted: got %s", plan.Files["extra/go.mod"])
		}
	})

	t.Run("review.yaml in same change stays high", func(t *testing.T) {
		root := writeReviewRepo(t)
		writeGoMod(t, root, "go.mod", mod)
		base := gitHead(t, root)
		p := filepath.Join(root, "go.mod")
		if err := os.WriteFile(p, []byte(strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		y := filepath.Join(root, "review.yaml")
		b, _ := os.ReadFile(y)
		if err := os.WriteFile(y, append(b, '#'), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := PlanReview(root, base, "", []string{"go.mod", "review.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Tier != "high" {
			t.Fatalf("overall tier %s", plan.Tier)
		}
	})
}

func gitHead(t *testing.T, root string) string {
	t.Helper()
	h, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return h
}