package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
)

func makefileRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "Makefile")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("Makefile not found")
		}
		root = parent
	}
}

func makefileBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(makefileRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// vulnRecipeShell is the shell fragment inside the Makefile vuln target (paths must not contain spaces).
const vulnRecipeShell = `set -e; roots="$("$GATE" module-roots)" || exit 1; [ -n "$roots" ] || { echo "gate: no module roots"; exit 1; }; for dir in $roots; do (cd "$dir" && govulncheck ./...) || exit 1; done`

func TestMakefileGatePrereqsArePhony(t *testing.T) {
	t.Helper()
	body := makefileBody(t)
	phony := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\.PHONY:\s*(.+)$`).FindAllStringSubmatch(body, -1) {
		for _, name := range strings.Fields(m[1]) {
			phony[name] = true
		}
	}
	for _, target := range []string{"gate", "static", "gomod"} {
		if !phony[target] {
			t.Fatalf("Makefile: target %q must be listed in .PHONY", target)
		}
	}
}

func TestMakefileRedgreenExportsHeadSupervisor(t *testing.T) {
	body := makefileBody(t)
	bdd := regexp.MustCompile(`(?ms)^bdd:.*?^\t.*EP_SUPERVISOR.*GATE_PG_BIN`)
	red := regexp.MustCompile(`(?ms)^redgreen:.*?EP_SUPERVISOR.*GATE_PG_BIN`)
	if !bdd.MatchString(body) {
		t.Fatal("Makefile bdd: must export EP_SUPERVISOR when GATE_PG_BIN is executable")
	}
	if !red.MatchString(body) {
		t.Fatal("Makefile redgreen: must export EP_SUPERVISOR when GATE_PG_BIN is executable (G4 parity)")
	}
}

// TestMakefileBDDHasTestTimeout pins the bdd target's explicit test timeout:
// the suite runs every feature in both profiles and takes longer than Go's
// 10-minute default, so a silent drop back to the default must fail here
// instead of panicking with "test timed out" in CI.
func TestMakefileBDDHasTestTimeout(t *testing.T) {
	body := makefileBody(t)
	block := regexp.MustCompile(`(?ms)^bdd:.*?^\S`).FindString(body)
	m := regexp.MustCompile(`-timeout[= ](\S+)`).FindStringSubmatch(block)
	if m == nil {
		t.Fatal("Makefile bdd: must pass -timeout explicitly; Go's 10m default is below the suite's cost")
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		t.Fatalf("Makefile bdd: -timeout %q is not a Go duration: %v", m[1], err)
	}
	if d < 15*time.Minute {
		t.Fatalf("Makefile bdd: -timeout %s leaves no headroom; want at least 15m", d)
	}
}

func TestMakefileVulnRecipeFailsClosed(t *testing.T) {
	body := makefileBody(t)
	if regexp.MustCompile(`module-roots\s*\|`).MatchString(body) {
		t.Fatal("Makefile vuln: must not pipe module-roots (fail-open when gate exits non-zero)")
	}
	if !strings.Contains(body, `roots="$$($(GATE) module-roots)" || exit 1`) {
		t.Fatal("Makefile vuln: must capture module-roots exit status before scanning")
	}
}

// TestMakefilePrefetchUsesPinnedBundle pins ADR-0015's single source for the
// bundle: pg-prefetch must fetch the version the driver starts (V16), so the
// strict error's "run make pg-prefetch" remedy cannot fetch a stale bundle.
func TestMakefilePrefetchUsesPinnedBundle(t *testing.T) {
	body := makefileBody(t)
	want := "--postgres-version " + string(embeddedpostgres.V16)
	recipe := regexp.MustCompile(`(?ms)^pg-prefetch:.*?(--postgres-version \S+)`)
	m := recipe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("Makefile pg-prefetch: must prefetch a pinned bundle")
	}
	if m[1] != want {
		t.Fatalf("Makefile pg-prefetch: prefetches %q, the driver starts %q; update the literal", m[1], want)
	}
}

// TestWorkflowCacheKeyDerivesModulePin pins ADR-0015's cache-key row: the key
// carries the go.mod module version, never a hand-maintained copy, so a bump
// cannot keep matching a stale entry.
func TestWorkflowCacheKeyDerivesModulePin(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(makefileRoot(t), ".github", "workflows", "gate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, `go list -m -f '{{.Version}}' github.com/fergusstrange/embedded-postgres/v2`) {
		t.Fatal("gate.yml: the Postgres cache key must derive its version from the go.mod pin")
	}
	if !regexp.MustCompile(`key: embedded-postgres-\$\{\{ env\.PG_MODULE_VERSION \}\}-`).MatchString(body) {
		t.Fatal("gate.yml: the cache key must use the derived module version")
	}
	if regexp.MustCompile(`embedded-postgres-v[0-9]`).MatchString(body) {
		t.Fatal("gate.yml: a hand-written module version in the cache key drifts from go.mod")
	}
}

func TestVulnRecipeShellFailsWhenModuleRootsFails(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "gate"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(dir, "govulncheck"), "#!/bin/sh\necho called >&2\nexit 0\n")
	cmd := exec.Command("sh", "-c", vulnRecipeShell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GATE=gate")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure when module-roots fails, got output: %s", out)
	}
	if strings.Contains(string(out), "called") {
		t.Fatalf("govulncheck should not run when module-roots fails: %s", out)
	}
}

func TestVulnRecipeShellScansEachRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(dir, "gate"), "#!/bin/sh\necho .\necho nested\n")
	log := filepath.Join(dir, "calls.log")
	writeExecutable(t, filepath.Join(dir, "govulncheck"), "#!/bin/sh\necho \"$PWD\" >>"+log+"\nexit 0\n")
	cmd := exec.Command("sh", "-c", vulnRecipeShell)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GATE=gate")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, _ := os.ReadFile(log)
	if strings.Count(string(b), "\n") != 2 {
		t.Fatalf("want 2 govulncheck runs, log:\n%s", b)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
