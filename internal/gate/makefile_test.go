package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMakefileGatePrereqsArePhony(t *testing.T) {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "Makefile")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("Makefile not found")
		}
		root = parent
	}
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
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
