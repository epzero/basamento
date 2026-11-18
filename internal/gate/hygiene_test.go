package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCodeHygieneCatchesTestDeterminismViolations(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pkg/bad_time_randv2_test.go": `package pkg

import (
	"math/rand/v2"
	t2 "time"
)

func f() {
	_ = t2.Now()
	_ = t2.Since(t2.Date(2026, 1, 1, 0, 0, 0, 0, t2.UTC))
	_ = t2.Until(t2.Date(2026, 1, 1, 0, 0, 0, 0, t2.UTC))
	t2.Sleep(0)
	_ = rand.IntN(10)
}
`,
		"pkg/bad_randv1_crypto_test.go": `package pkg

import (
	crand "crypto/rand"
	"math/rand"
)

func g() {
	_ = rand.Intn(10)
	var b [4]byte
	_, _ = crand.Read(b[:])
}
`,
		"pkg/bad_dot_test.go": `package pkg

import (
	. "math/rand/v2"
	. "time"
)

func h() {
	_ = Now()
	_ = IntN(5)
}
`,
		"internal/bdd/steps/bad.go": `package steps

import (
	"crypto/rand"
	"time"
)

func step() {
	_ = time.Now()
	var b [8]byte
	_, _ = rand.Read(b[:])
}
`,
		"core/coretest/bad_fake.go": `package coretest

import "time"

func BadNow() time.Time { return time.Now() }
`,
		"pkg/good_test.go": `package pkg

import (
	"math/rand/v2"
	"time"
)

type fakeClock struct{}

func (fakeClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func pick(rand *rand.Rand) int { return rand.IntN(10) }

func ok() {
	_ = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewPCG(1, 2))
	_ = pick(rng)
	time := fakeClock{}
	_ = time.Now()
}
`,
	})
	errs, err := CheckCodeHygiene(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pkg/bad_time_randv2_test.go:9: test uses t2.Now",
		"pkg/bad_time_randv2_test.go:10: test uses t2.Since",
		"pkg/bad_time_randv2_test.go:11: test uses t2.Until",
		"pkg/bad_time_randv2_test.go:12: test uses t2.Sleep",
		"pkg/bad_time_randv2_test.go:13: test uses global rand.IntN",
		`pkg/bad_randv1_crypto_test.go:4: test imports "crypto/rand"`,
		"pkg/bad_randv1_crypto_test.go:9: test uses global rand.Intn",
		`pkg/bad_dot_test.go:4: test must not dot-import "math/rand/v2"`,
		`pkg/bad_dot_test.go:5: test must not dot-import "time"`,
		`internal/bdd/steps/bad.go:4: test imports "crypto/rand"`,
		"internal/bdd/steps/bad.go:9: test uses time.Now",
		"core/coretest/bad_fake.go:5: test uses time.Now",
	} {
		if !hasErr(errs, want) {
			t.Errorf("missing hygiene error %q in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
	if hasErr(errs, "pkg/good_test.go") {
		t.Errorf("good_test.go with time.Date, seeded rand.New and shadowed locals must pass:\n%s", strings.Join(errs, "\n"))
	}
}

func TestCheckCodeHygieneCatchesProdTestLeakage(t *testing.T) {
	root := writeTree(t, map[string]string{
		"layering.yaml": "module: m\ntest_only: [core/coretest, pkg/testkit]\ntest_only_allowed_from: [internal/bdd]\n",
		"pkg/prod.go": `package pkg

import (
	"crypto/rand"
	"testing"
	"testing/fstest"
	"time"
)

func MarshalInnerForTest() []byte { return nil }
type FakeForTest struct{}
var HookForTesting int

func ok() {
	_ = time.Now()
	_ = testing.Testing()
	_ = fstest.MapFS{}
	var b [4]byte
	_, _ = rand.Read(b[:])
}
func helperForTest() {}
`,
		"pkg/testkit/helpers.go": `package testkit

import "testing"

func NewForTest(t *testing.T) {}
`,
	})
	errs, err := CheckCodeHygiene(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`pkg/prod.go:5: production code must not import "testing"`,
		`pkg/prod.go:6: production code must not import "testing/fstest"`,
		`pkg/prod.go:10: exported identifier "MarshalInnerForTest" is test-only`,
		`pkg/prod.go:11: exported identifier "FakeForTest" is test-only`,
		`pkg/prod.go:12: exported identifier "HookForTesting" is test-only`,
	} {
		if !hasErr(errs, want) {
			t.Errorf("missing hygiene error %q in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
	for _, forbidden := range []string{
		"time.Now",
		"crypto/rand",
		"helperForTest",
		"pkg/testkit",
	} {
		if hasErr(errs, forbidden) {
			t.Errorf("unexpected error mentioning %q in:\n%s", forbidden, strings.Join(errs, "\n"))
		}
	}
}

func TestCheckSpecSeparation(t *testing.T) {
	if errs := CheckSpecSeparation([]string{"specs/sql/design.md", "specs/sql/requirements.md"}); len(errs) != 0 {
		t.Errorf("spec-only change must pass: %v", errs)
	}
	if errs := CheckSpecSeparation([]string{"platform/sql/store.go", "specs/sql/store.feature", "specs/README.md"}); len(errs) != 0 {
		t.Errorf("implementation activating scenarios must pass: %v", errs)
	}
	errs := CheckSpecSeparation([]string{"platform/sql/store.go", "specs/sql/design.md"})
	if !hasErr(errs, "specs/sql/design.md: implementation changes (*.go) must not edit specs/*/design.md") {
		t.Errorf("implementation editing design.md must fail: %v", errs)
	}
}

func TestHygieneEndToEndSpecSeparation(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, root, "init", "-q", "-b", "main")
	write("pkg/a.go", "package pkg\n")
	write("specs/x/design.md", "# Design\n")
	mustGit(t, root, "add", ".")
	mustGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "init")
	mustGit(t, root, "checkout", "-q", "-b", "work")

	// Clean spec-only edit passes.
	write("specs/x/design.md", "# Design v2\n")
	if errs, err := Hygiene(root, "main"); err != nil || len(errs) != 0 {
		t.Fatalf("spec-only edit must pass Hygiene: %v %v", errs, err)
	}

	// Adding an untracked Go file triggers spec/code separation locally before git add.
	write("pkg/new.go", "package pkg\n")
	errs, err := Hygiene(root, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(errs, "specs/x/design.md: implementation changes (*.go) must not edit specs/*/design.md") {
		t.Fatalf("untracked .go + design.md edit must fail Hygiene: %v", errs)
	}

	// Committed Go + design.md edit also fails.
	mustGit(t, root, "add", ".")
	mustGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-m", "mixed")
	errs, err = Hygiene(root, "main")
	if err != nil || !hasErr(errs, "specs/x/design.md") {
		t.Fatalf("committed mixed change must fail Hygiene: %v %v", errs, err)
	}
}

func TestCheckSpecsReadmeDetectsDriftAndIntegrationWithDocs(t *testing.T) {
	root := writeTree(t, map[string]string{
		"specs/README.md": `# Specs

| File | Role |
| --- | --- |
| [` + "`constitution.md`" + `](constitution.md) | Principles. |

| Capability | Status |
| --- | --- |
| [` + "`done`" + `](done/requirements.md) | Planned (WP-L1-01). |
| [` + "`todo`" + `](todo/requirements.md) | Implemented (WP-L1-02). |
| [` + "`partial`" + `](partial/requirements.md) | Planned (WP-L1-03, WP-L1-04). |
| [` + "`dup`" + `](dup/requirements.md) | Planned (WP-L1-06). |
| [` + "`dup`" + `](dup/requirements.md) | Implemented (WP-L1-06). |
| [` + "`orphan`" + `](orphan/requirements.md) | Planned (WP-L9-99). |
`,
		"specs/done/requirements.md":    "### REQ-DONE-001 — d\nThe system shall.\n",
		"specs/done/a.feature":          feature("  @REQ-DONE-001 @id:DONE-001-01\n  Scenario: a\n    Given a\n"),
		"specs/todo/requirements.md":    "### REQ-TODO-001 — t\nThe system shall.\n",
		"specs/todo/a.feature":          feature("  @REQ-TODO-001 @id:TODO-001-01 @planned @wp:WP-L1-02\n  Scenario: a\n    Given a\n"),
		"specs/partial/requirements.md": "### REQ-PART-001 — p\nThe system shall.\n",
		"specs/partial/a.feature": feature("  @REQ-PART-001 @id:PART-001-01\n  Scenario: a\n    Given a\n\n" +
			"  @REQ-PART-001 @id:PART-001-02 @planned @wp:WP-L1-04\n  Scenario: b\n    Given b\n"),
		"specs/dup/requirements.md":     "### REQ-DUP-001 — u\nThe system shall.\n",
		"specs/dup/a.feature":           feature("  @REQ-DUP-001 @id:DUP-001-01 @planned @wp:WP-L1-06\n  Scenario: a\n    Given a\n"),
		"specs/missing/requirements.md": "### REQ-MISS-001 — m\nThe system shall.\n",
		"specs/missing/a.feature":       feature("  @REQ-MISS-001 @id:MISS-001-01 @planned @wp:WP-L1-05\n  Scenario: a\n    Given a\n"),
	})
	errs, err := CheckSpecsReadme(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`capability "done" has all 1 scenario(s) active, but its status still says "Planned (WP-L1-01)."`,
		`capability "todo" has no active scenarios (1 planned), but its status says "Implemented (WP-L1-02)."`,
		`missing capability row for "missing"`,
		`duplicate capability row for "dup"`,
		`orphan capability row "orphan"`,
	} {
		if !hasErr(errs, want) {
			t.Errorf("missing specs/README.md error %q in:\n%s", want, strings.Join(errs, "\n"))
		}
	}
	for _, okName := range []string{"partial", "constitution.md"} {
		if hasErr(errs, okName) {
			t.Errorf("unexpected error mentioning %q in:\n%s", okName, strings.Join(errs, "\n"))
		}
	}

	// Missing specs/README.md when specs/ exists must fail.
	if err := os.Remove(filepath.Join(root, "specs/README.md")); err != nil {
		t.Fatal(err)
	}
	if errs, err := CheckSpecsReadme(root); err != nil || !hasErr(errs, "missing capability index") {
		t.Fatalf("missing specs/README.md must fail: %v %v", errs, err)
	}

	// Docs integrates CheckSpecsReadme and surfaces drift.
	begin, end := markers("epics")
	docsRoot := writeTree(t, map[string]string{
		"layering.yaml":                 "module: m\nlayers:\n  core: []\n",
		"docs/architecture/overview.md": "x\n<!-- BEGIN GENERATED: modules (go tool gate docs -write) -->\n<!-- END GENERATED: modules -->\n",
		"docs/roadmap/README.md":        begin + "\n" + end + "\n<!-- BEGIN GENERATED: work-packages (go tool gate docs -write) -->\n<!-- END GENERATED: work-packages -->\n",
		"docs/roadmap/L1-one.md":        "# L1 — One\n\n### WP-L1-01 — First\nDepends: none.\n",
		"specs/README.md":               "| Capability | Status |\n| --- | --- |\n| [`done`](done/requirements.md) | Planned (WP-L1-01). |\n",
		"specs/done/requirements.md":    "### REQ-DONE-001 — d\nThe system shall.\n",
		"specs/done/a.feature":          feature("  @REQ-DONE-001 @id:DONE-001-01\n  Scenario: a\n    Given a\n"),
	})
	if _, err := Docs(docsRoot, true); err != nil {
		t.Fatal(err)
	}
	stale, err := Docs(docsRoot, false)
	if err != nil || !hasErr(stale, `capability "done"`) {
		t.Fatalf("Docs must report specs/README.md drift: %v %v", stale, err)
	}
}
