package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testReviewYAML = `default: medium
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
`

func seedReviewRepo(t *testing.T) string {
	t.Helper()
	root := gitRepo(t)
	commitBytes(t, root, "review.yaml", []byte(testReviewYAML))
	return root
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
		{"indented toolchain", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1) + "\ttoolchain go1.26.11\n"
		}, "high"},
		{"space-indented toolchain", "go.mod", func(_ string) string {
			return strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1) + "  toolchain go1.26.11\n"
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
		{"patch bump and replace", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "replace x.com/y => x.com/z v0.0.0\n"
		}, "high"},
		{"patch bump and exclude", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "exclude x.com/y v1.0.0\n"
		}, "high"},
		{"patch bump and retract", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "retract x.com/y v1.0.0\n"
		}, "high"},
		{"patch bump and godebug", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "godebug default=go1.21\n"
		}, "high"},
		{"patch bump and tool", "go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "tool example.com/tool\n"
		}, "high"},
		{"not a go.mod", "README.md", func(_ string) string { return "hi\n" }, "low"},
		{"non-ASCII go.mod diverge", "nested/é/go.mod", func(m string) string {
			return strings.Replace(m, "go 1.26.10", "go 1.26.11", 1) + "require z.com/w v1\n"
		}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := seedReviewRepo(t)
			commitBytes(t, root, "go.mod", []byte(mod))
			commitBytes(t, root, "runtime/foo/go.mod", []byte(mod))
			if tc.path == "nested/é/go.mod" {
				commitBytes(t, root, tc.path, []byte(mod))
			}
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

	t.Run("patch-only both modules", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		commitBytes(t, root, "runtime/foo/go.mod", []byte(mod))
		base := gitHead(t, root)
		bumped := strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		for _, p := range []string{"go.mod", "runtime/foo/go.mod"} {
			if err := os.WriteFile(filepath.Join(root, p), []byte(bumped), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := PlanReview(root, base, "", []string{"go.mod", "runtime/foo/go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Tier != "medium" || plan.Files["go.mod"] != "medium" || plan.Files["runtime/foo/go.mod"] != "medium" {
			t.Fatalf("tier %s files %v", plan.Tier, plan.Files)
		}
	})

	t.Run("base has no go.mod at that path", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		base := gitHead(t, root)
		newPath := "pkg/new/go.mod"
		commitBytes(t, root, newPath, []byte(strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)))
		plan, err := PlanReview(root, base, gitHead(t, root), []string{newPath})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files[newPath] != "high" {
			t.Fatalf("new file should be high, got %s", plan.Files[newPath])
		}
	})

	t.Run("renamed go.mod", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		commitBytes(t, root, "extra/go.mod", []byte(mod))
		base := gitHead(t, root)
		if err := os.MkdirAll(filepath.Join(root, "renamed"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(root, "extra/go.mod"), filepath.Join(root, "renamed/go.mod")); err != nil {
			t.Fatal(err)
		}
		mustGit(t, root, "add", "-A")
		mustGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "rename")
		plan, err := PlanReview(root, base, gitHead(t, root), []string{"renamed/go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["renamed/go.mod"] != "high" {
			t.Fatalf("renamed: got %s", plan.Files["renamed/go.mod"])
		}
	})

	t.Run("deleted go.mod", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		commitBytes(t, root, "extra/go.mod", []byte(mod))
		base := gitHead(t, root)
		mustGit(t, root, "rm", "extra/go.mod")
		plan, err := PlanReview(root, base, "", []string{"extra/go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["extra/go.mod"] != "high" {
			t.Fatalf("deleted: got %s", plan.Files["extra/go.mod"])
		}
	})

	t.Run("review.yaml in same change stays high", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
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

	t.Run("moved base same bump on main", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		mustGit(t, root, "checkout", "-b", "work")
		bumped := strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		commitBytes(t, root, "go.mod", []byte(bumped))
		workHead, err := git(root, "rev-parse", "work")
		if err != nil {
			t.Fatal(err)
		}
		mustGit(t, root, "checkout", "main")
		mustGit(t, root, "reset", "--hard", workHead+"^")
		commitBytesMsg(t, root, "go.mod", []byte(bumped), "main bump")
		mustGit(t, root, "checkout", "work")
		plan, err := PlanReview(root, "main", "work", []string{"go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["go.mod"] != "medium" {
			t.Fatalf("merge-base bump should be medium, got %s", plan.Files["go.mod"])
		}
	})

	t.Run("moved base unrelated main edit", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		mustGit(t, root, "checkout", "-b", "work")
		bumped := strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		commitBytes(t, root, "go.mod", []byte(bumped))
		mustGit(t, root, "checkout", "main")
		commitBytes(t, root, "go.mod", []byte(mod+"require other.test/m v1.0.0\n"))
		mustGit(t, root, "checkout", "work")
		plan, err := PlanReview(root, "main", "work", []string{"go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["go.mod"] != "medium" {
			t.Fatalf("branch patch from merge-base should be medium, got %s", plan.Files["go.mod"])
		}
	})

	t.Run("branch bump and dependency", func(t *testing.T) {
		root := seedReviewRepo(t)
		commitBytes(t, root, "go.mod", []byte(mod))
		mustGit(t, root, "checkout", "-b", "work")
		commitBytes(t, root, "go.mod", []byte(strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)+"require x.com/y v1.0.0\n"))
		plan, err := PlanReview(root, "main", "work", []string{"go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["go.mod"] != "high" {
			t.Fatalf("got %s", plan.Files["go.mod"])
		}
	})

	t.Run("fail closed without go.mod in high tier", func(t *testing.T) {
		yaml := `default: medium
high_owner: "@a @b"
go_mod_patch_bump:
  - go.mod
tiers:
  high:
    - review.yaml
  medium:
    - go.mod
`
		root := gitRepo(t)
		commitBytes(t, root, "review.yaml", []byte(yaml))
		commitBytes(t, root, "go.mod", []byte(mod))
		base := gitHead(t, root)
		patchReq := strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1) + "require x.com/y v1.0.0\n"
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(patchReq), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := PlanReview(root, base, "", []string{"go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["go.mod"] != "high" {
			t.Fatalf("dependency+bump: got %s", plan.Files["go.mod"])
		}
		patchOnly := strings.Replace(mod, "go 1.26.10", "go 1.26.11", 1)
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(patchOnly), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err = PlanReview(root, base, "", []string{"go.mod"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.Files["go.mod"] != "medium" {
			t.Fatalf("patch only: got %s", plan.Files["go.mod"])
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
