package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const reqs = "### REQ-X-001 — one\nThe system shall.\n### REQ-X-002 — two\nThe system shall.\n"
const wps = "### WP-L1-01 — something\n"

func feature(body string) string {
	return "Feature: f\n\n" + body
}

func load(t *testing.T, files map[string]string) *Spec {
	t.Helper()
	s, err := Load(writeTree(t, files))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func hasErr(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestLintCatchesViolations(t *testing.T) {
	s := load(t, map[string]string{
		"specs/x/requirements.md": reqs,
		"docs/roadmap/L1-test.md": wps,
		"specs/x/a.feature": feature(`
  @REQ-X-001
  Scenario: no id
    Given a

  @REQ-X-009 @id:X-001-01
  Scenario: unknown requirement
    Given a

  @REQ-X-001 @id:X-001-01 @wip
  Scenario: duplicate id and banned tag
    Given a

  @REQ-X-001 @id:X-001-03 @planned
  Scenario: planned without wp
    Given a

  @REQ-X-001 @id:X-001-04 @planned @wp:WP-L9-99
  Scenario: planned with unknown wp
    Given a

  @REQ-X-001 @id:X-001-05 @quarantine @issue:1 @until:2000-01-01
  Scenario: expired quarantine
    Given a

  @REQ-X-001 @id:X-001-06 @runtime:mars @mystery
  Scenario: bad enum and unknown tag
    Given a
`),
	})
	errs := Lint(s, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"missing scenario-level @id",
		"@REQ-X-009 is not defined",
		"duplicate @id:X-001-01",
		"tag @wip is not allowed",
		"@planned needs @wp:",
		"@wp:WP-L9-99 is not a work package",
		"quarantine expired",
		"@runtime:mars must be one of",
		"unknown tag @mystery",
	} {
		if !hasErr(errs, want) {
			t.Errorf("missing lint error %q in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
	_, terrs := Trace(s)
	if !hasErr(terrs, "REQ-X-002") {
		t.Errorf("trace should report REQ-X-002 without scenarios: %v", terrs)
	}
}

func TestHashIgnoresTagsButNotSteps(t *testing.T) {
	base := map[string]string{"specs/x/requirements.md": reqs}
	mk := func(tags, step string) string {
		f := map[string]string{}
		for k, v := range base {
			f[k] = v
		}
		f["specs/x/a.feature"] = feature("  " + tags + "\n  Scenario: s\n    Given " + step + "\n")
		return load(t, f).Scenarios[0].Hash
	}
	h1 := mk("@REQ-X-001 @id:X-001-01", "a thing")
	if h2 := mk("@REQ-X-001 @id:X-001-01 @slow", "a   thing"); h1 != h2 {
		t.Errorf("tags and whitespace must not change the hash")
	}
	if h3 := mk("@REQ-X-001 @id:X-001-01", "another thing"); h1 == h3 {
		t.Errorf("step text must change the hash")
	}
}

const (
	author   = "claude-code:spec-author:a1b2c3"
	reviewer = "cursor:spec-reviewer:d4e5f6"
)

func approve(t *testing.T, s *Spec, l Ledger, ids []string, tier, a, r, note string) []string {
	t.Helper()
	done, err := Approve(s, l, ApproveRequest{IDs: ids, Tier: tier, Author: a, Reviewer: r, Note: note, Date: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func TestLockDetectsChangesAndBadApprovals(t *testing.T) {
	files := map[string]string{
		"specs/x/requirements.md": reqs,
		"specs/x/a.feature":       feature("  @REQ-X-001 @id:X-001-01\n  Scenario: s\n    Given a\n"),
		"specs/x/b.feature":       feature("  @REQ-X-002 @id:X-002-01 @security\n  Scenario: t\n    Given b\n"),
	}
	root := writeTree(t, files)
	s, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	l := Ledger{}
	if errs := CheckLock(s, l); !hasErr(errs, "not approved") {
		t.Fatalf("unapproved scenarios must fail: %v", errs)
	}
	for _, bad := range []struct{ a, r, why string }{
		{author, "claude-code:spec-reviewer:a1b2c3", "same instance"},
		{author, "claude-code:implementer:d4e5f6", "reviewer role"},
		{"claude-code:spec-author:a1b2", reviewer, "malformed instance"},
		{"gpt:spec-author", reviewer, "malformed identity"},
	} {
		if _, err := Approve(s, l, ApproveRequest{IDs: []string{"*unapproved"}, Tier: "P3", Author: bad.a, Reviewer: bad.r, Note: "n"}); err == nil {
			t.Errorf("approval with %s must be refused", bad.why)
		}
	}
	if _, err := Approve(s, l, ApproveRequest{IDs: []string{"X-002-01"}, Tier: "P3", Author: author, Reviewer: reviewer}); err == nil {
		t.Error("a P0 approval without a note must be refused")
	}
	if done := approve(t, s, l, []string{"*unapproved"}, "P3", author, reviewer, "critical area"); len(done) != 2 {
		t.Fatalf("approve: %v", done)
	}
	if e, cap := l.Entry("X-002-01"); e.Tier != "P0" || cap != "x" {
		t.Errorf("@security scenario must be P0 in specs/x/, got %s in %s", e.Tier, cap)
	}
	if errs := CheckLock(s, l); len(errs) != 0 {
		t.Fatalf("approved lock must pass: %v", errs)
	}
	if errs := ReleaseCheck(l); !hasErr(errs, "X-002-01") {
		t.Errorf("P0 without human acceptance must block release: %v", errs)
	}
	if err := l.Write(root); err != nil {
		t.Fatal(err)
	}
	reread, err := ReadLedger(root)
	if err != nil || len(reread["x"].Scenarios) != 2 {
		t.Fatalf("ledger round trip: %v %v", reread, err)
	}
	// Edit the approved scenario: the lock must catch it, and re-approval floors at P1.
	if err := os.WriteFile(filepath.Join(root, "specs/x/a.feature"), []byte(feature("  @REQ-X-001 @id:X-001-01\n  Scenario: s\n    Given a changed step\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, _ := Load(root)
	if errs := CheckLock(s2, l); !hasErr(errs, "text changed since approval") {
		t.Fatalf("edited scenario must fail the lock: %v", errs)
	}
	approve(t, s2, l, []string{"X-001-01"}, "P3", author, reviewer, "reworded")
	if e, _ := l.Entry("X-001-01"); e.Tier != "P1" {
		t.Errorf("changing an approved scenario must be at least P1, got %s", e.Tier)
	}
}

func TestIdentity(t *testing.T) {
	for in, ok := range map[string]bool{
		"claude-code:implementer:a1b2c3": true,
		"cursor:spec-reviewer:000fff":    true,
		"human:ana":                      true,
		"human:spec-author:a1b2c3":       false,
		"claude-code:wizard:a1b2c3":      false,
		"claude-code:implementer:A1B2C3": false,
		"implementer:a1b2c3":             false,
	} {
		if _, err := ParseIdentity(in); (err == nil) != ok {
			t.Errorf("ParseIdentity(%q) error = %v, want ok=%v", in, err, ok)
		}
	}
	a, err := NewIdentity("Claude-Code", RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewIdentity("claude-code", RoleImplementer)
	if a.Harness != "claude-code" || a.Instance == b.Instance {
		t.Errorf("new identities must be normalized and distinct: %s %s", a, b)
	}
}

func TestActivated(t *testing.T) {
	base := load(t, map[string]string{
		"specs/x/requirements.md": reqs,
		"specs/x/a.feature": feature(`
  @REQ-X-001 @id:X-001-01
  Scenario: existing
    Given a

  @REQ-X-001 @id:X-001-02 @planned @wp:WP-L1-01
  Scenario: was planned
    Given a
`),
	})
	head := load(t, map[string]string{
		"specs/x/requirements.md": reqs,
		"specs/x/a.feature": feature(`
  @REQ-X-001 @id:X-001-01
  Scenario: existing
    Given a

  @REQ-X-001 @id:X-001-02 @wp:WP-L1-01
  Scenario: was planned
    Given a

  @REQ-X-002 @id:X-002-01
  Scenario: new
    Given b

  @REQ-X-002 @id:X-002-02 @live
  Scenario: new live
    Given b
`),
	})
	got := strings.Join(Activated(base, head), ",")
	if got != "X-001-02,X-002-01" {
		t.Errorf("activated = %s", got)
	}
}

func TestMatchLayer(t *testing.T) {
	ps := []string{"runtime", "runtime/temporal", "core"}
	cases := map[string]string{
		"runtime":                   "runtime",
		"runtime/temporal":          "runtime/temporal",
		"runtime/temporal/embedded": "runtime/temporal",
		"runtimes":                  "",
		"core/coretest":             "core",
	}
	for in, want := range cases {
		if got := matchLayer(in, ps); got != want {
			t.Errorf("matchLayer(%q) = %q, want %q", in, got, want)
		}
	}
}

// A nested module under the module path is not part of ./...'s package list;
// its packages are external to this module's layer graph (presets/codegen).
func TestLayeringTreatsNestedModuleImportsAsExternal(t *testing.T) {
	root := writeTree(t, map[string]string{
		"layering.yaml": "module: m\nlayers:\n  a: []\n  b: [a]\n  c: []\n",
	})
	bin := t.TempDir()
	script := `#!/bin/sh
cat <<'EOF'
{"ImportPath":"m/a","Imports":["m/presets/codegen"]}
{"ImportPath":"m/b","Imports":["m/a","m/presets/codegen"]}
{"ImportPath":"m/c","Imports":["m/a","m/presets/codegen"]}
EOF
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	errs, err := Layering(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "c (layer c)") || !strings.Contains(errs[0], "a (layer a)") {
		t.Fatalf("errors %v, want only c -> a; the nested module must be skipped", errs)
	}
}

func TestReduceDropsImpliedEdges(t *testing.T) {
	edges := map[string]map[string]bool{
		"a": {"b": true, "c": true},
		"b": {"c": true},
		"c": {},
	}
	reduce(edges)
	if edges["a"]["c"] || !edges["a"]["b"] || !edges["b"]["c"] {
		t.Fatalf("a→c is implied by a→b→c and must go: %v", edges)
	}
}

func TestDocsDetectsStaleBlocks(t *testing.T) {
	begin, end := markers("epics")
	root := writeTree(t, map[string]string{
		"layering.yaml":                 "module: m\nlayers:\n  core: []\n",
		"docs/architecture/overview.md": "x\n<!-- BEGIN GENERATED: modules (go tool gate docs -write) -->\n<!-- END GENERATED: modules -->\n",
		"docs/roadmap/README.md":        begin + "\n" + end + "\n<!-- BEGIN GENERATED: work-packages (go tool gate docs -write) -->\n<!-- END GENERATED: work-packages -->\n",
		"docs/roadmap/L1-one.md":        "# L1 — One\n\n### WP-L1-01 — First\nDepends: none.\n",
		"docs/roadmap/L2-two.md":        "# L2 — Two\n\n### WP-L2-01 — Second\nDepends: WP-L1-01.\n",
	})
	if stale, err := Docs(root, false); err != nil || len(stale) != 3 {
		t.Fatalf("empty blocks must be stale: %v %v", stale, err)
	}
	if _, err := Docs(root, true); err != nil {
		t.Fatal(err)
	}
	if stale, err := Docs(root, false); err != nil || len(stale) != 0 {
		t.Fatalf("written blocks must be current: %v %v", stale, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "docs/roadmap/README.md"))
	if !strings.Contains(string(b), "L1 --> L2") {
		t.Fatalf("epic edge missing:\n%s", b)
	}
}

func TestReviewTiers(t *testing.T) {
	r := ReviewRules{Default: "medium", Tiers: map[string][]string{
		"high":   {".github/**", "internal/gate/**", "runtime/**", "Makefile"},
		"medium": {"specs/**"},
		"low":    {"docs/**", "**/*.md"},
	}}
	for f, want := range map[string]string{
		".github/workflows/gate.yml":  "high",
		"internal/gate/lock.go":       "high",
		"runtime/temporal/x.go":       "high",
		"Makefile":                    "high",
		"docs/roadmap/L1-platform.md": "low",
		"model/README.md":             "low",
		"specs/x/requirements.md":     "medium",
		"model/model.go":              "medium",
		"internal/gateway/x.go":       "medium",
	} {
		if got := r.TierOf(f); got != want {
			t.Errorf("TierOf(%q) = %s, want %s", f, got, want)
		}
	}
}

func TestLintTraceability(t *testing.T) {
	s := load(t, map[string]string{
		"specs/x/requirements.md": reqs + "### REQ-X-003 — gone\nStatus: Retired\n### REQ-X-004 — also gone\nStatus: Retired\n",
		"specs/x/a.feature": feature(`
  @REQ-X-002 @id:X-001-01
  Scenario: id names another requirement
    Given a

  @REQ-X-002 @REQ-X-003 @id:X-002-01
  Scenario: points at a retired requirement
    Given a

  @REQ-X-001 @id:X-001-02 @quarantine @issue:1 @until:2026-01-01
  Scenario: quarantined through today
    Given a
`),
	})
	errs := Lint(s, time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"@id:X-001-01 names REQ-X-001 as its primary requirement",
		"@REQ-X-003 is retired",
	} {
		if !hasErr(errs, want) {
			t.Errorf("missing lint error %q in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
	if hasErr(errs, "quarantine expired") {
		t.Errorf("a quarantine holds through its @until day:\n%s", strings.Join(errs, "\n"))
	}
	if errs := Lint(s, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); !hasErr(errs, "quarantine expired") {
		t.Errorf("a quarantine expires the day after @until:\n%s", strings.Join(errs, "\n"))
	}
	if _, terrs := Trace(s); hasErr(terrs, "REQ-X-004") {
		t.Errorf("a retired requirement needs no scenario: %v", terrs)
	}
}

func TestReapprovalKeepsDecisions(t *testing.T) {
	s := load(t, map[string]string{
		"specs/x/requirements.md": reqs,
		"specs/x/a.feature":       feature("  @REQ-X-001 @id:X-001-01\n  Scenario: s\n    Given a\n"),
	})
	l := Ledger{}
	approve(t, s, l, []string{"X-001-01"}, "P0", author, reviewer, "durability")
	e, _ := l.Entry("X-001-01")
	e.Human = Human{Status: "rejected", By: "human:ana", At: "2026-01-02"}

	approve(t, s, l, []string{"X-001-01"}, "P3", author, reviewer, "second look")
	e, _ = l.Entry("X-001-01")
	if e.Human.Status != "rejected" {
		t.Errorf("approving unchanged text must not clear a person's rejection, got %q", e.Human.Status)
	}
	if e.Tier != "P0" {
		t.Errorf("approving unchanged text must not lower its tier, got %s", e.Tier)
	}
	if errs := CheckLock(s, l); !hasErr(errs, "rejected by a human reviewer") {
		t.Errorf("the rejection must still fail the lock: %v", errs)
	}

	e.Human = Human{Status: "accepted"}
	if errs := CheckLock(s, l); !hasErr(errs, "needs `by: human:<login>`") {
		t.Errorf("an acceptance without a person must fail the lock: %v", errs)
	}
}

func TestBaseRefsAreNotGitOptionsAndMustResolveToACommit(t *testing.T) {
	root := gitRepo(t)
	for _, base := range []string{"-x", "--all", "-"} {
		if _, err := ChangedFiles(root, base, ""); err == nil || !strings.Contains(err.Error(), "begins with '-'") {
			t.Errorf("ChangedFiles must refuse %q before git reads it as an option: %v", base, err)
		}
		if _, err := PlanReview(root, base, "", nil); err == nil || !strings.Contains(err.Error(), "begins with '-'") {
			t.Errorf("PlanReview must refuse %q before git reads it as an option: %v", base, err)
		}
		if _, _, err := RedGreen(root, base); err == nil || !strings.Contains(err.Error(), "begins with '-'") {
			t.Errorf("RedGreen must refuse %q before git reads it as an option: %v", base, err)
		}
	}
	for _, fn := range []func(string) error{
		func(base string) error { _, err := ChangedFiles(root, base, ""); return err },
		func(base string) error { _, err := PlanReview(root, base, "", nil); return err },
		func(base string) error { _, _, err := RedGreen(root, base); return err },
	} {
		// A revision-set negation exits 0 in rev-parse and prints ^<sha>; if
		// it passed, `git show ^main:review.yaml` would print nothing and
		// PlanReview would silently fall back to the default tier.
		for _, base := range []string{"origin/nope", "^main", "^HEAD", "^main^{commit}"} {
			if err := fn(base); err == nil || !strings.Contains(err.Error(), "does not name a commit") {
				t.Errorf("base %q must be refused as not naming a commit: %v", base, err)
			}
		}
	}

	if err := os.WriteFile(filepath.Join(root, "review.yaml"), []byte("default: medium\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"main", sha} {
		if _, err := PlanReview(root, base, "", []string{"a.go"}); err != nil {
			t.Errorf("review plan still accepts %q: %v", base, err)
		}
	}
}

func TestRemoteGitTimeoutErrorSaysWhatToCheck(t *testing.T) {
	repo := gitRepo(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	_, err := gitRemoteContext(ctx, repo, "fetch", "origin")
	if err == nil {
		t.Fatal("an unreachable remote must fail instead of hanging")
	}
	for _, want := range []string{"timed out after", "git remote -v", "network", "credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout error must say what to check (%q): %v", want, err)
		}
	}
}

func TestHeadRefIsNotAGitOption(t *testing.T) {
	root := gitRepo(t)
	for _, head := range []string{"--all", "-x", "--upload-pack=sh"} {
		if _, err := ChangedFiles(root, "main", head); err == nil || !strings.Contains(err.Error(), "begins with '-'") {
			t.Errorf("ChangedFiles must refuse head %q before git reads it as an option: %v", head, err)
		}
	}
}

func TestNewSessionRefsAreNotGitOptions(t *testing.T) {
	repo := gitRepo(t)
	for _, r := range []NewSessionRequest{
		{Harness: "codex", Role: RoleImplementer, Issue: "WP-L1-08", Base: "--no-checkout"},
		{Harness: "codex", Role: RoleImplementer, Issue: "WP-L1-08", Branch: "--upload-pack=sh"},
	} {
		if _, err := NewSession(repo, r); err == nil || !strings.Contains(err.Error(), "begins with '-'") {
			t.Errorf("NewSession must refuse %+v before git reads it as an option: %v", r, err)
		}
	}
}

func TestChangedFilesStartsAtTheMergeBase(t *testing.T) {
	root := t.TempDir()
	commit := func(file, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, file), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, root, "add", ".")
		mustGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", msg)
	}
	mustGit(t, root, "init", "-q", "-b", "main")
	commit("shared.go", "start")
	mustGit(t, root, "checkout", "-q", "-b", "work")
	commit("mine.go", "my change")
	mustGit(t, root, "checkout", "-q", "main")
	commit("theirs.go", "landed meanwhile")

	got, err := ChangedFiles(root, "main", "work")
	if err != nil || strings.Join(got, ",") != "mine.go" {
		t.Fatalf("a branch's change is what it added since leaving main: %v %v", got, err)
	}
	mustGit(t, root, "checkout", "-q", "work")
	if err := os.WriteFile(filepath.Join(root, "shared.go"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err = ChangedFiles(root, "main", ""); err != nil || strings.Join(got, ",") != "mine.go,shared.go" {
		t.Fatalf("without a head, uncommitted edits count: %v %v", got, err)
	}
}

func TestWorkPackageStates(t *testing.T) {
	root := writeTree(t, map[string]string{
		"specs/x/requirements.md": reqs,
		"docs/roadmap/L1-test.md": "# L1 — Test\n\n" +
			"### WP-L1-01 — first\nDepends: none.\nOwns: `a`, `b/c`.\n\n" +
			"### WP-L1-02 — second\nDepends: WP-L1-01.\nOwns: `d`.\n\n" +
			"### WP-L1-03 — third\nDepends: WP-L1-02.\nOwns: `e`.\n",
		"specs/x/a.feature": feature("  @REQ-X-001 @id:X-001-01 @wp:WP-L1-01\n  Scenario: s\n    Given a\n\n" +
			"  @REQ-X-002 @id:X-002-01 @planned @wp:WP-L1-02\n  Scenario: t\n    Given b\n"),
	})
	wps, err := WorkPackages(root)
	if err != nil || len(wps) != 3 {
		t.Fatalf("work packages: %+v, %v", wps, err)
	}
	if wps[0].State != WPDone || strings.Join(wps[0].Owns, ",") != "a,b/c" {
		t.Errorf("a work package whose scenarios are all active is done: %+v", wps[0])
	}
	if wps[1].State != WPReady {
		t.Errorf("planned scenarios and done dependencies mean ready: %+v", wps[1])
	}
	if wps[2].State != WPBlocked || strings.Join(wps[2].Waiting, ",") != "WP-L1-02" {
		t.Errorf("an unfinished dependency blocks: %+v", wps[2])
	}
}

func TestOwnershipOverlapsNeedAnOrder(t *testing.T) {
	epics := []Epic{{ID: "L1", WPs: []WorkPackage{
		{ID: "WP-L1-01", Owns: []string{"contracts"}},
		{ID: "WP-L1-02", Owns: []string{"contracts/worker"}},
		{ID: "WP-L1-03", Owns: []string{"contracts/worker/v2"}, Depends: []string{"WP-L1-02"}},
		{ID: "WP-L1-04", Owns: []string{"contractsx"}},
	}}}
	got := OwnershipOverlaps(epics)
	if len(got) != 2 || !hasErr(got, "WP-L1-01 owns contracts and WP-L1-02 owns contracts/worker") || !hasErr(got, "WP-L1-03") {
		t.Fatalf("unordered overlaps must be reported, ordered and unrelated ones not: %v", got)
	}
}
