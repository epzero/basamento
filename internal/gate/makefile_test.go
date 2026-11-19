package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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

func TestMakefileVulnRecipeFailsClosed(t *testing.T) {
	body := makefileBody(t)
	if regexp.MustCompile(`module-roots\s*\|`).MatchString(body) {
		t.Fatal("Makefile vuln: must not pipe module-roots (fail-open when gate exits non-zero)")
	}
	if !strings.Contains(body, `roots="$$($(GATE) module-roots)" || exit 1`) {
		t.Fatal("Makefile vuln: must capture module-roots exit status before scanning")
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
