package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func commitFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", name}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
}

func TestIssueSessionsTakeTurns(t *testing.T) {
	repo := gitRepo(t)
	author, err := NewSession(repo, NewSessionRequest{Harness: "codex", Role: RoleSpecAuthor, Issue: "WP-L3-05"})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, author.Worktree, "spec.txt")

	// The reviewer, then the implementer, continue on the issue's branch.
	reviewer, err := NewSession(repo, NewSessionRequest{Harness: "codex", Role: RoleSpecReviewer, Issue: "WP-L3-05"})
	if err != nil || reviewer.Worktree != author.Worktree || reviewer.Branch != author.Branch {
		t.Fatalf("reviewer should continue in the issue's worktree: %+v, %v", reviewer, err)
	}

	// A session a harness started in its own fresh worktree is sent to the issue's worktree too.
	freshBase, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(freshBase, "fresh")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "harness-x", fresh, "main").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	impl, err := NewSession(fresh, NewSessionRequest{Harness: "claude-code", Role: RoleImplementer, Issue: "WP-L3-05"})
	if err != nil || impl.Worktree != author.Worktree {
		t.Fatalf("implementer should continue in the issue's worktree: %+v, %v", impl, err)
	}
	if id, _ := CurrentIdentity(author.Worktree); id.String() != impl.Identity {
		t.Fatalf("the worktree belongs to the latest session, got %s", id)
	}

	// Code reviewers get detached snapshots of the branch, one each.
	cr, err := NewSession(repo, NewSessionRequest{Harness: "antigravity", Role: RoleCodeReviewer, Issue: "WP-L3-05"})
	if err != nil || cr.Branch != "" || cr.Reviews != author.Branch || cr.Worktree == author.Worktree {
		t.Fatalf("code reviewer should get its own snapshot: %+v, %v", cr, err)
	}
	if _, err := os.Stat(filepath.Join(cr.Worktree, "spec.txt")); err != nil {
		t.Fatalf("snapshot should contain the branch's commits: %v", err)
	}
	cr2, err := NewSession(fresh, NewSessionRequest{Harness: "claude-code", Role: RoleCodeReviewer, Issue: "WP-L3-05"})
	if err != nil || cr2.Worktree != fresh || cr2.Reviews != author.Branch {
		t.Fatalf("a clean harness worktree becomes the snapshot: %+v, %v", cr2, err)
	}
	inst := func(s Session) string { id, _ := ParseIdentity(s.Identity); return id.Instance }
	if _, err := ReleaseSession(repo, inst(cr), "main", false); err != nil {
		t.Fatalf("a snapshot is released without force: %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo := gitRepo(t)

	lead, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleDispatcher, NoWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if lead.Worktree != repo || lead.Branch != "main" {
		t.Fatalf("a dispatcher stays in the main checkout: %+v", lead)
	}

	worker, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L1-02", Parent: lead.Identity})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(worker.Branch, "WP-L1-02-") || filepath.Base(filepath.Dir(worker.Worktree)) != "repo-worktrees" {
		t.Fatalf("worker gets its own branch and worktree next to the checkout: %+v", worker)
	}
	if id, err := CurrentIdentity(worker.Worktree); err != nil || id.String() != worker.Identity {
		t.Fatalf("whoami in the worktree = %v, %v; want %s", id, err, worker.Identity)
	}
	if id, err := CurrentIdentity(repo); err != nil || id.String() != lead.Identity {
		t.Fatalf("the main checkout keeps the lead's identity: %v, %v", id, err)
	}

	// A session started inside an existing linked worktree reuses it.
	nested, err := NewSession(worker.Worktree, NewSessionRequest{Harness: "claude-code", Role: RoleSpecReviewer})
	if err != nil || nested.Worktree != worker.Worktree {
		t.Fatalf("no nested worktree expected: %+v, %v", nested, err)
	}
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "ipy 12"}); err == nil {
		t.Fatal("a malformed issue key must be refused")
	}

	// Unmerged work cannot be released without force.
	if err := os.WriteFile(filepath.Join(nested.Worktree, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f.txt"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "work"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = nested.Worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	sessions, err := ListSessions(repo, "main")
	if err != nil || len(sessions) != 2 {
		t.Fatalf("list: %+v, %v", sessions, err)
	}
	inst := func(s Session) string { id, _ := ParseIdentity(s.Identity); return id.Instance }
	if _, err := ReleaseSession(repo, inst(nested), "main", false); err == nil {
		t.Fatal("releasing unmerged work must be refused")
	}
	if _, err := ReleaseSession(repo, inst(lead), "main", false); err == nil {
		t.Fatal("the main checkout cannot be released")
	}
	if _, err := ReleaseSession(repo, inst(nested), "main", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nested.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree should be gone: %v", err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := git(dir, append([]string{"-c", "commit.gpgsign=false"}, args...)...); err != nil {
		t.Fatal(err)
	}
}

func TestStatusIsHandedToTheNextSession(t *testing.T) {
	repo := gitRepo(t)
	author, err := NewSession(repo, NewSessionRequest{Harness: "codex", Role: RoleSpecAuthor, Issue: "WP-L2-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetStatus(author.Worktree, "", "finished", ""); err == nil {
		t.Fatal("an unknown status must be refused")
	}
	if _, err := SetStatus(author.Worktree, "", "spec-review-requested", "review EXPR-001-01"); err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L2-01"})
	if err != nil {
		t.Fatal(err)
	}
	if p := reviewer.Previous; p == nil || p.Identity != author.Identity || p.Status != "spec-review-requested" || p.Note != "review EXPR-001-01" {
		t.Fatalf("the next session receives the hand-off: %+v", reviewer.Previous)
	}
	// The lead records a review outcome from the main checkout.
	if s, err := SetStatus(repo, "WP-L2-01", "ready-to-integrate", ""); err != nil || s.Identity != reviewer.Identity {
		t.Fatalf("status by work item: %+v, %v", s, err)
	}
	wps, _ := ListSessions(repo, "main")
	if len(wps) != 1 || wps[0].Status != "ready-to-integrate" {
		t.Fatalf("list shows the status: %+v", wps)
	}
}

func TestReleaseAfterASquashMerge(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L1-03"})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, s.Worktree, "work.txt")
	mustGit(t, s.Worktree, "push", "-q", "-u", "origin", s.Branch)
	id, _ := ParseIdentity(s.Identity)
	if _, err := ReleaseSession(repo, id.Instance, "main", false); err == nil {
		t.Fatal("a pushed but unmerged branch must not be released")
	}
	// The host squash-merged the pull request and deleted the branch.
	mustGit(t, repo, "push", "-q", "origin", "--delete", s.Branch)
	mustGit(t, repo, "fetch", "-q", "--prune")
	if _, err := ReleaseSession(repo, id.Instance, "main", false); err != nil {
		t.Fatalf("a branch whose remote is gone after a merge is released: %v", err)
	}
}

func TestHandoffSurvivesNewSessionInsideIssueWorktree(t *testing.T) {
	repo := gitRepo(t)
	author, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L4-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetStatus(author.Worktree, "", "spec-review-requested", "review round 1"); err != nil {
		t.Fatal(err)
	}
	// A later session starts inside the same worktree (harness reuse).
	reviewer, err := NewSession(author.Worktree, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L4-01"})
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.Worktree != author.Worktree || reviewer.Branch != author.Branch {
		t.Fatalf("should stay in the issue worktree: %+v", reviewer)
	}
	if p := reviewer.Previous; p == nil || p.Identity != author.Identity || p.Note != "review round 1" {
		t.Fatalf("hand-off must survive: %+v", reviewer.Previous)
	}
}

func TestCodeReviewerInsideIssueWorktreeGetsSnapshot(t *testing.T) {
	repo := gitRepo(t)
	author, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L5-02"})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, author.Worktree, "spec.txt")
	cr, err := NewSession(author.Worktree, NewSessionRequest{Harness: "antigravity", Role: RoleCodeReviewer, Issue: "WP-L5-02"})
	if err != nil {
		t.Fatal(err)
	}
	if cr.Branch != "" || cr.Reviews != author.Branch || cr.Worktree == author.Worktree {
		t.Fatalf("reviewer inside issue worktree must get a detached snapshot: %+v", cr)
	}
	if _, err := os.Stat(filepath.Join(cr.Worktree, "spec.txt")); err != nil {
		t.Fatalf("snapshot should contain branch commits: %v", err)
	}
	cur, err := readSession(author.Worktree)
	if err != nil || cur.Branch != author.Branch || cur.Worktree != author.Worktree {
		t.Fatalf("author worktree session must be unchanged: %+v, %v", cur, err)
	}
	abbref, err := git(author.Worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || abbref != author.Branch {
		t.Fatalf("author worktree must stay on branch %s, on %s", author.Branch, abbref)
	}
}

func TestFastForwardSkipsUnpushedBranch(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	author, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L5-01"})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, author.Worktree, "wip.txt")
	headBefore, err := git(author.Worktree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L5-01"})
	if err != nil {
		t.Fatalf("next turn must succeed with unpushed branch: %v", err)
	}
	headAfter, err := git(author.Worktree, "rev-parse", "HEAD")
	if err != nil || headBefore != headAfter {
		t.Fatalf("unpushed branch must leave checkout untouched")
	}
	if reviewer.Worktree != author.Worktree {
		t.Fatalf("should reuse worktree: %+v", reviewer)
	}
}

func TestIssueWorktreeFastForwardsFromOrigin(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	author, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L4-02"})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, author.Worktree, "v1.txt")
	mustGit(t, author.Worktree, "push", "-q", "-u", "origin", author.Branch)
	if _, err := SetStatus(author.Worktree, "", "spec-changes-requested", "fix tier"); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, repo, "clone", "-q", remote, other)
	mustGit(t, other, "checkout", "-q", author.Branch)
	commitFile(t, other, "v2.txt")
	mustGit(t, other, "push", "-q", "origin", author.Branch)

	author2, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L4-02"})
	if err != nil {
		t.Fatal(err)
	}
	if author2.Worktree != author.Worktree {
		t.Fatalf("should reuse the issue worktree")
	}
	if _, err := os.Stat(filepath.Join(author2.Worktree, "v2.txt")); err != nil {
		t.Fatalf("worktree should fast-forward to origin: %v", err)
	}
}

func TestContinueBranchFastForwardsWhenBranchExists(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L4-03-abc123"
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, repo, "clone", "-q", remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "spec.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	first, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L4-03", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.Worktree, "spec.txt")); err != nil {
		t.Fatal(err)
	}
	commitFile(t, other, "round2.txt")
	mustGit(t, other, "push", "-q", "origin", branch)

	second, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L4-03", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	if second.Worktree != first.Worktree {
		t.Fatalf("round 2 should reuse the branch worktree")
	}
	if _, err := os.Stat(filepath.Join(second.Worktree, "round2.txt")); err != nil {
		t.Fatalf("stale worktree must fast-forward: %v", err)
	}
}

func TestContinueBranchRefusesDiverged(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L4-04-div"
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, repo, "clone", "-q", remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "base.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	first, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L4-04", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, first.Worktree, "local-only.txt")
	commitFile(t, other, "remote.txt")
	mustGit(t, other, "push", "-q", "origin", branch)

	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L4-04", Branch: branch}); err == nil {
		t.Fatal("diverged branch must be refused")
	}
}

func TestContinueABranchFromAnotherClone(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	mustGit(t, repo, "init", "-q", "--bare", remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")
	// Another machine starts the work package and pushes its branch.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, repo, "clone", "-q", remote, other)
	mustGit(t, other, "checkout", "-q", "-b", "WP-L2-02-abc123")
	commitFile(t, other, "spec.txt")
	mustGit(t, other, "push", "-q", "origin", "WP-L2-02-abc123")

	reviewer, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L2-02", Branch: "WP-L2-02-abc123"})
	if err != nil || reviewer.Branch != "WP-L2-02-abc123" {
		t.Fatalf("the reviewer continues the pushed branch: %+v, %v", reviewer, err)
	}
	if _, err := os.Stat(filepath.Join(reviewer.Worktree, "spec.txt")); err != nil {
		t.Fatalf("its worktree holds the other clone's work: %v", err)
	}
	cr, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L2-02", Branch: "WP-L2-02-abc123"})
	if err != nil || cr.Branch != "" || cr.Reviews != "WP-L2-02-abc123" {
		t.Fatalf("a code reviewer gets a detached snapshot: %+v, %v", cr, err)
	}
}
