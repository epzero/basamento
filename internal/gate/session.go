package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session is one agent session: its identity and the worktree it owns.
// It is stored per worktree in the git directory (never committed), so
// sessions in different worktrees never see each other's file.
type Session struct {
	Identity string `json:"identity"`
	Parent   string `json:"parent,omitempty"`  // identity of the lead session that spawned this one
	Issue    string `json:"issue,omitempty"`   // the work item: a work package id, or a tracker key
	Branch   string `json:"branch"`            // empty for a detached review snapshot
	Reviews  string `json:"reviews,omitempty"` // branch a code reviewer's snapshot was taken from
	Worktree string `json:"worktree"`
	Created  string `json:"created"`
	// Status and Note are what the session left behind when it stopped; the
	// lead reads them to choose the next step.
	Status string `json:"status,omitempty"`
	Note   string `json:"note,omitempty"`
	// Previous is the hand-off of the session that last worked in this worktree.
	Previous *Handoff `json:"previous,omitempty"`
}

// Handoff is what one session leaves for the next one on the same work item.
type Handoff struct {
	Identity string `json:"identity"`
	Status   string `json:"status,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Statuses a session may end with (docs/engineering/agent-workflow.md §2.1).
var statuses = []string{"spec-review-requested", "spec-changes-requested", "spec-approved",
	"code-review-requested", "code-changes-requested", "ready-to-integrate", "needs-architect", "needs-human"}

// SetStatus records how a session ended. With an empty issue it is the session
// of the worktree at dir; otherwise the working session of that work item,
// which is how the lead records the outcome of a code review.
func SetStatus(dir, issue, status, note string) (Session, error) {
	if !contains(statuses, status) {
		return Session{}, fmt.Errorf("status %q is not one of %s", status, strings.Join(statuses, ", "))
	}
	var s Session
	var err error
	if issue == "" {
		if s, err = readSession(dir); err != nil {
			return Session{}, errors.New("no session in this worktree: run `go tool gate agent new` first")
		}
	} else {
		var ok bool
		if s, ok, err = issueWorktree(dir, issue, ""); err != nil {
			return Session{}, err
		} else if !ok {
			return Session{}, fmt.Errorf("no working session for %s", issue)
		}
	}
	s.Status, s.Note = status, note
	return s, writeSession(s)
}

// NewSessionRequest describes a session to start.
type NewSessionRequest struct {
	Harness    string
	Role       string
	Issue      string
	Parent     string
	Base       string // branch to start from; default "main"
	Branch     string // continue this pushed branch instead of starting one (work begun in another clone)
	NoWorktree bool   // stay in the current checkout (the default for dispatchers)
}

// A work package id (WP-L3-05) or a tracker key (ABC-12).
var issueKey = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*-\d+$`)

const sessionFile = "basamento-agent.json"

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func sessionPath(dir string) (string, error) {
	p, err := git(dir, "rev-parse", "--git-path", sessionFile)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

// inLinkedWorktree reports whether dir is a worktree other than the main checkout,
// for example one a harness created for a parallel agent.
func inLinkedWorktree(dir string) (bool, error) {
	gitDir, err := git(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, err
	}
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false, err
	}
	return filepath.Clean(gitDir) != filepath.Clean(common), nil
}

func validateBranchName(dir, branch string) error {
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, ":") || strings.Contains(branch, "..") || strings.Contains(branch, " ") || strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("branch %q is not an accepted name: use letters, digits, /, - and _; no spaces, .., leading -, :, or .lock", branch)
	}
	if _, err := git(dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("branch %q is not an accepted name: use letters, digits, /, - and _; no spaces, .., leading -, :, or .lock", branch)
	}
	return nil
}

// originHasBranch reports whether refs/heads/<branch> exists on origin.
// Exit code 2 from ls-remote means no such ref; other failures are reachability errors.
func originHasBranch(dir, branch string) (bool, error) {
	cmd := exec.Command("git", "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("cannot reach origin to check branch %q: %w; check network, credentials and git remote get-url origin", branch, err)
}

// fastForwardBranch fetches origin/<branch> and fast-forwards the checkout at dir.
// It refuses when the local branch has diverged or has unpushed commits origin lacks.
func fastForwardBranch(dir, branch string) error {
	if _, err := git(dir, "remote", "get-url", "origin"); err != nil {
		return nil // no origin: local-only worktrees need no sync
	}
	onOrigin, err := originHasBranch(dir, branch)
	if err != nil {
		return err
	}
	if !onOrigin {
		return nil
	}
	refspec := "refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := git(dir, "fetch", "origin", refspec); err != nil {
		return err
	}
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := git(dir, "rev-parse", "--verify", "origin/"+branch)
	if err != nil {
		return nil // branch not on origin yet
	}
	if head == remote {
		return nil
	}
	if _, err := git(dir, "merge-base", "--is-ancestor", head, remote); err != nil {
		return fmt.Errorf("branch %s cannot fast-forward to origin/%s: push your local commits to origin/%s first, or resolve the divergence by hand; never reset or discard commits", branch, branch, branch)
	}
	abbref, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || abbref != branch {
		return fmt.Errorf("worktree must be on branch %s to fast-forward (currently on %s); run git checkout %s in that worktree, then run gate agent new again", branch, abbref, branch)
	}
	if _, err := git(dir, "merge", "--ff-only", "origin/"+branch); err != nil {
		return fmt.Errorf("branch %s cannot fast-forward to origin/%s: fetch and resolve the divergence by hand; never reset or discard commits: %w", branch, branch, err)
	}
	return nil
}

// issueWorktree returns the latest working (non-reviewer) session of an issue
// whose worktree is not exclude.
func issueWorktree(dir, issue, exclude string) (Session, bool, error) {
	sessions, err := ListSessions(dir, "")
	if err != nil {
		return Session{}, false, err
	}
	for i := len(sessions) - 1; i >= 0; i-- {
		s := sessions[i].Session
		if s.Issue != issue || s.Branch == "" || filepath.Clean(s.Worktree) == filepath.Clean(exclude) {
			continue
		}
		if id, err := ParseIdentity(s.Identity); err == nil && id.Role == RoleCodeReviewer {
			continue
		}
		return s, true, nil
	}
	return Session{}, false, nil
}

// NewSession issues an identity and places the session in a worktree.
//
// An issue has one working branch that sessions take turns on: an author
// writes specs, a reviewer commits the approval, an implementer continues,
// a resumed session picks up after a person answered. So when the issue
// already has a worktree, a new session (other than a code reviewer) is
// recorded there and must work there. A code reviewer gets a detached
// snapshot of that branch instead, so finders can run in parallel without
// touching it. Otherwise, unless the session already runs in its own worktree
// or opts out, NewSession creates one next to the main checkout:
// <repo>-worktrees/<branch>, branch <issue>-<instance>.
func NewSession(dir string, r NewSessionRequest) (Session, error) {
	id, err := NewIdentity(r.Harness, r.Role)
	if err != nil {
		return Session{}, err
	}
	if r.Issue != "" && !issueKey.MatchString(r.Issue) {
		return Session{}, fmt.Errorf("issue %q is not a work package id (WP-L3-05) or a tracker key (ABC-12)", r.Issue)
	}
	if r.Parent != "" {
		if _, err := ParseIdentity(r.Parent); err != nil {
			return Session{}, fmt.Errorf("parent: %w", err)
		}
	}
	if r.Base == "" {
		r.Base = "main"
	}
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Session{}, err
	}
	if r.Branch != "" {
		if err := validateBranchName(top, r.Branch); err != nil {
			return Session{}, err
		}
	}
	s := Session{Identity: id.String(), Parent: r.Parent, Issue: r.Issue, Worktree: top, Created: time.Now().UTC().Format(time.RFC3339)}
	linked, err := inLinkedWorktree(top)
	if err != nil {
		return Session{}, err
	}
	if r.Issue != "" && !r.NoWorktree {
		prev, ok, err := issueWorktree(top, r.Issue, top)
		if err != nil {
			return Session{}, err
		}
		switch {
		case ok && id.Role == RoleCodeReviewer:
			return reviewSnapshot(top, linked, s, prev.Branch, id.Instance)
		case ok:
			s.Worktree, s.Branch = prev.Worktree, prev.Branch
			s.Previous = &Handoff{Identity: prev.Identity, Status: prev.Status, Note: prev.Note}
			if err := fastForwardBranch(s.Worktree, s.Branch); err != nil {
				return Session{}, err
			}
			return s, writeSession(s)
		case linked:
			if cur, err := readSession(top); err == nil && cur.Issue == r.Issue && cur.Branch != "" {
				if id.Role == RoleCodeReviewer {
					return reviewSnapshot(top, false, s, cur.Branch, id.Instance)
				}
				s.Worktree, s.Branch = top, cur.Branch
				s.Previous = &Handoff{Identity: cur.Identity, Status: cur.Status, Note: cur.Note}
				if err := fastForwardBranch(top, s.Branch); err != nil {
					return Session{}, err
				}
				return s, writeSession(s)
			}
		}
	}
	if r.Branch != "" && !r.NoWorktree {
		return continueBranch(top, linked, s, id, r.Branch)
	}
	switch {
	case linked || r.NoWorktree:
		if s.Branch, err = git(top, "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
			return Session{}, err
		}
	default:
		prefix := r.Issue
		if prefix == "" {
			prefix = id.Role
		}
		s.Branch = prefix + "-" + id.Instance
		s.Worktree = filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees", s.Branch)
		if _, err := git(top, "worktree", "add", "-b", s.Branch, s.Worktree, r.Base); err != nil {
			return Session{}, err
		}
	}
	return s, writeSession(s)
}

// continueBranch places a session on a branch that another clone pushed: a
// code reviewer on a detached snapshot of it, anyone else on the branch
// itself, in the harness's own clean worktree or a new one.
func continueBranch(top string, linked bool, s Session, id Identity, branch string) (Session, error) {
	if _, err := git(top, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		onOrigin, err := originHasBranch(top, branch)
		if err != nil {
			return Session{}, err
		}
		if !onOrigin {
			return Session{}, fmt.Errorf("branch %q is not on origin yet; push it from the clone that started it, then run gate agent new -branch %s again", branch, branch)
		}
		refspec := "refs/heads/" + branch + ":refs/remotes/origin/" + branch
		if _, err := git(top, "fetch", "origin", refspec); err != nil {
			return Session{}, err
		}
		if _, err := git(top, "branch", "--track", branch, "origin/"+branch); err != nil {
			return Session{}, err
		}
	}
	if id.Role == RoleCodeReviewer {
		return reviewSnapshot(top, linked, s, branch, id.Instance)
	}
	s.Branch = branch
	if linked {
		status, err := git(top, "status", "--porcelain")
		if err != nil {
			return Session{}, err
		}
		if status != "" {
			return Session{}, fmt.Errorf("this worktree has changes; it must be clean to check out %s", branch)
		}
		if _, err := git(top, "checkout", "-q", branch); err != nil {
			return Session{}, err
		}
		if err := fastForwardBranch(top, branch); err != nil {
			return Session{}, err
		}
		return s, writeSession(s)
	}
	s.Worktree = filepath.Join(filepath.Dir(top), filepath.Base(top)+"-worktrees", branch)
	if _, err := git(top, "worktree", "add", s.Worktree, branch); err != nil {
		if _, statErr := os.Stat(s.Worktree); statErr != nil {
			return Session{}, err
		}
		if cur, readErr := readSession(s.Worktree); readErr == nil && cur.Branch == branch {
			s.Previous = &Handoff{Identity: cur.Identity, Status: cur.Status, Note: cur.Note}
		}
		if err := fastForwardBranch(s.Worktree, branch); err != nil {
			return Session{}, err
		}
		return s, writeSession(s)
	}
	if err := fastForwardBranch(s.Worktree, branch); err != nil {
		return Session{}, err
	}
	return s, writeSession(s)
}

// reviewSnapshot places a code reviewer on a detached checkout of branch: the
// harness's own clean worktree if it runs in one, else a new worktree.
func reviewSnapshot(top string, linked bool, s Session, branch, instance string) (Session, error) {
	s.Branch, s.Reviews = "", branch
	if linked {
		status, err := git(top, "status", "--porcelain")
		if err != nil {
			return Session{}, err
		}
		if status != "" {
			return Session{}, fmt.Errorf("this worktree has changes; a code reviewer needs a clean one to check out %s", branch)
		}
		if _, err := git(top, "checkout", "-q", "--detach", branch); err != nil {
			return Session{}, err
		}
		return s, writeSession(s)
	}
	common, err := git(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Session{}, err
	}
	repo := filepath.Dir(common)
	s.Worktree = filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-worktrees", s.Issue+"-"+instance)
	if _, err := git(top, "worktree", "add", "--detach", s.Worktree, branch); err != nil {
		return Session{}, err
	}
	return s, writeSession(s)
}

func writeSession(s Session) error {
	p, err := sessionPath(s.Worktree)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

func readSession(dir string) (Session, error) {
	p, err := sessionPath(dir)
	if err != nil {
		return Session{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Session{}, err
	}
	var s Session
	return s, json.Unmarshal(b, &s)
}

// CurrentIdentity returns BASAMENTO_AGENT if set, else the identity of the
// session that owns this worktree.
func CurrentIdentity(dir string) (Identity, error) {
	if v := os.Getenv("BASAMENTO_AGENT"); v != "" {
		return ParseIdentity(v)
	}
	s, err := readSession(dir)
	if err != nil {
		return Identity{}, errors.New("no identity for this session: run `go tool gate agent new -harness <tool> -role <role>`")
	}
	return ParseIdentity(s.Identity)
}

// squashed reports whether branch was pushed and its remote branch is gone.
// A squash merge leaves no ancestry to check, and the host deletes the
// branch when it merges the pull request, so that is the trace it leaves.
// Run `git fetch --prune` first.
func squashed(dir, branch string) bool {
	if branch == "" {
		return false
	}
	remote, err := git(dir, "config", "--get", "branch."+branch+".remote")
	if err != nil || remote == "" {
		return false // never pushed
	}
	_, err = git(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	return err != nil
}

// SessionStatus is a session plus whether its branch is merged into the base.
type SessionStatus struct {
	Session
	Merged bool `json:"merged"`
}

// ListSessions returns the session of every worktree that has one.
func ListSessions(dir, base string) ([]SessionStatus, error) {
	if base == "" {
		base = "main"
	}
	out, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	if m, err := git(dir, "branch", "--format=%(refname:short)", "--merged", base); err == nil {
		for _, b := range strings.Fields(m) {
			merged[b] = true
		}
	}
	var res []SessionStatus
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		s, err := readSession(path)
		if err != nil {
			continue // a worktree without a session, such as the main checkout
		}
		res = append(res, SessionStatus{Session: s, Merged: merged[s.Branch] || squashed(dir, s.Branch)})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Created < res[j].Created })
	return res, nil
}

// ReleaseSession removes the worktree and branch of the session with the given
// instance. Without force it refuses while the branch is not merged into base.
func ReleaseSession(dir, instance, base string, force bool) (Session, error) {
	sessions, err := ListSessions(dir, base)
	if err != nil {
		return Session{}, err
	}
	for _, s := range sessions {
		id, err := ParseIdentity(s.Identity)
		if err != nil || id.Instance != instance {
			continue
		}
		if linked, _ := inLinkedWorktree(s.Worktree); !linked {
			return Session{}, errors.New("this session works in the main checkout; there is no worktree to release")
		}
		if s.Branch != "" && !s.Merged && !force {
			return Session{}, fmt.Errorf("branch %s is not merged into %s; integrate it first or pass -force", s.Branch, base)
		}
		args := []string{"worktree", "remove", s.Worktree}
		if force {
			args = []string{"worktree", "remove", "--force", s.Worktree}
		}
		if _, err := git(dir, args...); err != nil {
			return Session{}, err
		}
		if s.Branch == "" {
			return s.Session, nil // a review snapshot has no branch of its own
		}
		// -D: after a squash merge git's own -d does not see the branch as
		// merged; the check above already did.
		if _, err := git(dir, "branch", "-D", s.Branch); err != nil {
			return Session{}, err
		}
		return s.Session, nil
	}
	return Session{}, fmt.Errorf("no session with instance %q", instance)
}
