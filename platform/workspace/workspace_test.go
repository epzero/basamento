package workspace_test

import (
	"testing"

	"github.com/epzero/basamento/platform/workspace"
)

func TestValidPathAcceptsCleanRelativePaths(t *testing.T) {
	for _, name := range []string{"a", "cmd/main.go", "a/b/c.txt", ".gitignore", "a..b", "a/b..c"} {
		if !workspace.ValidPath(name) {
			t.Fatalf("%q is a clean relative path", name)
		}
	}
}

func TestValidPathRejectsEscapesAndDirtySpellings(t *testing.T) {
	for _, name := range []string{
		"", "/abs", "../escape", "a/../../b", "a/../b", "a//b", "a/", "./a", ".", "..",
		`a\b`, "a\x00b",
	} {
		if workspace.ValidPath(name) {
			t.Fatalf("%q is not a clean relative path", name)
		}
	}
}
