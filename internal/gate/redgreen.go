package gate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Activated returns scenario ids that become executable in head relative to
// base: new non-planned scenarios, and scenarios whose @planned tag was removed.
func Activated(base, head *Spec) []string {
	before := map[string]*Scenario{}
	if base != nil {
		for _, sc := range base.Scenarios {
			before[sc.ID] = sc
		}
	}
	var out []string
	for _, sc := range head.Scenarios {
		if sc.ID == "" || sc.IsPlanned || sc.Has("@live") || sc.Has("@eval") || sc.Has("@journey") {
			continue
		}
		if prev, ok := before[sc.ID]; !ok || prev.IsPlanned {
			out = append(out, sc.ID)
		}
	}
	sort.Strings(out)
	return out
}

// RedGreen checks that each activated scenario fails against base code (with
// head's specs) — proof that the scenario tests something — while the regular
// BDD run proves it passes at head. A base without a BDD harness counts as red.
func RedGreen(root, baseRef string) (activated []string, errs []string, err error) {
	if err := checkBaseRef(root, baseRef); err != nil {
		return nil, nil, err
	}
	head, err := Load(root)
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "gate-base-")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	wt := filepath.Join(tmp, "base")
	if out, err := run(root, "git", "worktree", "add", "--detach", wt, baseRef); err != nil {
		return nil, nil, fmt.Errorf("git worktree add %s: %w: %s", baseRef, err, out)
	}
	defer run(root, "git", "worktree", "remove", "--force", wt) //nolint:errcheck // best-effort cleanup

	var base *Spec
	if _, statErr := os.Stat(filepath.Join(wt, "specs")); statErr == nil {
		if base, err = Load(wt); err != nil {
			return nil, nil, fmt.Errorf("load base specs: %w", err)
		}
	}
	activated = Activated(base, head)
	if len(activated) == 0 {
		return nil, nil, nil
	}
	if _, statErr := os.Stat(filepath.Join(wt, "internal", "bdd")); statErr != nil {
		return activated, nil, nil // no harness at base: every scenario is red
	}
	if err := buildBaseForRedGreen(wt); err != nil {
		return nil, nil, err
	}
	if err := os.RemoveAll(filepath.Join(wt, "specs")); err != nil {
		return nil, nil, err
	}
	if out, err := run(root, "cp", "-R", filepath.Join(root, "specs"), filepath.Join(wt, "specs")); err != nil {
		return nil, nil, fmt.Errorf("copy specs: %w: %s", err, out)
	}
	for _, id := range activated {
		cmd := exec.Command("go", "test", "./internal/bdd/", "-count=1", "-run", "TestFeatures")
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "BDD_TAGS=@id:"+id)
		if out, err := cmd.CombinedOutput(); err == nil {
			errs = append(errs, fmt.Sprintf("%s passes against %s without this change: the scenario does not test the new behavior\n%s", id, baseRef, tail(out, 20)))
		}
	}
	return activated, errs, nil
}

// buildBaseForRedGreen verifies the base worktree compiles under GOTOOLCHAIN=auto so
// a newer go directive on the base ref fails closed instead of vacuous red→green.
// It runs the same compile as each per-scenario check (go test -run '^$'), not go build:
// on main, internal/bdd is test-only and go build ./internal/bdd/ refuses that package.
func buildBaseForRedGreen(wt string) error {
	cmd := exec.Command("go", "test", "-count=1", "-run", `^$`, "./internal/bdd/")
	cmd.Dir = wt
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("redgreen: base worktree did not build: %w\n%s", err, tail(out, 20))
	}
	return nil
}

func run(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(b []byte, n int) string {
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}
