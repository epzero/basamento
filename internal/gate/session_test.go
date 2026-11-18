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
		cmd.Env = testGitEnv()
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
	mustGit(t, repo, "worktree", "add", "-q", "-b", "harness-x", fresh, headBranchRef("main"))
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

func testGitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
}

// initBareRemote creates a bare repository whose default branch is main.
func initBareRemote(t *testing.T, dir, remote string) {
	t.Helper()
	mustGit(t, dir, "init", "-q", "--bare", "-b", "main", remote)
}

// cloneMain clones remote with local branch main checked out.
func cloneMain(t *testing.T, dir, remote, dest string) {
	t.Helper()
	mustGit(t, dir, "clone", "-q", "-b", "main", remote, dest)
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "commit.gpgsign=false",
		"-c", "user.name=t",
		"-c", "user.email=t@example.com",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, strings.TrimSpace(string(out)))
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
	initBareRemote(t, repo, remote)
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

func TestHandoffChainsThroughIntermediateSession(t *testing.T) {
	repo := gitRepo(t)
	author, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L5-04"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetStatus(author.Worktree, "", "spec-review-requested", "review EXPR-099"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L5-04"}); err != nil {
		t.Fatal(err)
	}
	impl, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L5-04"})
	if err != nil {
		t.Fatal(err)
	}
	if p := impl.Previous; p == nil || p.Identity != author.Identity || p.Note != "review EXPR-099" {
		t.Fatalf("main checkout: want author hand-off, got %+v", p)
	}
	inside, err := NewSession(impl.Worktree, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L5-04"})
	if err != nil {
		t.Fatal(err)
	}
	if p := inside.Previous; p == nil || p.Note != "review EXPR-099" {
		t.Fatalf("inside worktree: want author hand-off, got %+v", p)
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

func TestBranchNameValidation(t *testing.T) {
	repo := gitRepo(t)
	bad := []string{"main:refs/heads/x", "feat/foo", "HEAD", "$(rm)", "-evil", "a b", "ends.lock", "../../../tmp/x"}
	for _, b := range bad {
		if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L5-03", Branch: b}); err == nil {
			t.Fatalf("branch %q must be refused", b)
		} else if !strings.Contains(err.Error(), "not an accepted") {
			t.Fatalf("branch %q: want validation error, got %v", b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "repo-worktrees", "main:refs/heads/x")); err == nil {
		t.Fatal("invalid branch must not create a worktree path")
	}
	good := []string{"WP-L5-03", "EP0-47-23761f", "main", "branch_name", "a1b2c3"}
	for _, b := range good {
		if err := validateBranchName(b); err != nil {
			t.Fatalf("branch %q should be accepted: %v", b, err)
		}
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
	initBareRemote(t, repo, remote)
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
	initBareRemote(t, repo, remote)
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
	cloneMain(t, repo, remote, other)
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
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L4-03-abc123"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
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
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L4-04-div"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
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
	} else if !strings.Contains(err.Error(), "push your local commits") {
		t.Fatalf("error must say what to do next: %v", err)
	}
}

// repoWithPushedBranchAndConflictingTag tracks branch from origin, then adds a
// local tag with the same name on another commit (reviewer repro).
func repoWithPushedBranchAndConflictingTag(t *testing.T, branch string) (repo, other, branchHead string) {
	t.Helper()
	repo = gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "branch-tip.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)
	branchHead, _ = git(other, "rev-parse", headBranchRef(branch))
	mustGit(t, repo, "fetch", "origin", originFetchRefspec(branch))
	mustGit(t, repo, "branch", "--track", branch, remoteBranchRef(branch))
	mustGit(t, repo, "checkout", "-q", "main")
	commitFile(t, repo, "tag-target.txt")
	mustGit(t, repo, "tag", branch)
	return repo, other, branchHead
}

func TestContinueBranchLocalTagShadowsBranchName(t *testing.T) {
	const branch = "ambig08"
	repo, other, branchHead := repoWithPushedBranchAndConflictingTag(t, branch)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L6-07", Branch: branch})
	if err != nil {
		t.Fatalf("continue -branch with tag shadow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, "branch-tip.txt")); err != nil {
		t.Fatalf("worktree should follow branch ref, not tag: %v", err)
	}
	head, _ := git(s.Worktree, "rev-parse", "HEAD")
	if head != branchHead {
		t.Fatalf("HEAD %s should match branch tip %s", head, branchHead)
	}
	commitFile(t, other, "round2.txt")
	mustGit(t, other, "push", "-q", "origin", branch)
	s2, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L6-07", Branch: branch})
	if err != nil {
		t.Fatalf("reuse worktree with tag shadow: %v", err)
	}
	if s2.Worktree != s.Worktree {
		t.Fatal("should reuse branch worktree")
	}
	if _, err := os.Stat(filepath.Join(s2.Worktree, "round2.txt")); err != nil {
		t.Fatalf("fast-forward should reach origin: %v", err)
	}
}

func TestCodeReviewerSnapshotIgnoresLocalTagShadow(t *testing.T) {
	const branch = "ambig09"
	repo, other, branchHead := repoWithPushedBranchAndConflictingTag(t, branch)
	cr1, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-08", Branch: branch})
	if err != nil {
		t.Fatalf("round-1 reviewer: %v", err)
	}
	snap, _ := git(cr1.Worktree, "rev-parse", "HEAD")
	if snap != branchHead {
		t.Fatalf("snapshot %s should match branch %s", snap, branchHead)
	}
	commitFile(t, other, "v2.txt")
	mustGit(t, other, "push", "-q", "origin", branch)
	newHead, _ := git(other, "rev-parse", headBranchRef(branch))
	cr2, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-08", Branch: branch})
	if err != nil {
		t.Fatalf("round-2 reviewer: %v", err)
	}
	snap2, _ := git(cr2.Worktree, "rev-parse", "HEAD")
	if snap2 != newHead {
		t.Fatalf("round-2 snapshot %s should match origin %s", snap2, newHead)
	}
}

func TestReleaseSessionWithLocalTagShadowingBranch(t *testing.T) {
	const branch = "ambig10"
	repo, _, _ := repoWithPushedBranchAndConflictingTag(t, branch)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L6-09", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseIdentity(s.Identity)
	if _, err := ReleaseSession(repo, id.Instance, "main", true); err != nil {
		t.Fatalf("release with tag shadow: %v", err)
	}
	if _, err := git(repo, "rev-parse", "--verify", headBranchRef(branch)); err == nil {
		t.Fatal("branch ref should be deleted")
	}
}

func TestContinueBranchWhenNameMatchesWorktreeFile(t *testing.T) {
	const branch = "readme"
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	commitFile(t, repo, "readme")
	mustGit(t, repo, "push", "-q", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "spec-on-branch.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L6-10", Branch: branch})
	if err != nil {
		t.Fatalf("branch name matching a tree path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Worktree, "spec-on-branch.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCodeReviewerRound2SnapshotsPushedHead(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L6-01-aa0001"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "v1.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	cr1, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-01", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cr1.Worktree, "v1.txt")); err != nil {
		t.Fatal(err)
	}
	commitFile(t, other, "v2.txt")
	mustGit(t, other, "push", "-q", "origin", branch)

	cr2, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-01", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cr2.Worktree, "v2.txt")); err != nil {
		t.Fatalf("round-2 reviewer must snapshot origin head: %v", err)
	}
}

func TestContinueBranchRejectsPlainDirectoryReuse(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L6-02-bb0002"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "x.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	wt := filepath.Join(filepath.Dir(repo), "repo-worktrees", branch)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L6-02", Branch: branch}); err == nil {
		t.Fatal("plain directory must not be reused as a worktree")
	}
}

func TestContinueBranchRejectsWorktreeOnOtherBranch(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L6-03-cc0003"
	otherBranch := "WP-L6-03-other"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "x.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)
	mustGit(t, other, "checkout", "-q", "-b", otherBranch, "main")
	mustGit(t, other, "push", "-q", "-u", "origin", otherBranch)

	wt := filepath.Join(filepath.Dir(repo), "repo-worktrees", branch)
	mustGit(t, repo, "worktree", "add", "-b", otherBranch, wt, "main")
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L6-03", Branch: branch}); err == nil {
		t.Fatal("worktree on wrong branch must be refused")
	}
}

func TestFastForwardRefusesDetachedAtSameSHA(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L6-04-dd0004"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "x.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	first, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L6-04", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := git(first.Worktree, "rev-parse", "HEAD")
	mustGit(t, first.Worktree, "checkout", "-q", "--detach", head)
	commitFile(t, other, "y.txt")
	mustGit(t, other, "push", "-q", "origin", branch)

	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L6-04", Branch: branch}); err == nil {
		t.Fatal("fast-forward must refuse when checkout is detached")
	} else if !strings.Contains(err.Error(), "worktree must be on branch") {
		t.Fatalf("expected branch checkout error, got %v", err)
	}
}

func TestForceFetchUpdatesOriginTracking(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "WP-L6-05-ee0005"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "old.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)

	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L6-05", Branch: branch}); err != nil {
		t.Fatal(err)
	}
	mustGit(t, other, "checkout", "-q", branch)
	mustGit(t, other, "reset", "-q", "--hard", "main")
	commitFile(t, other, "rebased.txt")
	mustGit(t, other, "push", "-q", "--force", "origin", branch)

	// +refspec fetch must succeed; snapshot sees origin after a force-push without ff-resetting locals.
	cr, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-05", Branch: branch})
	if err != nil {
		t.Fatalf("force-pushed origin must be fetchable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cr.Worktree, "rebased.txt")); err != nil {
		t.Fatalf("reviewer snapshot should track force-pushed origin: %v", err)
	}
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecReviewer, Issue: "WP-L6-05", Branch: branch}); err == nil {
		t.Fatal("stale local branch after force-push must not fast-forward silently")
	} else if !strings.Contains(err.Error(), "push your local commits") && !strings.Contains(err.Error(), "resolve the divergence") {
		t.Fatalf("diverged local branch must refuse: %v", err)
	}
}

func TestTagDoesNotShadowReviewSnapshot(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")

	branch := "v1release"
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
	mustGit(t, other, "checkout", "-q", "-b", branch)
	commitFile(t, other, "on-branch.txt")
	mustGit(t, other, "push", "-q", "-u", "origin", branch)
	branchHead, _ := git(other, "rev-parse", "HEAD")
	mustGit(t, other, "checkout", "-q", "main")
	commitFile(t, other, "tagged.txt")
	mustGit(t, other, "tag", branch)
	tagSHA, _ := git(other, "rev-parse", branch)
	if tagSHA == branchHead {
		t.Fatal("tag and branch should differ for this test")
	}

	cr, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleCodeReviewer, Issue: "WP-L6-06", Branch: branch})
	if err != nil {
		t.Fatal(err)
	}
	snapSHA, _ := git(cr.Worktree, "rev-parse", "HEAD")
	if snapSHA != branchHead {
		t.Fatalf("snapshot should follow branch ref on origin, not tag %s", tagSHA)
	}
}

func TestListSessionsMergedWithTagNamedLikeBase(t *testing.T) {
	repo := gitRepo(t)
	commitFile(t, repo, "on-main.txt")
	initHead, _ := git(repo, "rev-parse", "HEAD~1")
	if initHead == "" {
		initHead, _ = git(repo, "rev-parse", "HEAD")
	}
	mustGit(t, repo, "tag", "-f", "main", initHead)
	mustGit(t, repo, "checkout", "-q", "-b", "feat")
	commitFile(t, repo, "feat.txt")
	mustGit(t, repo, "checkout", "-q", "main")
	mustGit(t, repo, "merge", "-q", "feat")
	wt := filepath.Join(filepath.Dir(repo), "repo-worktrees", "feat")
	mustGit(t, repo, "worktree", "add", wt, headBranchRef("feat"))
	if err := writeSession(Session{
		Identity: "cursor:implementer:fe0101",
		Branch:   "feat",
		Worktree: wt,
		Created:  "2020-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ss := range sessions {
		if ss.Branch == "feat" {
			found = true
			if !ss.Merged {
				t.Fatalf("feat should be merged into main; sessions=%+v", sessions)
			}
		}
	}
	if !found {
		t.Fatal("expected feat session")
	}
}

func TestNewSessionBaseOriginMain(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "-u", "origin", "main")
	mustGit(t, repo, "fetch", "-q", "origin", "main")
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleSpecAuthor, Issue: "WP-L7-01", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Base origin/main: %v", err)
	}
	if !strings.HasPrefix(s.Branch, "WP-L7-01-") {
		t.Fatalf("unexpected branch %+v", s)
	}
}

func TestNewSessionBaseMainWithTagShadow(t *testing.T) {
	repo := gitRepo(t)
	initHead, _ := git(repo, "rev-parse", "HEAD")
	mustGit(t, repo, "tag", "-f", "main", initHead)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L7-02", Base: "main"})
	if err != nil {
		t.Fatalf("Base main with tag shadow: %v", err)
	}
	if s.Branch == "" || s.Worktree == "" {
		t.Fatalf("expected worktree: %+v", s)
	}
}

func TestResolveBaseRejectsLeadingDash(t *testing.T) {
	repo := gitRepo(t)
	if _, err := ListSessions(repo, "-evil"); err != nil {
		t.Fatal("ListSessions must not error on unresolvable base")
	}
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L7-03", Base: "-x"}); err == nil {
		t.Fatal("NewSession must reject base -x")
	}
}

func repoWithOnlyOriginMain(t *testing.T) string {
	t.Helper()
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "-u", "origin", "main")
	mustGit(t, repo, "checkout", "-q", "--detach", "HEAD")
	mustGit(t, repo, "branch", "-D", "main")
	if _, err := git(repo, "rev-parse", "--verify", headBranchRef("main")); err == nil {
		t.Fatal("local main should be gone")
	}
	if _, err := git(repo, "rev-parse", "--verify", remoteBranchRef("main")); err != nil {
		t.Fatal("origin/main should exist")
	}
	return repo
}

func TestListSessionsDefaultBaseOnlyOriginMain(t *testing.T) {
	repo := repoWithOnlyOriginMain(t)
	if _, err := ListSessions(repo, "main"); err != nil {
		t.Fatalf("ListSessions with default base when only origin/main exists: %v", err)
	}
}

func TestNewSessionDefaultBaseOnlyOriginMain(t *testing.T) {
	repo := repoWithOnlyOriginMain(t)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L7-04"})
	if err != nil {
		t.Fatalf("NewSession default base from origin/main: %v", err)
	}
	if !strings.HasPrefix(s.Branch, "WP-L7-04-") {
		t.Fatalf("unexpected session %+v", s)
	}
}

func TestListSessionsUnresolvableBaseListsAnyway(t *testing.T) {
	repo := gitRepo(t)
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleDispatcher, NoWorktree: true}); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions(repo, "no-such-base")
	if err != nil {
		t.Fatalf("ListSessions must not fail on unresolvable base: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected one session, got %+v", sessions)
	}
	if sessions[0].Merged {
		t.Fatal("merged must be unknown when base does not resolve")
	}
}

func TestReleaseSessionUnresolvableBaseFailSafe(t *testing.T) {
	repo := gitRepo(t)
	s, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L7-05"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseIdentity(s.Identity)
	if _, err := ReleaseSession(repo, id.Instance, "no-such-base", false); err == nil {
		t.Fatal("must not release unproven merge without force")
	} else if !strings.Contains(err.Error(), "no-such-base") {
		t.Fatalf("error should name base: %v", err)
	}
}

func TestResolveBaseUnknownRejectsNewSession(t *testing.T) {
	repo := gitRepo(t)
	if _, err := NewSession(repo, NewSessionRequest{Harness: "cursor", Role: RoleImplementer, Issue: "WP-L7-06", Base: "no-such-base"}); err == nil {
		t.Fatal("unknown base must fail NewSession")
	} else if !strings.Contains(err.Error(), "no-such-base") || !strings.Contains(err.Error(), "local branch") {
		t.Fatalf("error should explain accepted bases: %v", err)
	}
}

func TestListSessionsWithOriginMainBase(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "-u", "origin", "main")
	mustGit(t, repo, "fetch", "-q", "origin", "main")
	if _, err := ListSessions(repo, "origin/main"); err != nil {
		t.Fatalf("ListSessions origin/main base: %v", err)
	}
}

func TestContinueABranchFromAnotherClone(t *testing.T) {
	repo := gitRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	initBareRemote(t, repo, remote)
	mustGit(t, repo, "remote", "add", "origin", remote)
	mustGit(t, repo, "push", "-q", "origin", "main")
	// Another machine starts the work package and pushes its branch.
	other := filepath.Join(t.TempDir(), "other")
	cloneMain(t, repo, remote, other)
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
