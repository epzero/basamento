package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
		cmd := exec.Command("go", "test", "-v", "./internal/bdd/", "-count=1", "-run", "TestFeatures")
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "BDD_TAGS=@id:"+id)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil && baseRunPostgresInfraFailure(out) {
			return activated, nil, fmt.Errorf("redgreen: %s base run could not run Postgres, the red proof is vacuous; run make pg-prefetch or set EP_SUPERVISOR/BDD_POSTGRES_URL, then retry:\n%s", id, tail(out, 20))
		}
		if runErr != nil {
			continue // expected red
		}
		switch outcome, reason := classifyBaseRun(wt, out); outcome {
		case baseRunSkipped:
			return activated, nil, fmt.Errorf("redgreen: base run for %s skipped the Postgres pass (%s); run make pg-prefetch or set BDD_POSTGRES_URL, then retry:\n%s", id, reason, tail(out, 20))
		case baseRunUnproven:
			return activated, nil, fmt.Errorf("redgreen: base run for %s left no evidence that the scenario ran, so the red proof is vacuous (ADR-0015 report or step summary); check the base's .gate/bdd.json and BDD_POSTGRES_URL, then retry:\n%s", id, tail(out, 20))
		}
		errs = append(errs, fmt.Sprintf("%s passes against %s without this change: the scenario does not test the new behavior\n%s", id, baseRef, tail(out, 20)))
	}
	return activated, errs, nil
}

// baseOutcome classifies a base run that exited zero.
type baseOutcome int

const (
	// baseRunPassed means the scenario ran and passed at base.
	baseRunPassed baseOutcome = iota
	// baseRunSkipped means a Postgres pass was skipped at base.
	baseRunSkipped
	// baseRunUnproven means the run left no evidence the scenario executed.
	baseRunUnproven
)

// baseRunPostgresInfraFailure reports whether a failed base run failed at the
// Postgres bootstrap or on the strict skip rule (ADR-0015) rather than on the
// behavior under test. Such a failure is not a red proof: counting it would
// make the delta vacuous.
func baseRunPostgresInfraFailure(out []byte) bool {
	for _, marker := range []string{
		"embedded postgres unavailable",
		"BDD_POSTGRES_URL is set but the server is unavailable",
		"gate strict: ",
	} {
		if bytes.Contains(out, []byte(marker)) {
			return true
		}
	}
	return false
}

// classifyBaseRun judges a base run that exited zero. The base's own report
// names the skip reason when it writes one (ADR-0015 .gate/bdd.json); the step
// summary is the evidence that the scenario actually ran, because godog counts
// a scenario skipped by a Before hook as passed and prints no passed step.
func classifyBaseRun(wt string, out []byte) (baseOutcome, string) {
	reason, reported := skippedRunsAt(filepath.Join(wt, ".gate", "bdd.json"))
	ran, passed := stepSummary(out)
	if passed {
		return baseRunPassed, ""
	}
	if reported {
		return baseRunSkipped, reason
	}
	if ran {
		return baseRunSkipped, "every step was skipped"
	}
	return baseRunUnproven, ""
}

// skippedRunsAt reads a base run's .gate/bdd.json, returning the skip reasons
// when it recorded any. A missing or unreadable report says nothing.
func skippedRunsAt(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var rep struct {
		SkipRuns map[string]int `json:"skip_runs"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return "", false
	}
	reasons := make([]string, 0, len(rep.SkipRuns))
	for reason, n := range rep.SkipRuns {
		if n > 0 {
			reasons = append(reasons, fmt.Sprintf("%s: %d", reason, n))
		}
	}
	if len(reasons) == 0 {
		return "", false
	}
	sort.Strings(reasons)
	return strings.Join(reasons, ", "), true
}

// stepSummary reports whether the pretty formatter printed a step summary at
// all, and whether any step passed. A skipped scenario's summary counts only
// skipped steps (for example "1 steps (1 skipped)").
func stepSummary(out []byte) (ran bool, passed bool) {
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, " steps (") {
			continue
		}
		ran = true
		if strings.Contains(line, " passed") {
			passed = true
		}
	}
	return ran, passed
}

// buildBaseForRedGreen compiles the base worktree's BDD test package under
// GOTOOLCHAIN=auto without running it: go test -c builds the test binary but
// writes it to os.DevNull, so internal/bdd compiles even though it holds only
// _test.go files, which go build refuses, and no init or TestMain executes.
// The per-scenario check that follows still runs TestFeatures. A base whose go
// directive the toolchain cannot satisfy, or whose test file does not compile,
// therefore fails closed instead of producing vacuous red→green.
func buildBaseForRedGreen(wt string) error {
	cmd := exec.Command("go", "test", "-c", "-o", os.DevNull, "./internal/bdd/")
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
